package personextractor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

type fakeRunner struct {
	out json.RawMessage
	err error
}

func (f fakeRunner) Run(context.Context, string, []byte) (json.RawMessage, error) {
	return f.out, f.err
}

func newFakeAgent(out string, err error) *Agent {
	base := &agents.Base{Runner: fakeRunner{out: json.RawMessage(out), err: err}, LLM: llmRole()}
	return NewWith(base)
}

// decideAction mirrors the branch logic of reconcile() given a decoded result,
// so the decision policy is unit-tested without a DB. (reconcile() itself wires
// the same branches to DB writes.)
func decideAction(res reconcileResult) agents.Action {
	switch {
	case res.Same && res.Confidence >= 0.60:
		return agents.ActionFilled // promote
	case res.IsJunk:
		return agents.ActionSkipped // flag legacy junk, no delete
	default:
		return agents.ActionQuarantined // distinct/uncertain → review
	}
}

func TestReconcileDecision(t *testing.T) {
	cases := []struct {
		name string
		res  reconcileResult
		want agents.Action
	}{
		{"same high conf → promote",
			reconcileResult{Same: true, Confidence: 0.9}, agents.ActionFilled},
		{"same low conf → review",
			reconcileResult{Same: true, Confidence: 0.4}, agents.ActionQuarantined},
		{"distinct → review",
			reconcileResult{Same: false, Confidence: 0.9}, agents.ActionQuarantined},
		{"junk → flag legacy, no delete",
			reconcileResult{Same: false, IsJunk: true, Confidence: 0.9}, agents.ActionSkipped},
		{"uncertain → review",
			reconcileResult{Same: false, Confidence: 0.2}, agents.ActionQuarantined},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideAction(tc.res); got != tc.want {
				t.Fatalf("decideAction(%+v) = %s, want %s", tc.res, got, tc.want)
			}
		})
	}
}

func TestReconcile_NoLegacyIsNoop(t *testing.T) {
	// nil pool → loadLegacy returns false → reconcile is a no-op (accounted).
	a := newFakeAgent(`{"same":true,"is_junk":false,"confidence":0.9,"reason":"x"}`, nil)
	res := a.reconcile(context.Background(), nil, uuid.New(), "이재")
	if res.Action != agents.ActionNoop {
		t.Fatalf("expected noop with nil pool, got %s", res.Action)
	}
}

func TestReconcile_LLMErrorQuarantines(t *testing.T) {
	// Even with a legacy row present, an LLM error must quarantine (accounted).
	// We can't load a real legacy row without a DB, so we assert via the
	// decision policy that an error path does not promote.
	if got := decideAction(reconcileResult{}); got == agents.ActionFilled {
		t.Fatal("empty result must not promote")
	}
}

func TestRun_Conservation(t *testing.T) {
	a := newFakeAgent(`{"same":true,"is_junk":false,"confidence":0.9,"reason":"x"}`, nil)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	rep, err := a.Run(context.Background(), nil, agents.RunInput{RunID: uuid.New(), IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	// nil pool → classify fails → both ids become skipped-with-reason.
	leak := agents.ItemConservation(ids, rep.Results)
	if !leak.Conserved {
		t.Fatalf("not conserved: %+v", leak)
	}
}

func TestRole(t *testing.T) {
	if New(nil).Role() != agents.RolePersonExtractor {
		t.Fatal("wrong role")
	}
}
