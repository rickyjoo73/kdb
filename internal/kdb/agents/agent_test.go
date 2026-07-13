package agents

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestItemConservation_AllAccounted(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	sel := []uuid.UUID{a, b, c}
	res := []ItemResult{
		{ID: a, Action: ActionFilled},
		{ID: b, Action: ActionRejected},
		{ID: c, Action: ActionSkipped, Reason: "operator_locked"},
	}
	leak := ItemConservation(sel, res)
	if !leak.Conserved {
		t.Fatalf("expected conserved, got %+v", leak)
	}
	if leak.Accounted != 3 {
		t.Fatalf("accounted=%d want 3", leak.Accounted)
	}
}

func TestItemConservation_SilentDrop(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	sel := []uuid.UUID{a, b, c}
	// c is silently dropped (never in results).
	res := []ItemResult{
		{ID: a, Action: ActionFilled},
		{ID: b, Action: ActionNoop},
	}
	leak := ItemConservation(sel, res)
	if leak.Conserved {
		t.Fatalf("expected leak, got conserved")
	}
	if len(leak.Unaccounted) != 1 || leak.Unaccounted[0] != c {
		t.Fatalf("expected c unaccounted, got %v", leak.Unaccounted)
	}
}

func TestItemConservation_SkipWithoutReasonIsLeak(t *testing.T) {
	a := uuid.New()
	leak := ItemConservation([]uuid.UUID{a}, []ItemResult{{ID: a, Action: ActionSkipped}})
	if leak.Conserved {
		t.Fatalf("skip without reason must be a leak")
	}
}

func TestItemConservation_Duplicate(t *testing.T) {
	a := uuid.New()
	leak := ItemConservation([]uuid.UUID{a}, []ItemResult{
		{ID: a, Action: ActionFilled},
		{ID: a, Action: ActionRejected},
	})
	if leak.Conserved || len(leak.Duplicated) != 1 {
		t.Fatalf("expected duplicate flagged, got %+v", leak)
	}
}

func TestItemConservation_Unexpected(t *testing.T) {
	a, x := uuid.New(), uuid.New()
	leak := ItemConservation([]uuid.UUID{a}, []ItemResult{
		{ID: a, Action: ActionFilled},
		{ID: x, Action: ActionFilled}, // not selected
	})
	if leak.Conserved || len(leak.Unexpected) != 1 || leak.Unexpected[0] != x {
		t.Fatalf("expected unexpected flagged, got %+v", leak)
	}
}

func TestRunReportSummarize(t *testing.T) {
	rep := RunReport{Results: []ItemResult{
		{Action: ActionFilled},
		{Action: ActionMerged},
		{Action: ActionQuarantined},
		{Action: ActionSkipped, Reason: "x"},
		{Action: ActionErrored},
		{Action: ActionNoop},
	}}
	rep.Summarize()
	if rep.Acted != 2 {
		t.Fatalf("acted=%d want 2", rep.Acted)
	}
	if rep.Quarantined != 1 || rep.Skipped != 1 || rep.Errored != 1 {
		t.Fatalf("counts wrong: %+v", rep)
	}
}

// fakeAgent for registry tests.
type fakeAgent struct{ role Role }

func (f fakeAgent) Role() Role { return f.role }
func (f fakeAgent) Select(context.Context, *pgxpool.Pool, int) ([]uuid.UUID, error) {
	return nil, nil
}
func (f fakeAgent) Run(context.Context, *pgxpool.Pool, RunInput) (RunReport, error) {
	return RunReport{}, nil
}
func (f fakeAgent) Criterion() Criterion { return LooseCriterion{} }

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(fakeAgent{role: RoleEnricher}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(fakeAgent{role: RoleClassifier}); err != nil {
		t.Fatal(err)
	}
	if r.Len() != 2 {
		t.Fatalf("len=%d want 2", r.Len())
	}
	if _, ok := r.Get(RoleEnricher); !ok {
		t.Fatal("enricher not found")
	}
	if err := r.Register(nil); err == nil {
		t.Fatal("nil agent should error")
	}
	if err := r.Register(fakeAgent{role: ""}); err == nil {
		t.Fatal("empty role should error")
	}
	roles := r.Roles()
	if len(roles) != 2 || roles[0] != RoleEnricher { // staged role precedes unknown-stage role
		t.Fatalf("roles not deterministic: %v", roles)
	}
}

func TestRegistryUsesSafetyStageOrder(t *testing.T) {
	r := NewRegistry()
	// Deliberately register in the unsafe reverse order.
	for _, role := range []Role{
		RoleStepPromoteConsensus,
		RoleCandidateGatekeeper,
		RoleStepRejectQualifiedMember,
		RoleStepRepairBrokenJamo,
	} {
		if err := r.Register(fakeAgent{role: role}); err != nil {
			t.Fatal(err)
		}
	}
	want := []Role{
		RoleStepRepairBrokenJamo,
		RoleStepRejectQualifiedMember,
		RoleCandidateGatekeeper,
		RoleStepPromoteConsensus,
	}
	got := r.Roles()
	if len(got) != len(want) {
		t.Fatalf("roles=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("roles=%v want=%v", got, want)
		}
	}
}

func TestLooseCriterion(t *testing.T) {
	ok, _ := LooseCriterion{}.Met(0, 0, RunReport{})
	if !ok {
		t.Fatal("loose criterion must always be met")
	}
}

// fakeRunner returns canned JSON for the Base test.
type fakeRunner struct {
	out json.RawMessage
	err error
}

func (f fakeRunner) Run(context.Context, string, []byte) (json.RawMessage, error) {
	return f.out, f.err
}

func TestBaseCallJSON(t *testing.T) {
	b := &Base{
		Runner: fakeRunner{out: json.RawMessage(`{"verdict":"keep"}`)},
		LLM: LLMRole{
			Role:        RoleCandidateGatekeeper,
			BuildPrompt: func(any) (string, error) { return "prompt", nil },
		},
	}
	var got struct {
		Verdict string `json:"verdict"`
	}
	if err := b.CallJSON(context.Background(), nil, &got); err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "keep" {
		t.Fatalf("verdict=%q", got.Verdict)
	}
}
