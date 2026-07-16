package kdbadmin

// handlers_agents.go — 에이전트 총괄 페이지 (/admin/agents, 오너 지시 2026-07-17).
// "에이전트 이름·역할·사용 스킬·상태·모니터링을 한 페이지에" — 파이프라인의 판단
// 주체(role agent)들을 도메인별로 등재하고, 라이브 실적을 SQL 로 붙인다.
// 스펙은 코드 정적 선언(등재 = 이 파일 수정) — 화면과 실제 구성이 어긋나지 않게
// env 킬스위치를 렌더 시점에 직접 읽는다.

import (
	"context"
	"net/http"
	"os"
	"time"
)

type agentSpec struct {
	Name    string // 에이전트 이름
	Domain  string // 담당 도메인(파이프라인 단계)
	Role    string // 한 줄 역할 설명
	Skills  string // 사용 스킬(LLM·검색·데이터소스)
	EnvKey  string // 킬스위치 env ("" = 항상 on)
	OffVal  string // 이 값이면 꺼짐 (기본 "0")
	Hermes  bool   // Hermes 감독 여부
	StatKey string // 라이브 실적 조회 키 (buildAgentStats)
}

type agentRow struct {
	agentSpec
	Enabled  bool
	StatText string // 라이브 실적 한 줄
}

// agentSpecs — 등재부. 새 에이전트는 여기 + StatKey 구현을 함께 추가한다.
func agentSpecs() []agentSpec {
	return []agentSpec{
		{Name: "KeywordTriage", Domain: "② 심사 게이트", Role: "오염 키워드 판별 — 문장형·조합어·비K 를 검색 전/무근거 시 기각(보수적, 오거부 방지)",
			Skills: "gemma(low) · 구조 판별", EnvKey: "KDB_TRIAGE_ENABLED", Hermes: false, StatKey: "triage"},
		{Name: "GateResolver (autoverify)", Domain: "② 심사 게이트", Role: "보류 키워드의 근거를 대신 수집해 게이트 재평가 — 승격/보류/기각. 번역miss 우선",
			Skills: "Naver encyc·news(예산 600/일) · SearXNG 폴백(화이트리스트 매체) · 구글번역 원제 재검색 · DecideIntake 규칙", EnvKey: "KDB_INTAKE_AUTOVERIFY", Hermes: false, StatKey: "autoverify"},
		{Name: "TypeVerifier (type-retrace)", Domain: "③ 발굴 · 검증", Role: "자동승인 엔티티의 타입을 이름만 무편향 재검색해 재판정 — 오염 교정(revert 가능)",
			Skills: "gemma(low) · Naver news · SearXNG · dataqa_log 감사", EnvKey: "KDB_TYPE_RETRACE_DAILY", Hermes: false, StatKey: "typefix"},
		{Name: "EvidenceVerifier", Domain: "③ 발굴 · 검증", Role: "unverified 엔티티를 뉴스 근거+판정으로 evidenced 승급 / 오염 의심 플래그",
			Skills: "gemma(low) · Naver news · SearXNG", EnvKey: "", Hermes: false, StatKey: "evidence"},
		{Name: "Gatekeeper", Domain: "② 심사 게이트", Role: "신규 후보 junk(문장/조사/혼합스크립트) 거름 — use/reject",
			Skills: "gemma(med) · 규칙(PreGate)", EnvKey: "", Hermes: true, StatKey: "hermes:Gatekeeper"},
		{Name: "PersonExtractor", Domain: "⑤ 서빙 DB", Role: "person 엔티티를 인물 DB 로 이전·동기화",
			Skills: "gemma(med)", EnvKey: "", Hermes: true, StatKey: "hermes:PersonExtractor"},
		{Name: "Enricher", Domain: "④ 다국어 채움", Role: "인물·고유명사 빈 필드 갭필 루프(공식소스→검색→LLM)",
			Skills: "MusicBrainz · Wikidata · TMDb · iTunes · gemma", EnvKey: "", Hermes: true, StatKey: "hermes:Enricher"},
		{Name: "Disambiguator", Domain: "③ 발굴 · 검증", Role: "오타/닉네임 alias 병합, 진짜 동명이인만 분리",
			Skills: "codex(high)·gemma 인계 메시", EnvKey: "", Hermes: true, StatKey: "hermes:Disambiguator"},
		{Name: "FillVerifier", Domain: "④ 다국어 채움", Role: "codex-fallback 값(미검증 LLM)을 Wikidata 공식 라벨로 재검증·승급",
			Skills: "Wikidata QID · gemma 판정 · dataqa_log 감사", EnvKey: "KDB_FILLVERIFY_ENABLED", OffVal: "", Hermes: true, StatKey: "hermes:FillVerifier"},
		{Name: "TranslateFiller (L5)", Domain: "④ 다국어 채움", Role: "공식소스 무신호 en 빈칸을 맥락문장+구글번역으로 채움(MT 표기, 상위소스 자동교체)",
			Skills: "gemma 맥락문장 · Google Translate v2(월 40만자 상한) · 캐시", EnvKey: "KDB_ENRICH_GTRANSLATE", Hermes: false, StatKey: "gtranslate"},
	}
}

func (s *Server) agentsPage(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	rows := []agentRow{}
	for _, sp := range agentSpecs() {
		row := agentRow{agentSpec: sp, Enabled: true}
		if sp.EnvKey != "" {
			v := os.Getenv(sp.EnvKey)
			if sp.OffVal == "" && sp.EnvKey == "KDB_FILLVERIFY_ENABLED" {
				row.Enabled = v == "1" // opt-in 형
			} else {
				row.Enabled = v != "0" // opt-out 형(기본 on)
			}
		}
		row.StatText = s.agentStat(ctx, sp.StatKey)
		rows = append(rows, row)
	}

	s.render(w, r, "agents.html", map[string]any{
		"title":  "에이전트",
		"agents": rows,
		"page":   "/admin/agents",
	})
}

// agentStat — 에이전트별 라이브 실적 한 줄. 실패 시 "—" (페이지는 항상 뜬다).
func (s *Server) agentStat(ctx context.Context, key string) string {
	q1 := func(sql string, args ...any) (int64, bool) {
		var n int64
		if err := s.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
			return 0, false
		}
		return n, true
	}
	switch key {
	case "triage":
		if n, ok := q1(`SELECT count(*) FROM kwave_entity_research_queue WHERE precheck_reason='triage_garbage' AND finished_at >= now()-interval '7 days'`); ok {
			return fmtN(n) + "건 오염 기각 (7일)"
		}
	case "autoverify":
		if n, ok := q1(`SELECT count(*) FROM kwave_entity_research_queue WHERE precheck_reason LIKE 'auto_evidence%' AND created_at >= now()-interval '7 days'`); ok {
			backlog, _ := q1(`SELECT count(*) FROM kwave_entity_research_queue q WHERE precheck_status='review'
			  AND status NOT IN ('pending','in_progress') AND NOT (precheck_flags && ARRAY['autoverify_exhausted'])
			  AND (next_attempt_at IS NULL OR next_attempt_at <= now())`)
			return fmtN(n) + "건 자동승인 (7일) · 대기 " + fmtN(backlog)
		}
	case "typefix":
		if n, ok := q1(`SELECT count(*) FROM kwave_kdb_dataqa_log WHERE verdict='retrace-type-fix'`); ok {
			return fmtN(n) + "건 타입 교정 (누적) · 매일 05:30"
		}
	case "evidence":
		if n, ok := q1(`SELECT count(*) FROM kwave_entities WHERE verification_evidence LIKE 'search+gemma%'`); ok {
			return fmtN(n) + "건 근거 승급 (누적)"
		}
	case "gtranslate":
		if n, ok := q1(`SELECT count(*) FROM kwave_entities WHERE canonical_en_source='gtranslate' AND status='active'`); ok {
			return fmtN(n) + "건 MT 표기 서빙 중 (상위소스 대체 대기)"
		}
	default:
		// hermes:<Role> — 최근 run 상태 + 24h 처리량.
		if len(key) > 7 && key[:7] == "hermes:" {
			role := key[7:]
			var status string
			var items int64
			err := s.pool.QueryRow(ctx, `
SELECT status, COALESCE((SELECT sum(items_out) FROM kwave_kdb_hermes_runs
                          WHERE role=$1 AND started_at >= now()-interval '24 hours'),0)
FROM kwave_kdb_hermes_runs WHERE role=$1 ORDER BY started_at DESC LIMIT 1`, role).Scan(&status, &items)
			if err == nil {
				return "최근 run " + status + " · 24h 처리 " + fmtN(items)
			}
			return "run 기록 없음"
		}
	}
	return "—"
}

func fmtN(n int64) string {
	s := ""
	v := n
	if v == 0 {
		return "0"
	}
	for v > 0 {
		d := v % 1000
		v /= 1000
		if v > 0 {
			s = "," + pad3(d) + s
		} else {
			s = itoa64(d) + s
		}
	}
	return s
}

func pad3(d int64) string {
	out := itoa64(d)
	for len(out) < 3 {
		out = "0" + out
	}
	return out
}

func itoa64(d int64) string {
	if d == 0 {
		return "0"
	}
	out := ""
	for d > 0 {
		out = string(rune('0'+d%10)) + out
		d /= 10
	}
	return out
}
