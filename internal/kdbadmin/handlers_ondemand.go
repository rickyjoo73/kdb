package kdbadmin

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

// --- 온디맨드(kstory) 섹션 --------------------------------------------------
// 프로액티브 RSS 크롤을 폐기하고 소비자 요청 키워드를 즉시 해소·서빙하는 모델로
// 전환하면서, 그 흐름(요청 → 발굴 → 응답)을 admin 에서 볼 수 있게 하는 두 화면.
//   1) 발굴 큐   : research_queue + entities 조인 → 요청 키워드별 처리 결과
//   2) 소비자    : api_requests + api_consumers 집계 → 누가·뭘·성공/미스

// ---------- 1. 발굴 큐 (/admin/ondemand/queue) ----------

type rqRow struct {
	ID           string
	Ko           string
	Type         string
	QStatus      string // pending / in_progress / done / failed
	Attempts     int
	LastError    string
	CreatedAt    time.Time
	FinishedAt   *time.Time
	EntityStatus string // active / candidate / rejected / ""(미생성)
	Outcome      string // 사람이 읽는 처리 결과
	OutcomeClass string // 뱃지 색 키
	Precheck     string
	PreReason    string
	Origin       string
	RequestCount int
}

// rqOutcome — 큐 상태 + 엔티티 상태를 "요청 키워드가 어떻게 처리됐나" 라벨로 환산.
func rqOutcome(qStatus, entityStatus, precheck string) (label, class string) {
	switch precheck {
	case "review", "legacy":
		return "고유명사 검토 · API 보류", "review"
	case "reject":
		return "사전 기각 · API 차단", "rej"
	}
	switch qStatus {
	case "pending":
		return "대기중", "wait"
	case "in_progress":
		return "발굴중", "prog"
	case "failed":
		return "실패", "fail"
	}
	// done — 실제 엔티티 결말로 판정
	switch entityStatus {
	case "active":
		return "해소됨 · active", "ok"
	case "candidate":
		return "후보 대기 · Inbox", "cand"
	case "rejected":
		return "기각 · 오염/범위밖", "rej"
	default:
		return "완료 · 미생성", "none"
	}
}

func (s *Server) ondemandQueue(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	p := newPage(r, "/admin/ondemand/queue")
	statusFilter := strings.TrimSpace(r.URL.Query().Get("status"))
	if statusFilter != "" {
		p.Extras["status"] = statusFilter
	}

	// 큐 상태 요약 (pending/in_progress/done/failed)
	var qPending, qProgress, qDone, qFailed int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='pending'),
       count(*) FILTER (WHERE status='in_progress'),
       count(*) FILTER (WHERE status='done'),
       count(*) FILTER (WHERE status='failed')
FROM kwave_entity_research_queue`).Scan(&qPending, &qProgress, &qDone, &qFailed)
	// approved 는 자동검증(auto_evidence_*)과 운영자 수동 승인을 분리 집계한다 —
	// 자동 승격이 사람 승인처럼 보이면 안 됨(정직한 가시성).
	var gatePass, gateReview, gateReject, gateAutoApproved, gateOperApproved int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE precheck_status='pass'),
       count(*) FILTER (WHERE precheck_status IN ('review','legacy')),
       count(*) FILTER (WHERE precheck_status='reject'),
       count(*) FILTER (WHERE precheck_status='approved' AND precheck_reason LIKE 'auto_evidence%'),
       count(*) FILTER (WHERE precheck_status='approved' AND precheck_reason NOT LIKE 'auto_evidence%')
FROM kwave_entity_research_queue`).Scan(&gatePass, &gateReview, &gateReject, &gateAutoApproved, &gateOperApproved)

	// 처리 결과(엔티티 결말) 요약 — 요청이 실제로 어떻게 끝났나
	var outActive, outCand, outRej, outNone int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE e.status='active'),
       count(*) FILTER (WHERE e.status='candidate'),
       count(*) FILTER (WHERE e.status='rejected'),
       count(*) FILTER (WHERE e.st IS NULL)
FROM kwave_entity_research_queue rq
LEFT JOIN LATERAL (
  SELECT status AS st, status FROM kwave_entities
  WHERE canonical_ko = rq.entity_ko
  ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END
  LIMIT 1
) e ON true`).Scan(&outActive, &outCand, &outRej, &outNone)

	// 큐 상태 필터 (pending/in_progress/done/failed)
	listArgs := []any{p.Limit, p.Offset}
	listWhere := ""
	if statusFilter != "" {
		listWhere = "WHERE rq.status = $3"
		listArgs = append(listArgs, statusFilter)
	}

	var total int64
	if statusFilter != "" {
		_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM kwave_entity_research_queue WHERE status = $1", statusFilter).Scan(&total)
	} else {
		_ = s.pool.QueryRow(ctx, "SELECT count(*) FROM kwave_entity_research_queue").Scan(&total)
	}
	p.finalize(total)
	rows, err := s.pool.Query(ctx, `
SELECT rq.id::text, rq.entity_ko, rq.requested_entity_type::text, rq.status, rq.attempts,
       COALESCE(rq.last_error,''), rq.created_at, rq.finished_at,
       COALESCE(e.status,''), rq.precheck_status, COALESCE(rq.precheck_reason,''),
       COALESCE(rq.intake_origin,'unknown'), rq.request_count
FROM kwave_entity_research_queue rq
LEFT JOIN LATERAL (
  SELECT status FROM kwave_entities
  WHERE canonical_ko = rq.entity_ko
  ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END
  LIMIT 1
) e ON true
`+listWhere+`
ORDER BY rq.created_at DESC
LIMIT $1 OFFSET $2`, listArgs...)
	if err != nil {
		s.renderError(w, r, "ondemand-queue", err)
		return
	}
	defer rows.Close()

	items := []rqRow{}
	for rows.Next() {
		var x rqRow
		if err := rows.Scan(&x.ID, &x.Ko, &x.Type, &x.QStatus, &x.Attempts,
			&x.LastError, &x.CreatedAt, &x.FinishedAt, &x.EntityStatus,
			&x.Precheck, &x.PreReason, &x.Origin, &x.RequestCount); err != nil {
			continue
		}
		x.Outcome, x.OutcomeClass = rqOutcome(x.QStatus, x.EntityStatus, x.Precheck)
		items = append(items, x)
	}

	s.render(w, r, "ondemand_queue.html", map[string]any{
		"title":            "온디맨드 발굴 큐",
		"items":            items,
		"p":                p,
		"statusFilter":     statusFilter,
		"qPending":         qPending,
		"qProgress":        qProgress,
		"qDone":            qDone,
		"qFailed":          qFailed,
		"outActive":        outActive,
		"outCand":          outCand,
		"outRej":           outRej,
		"outNone":          outNone,
		"gatePass":         gatePass,
		"gateReview":       gateReview,
		"gateReject":       gateReject,
		"gateAutoApproved": gateAutoApproved,
		"gateOperApproved": gateOperApproved,
		"page":             "/admin/ondemand/queue",
	})
}

func (s *Server) ondemandQueueApprove(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, _ = s.pool.Exec(r.Context(), `
UPDATE kwave_entity_research_queue
   SET precheck_status='approved', precheck_reason='operator_approved',
       status='pending', finished_at=NULL, picked_at=NULL, next_attempt_at=NULL,
       resolution_status='unknown', locale_status='unknown', last_outcome='', last_error=NULL,
       last_requested_at=now()
 WHERE id::text=$1 AND precheck_status IN ('review','legacy') AND status <> 'in_progress'`, id)
	http.Redirect(w, r, "/admin/ondemand/queue", http.StatusSeeOther)
}

func (s *Server) ondemandQueueReject(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_, _ = s.pool.Exec(r.Context(), `
UPDATE kwave_entity_research_queue
   SET precheck_status='reject', precheck_reason='operator_rejected',
       status='done', finished_at=now(), picked_at=NULL, next_attempt_at=NULL,
       resolution_status='rejected_precheck', locale_status='blocked_precheck',
       last_outcome='precheck_reject', last_error=NULL
 WHERE id::text=$1 AND status <> 'in_progress'`, id)
	http.Redirect(w, r, "/admin/ondemand/queue", http.StatusSeeOther)
}

// ---------- 3. 기사별 요청 (/admin/ondemand/articles) ----------
// 오너 지목(§7-3): 소비자(kstory)가 보낸 기사(source_url) 단위로 그 기사에서 요청된 키워드들과
// KDB 가 판별한 엔티티, 그리고 '모호'(동명이인·미검증·검토플래그·미해소)를 한눈에 본다.
// ★기사 본문은 kstory 로그인 게이트라 KDB 가 직접 못 읽는다 — 그래서 URL + 같은 기사에서 함께
//   요청된 키워드 묶음(기사 지문)으로 대신 보여준다. 판별·모호 판단은 우리 DB 기준.

type articleKw struct {
	Ko         string // 요청 키워드
	ReqType    string
	EntityID   string // 판별된 엔티티(있으면 상세 링크)
	EntityType string
	EntStatus  string // active/candidate/rejected/""(미생성)
	Tier       string // authoritative/evidenced/unverified/""
	Ambig      string // 사람이 읽는 모호/상태 라벨
	AmbigClass string // 뱃지 색 키(oc-*)
}

type articleRow struct {
	SourceURL  string
	Host       string
	Path       string
	LastAt     time.Time
	KwCount    int
	AmbigCount int // 확정(active+검증+단일)이 아닌 키워드 수
	Kws        []articleKw
}

// kwAmbiguity — 요청 키워드의 판별 결과를 '모호/상태' 라벨로 환산. 우선순위: 미해소 > 기각 >
// 후보 > 동명이인 > 검토플래그 > 미검증 > 확정. ambiguous=확정(oc-ok)이 아닌 모든 상태.
func kwAmbiguity(entStatus, tier string, homonyms int, reviewFlag bool) (label, class string) {
	switch {
	case entStatus == "":
		return "미해소 · 미생성", "none"
	case entStatus == "rejected":
		return "기각 · 범위밖", "rej"
	case entStatus == "candidate":
		return "후보 대기", "cand"
	case homonyms >= 2:
		return "동명이인 " + itoa(homonyms), "ambig"
	case reviewFlag:
		return "검토 플래그", "review"
	case tier == "unverified":
		return "미검증", "unv"
	default: // active + evidenced/authoritative + 단일
		return "확정", "ok"
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

func (s *Server) ondemandArticles(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	p := newPage(r, "/admin/ondemand/articles")

	// 요약(전역): 기사 수 · 요청 키워드 수.
	var totalArticles, totalKw int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(DISTINCT source_url), count(*)
FROM kwave_entity_research_queue
WHERE source_url IS NOT NULL AND source_url <> ''`).Scan(&totalArticles, &totalKw)
	p.finalize(totalArticles)

	// 1) 페이지에 표시할 기사(source_url) — 최근순 페이지네이션.
	urlRows, err := s.pool.Query(ctx, `
SELECT source_url, max(created_at), count(*)
FROM kwave_entity_research_queue
WHERE source_url IS NOT NULL AND source_url <> ''
GROUP BY source_url
ORDER BY max(created_at) DESC
LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "ondemand-articles", err)
		return
	}
	order := []string{}
	byURL := map[string]*articleRow{}
	for urlRows.Next() {
		var a articleRow
		if err := urlRows.Scan(&a.SourceURL, &a.LastAt, &a.KwCount); err != nil {
			continue
		}
		a.Host, a.Path = splitURL(a.SourceURL)
		order = append(order, a.SourceURL)
		byURL[a.SourceURL] = &a
	}
	urlRows.Close()

	// 2) 그 기사들의 키워드 + KDB 판별 엔티티 + 모호 신호(동명이인 수·검토플래그)를 한 번에.
	if len(order) > 0 {
		kwRows, err := s.pool.Query(ctx, `
SELECT rq.source_url, rq.entity_ko, rq.requested_entity_type::text,
       COALESCE(e.id::text,''), COALESCE(e.entity_type::text,''),
       COALESCE(e.status,''), COALESCE(e.verification_tier,''),
       (COALESCE(e.notes,'') LIKE '%[retrace:review]%'),
       COALESCE(h.cnt,0)
FROM kwave_entity_research_queue rq
LEFT JOIN LATERAL (
  SELECT id, entity_type, status, verification_tier, notes
  FROM kwave_entities WHERE canonical_ko = rq.entity_ko
  ORDER BY CASE status WHEN 'active' THEN 0 WHEN 'candidate' THEN 1 ELSE 2 END
  LIMIT 1
) e ON true
LEFT JOIN LATERAL (
  SELECT count(*) cnt FROM kwave_entities
  WHERE canonical_ko = rq.entity_ko AND status='active'
) h ON true
WHERE rq.source_url = ANY($1)
ORDER BY rq.created_at DESC`, order)
		if err != nil {
			s.renderError(w, r, "ondemand-articles", err)
			return
		}
		for kwRows.Next() {
			var srcURL string
			var k articleKw
			var homonyms int
			var reviewFlag bool
			if err := kwRows.Scan(&srcURL, &k.Ko, &k.ReqType, &k.EntityID, &k.EntityType,
				&k.EntStatus, &k.Tier, &reviewFlag, &homonyms); err != nil {
				continue
			}
			k.Ambig, k.AmbigClass = kwAmbiguity(k.EntStatus, k.Tier, homonyms, reviewFlag)
			if a := byURL[srcURL]; a != nil {
				a.Kws = append(a.Kws, k)
				if k.AmbigClass != "ok" {
					a.AmbigCount++
				}
			}
		}
		kwRows.Close()
	}

	items := make([]*articleRow, 0, len(order))
	for _, u := range order {
		items = append(items, byURL[u])
	}

	s.render(w, r, "ondemand_articles.html", map[string]any{
		"title":         "기사별 요청",
		"items":         items,
		"p":             p,
		"totalArticles": totalArticles,
		"totalKw":       totalKw,
		"page":          "/admin/ondemand/articles",
	})
}

// splitURL — 표시용 host / path 분리(스킴 제거). 파싱 실패해도 원본을 host 로.
func splitURL(u string) (host, path string) {
	s := u
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i], s[i:]
	}
	return s, ""
}

// ---------- 2. 소비자 대시보드 (/admin/ondemand/consumers) ----------

type ondemandConsumerRow struct {
	Label    string
	Active   bool
	Req7d    int64
	Miss7d   int64
	MissPct  int
	LastUsed *time.Time
	TopPath  string
}

func (s *Server) ondemandConsumers(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()

	var totalConsumers, activeConsumers, req7d, miss7d int64
	_ = s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE active) FROM kwave_kdb_api_consumers`).Scan(&totalConsumers, &activeConsumers)
	_ = s.pool.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE status>=400) FROM kwave_kdb_api_requests WHERE created_at > now()-interval '7 days'`).Scan(&req7d, &miss7d)
	missPct := 0
	if req7d > 0 {
		missPct = int(miss7d * 100 / req7d)
	}

	items := []ondemandConsumerRow{}
	rows, err := s.pool.Query(ctx, `
SELECT c.label, c.active, c.last_used_at,
  COUNT(ar.id) FILTER (WHERE ar.created_at > now()-interval '7 days'),
  COUNT(ar.id) FILTER (WHERE ar.created_at > now()-interval '7 days' AND ar.status >= 400),
  COALESCE((SELECT ar2.path FROM kwave_kdb_api_requests ar2
            WHERE ar2.consumer_id = c.id AND ar2.created_at > now()-interval '7 days'
            GROUP BY ar2.path ORDER BY count(*) DESC LIMIT 1), '')
FROM kwave_kdb_api_consumers c
LEFT JOIN kwave_kdb_api_requests ar ON ar.consumer_id = c.id
GROUP BY c.id, c.label, c.active, c.last_used_at
ORDER BY 4 DESC, c.created_at DESC`)
	if err != nil {
		s.renderError(w, r, "ondemand-consumers", err)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var x ondemandConsumerRow
		if err := rows.Scan(&x.Label, &x.Active, &x.LastUsed, &x.Req7d, &x.Miss7d, &x.TopPath); err != nil {
			continue
		}
		if x.Req7d > 0 {
			x.MissPct = int(x.Miss7d * 100 / x.Req7d)
		}
		items = append(items, x)
	}

	s.render(w, r, "ondemand_consumers.html", map[string]any{
		"title":           "소비자 · kstory 대시보드",
		"items":           items,
		"totalConsumers":  totalConsumers,
		"activeConsumers": activeConsumers,
		"req7d":           req7d,
		"miss7d":          miss7d,
		"missPct":         missPct,
		"page":            "/admin/ondemand/consumers",
	})
}

// ---------- 4. 요청 내용 (/admin/ondemand/requests) ----------
// 오너 지시(07-13): 매체가 API 로 보낸 요청 "내용"(기사 URL·키워드·type·KDB 응답)을
// 호출 단위 카드로 그대로 보고 빠르게 검토한다. HTTP 메타(경로·상태·속도)만 있는
// 클라이언트 요청 로그와 달리 본문 항목(mig 0092 kwave_kdb_request_terms)을 보여준다.

type reqTermChip struct {
	Ko, Type, Status, StatusClass string
	HasContext                    bool
}

type requestGroupRow struct {
	At         time.Time
	Consumer   string
	Origin     string
	SourceURL  string
	Host, Path string
	Terms      []reqTermChip
}

// reqTermClass — 항목 응답 상태 → 뱃지 색 키(oc-*).
func reqTermClass(status string) string {
	switch status {
	case "ready", "found", "auto_applied":
		return "ok"
	case "preparing", "new", "queued", "proposed", "verifying":
		return "cand"
	case "out_of_scope", "rejected_precheck", "rejected":
		return "rej"
	case "review_required":
		return "review"
	default: // miss 등
		return "none"
	}
}

func (s *Server) ondemandRequests(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	p := newPage(r, "/admin/ondemand/requests")

	var totalGroups, totalTerms, terms24h int64
	_ = s.pool.QueryRow(ctx, `
SELECT count(DISTINCT request_group), count(*),
       count(*) FILTER (WHERE created_at > now()-interval '24 hours')
FROM kwave_kdb_request_terms`).Scan(&totalGroups, &totalTerms, &terms24h)
	p.finalize(totalGroups)

	groupRows, err := s.pool.Query(ctx, `
SELECT request_group::text, min(created_at), consumer_id, origin,
       COALESCE(max(source_url) FILTER (WHERE source_url<>''), '')
FROM kwave_kdb_request_terms
GROUP BY request_group, consumer_id, origin
ORDER BY min(created_at) DESC
LIMIT $1 OFFSET $2`, p.Limit, p.Offset)
	if err != nil {
		s.renderError(w, r, "ondemand-requests", err)
		return
	}
	order := []string{}
	byGroup := map[string]*requestGroupRow{}
	for groupRows.Next() {
		var g requestGroupRow
		var id string
		if err := groupRows.Scan(&id, &g.At, &g.Consumer, &g.Origin, &g.SourceURL); err != nil {
			continue
		}
		g.Host, g.Path = splitURL(g.SourceURL)
		order = append(order, id)
		byGroup[id] = &g
	}
	groupRows.Close()

	if len(order) > 0 {
		termRows, err := s.pool.Query(ctx, `
SELECT request_group::text, term_ko, term_type, item_status, has_context
FROM kwave_kdb_request_terms
WHERE request_group::text = ANY($1)
ORDER BY id`, order)
		if err != nil {
			s.renderError(w, r, "ondemand-requests", err)
			return
		}
		for termRows.Next() {
			var id string
			var t reqTermChip
			if err := termRows.Scan(&id, &t.Ko, &t.Type, &t.Status, &t.HasContext); err != nil {
				continue
			}
			t.StatusClass = reqTermClass(t.Status)
			if g := byGroup[id]; g != nil {
				g.Terms = append(g.Terms, t)
			}
		}
		termRows.Close()
	}

	items := make([]requestGroupRow, 0, len(order))
	for _, id := range order {
		items = append(items, *byGroup[id])
	}

	s.render(w, r, "ondemand_requests.html", map[string]any{
		"title":       "요청 내용 · API 본문",
		"items":       items,
		"p":           p,
		"totalGroups": totalGroups,
		"totalTerms":  totalTerms,
		"terms24h":    terms24h,
		"page":        "/admin/ondemand/requests",
	})
}
