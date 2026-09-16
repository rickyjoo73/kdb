package kdb

import (
	"os"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

func orgEnt(ko string, p31 []string, countries []string, desc string) *wikidata.Entity {
	return &wikidata.Entity{
		QID:          "Q1",
		Labels:       map[string]string{"ko": ko},
		Aliases:      map[string][]string{},
		InstanceOf:   p31,
		CountryQIDs:  countries,
		Descriptions: map[string]string{"en": desc},
	}
}

// ★새 유형 후보 103건 전부가 앵커 0 이었다 (2026-09-16 실측).
//
//	앵커가 없으면 승급이 안 되고, 승급이 안 되면 다국어가 안 채워진다.
//	그래서 정당·기관·기업의 ja/zh/vi 가 전부 0 이었다.
//
// 이 시험이 지키는 것은 "많이 붙이기"가 아니라 **어떤 조건에서 붙이는가** 다.
func TestOrgAnchorVerdict(t *testing.T) {
	cases := []struct {
		name    string
		ko, typ string
		ent     *wikidata.Entity
		want    orgAnchorDecision
		wantWhy string
	}{
		{
			// 고용노동부 — P31=ministry, P17=한국. 이 레인이 존재하는 이유.
			"기관: 유형도 국가도 맞다", "고용노동부", "government_body",
			orgEnt("고용노동부", []string{"Q192350"}, []string{"Q884"}, ""),
			orgAnchorPromote, "country-kr",
		},
		{
			// 설명문에 국적이 없어도 P17 이 답한다 — person 관문이었다면 떨어졌다.
			"기관: 설명이 비어도 P17 이 답한다", "국세청", "government_body",
			orgEnt("국세청", []string{"Q327333"}, []string{"Q884"}, ""),
			orgAnchorPromote, "country-kr",
		},
		{
			// 반대로 P17 이 없을 때는 설명문이 대신 말해 준다.
			"국가가 없으면 설명문이 대신", "대한축구협회", "organization",
			orgEnt("대한축구협회", []string{"Q4438121"}, nil, "governing body of association football in South Korea"),
			orgAnchorPromote, "country-desc",
		},
		{
			// **국가도 설명도 없다.** QID 는 맞으니 앵커는 남기되 승급은 안 한다.
			"모르면 앵커만 남기고 멈춘다", "한국도자재단", "organization",
			orgEnt("한국도자재단", []string{"Q157031"}, nil, ""),
			orgAnchorHold, "country-unknown",
		},
		{
			// 국가가 **적혀 있는데 한국이 아니다.** 모르는 것과 다르다.
			"국가가 한국이 아니라고 적혀 있으면 붙이지 않는다", "레 미제라블", "musical_play",
			orgEnt("레 미제라블", []string{"Q2743"}, []string{"Q142"}, "musical"),
			orgAnchorSkip, "foreign",
		},
		{
			// P31 이 우리 유형과 어긋난다 — 이름만 같은 다른 것이다.
			"유형이 어긋나면 붙이지 않는다", "블루 아카이브", "government_body",
			orgEnt("블루 아카이브", []string{"Q7889"}, []string{"Q884"}, ""),
			orgAnchorSkip, "type-mismatch",
		},
		{
			// ★모르는 것으로 승급하지 않는다(D-37). 표에 없는 P31 은 "틀렸다"가 아니라
			//   "우리가 아직 안 적었다" 이므로, 판정 없이 지나가고 로그로 알린다.
			"우리 표에 없는 P31 은 판정하지 않는다", "한국캐릭터학회", "organization",
			orgEnt("한국캐릭터학회", []string{"Q7777777"}, []string{"Q884"}, ""),
			orgAnchorSkip, "type-unknown",
		},
		{
			// 이름이 실제로 같아야 한다. 검색 결과 1위를 무검증 채택하던 시절의 오염 경로.
			"이름이 다르면 붙이지 않는다", "동양대", "school",
			orgEnt("동양대학교 부설연구소", []string{"Q3918"}, []string{"Q884"}, ""),
			orgAnchorSkip, "name-mismatch",
		},
		{
			// "이름 그 자체" 항목 — person 레인이 이 경로로 87건 오염됐다.
			"이름 항목은 실재의 근거가 아니다", "보름", "organization",
			orgEnt("보름", []string{"Q11879590"}, []string{"Q884"}, ""),
			orgAnchorSkip, "name-element",
		},
		{
			"항목을 못 받아왔으면 아무것도 하지 않는다", "무엇", "company",
			nil, orgAnchorSkip, "fetch-failed",
		},
	}
	for _, c := range cases {
		got, why, _ := orgAnchorVerdict(c.ko, c.typ, c.ent)
		if got != c.want || why != c.wantWhy {
			t.Errorf("%s: (%v,%q), 기대 (%v,%q)", c.name, got, why, c.want, c.wantWhy)
		}
	}
}

// ★앵커 확신도는 승급분과 보류분이 달라야 한다. 같은 값을 주면 나중에 둘을 못 가른다.
func TestHeldAnchorIsLessConfidentThanPromoted(t *testing.T) {
	if anchorConfidence(orgAnchorHold) >= anchorConfidence(orgAnchorPromote) {
		t.Error("보류 앵커가 승급 앵커만큼 확신돼 있다 — 둘을 가릴 수 없게 된다")
	}
}

// ★이 레인이 보는 유형은 **실제로 존재하는 유형**이어야 한다.
// entity_types.go 가 단일 어휘표다 — 여기에만 있는 이름을 쓰면 SQL 이 조용히 0건을 돌려준다
// (관리 화면 유형 필터가 없는 값 4개로 HTTP 500 을 내던 것과 같은 계열, 2026-09-16).
func TestOrgAnchorTypesAreRealTypes(t *testing.T) {
	for _, typ := range OrgAnchorTypes {
		if !ValidEntityType(typ) {
			t.Errorf("%q 는 없는 유형이다 — 이 레인은 영원히 0건을 돈다", typ)
		}
		if PlaceholderTypes[typ] {
			t.Errorf("%q 는 임시 보관함이다 — 앵커를 붙일 대상이 아니다", typ)
		}
	}
	// 사람·그룹처럼 이미 제 레인이 있는 유형은 겹치면 안 된다(같은 행을 두 레인이 집는다).
	for _, typ := range OrgAnchorTypes {
		if typ == "person" {
			t.Error("person 은 DrainWikidataPersonCandidates 가 본다 — 두 레인이 같은 행을 집는다")
		}
	}
}

// ★관문 넷은 **전부** 있어야 한다. 하나라도 빠지면 그쪽으로만 오염이 들어온다.
func TestOrgAnchorKeepsAllFourGates(t *testing.T) {
	b, err := os.ReadFile("org_anchor_drain.go")
	if err != nil {
		t.Fatalf("org_anchor_drain.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, want := range []string{
		"IsNameElement",               // 이름 항목 배제
		"wikidata.EntityMatchesQuery", // 이름 일치 — SearchAndFetch 와 같은 함수
		"AnchorTypeAllowed",           // P31 유형 일치 — 감사·분류와 같은 표
		"IsSouthKorean",               // P17/P495 국가
	} {
		if !strings.Contains(src, want) {
			t.Errorf("관문 %s 가 없다 — 그쪽으로 오염이 들어온다", want)
		}
	}
}
