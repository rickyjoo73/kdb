package dataqa

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

type fakeRunner struct{ out string }

func (f fakeRunner) Run(_ context.Context, prompt string, schema []byte) (json.RawMessage, error) {
	return json.RawMessage(f.out), nil
}

func TestBuildPromptIncludesEid(t *testing.T) {
	id := uuid.New()
	p := BuildPrompt([]Entity{{ID: id, Type: "person", Ko: "다니엘", En: "Danielle", Idn: "Kang Daniel"}})
	if !strings.Contains(p, id.String()) {
		t.Fatal("prompt must echo eid for mapping back")
	}
	if !strings.Contains(p, "Kang Daniel") || !strings.Contains(p, "idn") {
		t.Fatal("prompt must carry idn locale value")
	}
}

func TestReviewParsesVerdicts(t *testing.T) {
	id := uuid.New()
	fr := fakeRunner{out: `{"reviews":[{"id":"` + id.String() + `","verdict":"contaminated","wrong_fields":["id"],"reason":"Kang Daniel은 다른 가수"}]}`}
	vs, err := Review(context.Background(), fr, Schema, []Entity{{ID: id, Type: "person", Ko: "다니엘"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Verdict != "contaminated" || vs[0].WrongFields[0] != "id" {
		t.Fatalf("unexpected verdicts: %+v", vs)
	}
}

func TestLocaleColumn(t *testing.T) {
	cases := map[string]string{"en": "canonical_en", "pt_br": "canonical_pt_br", "id": "canonical_id", "zz": ""}
	for loc, want := range cases {
		if got, _ := localeColumn(loc); got != want {
			t.Errorf("localeColumn(%q)=%q want %q", loc, got, want)
		}
	}
}

func TestSchemaEmbedded(t *testing.T) {
	if len(Schema) == 0 || !json.Valid(Schema) {
		t.Fatal("embedded Schema must be valid JSON")
	}
}
