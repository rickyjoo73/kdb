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
   error_text, report, self_check_ok, resolved)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)`,
		out.RunID, string(out.Role), nullUUID(cycleID), startedAt, finished,
		out.Status, nullStr(out.Severity),
		itemsIn, itemsOut, dropped, out.Retries,
		metricBefore, metricAfter,
		nullStr(errText), reportJSON, rep.SelfCheck.Pass, out.Resolved)
	if err != nil {
		log.Printf("hermes.record(%s): %v", out.Role, err)
	}
}

// resolveOpenIncidents marks a role's prior unresolved non-ok runs as resolved
// once that role completes a healthy run — so transient/外부 의존성 인시던트
// (예: 과거 breaker-open 기간)이 복구 후 운영자 뷰에서 self-clean 된다. 방금
// 기록한 성공 run(exceptRunID)은 건드리지 않는다. best-effort.
func (s *Supervisor) resolveOpenIncidents(ctx context.Context, role agents.Role, exceptRunID uuid.UUID) {
	if s == nil || s.Pool == nil {
		return
	}
	if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_kdb_hermes_runs
   SET resolved = true
 WHERE role = $1 AND resolved = false AND status <> 'ok' AND run_id <> $2`,
		string(role), exceptRunID); err != nil {
		log.Printf("hermes.resolveOpenIncidents(%s): %v", role, err)
	}
}

// persistAutopilotLog writes one summary row into kwave_kdb_autopilot_log for a
// completed cycle, aggregated from the kwave_kdb_hermes_runs rows just recorded
// under cycleID. This keeps the admin dashboard's autopilot-cycle table alive
// under Hermes, where the plain auto.Run persistLog path is bypassed. Mapping
// is role→category using items_out (=Acted); classify_deferred is the selected
// items the classify roles did not act on (items_in-items_out). Best-effort:
// a missing table (migration 0064 not applied) or any error is logged, never
// fatal — it must not affect the cycle.
func (s *Supervisor) persistAutopilotLog(ctx context.Context, cycleID uuid.UUID) {
	if s == nil || s.Pool == nil {
		return
	}
	if _, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_kdb_autopilot_log
  (ran_at, duration_ms, jamo_merged, jamo_rejected, persons_added,
   entity_type_fixed, non_entity_reject, classified, classify_deferred,
   promoted, enriched, quality_fixed, alias_resolved)
SELECT
  min(started_at),
  COALESCE(EXTRACT(EPOCH FROM (max(COALESCE(finished_at, started_at)) - min(started_at))) * 1000, 0)::int,
  COALESCE(sum(items_out) FILTER (WHERE role = 'step:RepairBrokenJamo'), 0),
  0,
  COALESCE(sum(items_out) FILTER (WHERE role IN ('step:SyncPersons', 'PersonExtractor')), 0),
  0,
  COALESCE(sum(items_out) FILTER (WHERE role = 'CandidateGatekeeper'), 0),
  COALESCE(sum(items_out) FILTER (WHERE role IN ('step:ClassifyUnknown', 'step:ResolveUnknowns', 'Classifier')), 0),
  COALESCE(sum(GREATEST(items_in - items_out, 0)) FILTER (WHERE role IN ('step:ClassifyUnknown', 'step:ReviewCandidates', 'Classifier')), 0),
  COALESCE(sum(items_out) FILTER (WHERE role IN ('step:PromoteConsensus', 'step:ReviewCandidates')), 0),
  COALESCE(sum(items_out) FILTER (WHERE role IN ('step:EnrichEmpty', 'Enricher')), 0),
  COALESCE(sum(items_out) FILTER (WHERE role = 'step:QualityReview'), 0),
  COALESCE(sum(items_out) FILTER (WHERE role IN ('step:ResolveAliasConflicts', 'Disambiguator')), 0)
FROM kwave_kdb_hermes_runs
WHERE cycle_id = $1
HAVING count(*) > 0`, cycleID); err != nil {
		log.Printf("hermes.persistAutopilotLog: %v", err)
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
