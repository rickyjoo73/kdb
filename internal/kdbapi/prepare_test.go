package kdbapi

import (
	"encoding/json"
	"testing"
)

func TestParsePrepareTerm(t *testing.T) {
	// 문자열 형태
	if pt := parsePrepareTerm(json.RawMessage(`"아이유"`)); pt.Ko != "아이유" || pt.Type != "" {
		t.Fatalf("string term = %+v", pt)
	}
	// 객체 형태 {ko,type}
	if pt := parsePrepareTerm(json.RawMessage(`{"ko":"박보검","type":"person"}`)); pt.Ko != "박보검" || pt.Type != "person" {
		t.Fatalf("object term = %+v", pt)
	}
	// 공백 trim
	if pt := parsePrepareTerm(json.RawMessage(`"  엔믹스  "`)); pt.Ko != "엔믹스" {
		t.Fatalf("trim = %q", pt.Ko)
	}
}

func TestExactKoMatch_TypeHintPriority(t *testing.T) {
	matches := []Entity{
		{ID: "1", CanonicalKO: "신화", EntityType: "group"},
		{ID: "2", CanonicalKO: "신화", EntityType: "term"},
	}
	// type 힌트 'term' → 동명 group 이 아니라 term 선택.
	if m, ok := exactKoMatch(matches, "신화", "term"); !ok || m.ID != "2" {
		t.Fatalf("type hint match = %+v ok=%v", m, ok)
	}
	// 힌트 없으면 첫 정확 일치(group).
	if m, ok := exactKoMatch(matches, "신화", ""); !ok || m.ID != "1" {
		t.Fatalf("no-hint match = %+v", m)
	}
	// alias 일치 fallback.
	al := []Entity{{ID: "9", CanonicalKO: "아이유", Aliases: AliasSets{KO: []string{"지은"}}}}
	if m, ok := exactKoMatch(al, "지은", ""); !ok || m.ID != "9" {
		t.Fatalf("alias match = %+v", m)
	}
}

func TestLocaleValuesAndGaps(t *testing.T) {
	e := Entity{CanonicalEN: "IU", CanonicalJA: "アイユー", CanonicalES: ""}
	values, _, missing := localeValuesAndGaps(e, []string{"en", "ja", "es"}, false)
	if values["en"] != "IU" || values["ja"] != "アイユー" {
		t.Fatalf("values = %v", values)
	}
	if len(missing) != 1 || missing[0] != "es" {
		t.Fatalf("missing = %v, want [es]", missing)
	}
}

func TestLocaleValuesAndGapsVerifiedOnly(t *testing.T) {
	// en=wikidata(검증), ja=codex(미검증). verified_only 면 ja 는 missing 으로 빠지고
	// en 만 값+provenance 로 반환.
	e := Entity{
		CanonicalEN: "IU", CanonicalENSource: "wikidata-label",
		CanonicalJA: "アイユー", CanonicalJASource: "codex-fallback",
	}
	values, prov, missing := localeValuesAndGaps(e, []string{"en", "ja"}, true)
	if values["en"] != "IU" || prov["en"] != "wikidata-label" {
		t.Fatalf("verified values=%v prov=%v", values, prov)
	}
	if _, ok := values["ja"]; ok {
		t.Fatalf("ja(codex) should be gated out under verified_only: %v", values)
	}
	if len(missing) != 1 || missing[0] != "ja" {
		t.Fatalf("missing = %v, want [ja]", missing)
	}
}

func TestNormalizePrepareLocales(t *testing.T) {
	if got := normalizePrepareLocales(nil); len(got) != 8 {
		t.Fatalf("default locales = %d, want 8", len(got))
	}
	if got := normalizePrepareLocales([]string{"zh-hant", "PT-BR"}); got[0] != "zh_hant" || got[1] != "pt_br" {
		t.Fatalf("normalize = %v", got)
	}
}
