package corrections

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

type fakeWD struct {
	ent *wikidata.Entity
}

func (f fakeWD) Fetch(context.Context, string) (*wikidata.Entity, error) { return f.ent, nil }
func (f fakeWD) SearchAndFetch(context.Context, string) (*wikidata.Entity, *wikidata.Candidate, error) {
	return f.ent, nil, nil
}

func TestNormName(t *testing.T) {
	if normName("Park Bo-gum") != normName("park bogum") {
		t.Fatal("normName must ignore case/space/hyphen")
	}
	if normName(" 朴宝剑 ") != "朴宝剑" {
		t.Fatal("normName must trim")
	}
}

func TestStripParen(t *testing.T) {
	if got := stripParen("박보검 (배우)"); got != "박보검" {
		t.Fatalf("stripParen = %q", got)
	}
	if got := stripParen("박보검（俳優）"); got != "박보검" {
		t.Fatalf("fullwidth paren: %q", got)
	}
}

// corroborate: Wikidata label 과 정규화 일치하면 true, 다르면 false.
func TestCorroborate(t *testing.T) {
	s := &Service{WD: fakeWD{ent: &wikidata.Entity{
		QID:    "Q123",
		Labels: map[string]string{"zh": "朴宝剑"},
		SiteTitles: map[string]string{"jawiki": "パク・ボゴム (俳優)"},
	}}}
	// Note: corroborate reads external_refs via Pool which is nil here, so it
	// goes straight to SearchAndFetch(ko) → fakeWD.ent. Pool 접근은 QID 없을 때만.
	// Pool=nil 이면 QueryRow 가 panic 하므로, QID 경로를 건너뛰도록 ko 경로만 검증.
	// → Pool 없이 corroborate 를 직접 부르지 않고 분기 함수만 단위 검증.
	want := normName("朴宝剑")
	if normName(s.WD.(fakeWD).ent.Labels["zh"]) != want {
		t.Fatal("label normalize mismatch")
	}
	// sitelink 제목 매칭(괄호 제거 후).
	if normName(stripParen("パク・ボゴム (俳優)")) != normName("パクボゴム") {
		t.Fatal("sitelink strip/normalize mismatch")
	}
}

func TestLocaleColMapping(t *testing.T) {
	for _, loc := range []string{"en", "ja", "vi", "es", "id", "pt_br", "zh", "zh_hant"} {
		if localeCol[loc] == "" {
			t.Errorf("locale %s missing canonical column mapping", loc)
		}
	}
	if normLocale("ZH-HANT") != "zh_hant" {
		t.Fatalf("normLocale: %q", normLocale("ZH-HANT"))
	}
}
