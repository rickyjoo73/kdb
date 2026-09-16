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
			// **국가도 설명도 없다.** 이름도 유형도 맞지만 쓰지 않는다 —
			// 그렇게 걸린 5건 중 4건이 틀린 앵커였다(공군=개념, 레 미제라블=해외).
			"모르면 아무것도 쓰지 않는다", "한국도자재단", "organization",
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

// ★보류는 **아무것도 쓰지 않는다** (2026-09-16 dry-run 이 뒤집은 설계).
//
//	처음엔 "QID 는 맞으니 앵커만 붙이자"고 했다. 그렇게 걸린 5건 중 4건이 틀렸다:
//	  공군 → Q61883 "air force" 는 병종이라는 **개념**이지 대한민국 공군이 아니다
//	  레 미제라블 → Q830581(1980년 뮤지컬) · 금도끼 은도끼 → Q3220700(이솝 우화)
//	  로보티즈 → Q110114841 만 맞았다
//
//	이름이 정확히 같고 P31 도 맞는데 틀렸다. 가름막이 국가 하나뿐이었고 그 값이
//	비어 있었기 때문이다. 빈칸이 곧 통과가 되면 관문이 아니다.
func TestHoldWritesNothing(t *testing.T) {
	b, err := os.ReadFile("org_anchor_drain.go")
	if err != nil {
		t.Fatalf("org_anchor_drain.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	// 보류 분기가 쓰기보다 **먼저** 끊어야 한다.
	i := strings.Index(src, "if dec == orgAnchorHold {")
	j := strings.Index(src, "INSERT INTO kwave_entity_external_refs")
	if i < 0 {
		t.Fatal("보류를 따로 끊는 분기가 없다 — 한국 근거 없는 앵커가 저장된다")
	}
	if j >= 0 && i > j {
		t.Error("보류 분기가 앵커 저장보다 뒤에 있다 — 틀린 앵커가 먼저 저장된다")
	}
	if strings.Contains(src, "anchorConfidence") {
		t.Error("보류 앵커용 확신도가 남아 있다 — 보류는 쓰지 않으므로 쓸 일이 없다")
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

// ★"못 했다"를 "없다"로 적으면 안 된다 (2026-09-16).
//
//	첫 dry-run 이 서울대학교·고용노동부·FC서울·쿠팡을 포함해 120건 전부
//	«검색없음» 으로 보고했다. 실제로는 컨테이너에 CA 인증서가 없어 위키데이터에
//	한 번도 닿지 못한 것이었다. 그 보고를 믿었다면 "위키데이터에 없으니 다른
//	출처를 붙이자"는 결론까지 갔을 것이다.
//
//	이 저장소가 이미 한 번 데인 계열이다(4e14f6f).
func TestSearchFailureIsNotCountedAsAbsence(t *testing.T) {
	b, err := os.ReadFile("org_anchor_drain.go")
	if err != nil {
		t.Fatalf("org_anchor_drain.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	if strings.Contains(src, "serr != nil || len(cands) == 0") {
		t.Error("검색 오류와 무결과를 한 줄로 묶었다 — 못 한 것이 없는 것으로 적힌다")
	}
	if !strings.Contains(src, "r.SearchFailed++") {
		t.Error("검색 실패를 따로 세지 않는다")
	}
	var r OrgAnchorResult
	r.SearchFailed = 3
	if r.NoHit != 0 {
		t.Error("검색 실패가 무결과 집계에 섞였다")
	}
}

// ★승급한 행은 검증 레인이 **집을 수 있어야** 한다.
//
//	검증 레인들은 전부 verification_tier='unverified' 를 조건으로 집는다
//	(verify/active_audit·tmdb_anchor_drain·mbgroup_anchor_drain). 빈 문자열로 두면
//	승급해 놓고 아무도 안 보는 자리에 앉히는 꼴이다.
//
//	'authoritative' 로 올려서도 안 된다 — 앵커가 권위 있다는 것과 표기가 권위
//	있다는 것은 다르다. 그 둘을 섞어 활성 인물 110건이 "틀린 항목에서 긁어온
//	이름을 가장 믿을 만한 등급으로" 내보냈다.
func TestPromotedRowsLandInTheVerifyQueue(t *testing.T) {
	b, err := os.ReadFile("org_anchor_drain.go")
	if err != nil {
		t.Fatalf("org_anchor_drain.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "THEN 'unverified' ELSE verification_tier END") {
		t.Error("승급 시 검증 등급을 안 둔다 — 검증 레인이 영영 못 집는다")
	}
	if strings.Contains(src, "verification_tier = 'authoritative'") {
		t.Error("앵커가 권위 있다고 표기까지 권위 있다고 적었다 — 110건을 오염시킨 그 길이다")
	}
}

// ★"FC서울" 로는 구단 본체에 닿지 못한다 (2026-09-16 실측).
//
//	ko 검색 상위 7건이 전부 파생 문서였다 — FC 서울 아카데미 · FC 서울의 수상자 ·
//	FC 서울의 국제클럽대항전 · FC 서울 코칭스태프 명단 · FC 서울의 역사.
//	위키데이터 표기가 "FC 서울"(사이 띄움)이고 검색이 앞맞춤이기 때문이다.
//
//	넓히는 것은 **무엇을 찾아보는가**이지 **무엇을 같다고 하는가**가 아니다.
//	normalizeName 이 공백을 지우므로 일치 기준은 손대지 않아도 이미 같다.
func TestSearchQueriesReachSpacedForms(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"FC서울", "FC 서울"},
		{"강원FC", "강원 FC"},
		{"수원KT소닉붐", "수원 KT 소닉붐"},
		{"울산HD", "울산 HD"},
	} {
		got := searchQueries(c.in)
		if len(got) != 2 || got[0] != c.in || got[1] != c.want {
			t.Errorf("%q → %v, 기대 [%q %q]", c.in, got, c.in, c.want)
		}
	}
	// 넣을 자리가 없으면 하나만 돌려준다 — 같은 검색을 두 번 하지 않는다.
	for _, one := range []string{"고용노동부", "FC 서울", "대한축구협회"} {
		if got := searchQueries(one); len(got) != 1 {
			t.Errorf("%q → %v, 하나여야 한다", one, got)
		}
	}
}
