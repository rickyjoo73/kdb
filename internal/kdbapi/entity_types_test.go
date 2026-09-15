package kdbapi

import (
	"os"
	"strings"
	"testing"
)

// 정치·경제·시사·스포츠 유형이 **API 에서 받아들여져야** 한다.
//
// ★운영자 지시 (2026-09-15): "KDB가 정치·경제·스포츠까지 받게 되면 정당·정부기관·
// 기업·선수 같은 유형 목록을 모두 준비해야지. 그래야 문서도 업데이트하고."
func TestNewCivicTypesAreAccepted(t *testing.T) {
	for _, typ := range []string{
		"political_party", "government_body", "company",
		"organization", "sports_team", "school",
	} {
		if !validEntityType(typ) {
			t.Errorf("%s 를 API 가 거부한다 — 소비자가 보내도 400 이 난다", typ)
		}
	}
	// 기존 유형이 하나라도 빠지면 안 된다.
	for _, typ := range []string{
		"person", "group", "show", "drama", "movie", "song_album", "agency",
		"channel_outlet", "brand_place", "event_tour", "character", "term", "unknown",
	} {
		if !validEntityType(typ) {
			t.Errorf("기존 유형 %s 가 사라졌다", typ)
		}
	}
	// 사람은 늘리지 않았다 — 선수·정치인은 person + occupation_domain 이다.
	for _, typ := range []string{"athlete", "politician", "businessperson"} {
		if validEntityType(typ) {
			t.Errorf("%s 가 유형으로 들어왔다 — 배우 겸 정치인을 어느 칸에 넣을지 정할 수 없다", typ)
		}
	}
}

// 문서가 **실제로 받는 유형**을 전부 적어야 한다. 안 적으면 소비자는 못 쓴다.
//
// ★운영자가 문서 갱신을 명시적으로 요구했다. 코드만 고치고 문서를 두면 이 변경은
// 소비자에게 없는 것과 같다.
func TestDocsListEveryAcceptedType(t *testing.T) {
	src, err := os.ReadFile("docs.go")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	for _, typ := range []string{
		"person", "group", "show", "drama", "movie", "song_album", "agency",
		"channel_outlet", "brand_place", "event_tour", "character", "term",
		"political_party", "government_body", "company", "organization",
		"sports_team", "school",
	} {
		if !strings.Contains(doc, "<code>"+typ+"</code>") {
			t.Errorf("문서에 %s 가 없다 — 받기는 받는데 쓰는 법을 안 알려준다", typ)
		}
	}
	// 영역 값도 문서에 있어야 소비자가 거를 수 있다.
	for _, d := range []string{"entertainment", "sports", "politics", "business"} {
		if !strings.Contains(doc, "<code>"+d+"</code>") {
			t.Errorf("문서에 영역 %s 가 없다", d)
		}
	}
	// 범위 확대가 문서에 반영됐는지 — 종전 "일반 기업·제품" 금지 문구가 남아 있으면 모순이다.
	if strings.Contains(doc, "일반 행정지명(서울·부산), 일반 기업·제품") {
		t.Error("범위 밖 규정이 옛 문구 그대로다 — 기업을 받으면서 기업을 보내지 말라고 한다")
	}
}
