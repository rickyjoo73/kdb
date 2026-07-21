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

func TestPrepareMatchSafe(t *testing.T) {
	// 단일 엔티티: 힌트가 틀려도(person 요청, 실재 channel_outlet) 안전 → 서빙.
	single := []Entity{{ID: "1", CanonicalKO: "최현서", EntityType: "channel_outlet"}}
	if !prepareMatchSafe(single, single[0], "최현서", "person") {
		t.Fatal("단일 엔티티는 오힌트여도 safe 여야 함")
	}
	// 동명이인 다수 + 힌트가 정확히 한 타입을 집음 → 안전.
	homo := []Entity{
		{ID: "1", CanonicalKO: "김하늘", EntityType: "person"},
		{ID: "2", CanonicalKO: "김하늘", EntityType: "movie"},
	}
	if !prepareMatchSafe(homo, homo[0], "김하늘", "person") {
		t.Fatal("동명이인이라도 힌트가 단일 타입 집으면 safe")
	}
	// 동명이인 다수 + 힌트가 아무 타입도 못 집음 → 애매 → 서빙 금지.
	if prepareMatchSafe(homo, homo[0], "김하늘", "song_album") {
		t.Fatal("동명이인 + 힌트 미스는 unsafe 여야 함")
	}
	// 동명이인 다수 + 힌트 없음 → 서빙 금지.
	if prepareMatchSafe(homo, homo[0], "김하늘", "") {
		t.Fatal("동명이인 + 무힌트는 unsafe 여야 함")
	}
	// 같은 타입 동명이인 2건(힌트가 한 타입에 2건 매칭) → 애매 → 금지.
	sameType := []Entity{
		{ID: "1", CanonicalKO: "아몬드", EntityType: "movie"},
		{ID: "2", CanonicalKO: "아몬드", EntityType: "movie"},
	}
	if prepareMatchSafe(sameType, sameType[0], "아몬드", "movie") {
		t.Fatal("같은 타입 동명 2건은 힌트로도 unsafe")
	}
	// NeedsDisambig 플래그면 무조건 금지.
	dis := []Entity{{ID: "1", CanonicalKO: "X", EntityType: "person", NeedsDisambig: true}}
	if prepareMatchSafe(dis, dis[0], "X", "person") {
		t.Fatal("NeedsDisambig 는 unsafe")
	}
}

func TestPrepareReady(t *testing.T) {
	// 미지정(기본 코어 en): en 안 비면 ready, 니치 로케일 비어도 ready.
	if !prepareReady([]string{"ja", "es", "zh"}, nil) {
		t.Fatal("en 채워짐(미지정)=ready 여야 함")
	}
	if prepareReady([]string{"en", "ja"}, nil) {
		t.Fatal("en 비면(미지정) preparing 여야 함")
	}
	// 명시 요청 로케일만 기준: en,ja 요청 시 zh 비어도 ready.
	if !prepareReady([]string{"zh"}, []string{"en", "ja"}) {
		t.Fatal("요청(en,ja) 다 차면 zh 비어도 ready")
	}
	if prepareReady([]string{"ja"}, []string{"en", "ja"}) {
		t.Fatal("요청 로케일(ja) 비면 preparing")
	}
	// 요청 로케일 정규화(PT-BR → pt_br).
	if prepareReady([]string{"pt_br"}, []string{"PT-BR"}) {
		t.Fatal("정규화된 요청 로케일이 비면 preparing")
	}
}
