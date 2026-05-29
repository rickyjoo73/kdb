package hermes

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Run status values (kwave_kdb_hermes_runs.status).
const (
	statusOK       = "ok"
	statusIncident = "incident"
	statusRetried  = "retried"
	statusLeak     = "leak"
)

// Severity values (kwave_kdb_hermes_runs.severity).
const (
	sevInfo     = "info"
	sevWarning  = "warning"
	sevCritical = "critical"
)

// IncidentTracker generalizes bridge_health.go's open/recover logic: a role
// fails N times consecutively → open an incident; a success → recover. It is
// the shared primitive Hermes uses per role AND that BridgeHealthCheck can
// build on (role="Bridge"). Concurrency-safe; keyed by role.
type IncidentTracker struct {
	mu        sync.Mutex
	failCount map[agents.Role]int
	open      map[agents.Role]bool
	threshold int
}

// NewIncidentTracker returns a tracker that opens an incident after
// `threshold` consecutive failures (default 3, matching bridge_health's
// bridgeFailThreshold).
func NewIncidentTracker(threshold int) *IncidentTracker {
	if threshold <= 0 {
		threshold = 3
	}
	return &IncidentTracker{
		failCount: make(map[agents.Role]int),
		open:      make(map[agents.Role]bool),
		threshold: threshold,
	}
}

// Record updates the consecutive-failure count for a role and reports the
// resulting transition. opened is true on the tick that crosses the threshold;
// recovered is true on the first success after an open incident.
func (t *IncidentTracker) Record(role agents.Role, ok bool) (opened, recovered bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if ok {
		if t.open[role] {
			t.open[role] = false
			t.failCount[role] = 0
			return false, true
		}
		t.failCount[role] = 0
		return false, false
	}
	t.failCount[role]++
	if t.failCount[role] >= t.threshold && !t.open[role] {
		t.open[role] = true
		return true, false
	}
	return false, false
}

// IsOpen reports whether a role currently has an open incident.
func (t *IncidentTracker) IsOpen(role agents.Role) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.open[role]
}

// record writes one kwave_kdb_hermes_runs row for a supervised outcome.
func (s *Supervisor) record(ctx context.Context, cycleID uuid.UUID, startedAt time.Time, out Outcome, errs []string) {
	if s == nil || s.Pool == nil {
		return
	}
	finished := time.Now()
	rep := out.Report
	rep.Role = out.Role
	rep.RunID = out.RunID
	if rep.StartedAt.IsZero() {
		rep.StartedAt = startedAt
	}
	rep.Duration = finished.Sub(startedAt)

	itemsIn := out.Leak.Selected
	if itemsIn == 0 {
		itemsIn = rep.Selected
	}
	itemsOut := rep.Acted
	dropped := len(out.Leak.Unaccounted) + len(out.Leak.Duplicated)

	errText := ""
	if len(errs) > 0 {
		errText = joinErrs(errs)
	}

	reportJSON := buildReportJSON(rep, out)

	var metricBefore, metricAfter *float64
	if !(out.Status == statusOK && itemsIn == 0) {
		mb, ma := out.MetricBefore, out.MetricAfter
		metricBefore, metricAfter = &mb, &ma
	}

	_, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_hermes_runs
  (run_id, role, cycle_id, started_at, finished_at, status, severity,
   items_in, items_out, items_dropped, retries, metric_before, metric_after,
   error_text, report, self_check_ok)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)`,
		out.RunID, string(out.Role), nullUUID(cycleID), startedAt, finished,
		out.Status, nullStr(out.Severity),
		itemsIn, itemsOut, dropped, out.Retries,
		metricBefore, metricAfter,
		nullStr(errText), reportJSON, rep.SelfCheck.Pass)
	if err != nil {
		log.Printf("hermes.record(%s): %v", out.Role, err)
	}
}

// RecordIncident writes a standalone incident row (no agent run) — used by the
// generalized bridge-health path and ad-hoc supervisor incidents.
func (s *Supervisor) RecordIncident(ctx context.Context, role agents.Role, status, severity, detail string) {
	if s == nil || s.Pool == nil {
		return
	}
	now := time.Now()
	det, _ := json.Marshal(map[string]string{"detail": detail})
	_, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_hermes_runs
  (run_id, role, started_at, finished_at, status, severity, error_text, report, self_check_ok)
VALUES ($1,$2,$3,$3,$4,$5,$6,$7,false)`,
		uuid.New(), string(role), now, status, nullStr(severity), nullStr(detail), det)
	if err != nil {
		log.Printf("hermes.RecordIncident(%s): %v", role, err)
	}
}

func buildReportJSON(rep agents.RunReport, out Outcome) []byte {
	payload := struct {
		Report      agents.RunReport `json:"report"`
		Leak        agents.Leak      `json:"leak"`
		CriterionOK bool             `json:"criterion_ok"`
		Detail      string           `json:"detail,omitempty"`
	}{Report: rep, Leak: out.Leak, CriterionOK: out.CriterionOK, Detail: out.Detail}
	b, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{}`)
	}
	return b
}

func joinErrs(errs []string) string {
	out := ""
	for i, e := range errs {
		if i > 0 {
			out += " | "
		}
		out += e
	}
	if len(out) > 4000 {
		out = out[:4000]
	}
	return out
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullUUID(u uuid.UUID) any {
	if u == uuid.Nil {
		return nil
	}
	return u
}
