package kdbadmin

import (
	"net/http"
	"strconv"
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
		"title":     "Hermes",
		"page":      "/admin/hermes",
		"Enrich":    s.enrichBacklogStats(r),
		"Runs":      runs,
		"Incidents": incidents,
		"Leaks":     leaks,
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
