// Offline end-to-end harness for the Hermes multi-agent layer.
//
// Runs entirely against in-memory fakes — no kdb-db. It drives the REAL
// Supervisor (plan->run->verify->retry->decide), the REAL
// agents.ItemConservation leak detector, and the REAL role agents
// (gatekeeper/personextractor/enricher/disambiguator) via their nil-runner
// constructors. Safe to run in CI:
//
//	go test ./internal/kdb/hermes/ -run TestHermesE2E -v
//	# or via docker (no local Go needed):
//	docker run --rm -v "$PWD":/app -w /app -e GOFLAGS=-buildvcs=false \
//	  -v kdb-platform_go-mod-cache:/go/pkg/mod -v kdb-platform_go-build-cache:/root/.cache/go-build \
//	  golang:1.23-bookworm sh -c 'go test ./internal/kdb/hermes/ -run TestHermesE2E -v'
//
// Supervisor.record() writes directly to kwave_kdb_hermes_runs and no-ops when
// Pool==nil, so the offline assertions observe the supervisor's decision via
// its return value and the per-role IncidentTracker. It proves, for every role
// + the supervisor: registrable role agents; the gatekeeper pre-gate
// accept/reject/gray; a healthy agent decided OK; a forced failed self-check
// decided as a (tracked) incident; a forced item-conservation leak detected;
// a forced run error surfaced as a critical incident; tracker open/recover.
package hermes

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/disambiguator"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/enricher"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/personextractor"
)

// e2eFakeAgent implements the real agents.Agent interface with scripted
// Select/Run results so the real Supervisor loop runs without a database.
type e2eFakeAgent struct {
	role    agents.Role
	ids     []uuid.UUID
	results []agents.ItemResult
	runErr  error
	scPass  bool
	critMet bool
}

func (a *e2eFakeAgent) Role() agents.Role { return a.role }
func (a *e2eFakeAgent) Select(context.Context, *pgxpool.Pool, int) ([]uuid.UUID, error) {
	return a.ids, nil
}
func (a *e2eFakeAgent) Run(context.Context, *pgxpool.Pool, agents.RunInput) (agents.RunReport, error) {
	if a.runErr != nil {
		return agents.RunReport{}, a.runErr
	}
	rep := agents.RunReport{
		Role: a.role, Selected: len(a.ids),
		Results:   a.results,
		SelfCheck: agents.SelfCheck{Pass: a.scPass},
	}
	rep.Summarize()
	return rep, nil
}
func (a *e2eFakeAgent) Criterion() agents.Criterion { return e2eFakeCriterion{met: a.critMet} }

type e2eFakeCriterion struct{ met bool }

func (c e2eFakeCriterion) Metric(context.Context, *pgxpool.Pool, []uuid.UUID) (float64, error) {
	return 0, nil
}
func (c e2eFakeCriterion) Met(_, _ float64, _ agents.RunReport) (bool, string) {
	return c.met, "fake criterion"
}

// e2eSup builds a supervisor with no pool (record() no-ops), no backoff, and
// minimal retries so the test is fast and deterministic.
func e2eSup() *Supervisor {
	sup := New(nil)
	sup.Config.BaseBackoff = 0
	sup.Config.MaxBackoff = 0
	sup.Config.MaxRetries = 1
	sup.Config.LogicalRetries = 0
	return sup
}

func e2eIDs(n int) []uuid.UUID {
	out := make([]uuid.UUID, n)
	for i := range out {
		out[i] = uuid.New()
	}
	return out
}

// e2eAccounted builds one accounted ItemResult per id, cycling through actions.
func e2eAccounted(idset []uuid.UUID, actions ...agents.Action) []agents.ItemResult {
	out := make([]agents.ItemResult, len(idset))
	for i, id := range idset {
		act := actions[i%len(actions)]
		out[i] = agents.ItemResult{ID: id, Action: act, Reason: string(act), Source: "test"}
	}
	return out
}

func TestHermesE2E(t *testing.T) {
	ctx := context.Background()
	cycle := uuid.New()

	// 1. Real role agents are registrable + expose Role/Criterion.
	reg := agents.NewRegistry()
	for _, ra := range []agents.Agent{
		gatekeeper.New(nil), personextractor.New(nil), enricher.New(nil), disambiguator.New(nil),
	} {
		if ra.Role() == "" || ra.Criterion() == nil {
			t.Fatalf("role agent %T missing role/criterion", ra)
		}
		if err := reg.Register(ra); err != nil {
			t.Fatalf("register %s: %v", ra.Role(), err)
		}
	}
	if reg.Len() != 4 {
		t.Fatalf("registry len = %d, want 4", reg.Len())
	}

	// 2. Gatekeeper deterministic pre-gate: accept (clean) vs reject (junk).
	if gatekeeper.PreGate("방탄소년단").Verdict == gatekeeper.PreReject {
		t.Error("clean proper noun wrongly rejected by pre-gate")
	}
	junk := []string{"이것은 아주 긴 문장입니다 그리고 더 길어요 정말", "hello this is a full sentence."}
	rejected := 0
	for _, j := range junk {
		if gatekeeper.PreGate(j).Verdict == gatekeeper.PreReject {
			rejected++
		}
	}
	if rejected == 0 {
		t.Errorf("pre-gate rejected none of the obvious junk %v", junk)
	}

	// 3. Healthy agents (accounted RunReport, met criterion, ok self-check) ->
	//    decided OK (no error, tracker not open). Covers each role's accounted
	//    disposition shape, verified conserved by the real leak detector.
	shapes := []struct {
		role    agents.Role
		actions []agents.Action
	}{
		{agents.RoleCandidateGatekeeper, []agents.Action{agents.ActionKept, agents.ActionRejected, agents.ActionQuarantined}},
		{agents.RolePersonExtractor, []agents.Action{agents.ActionFilled, agents.ActionSkipped}},
		{agents.RoleEnricher, []agents.Action{agents.ActionFilled, agents.ActionNoop, agents.ActionSkipped}},
		{agents.RoleDisambiguator, []agents.Action{agents.ActionMerged, agents.ActionSplit, agents.ActionQuarantined}},
	}
	for _, s := range shapes {
		sIDs := e2eIDs(len(s.actions))
		fa := &e2eFakeAgent{role: s.role, ids: sIDs, scPass: true, critMet: true,
			results: e2eAccounted(sIDs, s.actions...)}
		sup := e2eSup()
		if err := sup.Supervise(ctx, fa, cycle); err != nil {
			t.Fatalf("%s healthy: unexpected error %v", s.role, err)
		}
		if sup.Tracker.IsOpen(s.role) {
			t.Errorf("%s healthy run should not open an incident", s.role)
		}
		if leak := agents.ItemConservation(sIDs, fa.results); !leak.Conserved {
			t.Fatalf("%s shape should be conserved, got %+v", s.role, leak)
		}
	}

	// 4. Forced FAILED SELF-CHECK -> incident (recorded, tracked failure).
	scIDs := e2eIDs(2)
	scFail := &e2eFakeAgent{role: agents.RoleEnricher, ids: scIDs, scPass: false, critMet: true,
		results: e2eAccounted(scIDs, agents.ActionFilled)} // accounted -> NOT a leak
	if err := e2eSup().Supervise(ctx, scFail, cycle); err != nil {
		t.Fatalf("self-check failure should be a recorded incident, not a returned error: %v", err)
	}
	// 3 consecutive failures open the (threshold=3) tracker.
	supSC := e2eSup()
	for i := 0; i < 3; i++ {
		_ = supSC.Supervise(ctx, scFail, cycle)
	}
	if !supSC.Tracker.IsOpen(scFail.Role()) {
		t.Fatal("3 consecutive failed self-checks should open an incident in the tracker")
	}

	// 5. Forced ITEM-CONSERVATION LEAK -> detected + decided leak (recorded).
	leakIDs := e2eIDs(3)
	leakResults := []agents.ItemResult{{ID: leakIDs[0], Action: agents.ActionFilled, Reason: "ok"}}
	if leak := agents.ItemConservation(leakIDs, leakResults); leak.Conserved {
		t.Fatal("expected ItemConservation to report a leak")
	} else if len(leak.Unaccounted) != 2 {
		t.Fatalf("expected 2 unaccounted ids, got %d", len(leak.Unaccounted))
	}
	leaky := &e2eFakeAgent{role: agents.RoleDisambiguator, ids: leakIDs, scPass: true, critMet: true,
		results: leakResults}
	if err := e2eSup().Supervise(ctx, leaky, cycle); err != nil {
		t.Fatalf("leak should be recorded, not returned as error: %v", err)
	}

	// 6. Forced RUN ERROR -> retried then critical incident (returned error).
	erAgent := &e2eFakeAgent{role: agents.RoleClassifier, ids: e2eIDs(1), scPass: true, critMet: true,
		runErr: errors.New("transient source failure")}
	if err := e2eSup().Supervise(ctx, erAgent, cycle); err == nil {
		t.Fatal("a persistent run error should surface as a returned error (critical incident)")
	}

	// 7. SuperviseCycle drives a whole registry without panicking.
	cycleReg := agents.NewRegistry()
	cIDs := e2eIDs(2)
	_ = cycleReg.Register(&e2eFakeAgent{role: agents.RoleClassifier, ids: cIDs, scPass: true, critMet: true,
		results: e2eAccounted(cIDs, agents.ActionFilled, agents.ActionNoop)})
	e2eSup().SuperviseCycle(ctx, cycleReg)

	// 8. Tracker recovery on first success after an open incident.
	tr := NewIncidentTracker(2)
	tr.Record(agents.RoleEnricher, false)
	tr.Record(agents.RoleEnricher, false)
	if !tr.IsOpen(agents.RoleEnricher) {
		t.Fatal("tracker should be open after 2 failures")
	}
	if _, recovered := tr.Record(agents.RoleEnricher, true); !recovered {
		t.Fatal("tracker should recover on first success")
	}

	t.Log("E2E ok: role agents + healthy/self-check/leak/run-error decisions exercised offline")
}
