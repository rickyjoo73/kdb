package gatekeeper

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

func TestPreGate(t *testing.T) {
	cases := []struct {
		name string
		term string
		want PreVerdict
	}{
		// clean tokens — go to the gray band (LLM decides name vs common noun).
		{"clean name", "이정재", PreGray},
		{"clean name 2syl", "아이유", PreGray},
		{"clean group hangul", "방탄소년단", PreGray},
		{"clean 2 chars", "비비", PreGray},

		// obvious junk — hard reject.
		{"lone jamo", "임ㅇ원희", PreReject},
		{"four words phrase", "오늘 날씨 가 좋다", PreReject},
		{"josa tail hage", "건강하게", PreReject},
		{"verb ida", "배우이다", PreReject},
		{"verb eossda", "출연했었다", PreReject},
		{"over length", "아주아주아주아주아주아주아주아주아주긴이름입니다정말로", PreReject},
		{"hangul+digit", "방탄소년단7명", PreReject},

		// ambiguous — gray band (LLM decides), never hard-rejected.
		{"spaced two words", "레이 아미", PreGray},
		{"sentence punct", "정말?", PreGray},
		{"latin brand", "BTS", PreGray},
		{"latin+digit brand", "2NE1", PreGray},
		{"common noun single", "세계일주", PreGray}, // single token, no josa tail → LLM
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := PreGate(tc.term)
			if got.Verdict != tc.want {
				t.Fatalf("PreGate(%q) = %v (%s), want %v", tc.term, got.Verdict, got.Reason, tc.want)
			}
		})
	}
}

func TestPreGate_RealNamesNeverRejected(t *testing.T) {
	// Guard: a curated list of real K-content proper nouns must never be
	// hard-rejected by the cheap pre-gate.
	real := []string{"이정재", "송강호", "아이유", "뉴진스", "블랙핑크", "오징어 게임", "레이 아미"}
	for _, name := range real {
		if v := PreGate(name).Verdict; v == PreReject {
			t.Errorf("real name %q was hard-rejected", name)
		}
	}
}

// fakeRunner returns canned schema-valid JSON for the gray-band path.
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

func TestProcess_GrayBand(t *testing.T) {
	id := uuid.New()
	cases := []struct {
		name string
		out  string
		want agents.Action
	}{
		{"llm keep", `{"verdict":"proper_noun","keep":true,"confidence":0.9,"reason":"name"}`, agents.ActionKept},
		{"llm reject", `{"verdict":"common_noun","keep":false,"confidence":0.9,"reason":"generic"}`, agents.ActionRejected},
		{"llm uncertain", `{"verdict":"uncertain","keep":false,"confidence":0.3,"reason":"thin"}`, agents.ActionQuarantined},
		{"low conf keep is quarantine", `{"verdict":"proper_noun","keep":true,"confidence":0.4,"reason":"maybe"}`, agents.ActionQuarantined},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := newFakeAgent(tc.out, nil)
			// "레이 아미" is a gray-band term (spaced) so it reaches the LLM.
			res := a.process(context.Background(), nil, candRow{ID: id, Ko: "레이 아미"})
			if res.Action != tc.want {
				t.Fatalf("action=%s want %s (reason=%s)", res.Action, tc.want, res.Reason)
			}
		})
	}
}

func TestProcess_LLMErrorQuarantines(t *testing.T) {
	a := newFakeAgent("", context.DeadlineExceeded)
	res := a.process(context.Background(), nil, candRow{ID: uuid.New(), Ko: "레이 아미"})
	if res.Action != agents.ActionQuarantined {
		t.Fatalf("llm error must quarantine, got %s", res.Action)
	}
}

func TestProcess_PreGateShortCircuits(t *testing.T) {
	// A hard-reject term must NOT call the LLM (fake returns keep, but rules win).
	a := newFakeAgent(`{"verdict":"proper_noun","keep":true,"confidence":0.99,"reason":"x"}`, nil)
	res := a.process(context.Background(), nil, candRow{ID: uuid.New(), Ko: "건강하게"})
	if res.Action != agents.ActionRejected {
		t.Fatalf("hard-junk must be rejected by rules, got %s", res.Action)
	}
	// A clean name goes to the gray band: the fake returns keep=true → kept.
	a2 := newFakeAgent(`{"verdict":"proper_noun","keep":true,"confidence":0.9,"reason":"name"}`, nil)
	res = a2.process(context.Background(), nil, candRow{ID: uuid.New(), Ko: "이정재"})
	if res.Action != agents.ActionKept {
		t.Fatalf("clean name kept via LLM, got %s", res.Action)
	}
}

func TestProcess_SuggestionMustClearPreGate(t *testing.T) {
	id := uuid.New()
	// A clean suggestion is adopted (kept cleaned).
	clean := newFakeAgent(`{"verdict":"proper_noun","keep":true,"confidence":0.9,"reason":"x","canonical_suggestion":"아이유"}`, nil)
	res := clean.process(context.Background(), nil, candRow{ID: id, Ko: "레이 아미"})
	if res.Action != agents.ActionKept || !strings.Contains(res.Reason, "cleaned →") {
		t.Fatalf("clean suggestion should be adopted; got action=%s reason=%q", res.Action, res.Reason)
	}
	// A junk suggestion (over-length phrase) must be REJECTED by the pre-gate and
	// NOT written to canonical — the row is still kept, but with the original.
	junk := newFakeAgent(`{"verdict":"proper_noun","keep":true,"confidence":0.9,"reason":"x","canonical_suggestion":"박보검의 여자친구가 누구인지 모두가 궁금해하는 그 이름"}`, nil)
	res = junk.process(context.Background(), nil, candRow{ID: id, Ko: "레이 아미"})
	if res.Action != agents.ActionKept {
		t.Fatalf("junk-suggestion row should still be kept; got %s", res.Action)
	}
	if strings.Contains(res.Reason, "cleaned →") || !strings.Contains(res.Reason, "rejected by pre-gate") {
		t.Fatalf("junk suggestion must be rejected by pre-gate, not adopted; reason=%q", res.Reason)
	}
}

func TestRun_Conservation(t *testing.T) {
	// Every selected id (including a not-found one) must be accounted for.
	a := newFakeAgent(`{"verdict":"proper_noun","keep":true,"confidence":0.9,"reason":"ok"}`, nil)
	ids := []uuid.UUID{uuid.New(), uuid.New()}
	rep, err := a.Run(context.Background(), nil, agents.RunInput{RunID: uuid.New(), IDs: ids})
	if err != nil {
		t.Fatal(err)
	}
	// nil pool → load returns nothing → both ids become skipped-with-reason.
	leak := agents.ItemConservation(ids, rep.Results)
	if !leak.Conserved {
		t.Fatalf("not conserved: %+v", leak)
	}
}
