package disambiguator

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

func newTestAgent(out string, err error) *Agent {
	return NewWith(&agents.Base{Runner: fakeRunner{out: json.RawMessage(out), err: err}, LLM: llmRole()})
}

// Exercise actual routing without a database. Successful merge policy and
// transaction writes are covered by merge_integration_test.go, not a copied
// implementation of the gate (which previously diverged from production).
func classify(loser, winner member, asg memberResult) agents.Action {
	loser.id, winner.id = uuid.New(), uuid.New()
	wid := winner.id.String()
	asg.SameAs = &wid
	return (&Agent{}).applyDecision(context.Background(), nil, cluster{}, loser, asg,
		map[string]member{loser.id.String(): loser, wid: winner}).Action
}

func TestDecision_TypoNeedsTransaction(t *testing.T) {
	loser := member{ko: "방탄소년딘", agency: "HYBE", role: "idol", wellFormed: true}
	winner := member{ko: "방탄소년단", agency: "HYBE", role: "idol", wellFormed: true}
	asg := memberResult{Decision: "merge", Confidence: 0.95}
	if got := classify(loser, winner, asg); got != agents.ActionSkipped {
		t.Fatalf("typo without database → %s, want skipped", got)
	}
}

func TestDecision_NicknameNeedsTransaction(t *testing.T) {
	loser := member{ko: "지디", agency: "YG", role: "rapper", wellFormed: true}
	winner := member{ko: "권지용", agency: "YG", role: "rapper", wellFormed: true}
	asg := memberResult{Decision: "merge", Confidence: 0.8}
	if got := classify(loser, winner, asg); got != agents.ActionSkipped {
		t.Fatalf("nickname without database → %s, want skipped", got)
	}
}

func TestDecision_ConflictingAgencyNeverMerges(t *testing.T) {
	// Same name, DIFFERENT agency → evidence conflict → never merged.
	loser := member{ko: "윤성호", agency: "SM", role: "singer", wellFormed: true}
	winner := member{ko: "윤성호", agency: "CJ", role: "director", wellFormed: true}
	asg := memberResult{Decision: "merge", Confidence: 0.95}
	if got := classify(loser, winner, asg); got == agents.ActionMerged {
		t.Fatal("conflicting agency MUST NOT merge")
	}
	if got := classify(loser, winner, asg); got != agents.ActionSkipped {
		t.Fatalf("conflicting agency without database → %s, want skipped", got)
	}
}

func TestDecision_MalformedWinnerRefused(t *testing.T) {
	loser := member{ko: "방탄소년단", wellFormed: true}
	winner := member{ko: "방탄소ㄴ단", wellFormed: false} // jamo-broken proposed winner
	asg := memberResult{Decision: "merge", Confidence: 0.95}
	if got := classify(loser, winner, asg); got != agents.ActionQuarantined {
		t.Fatalf("malformed winner → %s, want quarantined", got)
	}
}

func TestDecision_UncertainQuarantines(t *testing.T) {
	m := member{ko: "이재", wellFormed: true}
	asg := memberResult{Decision: "uncertain", Confidence: 0.3}
	if got := classify(m, m, asg); got != agents.ActionQuarantined {
		t.Fatalf("uncertain → %s, want quarantined", got)
	}
}

func TestDecision_LowConfMergeQuarantines(t *testing.T) {
	loser := member{ko: "방탄소년딘", agency: "HYBE", wellFormed: true}
	winner := member{ko: "방탄소년단", agency: "HYBE", wellFormed: true}
	asg := memberResult{Decision: "merge", Confidence: 0.5} // below threshold
	if got := classify(loser, winner, asg); got != agents.ActionQuarantined {
		t.Fatalf("low-conf merge → %s, want quarantined", got)
	}
}

// --- conservation (Run accounts every selected id) ------------------------

func TestRun_Conservation_NilPool(t *testing.T) {
	a := newTestAgent(`{"assignments":[]}`, nil)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	rep, err := a.Run(context.Background(), nil, agents.RunInput{RunID: uuid.New(), IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	// nil pool → no clusters → every id is a no-op (accounted, not dropped).
	leak := agents.ItemConservation(ids, rep.Results)
	if !leak.Conserved {
		t.Fatalf("not conserved: %+v", leak)
	}
}

func TestSelfCheck_DecisionsJustified(t *testing.T) {
	a := &Agent{}
	results := []agents.ItemResult{
		{ID: uuid.New(), Action: agents.ActionMerged, Reason: "typo"},
		{ID: uuid.New(), Action: agents.ActionSplit, Reason: ""}, // bad
	}
	if a.selfCheck(results).Pass {
		t.Fatal("self-check should fail when a decision lacks a reason")
	}
}

func TestTrigramClose(t *testing.T) {
	if !trigramClose("방탄소년단", "방탄소년딘") {
		t.Error("near-identical names should be trigram-close")
	}
	if trigramClose("방탄소년단", "블랙핑크") {
		t.Error("unrelated names should not be trigram-close")
	}
}

func TestRole(t *testing.T) {
	if New(nil).Role() != agents.RoleDisambiguator {
		t.Fatal("wrong role")
	}
}
