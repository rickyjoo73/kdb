package fillverifier

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// fakeRunner returns canned JSON (or an error) for the gemma judge call.
type fakeRunner struct {
	out json.RawMessage
	err error
}

func (f fakeRunner) Run(_ context.Context, _ string, _ []byte) (json.RawMessage, error) {
	return f.out, f.err
}

func judgeAgent(out string, err error) *Agent {
	return &Agent{judge: &agents.Base{Runner: fakeRunner{out: json.RawMessage(out), err: err}, LLM: judgeLLMRole()}}
}

func TestJudgeReplace(t *testing.T) {
	cases := []struct {
		name, out string
		err       bool
		want      string
	}{
		{"use_source", `{"verdict":"use_source","reason":"official"}`, false, "use_source"},
		{"keep", `{"verdict":"keep"}`, false, "keep"},
		{"uncertain→keep", `{"verdict":"uncertain"}`, false, "keep"},
		{"error→keep", "", true, "keep"},
		{"garbage→keep", `not json`, false, "keep"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var a *Agent
			if c.err {
				a = judgeAgent("", context.DeadlineExceeded)
			} else {
				a = judgeAgent(c.out, nil)
			}
			got := a.judgeReplace(context.Background(), judgeInput{Ko: "아이유", Locale: "en", Current: "IU", Wikidata: "IU"})
			if got != c.want {
				t.Fatalf("verdict=%q, want %q", got, c.want)
			}
		})
	}
}

// nil judge must never replace (conservative).
func TestJudgeReplaceNilJudge(t *testing.T) {
	a := &Agent{}
	if got := a.judgeReplace(context.Background(), judgeInput{}); got != "keep" {
		t.Fatalf("nil judge = %q, want keep", got)
	}
}

func TestNormEq(t *testing.T) {
	yes := [][2]string{{"Park Bo-gum", "park bo gum"}, {"IU", "i.u"}, {"방탄소년단", "방탄소년단"}}
	for _, p := range yes {
		if !normEq(p[0], p[1]) {
			t.Errorf("normEq(%q,%q) = false, want true", p[0], p[1])
		}
	}
	if normEq("IU", "Lee Ji-eun") {
		t.Error("distinct values must not be equal")
	}
}

func TestRole(t *testing.T) {
	if New(nil).Role() != agents.RoleFillVerifier {
		t.Fatalf("Role = %q", New(nil).Role())
	}
}

func TestBuildJudgePromptRules(t *testing.T) {
	p := buildJudgePrompt(judgeInput{Ko: "아이유", Locale: "zh", EntityType: "person", Current: "X", Wikidata: "李知恩"})
	for _, want := range []string{"use_source", "keep", "uncertain", "간체", "李知恩", "아이유"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestJudgeBuildPromptBadInput(t *testing.T) {
	if _, err := judgeLLMRole().BuildPrompt(123); err == nil {
		t.Fatal("BuildPrompt(non-judgeInput) must error")
	}
}
