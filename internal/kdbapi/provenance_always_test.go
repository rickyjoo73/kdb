package kdbapi

import "testing"

// ★값이 어디서 왔는지는 묻지 않아도 말해 준다 (2026-09-15).
//
//	서빙되는 영문의 33%(4,204칸)가 기계번역인데, 소비자는 verified_only 를 쓰지 않는 한
//	그 사실을 알 방법이 없었다. 그리고 그걸 쓰는 소비자는 presslocale 하나(요청의 22%)뿐이다.
func TestProvenanceIsAttachedWithoutAsking(t *testing.T) {
	e := Entity{CanonicalKO: "수제천", CanonicalEN: "Handmade Cloth", CanonicalENSource: "gtranslate"}
	attachLocaleProvenance(&e)
	if e.LocaleProvenance["en"] == "" {
		t.Fatal("출처 라벨이 안 붙었다 — 소비자가 기계번역을 구분할 수 없다")
	}
	if provenanceIsVerified(e.LocaleProvenance["en"]) {
		t.Fatalf("기계번역이 검증됨으로 표시됐다: %q", e.LocaleProvenance["en"])
	}
	// 빈 칸에는 출처를 지어내지 않는다.
	if _, ok := e.LocaleProvenance["ja"]; ok {
		t.Fatal("값이 없는 locale 에 출처가 붙었다")
	}
}

// 검증 게이트가 이미 붙였으면 덮어쓰지 않는다 — 게이트가 비운 칸을 되살리면 안 된다.
func TestVerifiedGateLabelsAreNotOverwritten(t *testing.T) {
	e := Entity{CanonicalEN: "IU", CanonicalENSource: "wikidata-label"}
	applyLocaleVerifiedGate(&e)
	before := e.LocaleProvenance["en"]
	attachLocaleProvenance(&e)
	if e.LocaleProvenance["en"] != before {
		t.Fatalf("게이트 라벨이 덮였다: %q → %q", before, e.LocaleProvenance["en"])
	}
}

// 게이트와 라벨이 **같은 locale 목록**을 봐야 한다 — 한쪽에만 늘면 그 칸은 출처 없이 나간다.
func TestGateAndProvenanceShareTheSameLocaleList(t *testing.T) {
	e := Entity{
		CanonicalEN: "a", CanonicalJA: "a", CanonicalVI: "a", CanonicalZH: "a",
		CanonicalZHHant: "a", CanonicalES: "a", CanonicalID: "a", CanonicalPTBR: "a",
		CanonicalENSource: "gtranslate",
	}
	attachLocaleProvenance(&e)
	if len(e.LocaleProvenance) != len(localeValueFields(&e)) {
		t.Fatalf("출처가 %d개 붙었는데 locale 칸은 %d개다", len(e.LocaleProvenance), len(localeValueFields(&e)))
	}
}
