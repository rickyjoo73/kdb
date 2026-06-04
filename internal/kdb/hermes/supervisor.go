// Package hermes — the supervisor that makes each KDB agent accountable
// (docs/KDB_HERMES_AGENTS_DESIGN.md §C).
//
// For each agent, per cycle, Hermes runs plan→run→verify→retry:
//
//  1. Plan   : agent.Select returns ids; snapshot a baseline success metric.
//  2. Run    : agent.Run with a per-cycle budget → RunReport (before/after).
//  3. Verify : re-measure the metric; evaluate the role's Criterion; run the
//     item-conservation (leak) detector over the RunReport.
//  4. Decide : criterion met & conserved & self-check ok → record an `ok` run;
//     otherwise open an incident (severity-classified) and, for a
//     leak, a `leak` run.
//  5. Retry  : exponential backoff with jitter, capped. Transient errors
//     (bridge/timeout — gated by the breaker) retry up to MaxRetries;
//     a logical failure (criterion unmet though the call succeeded)
//     gets at most LogicalRetries, then an incident — never an
//     infinite loop.
//
// The incident open/recover/breaker primitive is generalized from
// internal/kdb/bridge_health.go (BridgeHealthCheck N-consecutive-fail →
// incident; BreakerIsOpen/BreakerRecordResult). Those keep working unchanged;
// IncidentTracker (incident.go) is the shared open/recover core, and the
// breaker is reused via Hooks to avoid an import cycle.
package hermes

import (
	"context"
	"errors"
	"log"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Config tunes the supervisor loop. Zero value is usable via DefaultConfig.
type Config struct {
	MaxRetries     int           // transient-error retry cap (default 2)
	BaseBackoff    time.Duration // first backoff (default 2s)
	MaxBackoff     time.Duration // backoff ceiling (default 30s)
	LogicalRetries int           // retries for a met-criterion failure (default 1)
	DefaultBudget  int           // per-run budget passed to Select/Run (default 20)
}

// DefaultConfig returns the standard supervisor tuning.
func DefaultConfig() Config {
	return Config{
		MaxRetries:     2,
		BaseBackoff:    2 * time.Second,
		MaxBackoff:     30 * time.Second,
		LogicalRetries: 1,
		DefaultBudget:  20,
	}
}

// Hooks lets the supervisor reuse the existing circuit breaker
// (internal/kdb.BreakerIsOpen / BreakerRecordResult) without an import cycle:
// the cmd wires these in. All hooks are optional (nil → ignored).
type Hooks struct {
	BreakerIsOpen       func() bool
	BreakerRecordResult func(ok bool)
}

// Supervisor drives audited agent runs and records to kwave_kdb_hermes_runs.
type Supervisor struct {
	Pool    *pgxpool.Pool
	Config  Config
	Hooks   Hooks
	Tracker *IncidentTracker
}

// New returns a Supervisor with default config and a fresh incident tracker.
func New(pool *pgxpool.Pool) *Supervisor {
	return &Supervisor{Pool: pool, Config: DefaultConfig(), Tracker: NewIncidentTracker(3)}
}

// SuperviseCycle runs every agent in the registry under supervision, tagging
// them with a shared cycleID. Errors are logged, never fatal (mirrors the
// existing autopilot step error handling).
func (s *Supervisor) SuperviseCycle(ctx context.Context, reg *agents.Registry) {
	cycleID := uuid.New()
	for _, a := range reg.All() {
		if err := s.Supervise(ctx, a, cycleID); err != nil {
			log.Printf("hermes: %s: %v", a.Role(), err)
		}
	}
	// Mirror this cycle into kwave_kdb_autopilot_log so the admin dashboard's
	// "최근 autopilot cycle" 표 stays populated under Hermes (the plain
	// auto.Run persistLog path is bypassed when the supervisor drives the
	// steps). Aggregated from the hermes_runs rows just written for this cycle.
	s.persistAutopilotLog(ctx, cycleID)
}

// Outcome captures the supervised result of a single agent run.
type Outcome struct {
	Role         agents.Role
	RunID        uuid.UUID
	Status       string // ok | incident | retried | leak
	Severity     string // info | warning | critical
	Report       agents.RunReport
	Leak         agents.Leak
	MetricBefore float64
	MetricAfter  float64
	CriterionOK  bool
	Detail       string
	Retries      int
	Resolved     bool // true면 미해결 인시던트로 안 뜸(예: breaker-open 스킵 = 양성)
}

// errBreakerOpen — 외부 LLM 의존성(codex) 일시 차단으로 run 을 건너뛴 경우의 sentinel.
// 에이전트 결함이 아니라 정상적 방어이므로 critical incident 로 적재하지 않는다.
var errBreakerOpen = errors.New("circuit breaker open — skipping run")

// Supervise runs one agent through plan→run→verify→retry and records the
// result. Returns an error only for a hard run/select failure.
func (s *Supervisor) Supervise(ctx context.Context, a agents.Agent, cycleID uuid.UUID) error {
	budget := s.Config.DefaultBudget
	if budget <= 0 {
		budget = 20
	}
	runID := uuid.New()
	startedAt := time.Now()

	// --- Plan -------------------------------------------------------------
	ids, err := a.Select(ctx, s.Pool, budget)
	if err != nil {
		s.tracked(a.Role(), false)
		out := Outcome{Role: a.Role(), RunID: runID, Status: statusIncident, Severity: sevWarning,
			Detail: "select: " + err.Error()}
		s.record(ctx, cycleID, startedAt, out, []string{err.Error()})
		return err
	}
	if len(ids) == 0 {
		// Nothing to do — record a clean no-op ok row so the cycle is auditable.
		s.tracked(a.Role(), true)
		out := Outcome{Role: a.Role(), RunID: runID, Status: statusOK, Severity: sevInfo,
			CriterionOK: true, Detail: "no items selected"}
		out.Report = agents.RunReport{Role: a.Role(), RunID: runID, StartedAt: startedAt,
			SelfCheck: agents.SelfCheck{Pass: true}}
		s.record(ctx, cycleID, startedAt, out, nil)
		// "할 일 없음"도 건강한 상태 → 이 role 의 과거 미해결 인시던트 self-clean.
		s.resolveOpenIncidents(ctx, a.Role(), runID)
		return nil
	}

	crit := a.Criterion()
	before := s.measure(ctx, crit, ids)

	// --- Run + Verify + Retry --------------------------------------------
	var (
		rep        agents.RunReport
		leak       agents.Leak
		after      float64
		critOK     bool
		critDetail string
		runErr     error
		retries    int
	)
	in := agents.RunInput{RunID: runID, IDs: ids, Budget: budget}

	for attempt := 0; ; attempt++ {
		if s.breakerOpen() {
			runErr = errBreakerOpen
			break
		}
		rep, runErr = a.Run(ctx, s.Pool, in)
		s.breakerRecord(runErr == nil)

		if runErr != nil {
			// Transient: retry with backoff up to MaxRetries.
			if attempt < s.Config.MaxRetries {
				retries++
				s.backoff(ctx, attempt)
				continue
			}
			break
		}

		rep.Role, rep.RunID = a.Role(), runID
		rep.Summarize()
		leak = agents.ItemConservation(ids, rep.Results)
		after = s.measure(ctx, crit, ids)
		critOK, critDetail = crit.Met(before, after, rep)

		// Logical failure: calls succeeded but criterion unmet → at most
		// LogicalRetries, then stop (incident below). Never loop forever.
		if !critOK && retries < s.Config.LogicalRetries {
			retries++
			s.backoff(ctx, attempt)
			continue
		}
		break
	}

	out := Outcome{
		Role: a.Role(), RunID: runID, Report: rep, Leak: leak,
		MetricBefore: before, MetricAfter: after, CriterionOK: critOK,
		Retries: retries,
	}

	switch {
	case errors.Is(runErr, errBreakerOpen):
		// 외부 LLM 일시 차단으로 스킵 — 에이전트 결함 아님. 양성으로 적재하고
		// (resolved=true) 미해결 인시던트 뷰를 오염시키지 않는다. breaker 가
		// 다시 닫히면 다음 cycle 에 정상 run 으로 복귀한다.
		out.Status = statusIncident
		out.Severity = sevInfo
		out.Resolved = true
		out.Detail = runErr.Error()
		s.record(ctx, cycleID, startedAt, out, nil)
		return nil
	case runErr != nil:
		s.tracked(a.Role(), false)
		out.Status = statusIncident
		out.Severity = sevCritical
		out.Detail = "run failed: " + runErr.Error()
		s.record(ctx, cycleID, startedAt, out, append(rep.Errors, runErr.Error()))
		return runErr
	case !leak.Conserved:
		// Leak takes precedence as a warning even if the criterion passed:
		// silent drops are the core accountability failure (§C.2).
		s.tracked(a.Role(), true) // calls succeeded; leak is a data-flow warning, not a transient failure
		out.Status = statusLeak
		out.Severity = sevWarning
		out.Detail = leakDetail(leak)
		s.record(ctx, cycleID, startedAt, out, rep.Errors)
		log.Printf("hermes: %s LEAK %s", a.Role(), out.Detail)
		return nil
	case !critOK || !rep.SelfCheck.Pass:
		s.tracked(a.Role(), false)
		out.Status = statusIncident
		out.Severity = sevWarning
		out.Detail = critDetail
		if !rep.SelfCheck.Pass {
			out.Detail = strings.TrimSpace(out.Detail + "; self-check failed")
		}
		s.record(ctx, cycleID, startedAt, out, rep.Errors)
		return nil
	default:
		s.tracked(a.Role(), true)
		out.Status = statusOK
		out.Severity = sevInfo
		out.Detail = critDetail
		if retries > 0 {
			out.Status = statusRetried
		}
		s.record(ctx, cycleID, startedAt, out, rep.Errors)
		// 복구 시 자동 해소: 이 role 이 정상 run 을 마쳤으면 그동안 쌓인 미해결
		// 인시던트(예: 과거 breaker-open 기간의 critical)는 더는 actionable 하지
		// 않으므로 resolved 처리해 운영자 뷰를 self-clean 한다.
		s.resolveOpenIncidents(ctx, a.Role(), out.RunID)
		return nil
	}
}

// tracked updates the per-role consecutive-failure tracker and logs an
// incident-opened / recovered transition (the generalized bridge_health
// behaviour).
func (s *Supervisor) tracked(role agents.Role, ok bool) {
	if s.Tracker == nil {
		return
	}
	opened, recovered := s.Tracker.Record(role, ok)
	if opened {
		log.Printf("hermes: %s INCIDENT OPEN (%d consecutive failures)", role, s.Tracker.threshold)
	}
	if recovered {
		log.Printf("hermes: %s RECOVERED", role)
	}
}

func (s *Supervisor) measure(ctx context.Context, crit agents.Criterion, ids []uuid.UUID) float64 {
	if crit == nil {
		return 0
	}
	v, err := crit.Metric(ctx, s.Pool, ids)
	if err != nil {
		log.Printf("hermes: metric: %v", err)
		return 0
	}
	return v
}

func (s *Supervisor) breakerOpen() bool {
	if s.Hooks.BreakerIsOpen == nil {
		return false
	}
	return s.Hooks.BreakerIsOpen()
}

func (s *Supervisor) breakerRecord(ok bool) {
	if s.Hooks.BreakerRecordResult != nil {
		s.Hooks.BreakerRecordResult(ok)
	}
}

// backoff sleeps exponentially with jitter, capped at MaxBackoff, honoring ctx.
func (s *Supervisor) backoff(ctx context.Context, attempt int) {
	base := s.Config.BaseBackoff
	if base <= 0 {
		base = 2 * time.Second
	}
	d := base << attempt
	if mx := s.Config.MaxBackoff; mx > 0 && d > mx {
		d = mx
	}
	// jitter: ±25%
	jitter := time.Duration(rand.Int63n(int64(d/2+1))) - d/4
	d += jitter
	if d < 0 {
		d = base
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func leakDetail(l agents.Leak) string {
	parts := []string{}
	if n := len(l.Unaccounted); n > 0 {
		parts = append(parts, "unaccounted="+strconv.Itoa(n))
	}
	if n := len(l.Duplicated); n > 0 {
		parts = append(parts, "duplicated="+strconv.Itoa(n))
	}
	if n := len(l.Unexpected); n > 0 {
		parts = append(parts, "unexpected="+strconv.Itoa(n))
	}
	return "leak: selected=" + strconv.Itoa(l.Selected) + " accounted=" + strconv.Itoa(l.Accounted) +
		" (" + strings.Join(parts, ", ") + ")"
}
