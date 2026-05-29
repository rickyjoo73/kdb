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
		"Title":     "Hermes",
		"Active":    "hermes",
		"Runs":      runs,
		"Incidents": incidents,
		"Leaks":     leaks,
	})
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
