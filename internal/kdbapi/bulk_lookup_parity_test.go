package kdbapi

import (
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
