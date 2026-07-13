package kdbadmin

import (
	"context"
	"fmt"
	"net/http"
	"time"

	kdb "github.com/rickyjoo73/kdb/internal/kdb"
)

// --- Workflow map ----------------------------------------------------------
//
// The /admin/hermes page is organized as the ACTUAL end-to-end workflow, split
// into the two tracks the system really runs on:
//
//	Track A — 요청 처리 (synchronous client hot-path): 입력 → 매칭 → 제공 + 수정
//	Track B — 자율 품질 (asynchronous Hermes/autopilot): 수집 → 검수 → 보강
//
// Every step is owned by an agent. Deterministic steps (HTTP fetch / SQL /
// cheap-gate) carry no LLM; judgment steps (extract / classify / gate / match /
// correct / enrich / QA) do. Steps whose judgment currently has NO LLM, or that
// run OUTSIDE Hermes supervision, are surfaced honestly (Warn) — they are the
// Phase B/C work-list, not hidden. See .claude/plans for the full design.

// workflowStepVM is one rendered step row.
type workflowStepVM struct {
	Num        string
	Name       string
	Detail     string
	Agent      string // owning agent (existing or planned)
	LLMUsed    bool   // false → "LLM 미투입" badge
	Provider   string // resolved: gemma | codex | literal (e.g. "gemma(서버22)")
	Effort     string // codex only
	Supervised bool   // true → under Hermes (run row + leak/incident)
	Metric     string // live metric ("" = none)
	Warn       string // honest gap note ("" = none)
}

// workflowTrackVM is one track (A or B) with its ordered steps.
type workflowTrackVM struct {
	Key   string
	Title string
	Desc  string
	Steps []workflowStepVM
}

// workflowStepSpec is the static definition of a step. Provider/Effort are
// resolved at render time via hermesResolveProvider/hermesResolveEffort so the
// page reflects live env + GemmaDown fallback (ProviderLiteral bypasses that for
// externally-run models, e.g. the server22 QA worker).
type workflowStepSpec struct {
	Num, Name, Detail, Agent string
	LLMUsed                  bool
	ProviderKey, ProviderDef string
	ProviderLiteral          string
	EffortKey, EffortDef     string
	Supervised               bool
	Warn                     string
	MetricKey                string
}

type workflowTrackSpec struct {
	Key, Title, Desc string
	Steps            []workflowStepSpec
}

// workflowSpecs encodes the verified workflow (5 explore-agent traces + live
// routing). Keep in sync with the request handlers (internal/kdbapi) and the
// autopilot/Hermes wiring (internal/kdb/autopilot, internal/kdb/hermes).
func workflowSpecs() []workflowTrackSpec {
	const planC = "판단 단계인데 LLM 미투입 — 신규 에이전트 예정(Phase C)"
	const supB = "Hermes 감독 밖 — 에이전트 편입 예정(Phase B)"
	return []workflowTrackSpec{
		{
			Key: "A", Title: "트랙 A — 요청 처리 (동기, 클라이언트 핫패스)",
			Desc: "참조(lookup/match) · 요청(prepare) · 수정(corrections) 수신 → 매칭 → 제공",
			Steps: []workflowStepSpec{
				{Num: "A1", Name: "인증/티어", Detail: "X-KDB-Key: env키=write · DB키=read", Agent: "Auth"},
				{Num: "A2", Name: "입력 게이트", Detail: "PreGate junk 차단(문장/조사/난수/자모/혼합스크립트)", Agent: "InputGate"},
				{Num: "A3", Name: "키워드 교정", Detail: "오타·오염 키워드 정규화", Agent: "KeywordFixer(예정)", Warn: planC},
				{Num: "A4", Name: "참조 · lookup", Detail: "canonical_ko ILIKE + alias 부분일치", Agent: "LexicalMatch", MetricKey: "lookup"},
				{Num: "A5", Name: "참조 · match", Detail: "source_text strpos + locale별 alias", Agent: "LocaleMatch", MetricKey: "match"},
				{Num: "A6", Name: "alias 매칭", Detail: "pg_trgm 오타 + 한글 약자 휴리스틱", Agent: "AliasMatch"},
				{Num: "A7", Name: "문맥 동명이인", Detail: "다중 homonym → 문맥으로 선택", Agent: "ContextDisambig(예정)", Warn: planC},
				{Num: "A8", Name: "miss 추출", Detail: "lookup/prepare=research큐 · match miss=빈응답", Agent: "MatchMissExtractor(예정)", Warn: "match miss 시 엔티티 추출 LLM 미투입 — 신규 에이전트 예정(Phase C)"},
				{Num: "A9", Name: "요청 · prepare", Detail: "terms→ready/preparing/new + 즉시 enrich(L2/L3)", Agent: "Prepare"},
				{Num: "A10", Name: "수정 · 수신", Detail: "정정 신고 → kwave_kdb_corrections", Agent: "CorrectionIntake", MetricKey: "corrections"},
				{Num: "A11", Name: "수정 · 검증", Detail: "Wikidata 교차 + LLM 검증 → auto/proposed/pending", Agent: "CorrectionVerifier",
					LLMUsed: true, ProviderKey: "CORRECTION", ProviderDef: "codex", EffortKey: "CORRECTION", EffortDef: "medium", Supervised: false, Warn: supB},
				{Num: "A12", Name: "제공/직렬화", Detail: "8 locale + provenance 응답, 빈칸→bg enrich", Agent: "Serve", MetricKey: "coverage"},
				{Num: "A13", Name: "응답 품질가드", Detail: "VerifiedOnly / min_conf / status 필터", Agent: "Serve"},
				{Num: "A14", Name: "요청 로그", Detail: "kwave_kdb_api_requests 적재", Agent: "RequestLog"},
			},
		},
		{
			Key: "B", Title: "트랙 B — 자율 품질 (비동기, Hermes/autopilot)",
			Desc: "수집 → 검수 → 보강. fast 30s · poll 15m · autopilot 30m · research 60s · dataqa 20m",
			Steps: []workflowStepSpec{
				{Num: "B1", Name: "RSS 수집(15m)", Detail: "화이트리스트 fetch+parse → raw", Agent: "Poller", MetricKey: "obs24h"},
				{Num: "B2", Name: "cheap-gate", Detail: "EntityIndex substring 매칭", Agent: "CheapGate"},
				{Num: "B3", Name: "추출(30s)", Detail: "raw → spellings LLM 추출", Agent: "Extractor",
					LLMUsed: true, ProviderKey: "", ProviderDef: "", EffortKey: "EXTRACT", EffortDef: "low", Supervised: false, Warn: supB, MetricKey: "rawpending"},
				{Num: "B4", Name: "관찰/누적", Detail: "candidates Observe + source_domains 누적", Agent: "Observer", MetricKey: "candidates"},
				{Num: "B5", Name: "후보 게이트", Detail: "junk reject + 회색대 판정", Agent: "Gatekeeper",
					LLMUsed: true, ProviderKey: "", ProviderDef: "", EffortKey: "GATEKEEPER", EffortDef: "medium", Supervised: true, MetricKey: "candidates"},
				{Num: "B6", Name: "분류", Detail: "unknown→type · resolve · 2매체 승급", Agent: "Classifier",
					LLMUsed: true, ProviderKey: "CLASSIFY", ProviderDef: "gemma", EffortKey: "CLASSIFY", EffortDef: "medium", Supervised: true,
					Warn: "전용 RoleClassifier 미등록(phantom) — step:* 이 수행", MetricKey: "unknown"},
				{Num: "B7", Name: "인물 동기화", Detail: "person ↔ kwave_persons · 고아 정합", Agent: "PersonExtractor",
					LLMUsed: true, ProviderKey: "", ProviderDef: "", EffortKey: "RECONCILE", EffortDef: "medium", Supervised: true},
				{Num: "B8", Name: "동명이인", Detail: "alias 클러스터 병합/분리 · 자모복구", Agent: "Disambiguator",
					LLMUsed: true, ProviderKey: "DISAMBIG", ProviderDef: "codex", EffortKey: "", EffortDef: "", Supervised: true, MetricKey: "disambig"},
				{Num: "B9", Name: "dataqa(20m)", Detail: "로마자/CJK 오염 탐지·정리(감사·복구가능)", Agent: "DataQA",
					LLMUsed: true, ProviderKey: "DATAQA", ProviderDef: "codex", EffortKey: "", EffortDef: "", Supervised: false, Warn: supB, MetricKey: "dataqa"},
				{Num: "B10", Name: "현지표기 검색보강(LocalFill)", Detail: "KDB 자체(SearXNG 메타검색)+gemma 다회투표 → 2단계 확정-승급(약→local-search 빈칸, 강=만장일치+grounding→local-usage tier1) · 재그라운딩(codex-fallback 교체, gemma 불확실시 gpt-5.5 교정). server22 불요", Agent: "LocalFill",
					LLMUsed: true, ProviderLiteral: "gemma+gpt-5.5(교정)", Supervised: true, MetricKey: "localusage"},
				{Num: "B11", Name: "cascade L2/L3", Detail: "MusicBrainz/TMDb/KOFIC/Wikidata 채움", Agent: "SourceFetch"},
				{Num: "B12", Name: "cascade L4", Detail: "빈칸 LLM 합성(codex-fallback)", Agent: "Enricher",
					LLMUsed: true, ProviderKey: "FILL", ProviderDef: "gemma", EffortKey: "FILL", EffortDef: "medium", Supervised: true, MetricKey: "backlog"},
				{Num: "B13", Name: "fallback 검증·승급", Detail: "codex-fallback → Wikidata QID 라벨로 재검증·승급(QID 보유분). QID 없는 사각지대는 B10 재그라운딩이 담당", Agent: "FillVerifier",
					LLMUsed: true, ProviderKey: "FILLVERIFY", ProviderDef: "gemma", EffortKey: "FILLVERIFY", EffortDef: "low", Supervised: true,
					Warn: "KDB_VERIFY_CODEX_FALLBACK=1 일 때만 승급 실행", MetricKey: "fallback"},
				{Num: "B14", Name: "finalizer ×6", Detail: "DrainOnDemand·FillPersonDetails·DedupEn·SweepContam·ScopeReview·clearDisambig", Agent: "runTail", Warn: supB},
			},
		},
	}
}

// heartbeatVM is one autonomy heartbeat (a loop's most-recent run).
type heartbeatVM struct {
	Label string
	Ago   string // "12분 전" | "—"
	Stale bool   // exceeded threshold → red
}

// selfHealVM — #7 자가복구 표시: 양방향 자가복구 메시(16차)의 현재 활성 상태 +
// heartbeat stall 요약. codex breaker 가 열리면 codex-role(동명이인/dataqa/정정검증)이
// gemma 로 인계되고, gemma incident 가 열리면 CLASSIFY/FILL 이 codex 로 폴백된다.
// 평소엔 둘 다 정상(inert) — 페이지에서 한눈에 장애·인계 여부를 본다.
type selfHealVM struct {
	CodexDown   bool     // codex breaker open → gemma 인계 중
	GemmaDown   bool     // gemma incident open → codex 폴백 중
	Healthy     bool     // 둘 다 정상
	StaleCount  int      // stall 난 heartbeat 수
	StaleLabels []string // stall 난 loop 라벨
}

// selfHealStatus — 라이브 자가복구 상태를 조립한다. codexcli.GemmaDown/CodexDown 훅이
// 참조하는 동일한 소스(kdb.BreakerIsOpen·kdb.GemmaHealthy)를 읽어 페이지가 실제 라우팅과
// 일치한다. heartbeats 의 Stale 을 모아 stall 경고로 노출.
func (s *Server) selfHealStatus(heartbeats []heartbeatVM) selfHealVM {
	vm := selfHealVM{
		CodexDown: kdb.BreakerIsOpen(),
		GemmaDown: !kdb.GemmaHealthy(),
	}
	vm.Healthy = !vm.CodexDown && !vm.GemmaDown
	for _, hb := range heartbeats {
		if hb.Stale {
			vm.StaleCount++
			vm.StaleLabels = append(vm.StaleLabels, hb.Label)
		}
	}
	return vm
}

// workflowStats are the live numbers attached to steps. Every field is best
// effort: a failed/absent query leaves it at zero so the page still renders.
type workflowStats struct {
	CandidatesPending  int
	Obs24h             int
	RawPending         int
	UnknownActive      int
	NeedsDisambig      int
	DataQA24h          int
	CorrectionsPending int
	EnrichBacklog      int
	CodexFallbackPct   int
	AvgLocalePct       int
	LocalUsage         int
	LocalSearch        int
	ReqMatch7d         int
	ReqLookup7d        int
	ReqCorrections7d   int
}

// collectWorkflowStats runs the (cheap, bounded) live aggregations. nil pool or
// any per-query error → that stat stays zero. enrichBacklog is passed in so we
// reuse the already-computed value instead of re-running the heavy query.
func (s *Server) collectWorkflowStats(r *http.Request, enrichBacklog int) workflowStats {
	st := workflowStats{EnrichBacklog: enrichBacklog}
	if s.pool == nil {
		return st
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	q := func(sql string, dst *int) { _ = s.pool.QueryRow(ctx, sql).Scan(dst) }

	q(`SELECT count(*) FROM kwave_entities WHERE status='candidate'`, &st.CandidatesPending)
	q(`SELECT count(*) FROM kwave_media_observations WHERE observed_at > now() - interval '24 hours'`, &st.Obs24h)
	q(`SELECT count(*) FROM kwave_rss_items_raw WHERE codex_status='pending'`, &st.RawPending)
	q(`SELECT count(*) FROM kwave_entities WHERE status='active' AND entity_type='unknown'`, &st.UnknownActive)
	q(`SELECT count(*) FROM kwave_entities WHERE needs_disambig AND status IN ('active','candidate')`, &st.NeedsDisambig)
	q(`SELECT count(*) FROM kwave_kdb_dataqa_log WHERE created_at > now() - interval '24 hours'`, &st.DataQA24h)
	q(`SELECT count(*) FROM kwave_kdb_corrections WHERE resolved_at IS NULL`, &st.CorrectionsPending)
	q(`SELECT COALESCE(count(*) FILTER (WHERE path='/v1/entities/match'),0) FROM kwave_kdb_api_requests WHERE created_at > now() - interval '7 days'`, &st.ReqMatch7d)
	q(`SELECT COALESCE(count(*) FILTER (WHERE path='/v1/lookup'),0) FROM kwave_kdb_api_requests WHERE created_at > now() - interval '7 days'`, &st.ReqLookup7d)
	q(`SELECT COALESCE(count(*) FILTER (WHERE path='/v1/corrections'),0) FROM kwave_kdb_api_requests WHERE created_at > now() - interval '7 days'`, &st.ReqCorrections7d)

	// codex-fallback share across all locale source columns (the quality soft-spot).
	q(`SELECT COALESCE(round(100.0 * count(*) FILTER (WHERE src='codex-fallback') / NULLIF(count(*),0)),0)::int FROM (
	     SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,
	                         canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) src
	       FROM kwave_entities WHERE status='active') t WHERE COALESCE(src,'')<>''`, &st.CodexFallbackPct)
	// average per-locale canonical fill % over active entities. COALESCE so a
	// NULL canonical counts as empty (not dropped from the avg — that would
	// inflate the number; an honest figure must treat NULL as a gap).
	q(`SELECT COALESCE(round((
	      avg((COALESCE(canonical_en,'')<>'')::int)+avg((COALESCE(canonical_ja,'')<>'')::int)
	    + avg((COALESCE(canonical_vi,'')<>'')::int)+avg((COALESCE(canonical_id,'')<>'')::int)
	    + avg((COALESCE(canonical_es,'')<>'')::int)+avg((COALESCE(canonical_pt_br,'')<>'')::int)
	    + avg((COALESCE(canonical_zh,'')<>'')::int)+avg((COALESCE(canonical_zh_hant,'')<>'')::int)
	    )*100.0/8),0)::int FROM kwave_entities WHERE status='active'`, &st.AvgLocalePct)

	// local-usage(검색그라운드 확정·tier1 승급) / local-search(잠정·빈칸) 산출 추이 — 한 스캔.
	// 둘 다 0이면 서버22 QA 워커가 fills 를 안 보내는 상태(정직한 갭 노출).
	_ = s.pool.QueryRow(ctx, `SELECT
	      COALESCE(count(*) FILTER (WHERE src='local-usage'),0),
	      COALESCE(count(*) FILTER (WHERE src='local-search'),0)
	    FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,
	                              canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) src
	          FROM kwave_entities WHERE status='active') t`).Scan(&st.LocalUsage, &st.LocalSearch)
	return st
}

// pipelineHeartbeats reports each autonomous loop's most-recent run. Stale
// thresholds are generous multiples of each loop's interval.
func (s *Server) pipelineHeartbeats(r *http.Request) []heartbeatVM {
	specs := []struct {
		label, sql   string
		staleMinutes float64
	}{
		{"수집(poll·15m)", `SELECT max(started_at) FROM kwave_kdb_poll_cycles`, 45},
		{"추출(fast·30s)", `SELECT max(observed_at) FROM kwave_media_observations`, 30},
		{"autopilot(30m)", `SELECT max(ran_at) FROM kwave_kdb_autopilot_log`, 90},
		{"Hermes 감독", `SELECT max(created_at) FROM kwave_kdb_hermes_runs`, 90},
		{"dataqa(20m)", `SELECT max(created_at) FROM kwave_kdb_dataqa_log`, 60},
	}
	out := make([]heartbeatVM, 0, len(specs))
	if s.pool == nil {
		for _, sp := range specs {
			out = append(out, heartbeatVM{Label: sp.label, Ago: "—", Stale: true})
		}
		return out
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	for _, sp := range specs {
		hb := heartbeatVM{Label: sp.label, Ago: "—", Stale: true}
		var last *time.Time
		if err := s.pool.QueryRow(ctx, sp.sql).Scan(&last); err == nil && last != nil {
			d := time.Since(*last)
			hb.Ago = humanizeAgo(d)
			hb.Stale = d > time.Duration(sp.staleMinutes)*time.Minute
		}
		out = append(out, hb)
	}
	return out
}

func humanizeAgo(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d초 전", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d분 전", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d시간 전", int(d.Hours()))
	default:
		return fmt.Sprintf("%d일 전", int(d.Hours()/24))
	}
}

// buildWorkflowTracks resolves the static specs into view-models: live provider/
// effort (via the shared resolve helpers, so env + GemmaDown are honored) and a
// live metric string per step.
func (s *Server) buildWorkflowTracks(st workflowStats) []workflowTrackVM {
	var out []workflowTrackVM
	for _, ts := range workflowSpecs() {
		tvm := workflowTrackVM{Key: ts.Key, Title: ts.Title, Desc: ts.Desc}
		for _, sp := range ts.Steps {
			step := workflowStepVM{
				Num: sp.Num, Name: sp.Name, Detail: sp.Detail, Agent: sp.Agent,
				LLMUsed: sp.LLMUsed, Supervised: sp.Supervised, Warn: sp.Warn,
				Metric: workflowMetric(sp.MetricKey, st),
			}
			if sp.LLMUsed {
				if sp.ProviderLiteral != "" {
					step.Provider = sp.ProviderLiteral
				} else {
					step.Provider = hermesResolveProvider(sp.ProviderKey, sp.ProviderDef)
					step.Effort = hermesResolveEffort(sp.EffortKey, sp.EffortDef)
				}
			}
			tvm.Steps = append(tvm.Steps, step)
		}
		out = append(out, tvm)
	}
	return out
}

// workflowMetric renders the live metric string for a step's MetricKey.
func workflowMetric(key string, st workflowStats) string {
	switch key {
	case "lookup":
		return fmt.Sprintf("7d 요청 %d", st.ReqLookup7d)
	case "match":
		return fmt.Sprintf("7d 요청 %d", st.ReqMatch7d)
	case "corrections":
		return fmt.Sprintf("미해결 %d · 7d %d", st.CorrectionsPending, st.ReqCorrections7d)
	case "coverage":
		return fmt.Sprintf("평균 언어 확보율 %d%%", st.AvgLocalePct)
	case "obs24h":
		return fmt.Sprintf("24h 관측 %d", st.Obs24h)
	case "rawpending":
		return fmt.Sprintf("raw pending %d", st.RawPending)
	case "candidates":
		return fmt.Sprintf("candidate %d", st.CandidatesPending)
	case "unknown":
		return fmt.Sprintf("active unknown %d", st.UnknownActive)
	case "disambig":
		return fmt.Sprintf("needs_disambig %d", st.NeedsDisambig)
	case "dataqa":
		return fmt.Sprintf("24h 검수 %d", st.DataQA24h)
	case "backlog":
		return fmt.Sprintf("backlog %d", st.EnrichBacklog)
	case "fallback":
		return fmt.Sprintf("codex-fallback %d%%", st.CodexFallbackPct)
	case "localusage":
		return fmt.Sprintf("local-usage %d · local-search %d", st.LocalUsage, st.LocalSearch)
	}
	return ""
}
