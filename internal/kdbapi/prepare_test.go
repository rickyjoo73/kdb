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
	// 07-21 en-폴백(오너 승인): 빈 비-en locale 은 en 표기를 provenance='en-fallback'
	// 으로 폴백 서빙 — missing 이 아니다(홀드 제거).
	e := Entity{CanonicalEN: "IU", CanonicalJA: "アイユー", CanonicalES: ""}
	values, prov, missing := localeValuesAndGaps(e, []string{"en", "ja", "es"}, false)
	if values["en"] != "IU" || values["ja"] != "アイユー" {
		t.Fatalf("values = %v", values)
	}
	if values["es"] != "IU" || prov["es"] != "en-fallback" {
		t.Fatalf("es should en-fallback: values=%v prov=%v", values, prov)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want []", missing)
	}
	// en 자체가 비면 폴백 불가 → 그때만 missing.
	e2 := Entity{CanonicalJA: "アイユー"}
	_, _, missing2 := localeValuesAndGaps(e2, []string{"en", "es"}, false)
	if len(missing2) != 2 {
		t.Fatalf("missing2 = %v, want [en es]", missing2)
	}
}

func TestLocaleValuesAndGapsVerifiedOnly(t *testing.T) {
	// en=wikidata(검증), ja=codex(미검증). verified_only 면 미검증 ja 네이티브 값은
	// 빠지되, 07-21 en-폴백에 따라 검증된 en 표기가 provenance='en-fallback' 으로 대신
	// 서빙된다(미검증 값 노출 없이 홀드도 없앰).
	e := Entity{
		CanonicalEN: "IU", CanonicalENSource: "wikidata-label",
		CanonicalJA: "アイユー", CanonicalJASource: "codex-fallback",
	}
	values, prov, missing := localeValuesAndGaps(e, []string{"en", "ja"}, true)
	if values["en"] != "IU" || prov["en"] != "wikidata-label" {
		t.Fatalf("verified values=%v prov=%v", values, prov)
	}
	if values["ja"] != "IU" || prov["ja"] != "en-fallback" {
		t.Fatalf("ja should serve verified en-fallback (never the codex value): values=%v prov=%v", values, prov)
	}
	if len(missing) != 0 {
		t.Fatalf("missing = %v, want []", missing)
	}
	// en 폴백조차 미검증(codex)이면 ja 는 값 없이 missing — 미검증 노출 금지 유지.
	e2 := Entity{
		CanonicalEN: "IU", CanonicalENSource: "codex-fallback",
		CanonicalJA: "アイユー", CanonicalJASource: "codex-fallback",
	}
	v2, _, missing2 := localeValuesAndGaps(e2, []string{"ja"}, true)
	if _, ok := v2["ja"]; ok || len(missing2) != 1 {
		t.Fatalf("unverified en must not fallback: v=%v missing=%v", v2, missing2)
	}
}

func TestExactKoMatchNormalized(t *testing.T) {
	// 감사 07-25: 공백·문장부호 변형은 정규화 폴백으로 매칭(서빙-인테이크 비대칭 제거).
	matches := []Entity{{ID: "1", CanonicalKO: "쇼미더머니", EntityType: "show"}}
	if m, ok := exactKoMatch(matches, "쇼 미 더 머니", ""); !ok || m.ID != "1" {
		t.Fatalf("normalized match = %+v ok=%v", m, ok)
	}
	al := []Entity{{ID: "2", CanonicalKO: "에이비식스", Aliases: AliasSets{EN: []string{"AB6IX"}}}}
	if m, ok := exactKoMatch(al, "ab6ix", ""); !ok || m.ID != "2" {
		t.Fatalf("normalized en-alias match = %+v ok=%v", m, ok)
	}
	// exact 가 있으면 exact 우선(정규화 폴백은 후순위).
	both := []Entity{
		{ID: "3", CanonicalKO: "6시 내고향"},
		{ID: "4", CanonicalKO: "6시내고향"},
	}
	if m, ok := exactKoMatch(both, "6시내고향", ""); !ok || m.ID != "4" {
		t.Fatalf("exact should win over normalized: %+v", m)
	}
}

func TestHasNormalizedHit(t *testing.T) {
	matches := []Entity{{CanonicalKO: "주이재"}, {CanonicalKO: "이주원"}}
	// 부분일치-only 응답 — 발굴 신호 필요(false).
	if hasNormalizedHit(matches, "주이") {
		t.Fatal("substring-only should not count as hit")
	}
	if !hasNormalizedHit([]Entity{{CanonicalKO: "쇼미더머니"}}, "쇼 미 더 머니") {
		t.Fatal("normalized canonical should count as hit")
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
