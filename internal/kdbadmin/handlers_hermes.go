package kdbadmin

import (
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// hermesRunVM is the view-model for one role's most-recent run, read from
// kwave_kdb_hermes_runs (migration 0061).
type hermesRunVM struct {
	Role         string
	Status       string // ok | incident | retried | leak
	Severity     string // info | warning | critical
	StatusClass  string // badge css class: ok | warn | err | muted
	ItemsIn      int
	ItemsOut     int
	ItemsDropped int
	Retries      int
	SelfCheckOK  bool
	MetricBefore string // formatted; "–" when null
	MetricAfter  string
	CreatedAt    string
}

// hermesEnrichVM is the Enricher convergence dashboard: how much fillable
// backlog remains (exhaustion-aware, == the agent's Select universe), how many
// field-gaps are provably source-exhausted, recent throughput, and a rough ETA.
type hermesEnrichVM struct {
	Backlog   int    // entities with ≥1 empty, non-exhausted fillable field
	Exhausted int    // ledger rows marked source-exhausted (within 7d cooldown)
	Done24h   int    // enriched count summed from autopilot_log over 24h
	PerHour   int    // Done24h / 24
	ETA       string // "~Nh" estimate, or "–"
}

// hermesAgentVM is one agent inside a domain: a dedicated role-agent ("전용
// agent") or a wrapped legacy sweep step ("레거시 step"). Run is the agent's
// most-recent run, or nil when the role is defined but has never run (e.g. the
// Classifier role-agent, which is declared but not registered — the legacy
// step:* steps do the classifying). A nil Run is surfaced, not hidden.
type hermesAgentVM struct {
	Role       string
	Kind       string // 전용 agent | 레거시 step | 미분류
	Registered bool   // false → declared but never wired into the registry (phantom)
	Run        *hermesRunVM
}

// hermesDomainVM is one functional domain (분류/후보게이트/인물/보강/명명·동명이인)
// with its resolved LLM backend and the agents that work it. Provider/Effort are
// resolved at render time via the SAME codexcli.RoleProvider/RoleEffort the
// agents call, so the page always reflects live env (KDB_LLM_*, CODEX_EFFORT_*,
// KDB_LLM_PROVIDER) and the GemmaDown auto-fallback.
type hermesDomainVM struct {
	Title      string
	Desc       string
	Provider   string // resolved: gemma | codex
	Effort     string // resolved reasoning effort (codex only; gemma ignores it)
	LLMNote    string // env keys that drive this domain's routing
	HasPrimary bool
	Primary    hermesAgentVM
	Steps      []hermesAgentVM
}

// hermesDomainSpec is the static map of a domain to its dedicated role-agent,
// the legacy sweep steps that still run under it, and the routing keys its LLM
// calls use. ProviderKey/EffortKey == "" means the agent forces no override and
// inherits the global runner provider / CODEX_REASONING_EFFORT.
type hermesDomainSpec struct {
	Title       string
	Desc        string
	Primary     agents.Role // dedicated role-agent ("" if none)
	Steps       []agents.Role
	ProviderKey string // RoleProvider key; "" = inherits KDB_LLM_PROVIDER
	ProviderDef string
	EffortKey   string // RoleEffort key; "" = inherits CODEX_REASONING_EFFORT
	EffortDef   string
	// PrimaryPhantom marks a Primary role that is DECLARED (a Role const) but
	// not wired into the registry, so it never runs — the legacy step:* rows do
	// its work instead. Surfaced as "미등록" rather than hidden. Only Classify.
	PrimaryPhantom bool
}

// hermesDomainSpecs mirrors the live wiring: which role-agent (RegisterRoleAgents)
// and legacy steps (RegisterSteps, internal/kdb/autopilot/agents.go) belong to
// each domain, and each domain's routing keys (the RoleProvider/RoleEffort call
// sites in the agents). Keep in sync with those files.
func hermesDomainSpecs() []hermesDomainSpec {
	return []hermesDomainSpec{
		{
			Title: "분류 (Classify)",
			Desc:  "엔티티 타입 판정 · unknown 해소 · 합의 승급",
			// RoleClassifier 는 정의만 됨(미등록) — 분류는 step:* 가 수행. 유령 표시.
			Primary: agents.RoleClassifier,
			Steps: []agents.Role{
				agents.RoleStepClassifyUnknown,
				agents.RoleStepResolveUnknowns,
				agents.RoleStepPromoteConsensus,
			},
			ProviderKey: "CLASSIFY", ProviderDef: "gemma",
			EffortKey: "CLASSIFY", EffortDef: "medium",
			PrimaryPhantom: true,
		},
		{
			Title:   "후보 게이트 (Candidate Gate)",
			Desc:    "신규 후보 junk 거름 · 정밀 use/reject · 회색대만 LLM 판정",
			Primary: agents.RoleCandidateGatekeeper,
			Steps:   []agents.Role{agents.RoleStepReviewCandidates},
			// provider 미강제 → 전역 KDB_LLM_PROVIDER 상속.
			ProviderKey: "", ProviderDef: "",
			EffortKey: "GATEKEEPER", EffortDef: "medium",
		},
		{
			Title:   "인물 (Persons)",
			Desc:    "person 엔티티 ↔ 인물 DB 동기화 · 고아 legacy 정합",
			Primary: agents.RolePersonExtractor,
			Steps:   []agents.Role{agents.RoleStepSyncPersons},
			ProviderKey: "", ProviderDef: "",
			EffortKey: "RECONCILE", EffortDef: "medium",
		},
		{
			Title:   "보강 (Enrich)",
			Desc:    "빈 필드 갭필(L2 MusicBrainz→L3 Wikidata→L4 LLM) · 품질 재검토",
			Primary: agents.RoleEnricher,
			Steps:   []agents.Role{agents.RoleStepEnrichEmpty, agents.RoleStepQualityReview},
			ProviderKey: "FILL", ProviderDef: "gemma",
			EffortKey: "FILL", EffortDef: "medium",
		},
		{
			Title:   "명명 · 동명이인 (Naming & Disambiguation)",
			Desc:    "alias 병합 · 동명이인 분리 · 깨진 자모 복구 · alias 충돌 해소",
			Primary: agents.RoleDisambiguator,
			Steps:   []agents.Role{agents.RoleStepRepairBrokenJamo, agents.RoleStepResolveAliasConflicts},
			ProviderKey: "DISAMBIG", ProviderDef: "codex",
			// effort 미강제 → 전역 CODEX_REASONING_EFFORT 상속.
			EffortKey: "", EffortDef: "",
		},
	}
}

// hermesResolveProvider resolves a domain's live LLM backend exactly as the
// runner would at call time: a forced RoleProvider(key,def) when the agent sets
// one, otherwise the global KDB_LLM_PROVIDER (codex when unset). GemmaDown
// auto-fallback is honored via RoleProvider.
func hermesResolveProvider(key, def string) string {
	if key == "" {
		def = strings.TrimSpace(os.Getenv("KDB_LLM_PROVIDER"))
		if def == "" {
			return "codex" // Run() takes the codex CLI path when nothing routes to gemma
		}
		key = "_GLOBAL_" // no KDB_LLM__GLOBAL_ env → returns def, still applies GemmaDown
	}
	return codexcli.RoleProvider(key, def)
}

// hermesResolveEffort resolves a domain's reasoning effort, mirroring the
// agents: RoleEffort(key,def) when forced, else the global CODEX_REASONING_EFFORT.
func hermesResolveEffort(key, def string) string {
	if key == "" {
		if e := strings.TrimSpace(os.Getenv("CODEX_REASONING_EFFORT")); e != "" {
			return e
		}
		return "(codex 기본)"
	}
	return codexcli.RoleEffort(key, def)
}

// hermesLLMNote names the env keys that drive a domain's routing, so an operator
// knows exactly what to flip to re-route it.
func hermesLLMNote(spec hermesDomainSpec) string {
	p := "KDB_LLM_PROVIDER(전역)"
	if spec.ProviderKey != "" {
		p = "KDB_LLM_" + spec.ProviderKey
	}
	e := "CODEX_REASONING_EFFORT(전역)"
	if spec.EffortKey != "" {
		e = "CODEX_EFFORT_" + spec.EffortKey
	}
	return p + " · " + e
}

// buildHermesDomains groups the latest-run-per-role rows into the functional
// domains, resolving each domain's live LLM backend. Any recorded role not
// mapped to a domain is returned separately so nothing is silently hidden.
func buildHermesDomains(byRole map[string]hermesRunVM) ([]hermesDomainVM, []hermesAgentVM) {
	consumed := make(map[string]bool)
	agentVM := func(role agents.Role, kind string, registered bool) hermesAgentVM {
		av := hermesAgentVM{Role: string(role), Kind: kind, Registered: registered}
		if run, ok := byRole[string(role)]; ok {
			rc := run
			av.Run = &rc
			consumed[string(role)] = true
		}
		return av
	}

	var out []hermesDomainVM
	for _, spec := range hermesDomainSpecs() {
		dvm := hermesDomainVM{
			Title:    spec.Title,
			Desc:     spec.Desc,
			Provider: hermesResolveProvider(spec.ProviderKey, spec.ProviderDef),
			Effort:   hermesResolveEffort(spec.EffortKey, spec.EffortDef),
			LLMNote:  hermesLLMNote(spec),
		}
		if spec.Primary != "" {
			dvm.Primary = agentVM(spec.Primary, "전용 agent", !spec.PrimaryPhantom)
			dvm.HasPrimary = true
		}
		for _, st := range spec.Steps {
			dvm.Steps = append(dvm.Steps, agentVM(st, "레거시 step", true))
		}
		out = append(out, dvm)
	}

	// Leftover: any recorded role not claimed by a domain (honest visibility —
	// e.g. a future role, or Bridge). Normally empty.
	var other []hermesAgentVM
	leftover := make([]string, 0)
	for role := range byRole {
		if !consumed[role] {
			leftover = append(leftover, role)
		}
	}
	sort.Strings(leftover)
	for _, role := range leftover {
		run := byRole[role]
		rc := run
		other = append(other, hermesAgentVM{Role: role, Kind: "미분류", Registered: true, Run: &rc})
	}
	return out, other
}

// hermesIncidentVM is the view-model for one unresolved incident / leak.
type hermesIncidentVM struct {
	ID           int64
	Role         string
	Status       string
	Severity     string
	ItemsDropped int
	ErrorText    string
	CreatedAt    string
}

// handleHermes renders the Hermes operator-accountability report: the latest
// run per role (status / items in→out→dropped / retries / self-check /
// metric before→after), all open incidents, and the detected conservation
// leaks. The page renders cleanly with zero rows ("no runs yet") because
// kwave_kdb_hermes_runs is empty until Hermes runs live (KDB_HERMES_ENABLED=1).
func (s *Server) handleHermes(w http.ResponseWriter, r *http.Request) {
	runs := s.latestHermesRunsPerRole(r)
	incidents := s.openHermesIncidents(r)

	// Group the latest-run-per-role rows into functional domains (분류/후보게이트/
	// 인물/보강/명명·동명이인), each annotated with its live LLM backend.
	byRole := make(map[string]hermesRunVM, len(runs))
	for _, run := range runs {
		byRole[run.Role] = run
	}
	domains, other := buildHermesDomains(byRole)

	// The page's primary structure: the actual end-to-end workflow as two tracks
	// (요청 처리 / 자율 품질), each step annotated with its owning agent, live LLM
	// backend (or "LLM 미투입"), Hermes-supervision status, a live metric, and an
	// honest gap warning. The LLM-role cards (domains) are absorbed below it.
	enrich := s.enrichBacklogStats(r)
	stats := s.collectWorkflowStats(r, enrich.Backlog)
	tracks := s.buildWorkflowTracks(stats)
	heartbeats := s.pipelineHeartbeats(r)
	selfHeal := s.selfHealStatus(heartbeats) // #7 자가복구 활성상태 + stall 경고

	// Leaks are the subset of latest runs whose conservation invariant was
	// violated (status='leak' or items_dropped>0) — surfaced separately so the
	// operator sees silent-drop accounting at a glance.
	var leaks []hermesRunVM
	for _, run := range runs {
		if run.Status == "leak" || run.ItemsDropped > 0 {
			leaks = append(leaks, run)
		}
	}

	s.render(w, r, "kdb_hermes.html", map[string]any{
		// title/page 는 소문자 키 컨벤션(partials.html <title>={{.title}},
		// nav 하이라이트=eq $.page .Path). 기존 "Title"/"Active" 대문자는 어느
		// 템플릿도 안 읽어 제목 빈값·nav 미강조였음 — 다른 핸들러와 정합.
		"title":      "Hermes",
		"page":       "/admin/hermes",
		"Tracks":     tracks,
		"Heartbeats": heartbeats,
		"SelfHeal":   selfHeal,
		"Enrich":     enrich,
		"Domains":    domains,
		"Other":      other,
		"Incidents":  incidents,
		"Leaks":      leaks,
	})
}

// enrichBacklogStats computes the Enricher convergence dashboard. backlog mirrors
// the agent's exhaustion-aware Select (internal/kdb/agents/enricher/agent.go) so
// the operator sees the same universe the agent works; Done24h/PerHour come from
// the autopilot cycle log. Returns a zero VM on any error / nil pool.
func (s *Server) enrichBacklogStats(r *http.Request) hermesEnrichVM {
	var vm hermesEnrichVM
	if s.pool == nil {
		return vm
	}
	ctx := r.Context()
	// backlog: ≥1 fillable field empty AND that field not source-exhausted (7d).
	const qBacklog = `
SELECT count(*) FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
  LEFT JOIN LATERAL (
    SELECT COALESCE(array_agg(a.field) FILTER (
             WHERE a.exhausted AND a.last_attempt_at > now() - interval '7 days'), '{}') AS f
      FROM kwave_kdb_enrich_attempts a WHERE a.entity_id = e.id) ex ON true
 WHERE e.status='active' AND e.operator_locked = false AND e.entity_type <> 'unknown'
   AND (
        (COALESCE(e.canonical_en,'')=''      AND NOT 'canonical_en'      = ANY(ex.f))
     OR (COALESCE(e.canonical_ja,'')=''      AND NOT 'canonical_ja'      = ANY(ex.f))
     OR (COALESCE(e.canonical_vi,'')=''      AND NOT 'canonical_vi'      = ANY(ex.f))
     OR (COALESCE(e.canonical_id,'')=''      AND NOT 'canonical_id'      = ANY(ex.f))
     OR (COALESCE(e.canonical_es,'')=''      AND NOT 'canonical_es'      = ANY(ex.f))
     OR (COALESCE(e.canonical_pt_br,'')=''   AND NOT 'canonical_pt_br'   = ANY(ex.f))
     OR (COALESCE(e.canonical_zh,'')=''      AND NOT 'canonical_zh'      = ANY(ex.f))
     OR (COALESCE(e.canonical_zh_hant,'')='' AND NOT 'canonical_zh_hant' = ANY(ex.f))
     OR ((e.aliases_ko IS NULL OR array_length(e.aliases_ko,1) IS NULL)
                                             AND NOT 'aliases_ko'        = ANY(ex.f))
     OR (e.entity_type='person' AND (
            (COALESCE(d.agency,'')=''                  AND NOT 'agency'          = ANY(ex.f))
         OR (d.birth_year IS NULL                      AND NOT 'birth_year'      = ANY(ex.f))
         OR (COALESCE(d.gender,'')=''                  AND NOT 'gender'          = ANY(ex.f))
         OR (array_length(d.groups,1) IS NULL          AND NOT 'groups'          = ANY(ex.f))
         OR (array_length(d.notable_works,1) IS NULL   AND NOT 'notable_works'   = ANY(ex.f))
         OR (array_length(d.secondary_roles,1) IS NULL AND NOT 'secondary_roles' = ANY(ex.f))
         OR ((d.primary_role IS NULL OR d.primary_role='other')
                                                       AND NOT 'primary_role'    = ANY(ex.f))
        ))
   )`
	_ = s.pool.QueryRow(ctx, qBacklog).Scan(&vm.Backlog)
	_ = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM kwave_kdb_enrich_attempts
		  WHERE exhausted AND last_attempt_at > now() - interval '7 days'`).Scan(&vm.Exhausted)
	_ = s.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(enriched),0) FROM kwave_kdb_autopilot_log
		  WHERE ran_at > now() - interval '24 hours'`).Scan(&vm.Done24h)

	vm.PerHour = vm.Done24h / 24
	vm.ETA = "–"
	if vm.PerHour > 0 && vm.Backlog > 0 {
		vm.ETA = "~" + strconv.Itoa((vm.Backlog+vm.PerHour-1)/vm.PerHour) + "h"
	}
	return vm
}

// latestHermesRunsPerRole returns the most recent run row per role. Returns nil
// on any error / when the pool is unset so the page still renders its empty
// state.
func (s *Server) latestHermesRunsPerRole(r *http.Request) []hermesRunVM {
	if s.pool == nil {
		return nil
	}
	const q = `
SELECT DISTINCT ON (role)
       role, status, COALESCE(severity,''), items_in, items_out, items_dropped,
       retries, self_check_ok, metric_before, metric_after,
       to_char(created_at, 'YYYY-MM-DD HH24:MI:SS')
  FROM kwave_kdb_hermes_runs
 ORDER BY role, created_at DESC`
	rows, err := s.pool.Query(r.Context(), q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []hermesRunVM
	for rows.Next() {
		var v hermesRunVM
		var before, after *float64
		if err := rows.Scan(
			&v.Role, &v.Status, &v.Severity, &v.ItemsIn, &v.ItemsOut, &v.ItemsDropped,
			&v.Retries, &v.SelfCheckOK, &before, &after, &v.CreatedAt,
		); err != nil {
			return out
		}
		v.MetricBefore = fmtMetric(before)
		v.MetricAfter = fmtMetric(after)
		v.StatusClass = hermesBadgeClass(v.Status, v.Severity)
		out = append(out, v)
	}
	return out
}

// openHermesIncidents returns unresolved non-ok rows (incidents + leaks),
// newest first — the same set the supervisor's "open" partial index covers.
func (s *Server) openHermesIncidents(r *http.Request) []hermesIncidentVM {
	if s.pool == nil {
		return nil
	}
	const q = `
SELECT id, role, status, COALESCE(severity,''), items_dropped, COALESCE(error_text,''),
       to_char(created_at, 'YYYY-MM-DD HH24:MI:SS')
  FROM kwave_kdb_hermes_runs
 WHERE status <> 'ok' AND resolved = false
 ORDER BY created_at DESC
 LIMIT 200`
	rows, err := s.pool.Query(r.Context(), q)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []hermesIncidentVM
	for rows.Next() {
		var v hermesIncidentVM
		if err := rows.Scan(&v.ID, &v.Role, &v.Status, &v.Severity, &v.ItemsDropped, &v.ErrorText, &v.CreatedAt); err != nil {
			return out
		}
		out = append(out, v)
	}
	return out
}

// fmtMetric renders a nullable metric scalar for the report.
func fmtMetric(p *float64) string {
	if p == nil {
		return "–"
	}
	return strconv.FormatFloat(*p, 'f', 3, 64)
}

// hermesBadgeClass maps a run status/severity to the admin's badge CSS class
// (badge {ok|warn|err|muted}; see templates/partials.html).
func hermesBadgeClass(status, severity string) string {
	switch status {
	case "ok":
		return "ok"
	case "leak":
		return "err"
	case "incident":
		if severity == "critical" {
			return "err"
		}
		return "warn"
	case "retried":
		return "warn"
	default:
		return "muted"
	}
}
