package readiness

import "testing"

// qualifiedSourceList 와 qualifiedSource 가 갈라지면, 교체가 조용히 안 일어나거나
// 기계값으로 기계값을 밀어낸다. 오판 29(대응표가 존재하지 않는 값을 봤다)와 같은 계열이다.
func TestQualifiedSourceListMatchesPredicate(t *testing.T) {
	got := qualifiedSourceList()
	if len(got) == 0 {
		t.Fatal("자격 출처 목록이 비었다 — 교체가 한 번도 안 일어난다")
	}
	for _, s := range got {
		if !qualifiedSource(s) {
			t.Errorf("%q 가 목록에 있는데 qualifiedSource 는 아니라고 한다", s)
		}
	}
	// 기계값은 교체 근거가 아니다. 기계값으로 기계값을 밀어내는 것은 교체가 아니다.
	for _, s := range []string{"gtranslate", "codex-fallback", "romanization", "kana-rule", "opencc", "unknown", ""} {
		for _, q := range got {
			if q == s {
				t.Errorf("%q 가 교체 근거로 들어가 있다", s)
			}
		}
	}
}

// 제안의 성격 표시는 버리지 않는다 — 모르면 'unspecified' 로 적는다(D-37).
func TestSuggestionBasisNeverDropsTheValue(t *testing.T) {
	for in, want := range map[string]string{
		"literal": "literal", "translated": "literal",
		"transliteration": "transliteration", "romanization": "transliteration",
		"official": "official", "": "unspecified", "무슨말": "unspecified",
	} {
		if got := suggestionBasis(in); got != want {
			t.Errorf("%q → %q, 기대 %q", in, got, want)
		}
	}
}

// 지문은 **물은 것**만 덮어야 한다. 제안이 지문에 들어가면 같은 기사를 다시 준비할 때
// 모델 출력이 달라져 같은 키가 ErrConflict 로 튕긴다.
func TestAskedForDropsSuggestions(t *testing.T) {
	in := Input{
		Terms: []Term{{KO: "기쁜 우리 좋은 날", Type: "drama",
			Suggestions: map[string]Suggestion{"ja": {Value: "私たちのうれしい良き日", Basis: "literal"}}}},
		Locales:        []string{"ja"},
		SuggestionMeta: SuggestionMeta{Producer: "presslocale", Model: "gpt-5.6-sol"},
	}
	a := in.AskedFor()
	if a.Terms[0].Suggestions != nil || a.SuggestionMeta.Producer != "" {
		t.Fatal("AskedFor 가 제안을 안 걷어냈다")
	}
	if in.Terms[0].Suggestions == nil {
		t.Fatal("원본을 건드렸다 — 저장할 것이 사라진다")
	}
	b := in
	b.Terms = []Term{{KO: "기쁜 우리 좋은 날", Type: "drama",
		Suggestions: map[string]Suggestion{"ja": {Value: "우리의 기쁜 좋은 날", Basis: "literal"}}}}
	if hash(a) != hash(b.AskedFor()) {
		t.Fatal("제안만 다른데 지문이 달라졌다 — 같은 기사를 다시 준비하면 ErrConflict 가 난다")
	}
}
