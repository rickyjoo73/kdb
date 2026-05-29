package enricher

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

func newTestAgent(localeOut, personOut string, err error) *Agent {
	lb := &agents.Base{Runner: fakeRunner{out: json.RawMessage(localeOut), err: err}, LLM: localeLLMRole()}
	pb := &agents.Base{Runner: fakeRunner{out: json.RawMessage(personOut), err: err}, LLM: personLLMRole()}
	return NewWith(lb, pb, nil, nil)
}

// --- gap computation ------------------------------------------------------

func TestGapCount_AllEmptyPerson(t *testing.T) {
	r := &record{
		isPerson:   true,
		localeVals: map[string]string{}, // all 8 locales empty
	}
	// 8 locale canonicals + aliases_ko empty = 9; 7 person fields empty = 16.
	if got := GapCount(r); got != 16 {
		t.Fatalf("GapCount all-empty person = %d, want 16", got)
	}
}

func TestGapCount_RespectsFilled(t *testing.T) {
	r := &record{
		isPerson: false,
		localeVals: map[string]string{
			"canonical_en": "BTS", "canonical_ja": "防弾少年団",
		},
		aliasesKo: []string{"방탄"},
	}
	// non-person: 9 locale/alias fields; en+ja filled + aliases_ko filled = 6 empty.
	if got := GapCount(r); got != 6 {
		t.Fatalf("GapCount = %d, want 6", got)
	}
}

func TestPersonFieldEmpty(t *testing.T) {
	r := &record{
		agency: "HYBE", birthYear: 0, gender: "", groups: []string{"BTS"},
		works: nil, secondary: nil, primaryRole: "other",
	}
	cases := map[string]bool{
		"agency":          false, // filled
		"birth_year":      true,  // 0
		"gender":          true,  // empty
		"groups":          false, // filled
		"notable_works":   true,  // empty
		"secondary_roles": true,  // empty
		"primary_role":    true,  // 'other'
	}
	for f, want := range cases {
		if got := personFieldEmpty(r, f); got != want {
			t.Errorf("personFieldEmpty(%s) = %v, want %v", f, got, want)
		}
	}
}

func TestSplitFields(t *testing.T) {
	loc, per := splitFields([]string{"canonical_en", "agency", "aliases_ko", "groups"})
	if len(loc) != 2 || len(per) != 2 {
		t.Fatalf("split = %v / %v, want 2/2", loc, per)
	}
}

// --- source-exhaustion / termination --------------------------------------

func TestGaps_ExcludesExhausted_NilPoolNoExhaustion(t *testing.T) {
	a := newTestAgent(`{"spellings":[],"skipped":[]}`, `{"fields":{},"skipped":[]}`, nil)
	r := &record{isPerson: false, localeVals: map[string]string{}}
	// nil pool → exhaustedFields returns empty → all 9 locale gaps surface.
	got := a.gaps(context.Background(), nil, r)
	if len(got) != 9 {
		t.Fatalf("gaps with nil pool = %d, want 9", len(got))
	}
}

// --- conservation (Run accounts every selected id) ------------------------

func TestRun_Conservation_NilPool(t *testing.T) {
	a := newTestAgent(`{"spellings":[],"skipped":[]}`, `{"fields":{},"skipped":[]}`, nil)
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	rep, err := a.Run(context.Background(), nil, agents.RunInput{RunID: uuid.New(), IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	// nil pool → loadRecord fails → every id is errored (accounted, not dropped).
	leak := agents.ItemConservation(ids, rep.Results)
	if !leak.Conserved {
		t.Fatalf("not conserved: %+v", leak)
	}
	for _, res := range rep.Results {
		if res.Action != agents.ActionErrored {
			t.Errorf("nil-pool id %s → %s, want errored", res.ID, res.Action)
		}
	}
}

func TestSelfCheck_FilledNeedsReason(t *testing.T) {
	a := &Agent{}
	results := []agents.ItemResult{
		{ID: uuid.New(), Action: agents.ActionFilled, Reason: "filled canonical_en"},
		{ID: uuid.New(), Action: agents.ActionFilled, Reason: ""}, // bad
	}
	sc := a.selfCheck(results)
	if sc.Pass {
		t.Fatal("self-check should fail when a filled item lacks a reason")
	}
}

// --- Criterion.Met semantics ---------------------------------------------

func TestCriterion_Met(t *testing.T) {
	c := Criterion{}
	ok := func(rep agents.RunReport) agents.RunReport { rep.SelfCheck.Pass = true; return rep }
	// decreased → met
	if met, _ := c.Met(10, 4, ok(agents.RunReport{})); !met {
		t.Error("gaps decreased should be met")
	}
	// flat → met (converging)
	if met, _ := c.Met(5, 5, ok(agents.RunReport{})); !met {
		t.Error("flat gaps (no regression) should be met")
	}
	// increased → not met (regression)
	if met, _ := c.Met(3, 7, ok(agents.RunReport{})); met {
		t.Error("gaps increased should NOT be met")
	}
	// self-check fail → not met
	if met, _ := c.Met(10, 1, agents.RunReport{SelfCheck: agents.SelfCheck{Pass: false}}); met {
		t.Error("self-check fail should NOT be met")
	}
}
