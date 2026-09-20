package wikidata

import (
	"encoding/json"
	"testing"
)

// ★이 시험이 지키는 것 (2026-09-20).
//
//	BatchClaims 는 itemQIDs 를 쓰고, itemQIDs 는 datavalue.type 이 `wikibase-entityid`
//	인 것만 통과시킨다. 학명(P225)·공식명(P1448) 은 `string` 이라 **한 건도 안 나온다.**
//	부르는 쪽은 그것을 "이 항목엔 학명이 없다"로 읽는다 — 값이 있는데 없다고 답하는
//	조용한 0건이다. 그래서 문자열 전용 문(stringValues/BatchStringClaims)을 따로 두고,
//	두 문이 서로 다른 타입을 본다는 사실을 여기서 못 박는다.

func snak(typ string, raw string) claimSnak {
	var c claimSnak
	c.Mainsnak.DataValue.Type = typ
	c.Mainsnak.DataValue.Value = json.RawMessage(raw)
	return c
}

func TestStringValues_문자열_datavalue를_뽑는다(t *testing.T) {
	got := stringValues([]claimSnak{snak("string", `"Sebastes schlegelii"`)}, 50)
	if len(got) != 1 || got[0] != "Sebastes schlegelii" {
		t.Fatalf("stringValues = %v, want [Sebastes schlegelii]", got)
	}
}

func TestStringValues_엔티티ID는_문자열이_아니다(t *testing.T) {
	got := stringValues([]claimSnak{snak("wikibase-entityid", `{"id":"Q1985193"}`)}, 50)
	if len(got) != 0 {
		t.Fatalf("stringValues = %v, want 빈 목록 — 엔티티 ID 는 이 문의 대상이 아니다", got)
	}
}

func TestItemQIDs_문자열은_못_뽑는다_그래서_별도_문이_필요하다(t *testing.T) {
	// 이 시험은 **결함을 박제한다.** itemQIDs 가 학명을 뽑게 되면(동작이 바뀌면)
	// 여기서 깨지고, 그때 두 문을 다시 합칠지 판단하면 된다.
	got := itemQIDs([]claimSnak{snak("string", `"Sebastes schlegelii"`)}, 50)
	if len(got) != 0 {
		t.Fatalf("itemQIDs = %v — 동작이 바뀌었다. BatchStringClaims 와의 역할 분담을 다시 보라", got)
	}
}

func TestStringValues_공백과_상한(t *testing.T) {
	in := []claimSnak{
		snak("string", `"  "`),
		snak("string", `"  Konosirus punctatus "`),
		snak("string", `"Platycodon grandiflorus"`),
	}
	got := stringValues(in, 1)
	if len(got) != 1 || got[0] != "Konosirus punctatus" {
		t.Fatalf("stringValues = %v, want [Konosirus punctatus] (공백 제거 + 상한 1)", got)
	}
}
