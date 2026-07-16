package kdbadmin

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// --- 워크플로우 보드 (/admin/workflow) --------------------------------------
// 오너 요구 (2026-07-16): "파이프라인 단계를 열로 나누고, 키워드를 카드로 만들어
// 진행되는 내용을 모니터링" — 칸반형 관제판. 두 레인 원칙:
//   신규 레인  = 카드 (실시간 흐름: 유입→심사→발굴→채움→서빙, 최근 24h)
//   백로그 레인 = 컬럼 헤더 배지 (누적 잔량 — autopilot/drain 이 유휴 시간에 소진)

type wfCard struct {
	Ko       string
	Type     string // entity_type 칩
	Sub      string // 부가정보: 보류사유·origin·오류
	Chips    []wfChip
	Age      string // "3분" / "2시간" — 현 단계 체류시간
	AgeClass string // ok / slow / stuck (카드 우측 시간 색)
	Href     string
}

// wfChip — locale 채움 상태 칩 (On=채워짐 초록 / Off=빈칸 회색).
type wfChip struct {
	Code string
	On   bool
}

type wfStage struct {
	Key          string // INTAKE / GATE / RESOLVE / FILL / SERVE
	Label        string
	Desc         string // 헤더 밑 한 줄: 이 단계가 하는 일
	Accent       string // 헤더 배경 tailwind 클래스
	Count        int64  // 카드 모수 (이 단계에 지금/최근 있는 것)
	CountLabel   string // "대기" / "24h" 등 Count 의 의미
	Items        []wfCard
	More         int64  // Count - len(Items)
	MoreHref     string
	Backlog      int64  // 백로그 잔량 (누적) — 0 이면 배지 숨김
	BacklogLabel string
	BacklogHref  string
	EmptyText    string // 카드 0건일 때: 왜 비어 있는 게 정상인지
	SubNote      string // 헤더 하단 보조 설명 (제외분 표기 등 — 숨기지 않고 명시)
}

// (게이트 사유·유형·출처 한글 라벨은 labels.go 공용 헬퍼 사용)

type wfTile struct {
	Label string
	Value string
	Sub   string // 값 아래 작은 분해 표기 (예: 유입 origin 별)
	Class string // 숫자 색
	Href  string
}

type wfIssue struct {
	Title    string
	Count    int64
	Detail   string
	Href     string
	Severity string // red / amber / slate
}

// fmtAgoKo — 체류/경과 시간을 짧은 한국어로.
func fmtAgoKo(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "방금"
	case d < time.Hour:
		return fmt.Sprintf("%d분", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d시간", int(d.Hours()))
	default:
		return fmt.Sprintf("%d일", int(d.Hours()/24))
	}
}

// ageClass — 처리 대기 카드의 체류시간 경고색. 즉시채움 SLA(2분)와 정체(15분) 기준.
func ageClass(t time.Time) string {
	d := time.Since(t)
	switch {
	case d > 15*time.Minute:
		return "stuck"
	case d > 2*time.Minute:
		return "slow"
	default:
		return "ok"
	}
}

const wfCardLimit = 12

func (s *Server) workflowBoard(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	// ---- 요약 타일 (24h cohort) ----
	// 유입은 origin 으로 성격 구분 (오너 지시 07-16): lookup/correction-miss=지금 번역에
	// 필요(급함) vs prepare=사전 준비. 병합 저장하되 읽기는 반드시 구분.
	var in24h, in24hMiss, in24hPrep, done24h, review24h, failedNow, stuck15 int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE created_at >= now()-interval '24 hours'),
       count(*) FILTER (WHERE created_at >= now()-interval '24 hours' AND intake_origin IN ('lookup-miss','correction-miss')),
       count(*) FILTER (WHERE created_at >= now()-interval '24 hours' AND intake_origin='prepare'),
       count(*) FILTER (WHERE finished_at >= now()-interval '24 hours' AND status='done'),
       count(*) FILTER (WHERE precheck_status='review' AND created_at >= now()-interval '24 hours'),
       count(*) FILTER (WHERE status='failed'),
       count(*) FILTER (WHERE (status='in_progress' AND COALESCE(picked_at,created_at) < now()-interval '15 minutes')
                            OR (status='pending' AND created_at < now()-interval '15 minutes'))
FROM kwave_entity_research_queue`).Scan(&in24h, &in24hMiss, &in24hPrep, &done24h, &review24h, &failedNow, &stuck15)

	// en 빈칸 백로그 — translate-fill 대상 정의와 동일 (가드기각 잔여 포함).
	var enBlank int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities
WHERE status='active' AND operator_locked=false
  AND COALESCE(canonical_en,'')='' AND canonical_ko ~ '[가-힣]'
  AND entity_type NOT IN ('unknown','term')`).Scan(&enBlank)

	// 2분 SLA 초과율 (dashboard 와 동일 정의).
	var doneCnt, over2m int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE EXTRACT(EPOCH FROM (finished_at-created_at)) > 120)
FROM kwave_entity_research_queue
WHERE finished_at IS NOT NULL AND created_at > now()-interval '24 hours'`).Scan(&doneCnt, &over2m)
	slaOverPct := int64(0)
	if doneCnt > 0 {
		slaOverPct = over2m * 100 / doneCnt
	}

	_ = review24h // 게이트 컬럼과 동일 정의(중복 제외)를 쓰기 위해 타일은 컬럼 계산 뒤 생성

	// ---- 워커 스트립: 자동 파이프라인이 살아있나 ----
	var lastAutopilot, lastResolve, lastFill *time.Time
	_ = s.pool.QueryRow(ctx, `SELECT max(ran_at) FROM kwave_kdb_autopilot_log`).Scan(&lastAutopilot)
	_ = s.pool.QueryRow(ctx, `SELECT max(finished_at) FROM kwave_entity_research_queue WHERE status='done'`).Scan(&lastResolve)
	_ = s.pool.QueryRow(ctx, `SELECT max(created_at) FROM kwave_kdb_translate_cache`).Scan(&lastFill)
	agoOrDash := func(t *time.Time) string {
		if t == nil {
			return "—"
		}
		return fmtAgoKo(*t) + " 전"
	}

	// ---- 컬럼 ① 유입 대기 ----
	intake := wfStage{
		Key: "INTAKE", Label: "① 유입 대기", CountLabel: "대기",
		Desc:   "소비자 요청 도착 — 카드의 회색 글자가 출처(lookup-miss=지금 번역에 필요 · prepare=사전 준비)",
		Accent: "bg-sky-50", MoreHref: "/admin/ondemand/queue?status=pending",
		EmptyText: "대기 0건 — 새 키워드는 유입 즉시 발굴로 넘어갑니다",
	}
	_ = s.pool.QueryRow(ctx, `SELECT count(DISTINCT entity_ko) FROM kwave_entity_research_queue WHERE status='pending'`).Scan(&intake.Count)
	func() {
		rows, err := s.pool.Query(ctx, `
SELECT t.entity_ko, t.rt, t.origin, t.created_at FROM (
  SELECT DISTINCT ON (entity_ko) entity_ko, requested_entity_type::text AS rt,
         COALESCE(intake_origin,'') AS origin, created_at
  FROM kwave_entity_research_queue WHERE status='pending'
  ORDER BY entity_ko, created_at ASC
) t ORDER BY t.created_at ASC LIMIT $1`, wfCardLimit)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var c wfCard
			var origin string
			var at time.Time
			if rows.Scan(&c.Ko, &c.Type, &origin, &at) != nil {
				continue
			}
			c.Sub, c.Age, c.AgeClass = originKo(origin), fmtAgoKo(at), ageClass(at)
			c.Href = "/admin/ondemand/queue?q=" + c.Ko
			intake.Items = append(intake.Items, c)
		}
	}()

	// ---- 컬럼 ② 심사 게이트 (보류) ----
	// 이미 active 로 서빙 중인 키워드의 중복 요청(duplicate_live_request 등)은 심사할
	// 것이 없으므로 카드에서 제외하되, 제외 건수를 SubNote 로 명시한다(숨김 아님).
	// 오너 지적 2026-07-16: "K-POP이 서빙도달에도 있는데 심사게이트에 또 있다".
	gate := wfStage{
		Key: "GATE", Label: "② 심사 게이트", CountLabel: "24h 신규",
		Desc:   "K-엔터 고유명사 맞는지 1차 심사 — 보류분은 자동검증이 근거 수집 후 재평가",
		Accent: "bg-indigo-50", MoreHref: "/admin/ondemand/queue",
		BacklogLabel: "미해결 보류", BacklogHref: "/admin/ondemand/queue",
		EmptyText: "24h 신규 보류 0건",
	}
	const gateNotServed = `NOT EXISTS (SELECT 1 FROM kwave_entities e
   WHERE e.status='active' AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))`
	_ = s.pool.QueryRow(ctx, `
SELECT count(DISTINCT entity_ko) FROM kwave_entity_research_queue q
WHERE precheck_status='review' AND created_at >= now()-interval '24 hours' AND `+gateNotServed).Scan(&gate.Count)
	_ = s.pool.QueryRow(ctx, `
SELECT count(DISTINCT entity_ko) FROM kwave_entity_research_queue q
WHERE precheck_status='review' AND `+gateNotServed).Scan(&gate.Backlog)
	var gateDup24h int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(DISTINCT entity_ko) FROM kwave_entity_research_queue q
WHERE precheck_status='review' AND created_at >= now()-interval '24 hours' AND NOT (`+gateNotServed+`)`).Scan(&gateDup24h)
	if gateDup24h > 0 {
		gate.SubNote = fmt.Sprintf("이미 서빙 중인 키워드의 중복 요청 %d건은 표시 제외", gateDup24h)
	}
	func() {
		rows, err := s.pool.Query(ctx, `
SELECT t.entity_ko, t.rt, t.reason, t.created_at FROM (
  SELECT DISTINCT ON (entity_ko) entity_ko, requested_entity_type::text AS rt,
         COALESCE(precheck_reason,'') AS reason, created_at
  FROM kwave_entity_research_queue q
  WHERE precheck_status='review' AND created_at >= now()-interval '24 hours' AND `+gateNotServed+`
  ORDER BY entity_ko, created_at DESC
) t ORDER BY t.created_at DESC LIMIT $1`, wfCardLimit)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var c wfCard
			var reason string
			var at time.Time
			if rows.Scan(&c.Ko, &c.Type, &reason, &at) != nil {
				continue
			}
			c.Sub = precheckReasonKo(reason)
			c.Age, c.AgeClass = fmtAgoKo(at), "ok" // 보류는 정체가 아니라 심사 상태 — 색 경고 없음
			c.Href = "/admin/ondemand/queue?q=" + c.Ko
			gate.Items = append(gate.Items, c)
		}
	}()

	// ---- 컬럼 ③ 발굴 · 검증 중 ----
	resolve := wfStage{
		Key: "RESOLVE", Label: "③ 발굴 · 검증", CountLabel: "진행중",
		Desc:   "뉴스 근거 수집 → AI가 정체(유형·동명) 특정 → 공식소스 대조 후 엔티티 생성",
		Accent: "bg-violet-50", MoreHref: "/admin/ondemand/queue?status=in_progress",
		EmptyText: "발굴 중 0건 — 유입이 곧바로 처리되고 있습니다",
	}
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entity_research_queue WHERE status IN ('in_progress','failed')`).Scan(&resolve.Count)
	func() {
		rows, err := s.pool.Query(ctx, `
SELECT entity_ko, requested_entity_type::text, status, COALESCE(last_error,''), COALESCE(picked_at, created_at)
FROM kwave_entity_research_queue WHERE status IN ('in_progress','failed')
ORDER BY COALESCE(picked_at, created_at) DESC LIMIT $1`, wfCardLimit)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var c wfCard
			var qStatus, lastErr string
			var at time.Time
			if rows.Scan(&c.Ko, &c.Type, &qStatus, &lastErr, &at) != nil {
				continue
			}
			c.Age = fmtAgoKo(at)
			if qStatus == "failed" {
				c.Sub, c.AgeClass = "실패: "+trimTo(lastErr, 40), "stuck"
			} else {
				c.AgeClass = ageClass(at)
			}
			c.Href = "/admin/ondemand/queue?q=" + c.Ko
			resolve.Items = append(resolve.Items, c)
		}
	}()

	// ---- 컬럼 ④ 다국어 채움 ----
	// 카드=최근 48h 흐름, 배지=전체 백로그(우선어 하나라도 빈 active), SubNote=언어별 분해.
	// 오너 지적 07-16: "채워야 할 DB가 왜 적어 보이나" — en 만 보여주던 것을 8언어 전체로.
	fill := wfStage{
		Key: "FILL", Label: "④ 다국어 채움", CountLabel: "48h 진행",
		Desc:   "언어별 공식 표기 채움 — 초록 칩=채워짐 · 회색 칩=아직 없음",
		Accent: "bg-fuchsia-50", MoreHref: "/admin/entities/locale-gaps",
		BacklogLabel: "빈칸 보유", BacklogHref: "/admin/entities/locale-gaps",
		EmptyText: "최근 48h 채움 대상 0건",
	}
	var mEn, mJa, mVi, mZh, mZhh, mEs, mID, mPt int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE COALESCE(canonical_en,'')='' OR COALESCE(canonical_ja,'')=''
                           OR COALESCE(canonical_vi,'')='' OR COALESCE(canonical_zh,'')=''
                           OR COALESCE(canonical_zh_hant,'')='' OR COALESCE(canonical_es,'')=''
                           OR COALESCE(canonical_id,'')='' OR COALESCE(canonical_pt_br,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_en,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_ja,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_vi,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_zh,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_zh_hant,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_es,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_id,'')=''),
       count(*) FILTER (WHERE COALESCE(canonical_pt_br,'')='')
FROM kwave_entities WHERE status='active'`).Scan(&fill.Backlog, &mEn, &mJa, &mVi, &mZh, &mZhh, &mEs, &mID, &mPt)
	fill.SubNote = fmt.Sprintf("언어별 빈칸: en %d · ja %d · vi %d · zh %d · 번체 %d · es %d · id %d · pt %d",
		mEn, mJa, mVi, mZh, mZhh, mEs, mID, mPt)
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities
WHERE status='active' AND operator_locked=false AND entity_type NOT IN ('unknown','term')
  AND (COALESCE(canonical_en,'')='' OR COALESCE(canonical_ja,'')='' OR COALESCE(canonical_vi,'')='')
  AND updated_at >= now()-interval '48 hours'`).Scan(&fill.Count)
	func() {
		rows, err := s.pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text, updated_at,
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_vi,'')
FROM kwave_entities
WHERE status='active' AND operator_locked=false AND entity_type NOT IN ('unknown','term')
  AND (COALESCE(canonical_en,'')='' OR COALESCE(canonical_ja,'')='' OR COALESCE(canonical_vi,'')='')
  AND updated_at >= now()-interval '48 hours'
ORDER BY updated_at DESC LIMIT $1`, wfCardLimit)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var c wfCard
			var id, en, ja, vi string
			var at time.Time
			if rows.Scan(&id, &c.Ko, &c.Type, &at, &en, &ja, &vi) != nil {
				continue
			}
			// 우선 locale 전체를 칩으로: 채워진 것(On)과 빈칸을 함께 보여준다(오너 요구 07-16).
			c.Chips = []wfChip{
				{Code: "en", On: en != ""},
				{Code: "ja", On: ja != ""},
				{Code: "vi", On: vi != ""},
			}
			c.Age, c.AgeClass = fmtAgoKo(at), "ok"
			c.Href = "/admin/entities/" + id
			fill.Items = append(fill.Items, c)
		}
	}()

	// ---- 컬럼 ⑤ 서빙 도달 (24h) ----
	serve := wfStage{
		Key: "SERVE", Label: "⑤ 서빙 도달", CountLabel: "24h",
		Desc:   "발굴 완료 + active 승급 — 소비자 API 로 응답 중",
		Accent: "bg-emerald-50", MoreHref: "/admin/ondemand/queue?status=done",
		EmptyText: "최근 24h 서빙 도달 0건",
	}
	_ = s.pool.QueryRow(ctx, `
SELECT count(DISTINCT q.entity_ko)
FROM kwave_entity_research_queue q
WHERE q.status='done' AND q.finished_at >= now()-interval '24 hours'
  AND EXISTS (SELECT 1 FROM kwave_entities e WHERE e.status='active'
              AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))`).Scan(&serve.Count)
	func() {
		rows, err := s.pool.Query(ctx, `
SELECT t.entity_ko, t.et, t.eid, t.finished_at FROM (
  SELECT DISTINCT ON (q.entity_ko) q.entity_ko, e.entity_type::text AS et, e.id::text AS eid, q.finished_at
  FROM kwave_entity_research_queue q
  JOIN kwave_entities e ON e.status='active'
   AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko))
  WHERE q.status='done' AND q.finished_at >= now()-interval '24 hours'
  ORDER BY q.entity_ko, q.finished_at DESC
) t ORDER BY t.finished_at DESC LIMIT $1`, wfCardLimit)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var c wfCard
			var eid string
			var at time.Time
			if rows.Scan(&c.Ko, &c.Type, &eid, &at) != nil {
				continue
			}
			c.Age, c.AgeClass = fmtAgoKo(at), "ok"
			c.Href = "/admin/entities/" + eid
			serve.Items = append(serve.Items, c)
		}
	}()

	stages := []*wfStage{&intake, &gate, &resolve, &fill, &serve}
	for _, st := range stages {
		if more := st.Count - int64(len(st.Items)); more > 0 {
			st.More = more
		}
	}

	// ---- 요약 타일 — 컬럼과 동일 정의(중복 제외)로 숫자 불일치 방지 ----
	tiles := []wfTile{
		{Label: "유입 24h", Value: fmt.Sprintf("%d", in24h),
			Sub:   fmt.Sprintf("번역miss %d · 사전준비 %d · 기타 %d", in24hMiss, in24hPrep, in24h-in24hMiss-in24hPrep),
			Class: "text-slate-800", Href: "/admin/ondemand/queue"},
		{Label: "발굴 완료 24h", Value: fmt.Sprintf("%d", done24h), Class: "text-emerald-700", Href: "/admin/ondemand/queue"},
		{Label: "심사 보류 24h", Value: fmt.Sprintf("%d", gate.Count), Class: tern(gate.Count > 0, "text-amber-700", "text-slate-400"), Href: "/admin/ondemand/queue"},
		{Label: "실패 · 재시도", Value: fmt.Sprintf("%d", failedNow), Class: tern(failedNow > 0, "text-red-700", "text-slate-400"), Href: "/admin/ondemand/queue?status=failed"},
		{Label: "en 빈칸 백로그", Value: fmt.Sprintf("%d", enBlank), Class: tern(enBlank > 0, "text-amber-700", "text-slate-400"), Href: "/admin/entities/locale-gaps"},
		{Label: "2분 SLA 초과율", Value: fmt.Sprintf("%d%%", slaOverPct), Class: tern(slaOverPct > 50, "text-red-700", "text-slate-800"), Href: "/admin/ops/health"},
	}

	// ---- 지금 막힌 곳 (issues) ----
	inbox := s.fetchInboxCounts(ctx)
	var issues []wfIssue
	addIssue := func(cnt int64, redAt int64, title, detail, href string) {
		if cnt <= 0 {
			return
		}
		sev := "amber"
		if redAt > 0 && cnt >= redAt {
			sev = "red"
		}
		issues = append(issues, wfIssue{Title: title, Count: cnt, Detail: detail, Href: href, Severity: sev})
	}
	addIssue(stuck15, 1, "15분+ 정체", "대기·발굴 중인데 15분 넘게 안 움직인 키워드 — 워커 점검 필요", "/admin/ondemand/queue")
	addIssue(failedNow, 5, "발굴 실패", "재시도 대기 중 — 반복 실패면 원인 확인", "/admin/ondemand/queue?status=failed")
	addIssue(inbox.NewCandidates, 10, "신규 후보 승인 대기", "발굴된 새 엔티티 — 승인해야 서빙", "/admin/kdb/inbox")
	addIssue(inbox.Corrections, 10, "교정요청 심사", "소비자가 제안한 표기 수정", "/admin/corrections")
	addIssue(inbox.Conflicts, 10, "충돌 · 동명이인", "같은 이름 다른 정체 — 병합/구분 필요", "/admin/entities/conflicts")
	addIssue(enBlank, 0, "en 빈칸 (가드기각 잔여)", "번역 가드에 걸려 자동 재시도 안 됨 — 가드 튜닝/수동 처리 대상", "/admin/entities/locale-gaps")

	s.render(w, r, "workflow_board.html", map[string]any{
		"title":         "워크플로우 보드",
		"page":          "/admin/workflow",
		"tiles":         tiles,
		"stages":        stages,
		"issues":        issues,
		"lastAutopilot": agoOrDash(lastAutopilot),
		"lastResolve":   agoOrDash(lastResolve),
		"lastFill":      agoOrDash(lastFill),
		"now":           time.Now().Format("15:04:05"),
	})
}

func tern(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

func trimTo(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
