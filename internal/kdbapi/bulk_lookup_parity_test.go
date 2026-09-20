package kdbapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// 묶음 조회가 단건 조회와 **같은 계약**을 주는지 고정한다.
//
// ★2026-09-15 실측 결함. 우리는 "한도를 아끼려면 묶어 보내라"고 권하는데,
// 묶음에는 verified_only 게이트도 out_of_scope 종결 통지도 없었다.
// 권장하는 문에 계약이 빠지면 **권장을 따를수록 손해**가 된다.
// 소비자가 "이름 조회로는 출처 등급을 알 수 없다"고 한 것이 정확한 관찰이었다.
func TestBulkLookupHasSameContractAsSingle(t *testing.T) {
	single := reflect.TypeOf(LookupRequest{})
	bulk := reflect.TypeOf(BulkLookupRequest{})
	for _, name := range []string{"Type", "Status", "Limit", "VerifiedOnly"} {
		sf, sok := single.FieldByName(name)
		bf, bok := bulk.FieldByName(name)
		if !sok || !bok {
			t.Fatalf("%s 가 한쪽에만 있다 (단건 %v / 묶음 %v)", name, sok, bok)
		}
		if sf.Type != bf.Type {
			t.Errorf("%s 의 타입이 다르다: 단건 %v · 묶음 %v", name, sf.Type, bf.Type)
		}
		if sf.Tag.Get("json") != bf.Tag.Get("json") {
			t.Errorf("%s 의 json 이름이 다르다: %q vs %q", name, sf.Tag.Get("json"), bf.Tag.Get("json"))
		}
	}
	// 응답의 종결 통지도 같은 자리에 있어야 한다.
	if _, ok := reflect.TypeOf(LookupResponse{}).FieldByName("Status"); !ok {
		t.Fatal("LookupResponse.Status 가 없다 — miss 와 out_of_scope 를 구분할 수 없다")
	}
}

// 문서가 묶음 조회의 verified_only 를 말해야 한다. 있는데 아무도 모르면 없는 것과 같다.
func TestDocsMentionBulkVerifiedOnly(t *testing.T) {
	if !strings.Contains(docsHTML, "/v1/lookup/bulk") {
		t.Fatal("문서에 /v1/lookup/bulk 가 없다")
	}
	if !strings.Contains(docsHTML, "locale_provenance") {
		t.Fatal("문서에 locale_provenance 가 없다 — 출처 등급을 어떻게 받는지 알 수 없다")
	}
}

// 묶음 조회가 **이름마다 유형**을 받는지 고정한다.
//
// ★2026-09-15 지적으로 드러난 결함. queries 가 []string 이라 Type 이 묶음 전체에
// 하나뿐이었다 — "박보검=person · 폭싹 속았수다=drama" 를 한 번에 못 보냈다.
// 그런데 묶음이 권장 경로다. 권장하는 문이 정체성 정보를 못 받으면 동명이인을 가릴
// 재료가 애초에 안 들어온다.
func TestBulkQueriesCarryTypePerName(t *testing.T) {
	raw := []json.RawMessage{
		json.RawMessage(`"아이유"`),
		json.RawMessage(`{"ko":"채영","type":"person","context":"트와이스 채영이"}`),
		json.RawMessage(`{"ko":"폭싹 속았수다","type":"drama"}`),
		json.RawMessage(`{"ko":"","type":"person"}`),
		json.RawMessage(`{"ko":"엑스","type":"엉터리"}`),
		json.RawMessage(`12345`),
	}
	got := parseBulkQueries(raw, "person")
	if len(got) != 4 {
		t.Fatalf("파싱 %d건, 기대 4건: %+v", len(got), got)
	}
	if got[0].Ko != "아이유" || got[0].Type != "person" {
		t.Errorf("문자열 원소가 묶음 기본 유형을 못 받았다: %+v", got[0])
	}
	if got[2].Type != "drama" {
		t.Errorf("항목별 유형이 묶음 기본값을 못 덮었다: %+v", got[2])
	}
	if got[3].Type != "" {
		t.Errorf("엉터리 유형이 그대로 통과했다: %+v", got[3])
	}
	if got[1].Context == "" {
		t.Errorf("항목별 문맥이 사라졌다: %+v", got[1])
	}
}

// 동명이 둘 이상이면 **하나를 골라 주지 않는다**. 문맥 없이 고르면 틀린 사람을 확정하고,
// 소비자가 그것을 저장한다. 후보를 전부 주고 status=ambiguous 로 알린다.
func TestHomonymChoiceIsReportedNotDecided(t *testing.T) {
	two := []Entity{{CanonicalKO: "채영", Disambig: "(TWICE)"}, {CanonicalKO: "채영", Disambig: "(CLC)"}}
	if !hasHomonymChoice(two, "채영") {
		t.Fatal("동명 둘을 못 알아봤다")
	}
	// 부분일치는 동명이 아니다 — '중앙동' 을 찾을 때 'CU 송탄중앙동점' 은 다른 이름이다.
	partial := []Entity{{CanonicalKO: "중앙동"}, {CanonicalKO: "CU 송탄중앙동점"}}
	if hasHomonymChoice(partial, "중앙동") {
		t.Fatal("부분일치를 동명으로 셌다")
	}
	if hasHomonymChoice([]Entity{{CanonicalKO: "아이유"}}, "아이유") {
		t.Fatal("하나뿐인데 모호하다고 했다")
	}
}

// **우리가 권하는 문**에 계약이 다 있는지 한 번에 고정한다.
//
// ★오늘만 네 번 같은 계열로 깨졌다: verified_only · status · 이름별 type · 빈칸 이유.
// 전부 "단건/핫패스에는 있고 우리가 권하는 문에는 없던" 것이다.
// 문서가 lookup/bulk 를 권하므로, 그 문이 match 와 같은 안내를 줘야 한다.
func TestRecommendedPathsCarryTheSameContract(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  reflect.Type
		want []string
	}{
		{"LookupRequest", reflect.TypeOf(LookupRequest{}), []string{"VerifiedOnly", "IncludeAbsent", "Locales"}},
		{"BulkLookupRequest", reflect.TypeOf(BulkLookupRequest{}), []string{"VerifiedOnly", "IncludeAbsent", "Locales", "Type"}},
		{"MatchEntitiesRequest", reflect.TypeOf(MatchEntitiesRequest{}), []string{"VerifiedOnly", "IncludeAbsent"}},
	} {
		for _, f := range tc.want {
			if _, ok := tc.typ.FieldByName(f); !ok {
				t.Errorf("%s 에 %s 가 없다 — 권하는 문에 계약이 빠지면 권장을 따를수록 손해다", tc.name, f)
			}
		}
	}
	// 빈칸 안내를 실어 나를 자리가 응답에도 있어야 한다.
	if _, ok := reflect.TypeOf(Entity{}).FieldByName("AbsentLocales"); !ok {
		t.Fatal("Entity.AbsentLocales 가 없다 — lookup 이 빈칸 이유를 실어 보낼 수 없다")
	}
	if _, ok := reflect.TypeOf(PrepareItem{}).FieldByName("AbsentLocales"); !ok {
		t.Fatal("PrepareItem.AbsentLocales 가 없다 — prepare 의 missing 이 이유를 못 말한다")
	}
}

// 없음의 이유를 정하는 규칙을 고정한다. 영어 폴백도 '없음'이다 —
// 값이 비어 있지 않아 처음엔 안내를 안 붙였는데, 소비자가 조치해야 하는 자리가 바로 거기다.
func TestAbsenceReasons(t *testing.T) {
	for _, c := range []struct {
		val, src, typ string
		fallback      bool
		want, hint    string
	}{
		{"", "", "person", false, "no_value", "transliterate"},
		{"", "", "drama", false, "no_value", "translate_title"},
		{"Love Is Coming", "tmdb", "drama", true, "fallback_en", "translate_title"},
		{"パク・ボゴム", "codex-fallback", "person", false, "llm_only", "transliterate"},
		{"パク・ボゴム", "wikidata-label", "person", false, "", ""},
	} {
		got := absenceFor(c.val, c.src, c.typ, "", c.fallback)
		if got.Reason != c.want || got.FillHint != c.hint {
			t.Errorf("(%q,%q,%q,fb=%v) → %+v, 기대 %q/%q", c.val, c.src, c.typ, c.fallback, got, c.want, c.hint)
		}
	}
}
