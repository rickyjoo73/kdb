package kdb

import (
	"regexp"
	"testing"
)

// TestAnchorTypesAreRealEntityTypes — 앵커 표의 값이 **DB 에 실제로 있는 유형**인지 본다.
//
// ★왜 (2026-09-16). 이 표는 감사와 분류가 함께 본다. 여기 없는 유형을 적으면
// 인입에서 통과한 유형을 감사가 «어긋남»으로 찍고, 오타 하나가 조용히 전 유형을
// «판정하지 않음»으로 흘려보낸다. 마이그레이션의 enum 을 그대로 읽어 대조한다 —
// 목록을 두 벌 적으면 어긋나는 날이 온다(칼럼 쌍둥이에서 이미 겪었다).
func TestAnchorTypesAreRealEntityTypes(t *testing.T) {
	known := entityTypeEnumFromMigrations(t)
	if len(known) < 20 {
		t.Fatalf("마이그레이션에서 읽은 유형이 %d개뿐이다 — 읽기가 깨졌다", len(known))
	}
	for qid, types := range anchorExpectedType {
		if len(types) == 0 {
			t.Errorf("%s 에 유형이 비어 있다", qid)
		}
		for _, ty := range types {
			if !known[ty] {
				t.Errorf("%s → %q 는 DB 에 없는 유형이다", qid, ty)
			}
		}
	}
}

// TestAnchorQIDShape — QID 는 Q + 숫자다. 기억으로 적다가 'Q1ui' 같은 것을 넣으면
// 위키데이터 배치 조회가 **통째로** 실패한다(그래서 실제로 40건이 조용히 안 돌아왔다).
func TestAnchorQIDShape(t *testing.T) {
	re := regexp.MustCompile(`^Q[1-9][0-9]*$`)
	for qid := range anchorExpectedType {
		if !re.MatchString(qid) {
			t.Errorf("QID 모양이 아니다: %q", qid)
		}
	}
}

// TestAmbiguousClassesStayAmbiguous — QID 가 **못 가르는** 것을 가른다고 하지 않는다.
// 삼성전자(company)와 하이브(agency)는 둘 다 Q4830453 "business" 다.
func TestAmbiguousClassesStayAmbiguous(t *testing.T) {
	for _, c := range []struct {
		qid   string
		types []string
	}{
		{"Q4830453", []string{"agency", "company"}},
		{"Q4438121", []string{"organization", "sports_team"}},
		{"Q1004", []string{"webtoon", "publication"}},
	} {
		for _, want := range c.types {
			if ok, known := AnchorTypeAllowed(c.qid, want); !known || !ok {
				t.Errorf("%s 는 %q 를 허용해야 한다 (allowed=%v known=%v)", c.qid, want, ok, known)
			}
		}
		if ok, known := AnchorTypeAllowed(c.qid, "person"); !known || ok {
			t.Errorf("%s 가 person 을 허용하면 안 된다", c.qid)
		}
	}
	// 표에 없는 QID 는 **판정하지 않는다**(D-37). false 가 아니라 unknown 이다.
	if _, known := AnchorTypeAllowed("Q99999999", "person"); known {
		t.Error("모르는 QID 를 판정했다")
	}
}

// TestNewTypesHaveAnchorClasses — 0143·0146 으로 늘린 유형은 전부 앵커 클래스를 가져야
// 한다. 유형만 늘리고 표를 안 늘리면 문서는 새 유형을 약속하는데 기계는 모른다.
func TestNewTypesHaveAnchorClasses(t *testing.T) {
	covered := map[string]bool{}
	for _, types := range anchorExpectedType {
		for _, ty := range types {
			covered[ty] = true
		}
	}
	for _, ty := range []string{
		"political_party", "government_body", "company", "organization",
		"sports_team", "school", "game", "musical_play", "webtoon", "publication",
	} {
		if !covered[ty] {
			t.Errorf("%q 에 대응하는 P31 클래스가 표에 없다 — 감사·분류가 이 유형을 못 본다", ty)
		}
	}
}

// entityTypeEnumFromMigrations — 유형 목록. **원본은 kdb.EntityTypes 하나다**
// (2026-09-16). 종전엔 kdbapi/api.go 의 switch 를 파싱했는데, 그 함수가 위임
// 한 줄로 바뀌면서 목록을 못 읽게 됐다 — 목록의 자리가 옮겨가면 그것을 읽던
// 시험도 같이 옮겨야 한다.
func entityTypeEnumFromMigrations(t *testing.T) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, v := range EntityTypes {
		out[v] = true
	}
	return out
}

// TestAnchorTypeTableNamesRealTypes — 표가 가리키는 유형은 **실제로 존재해야** 한다.
//
// ★2026-09-16. 표를 23개 클래스 늘리면서 brand_place 같은 기존 유형도 새로 적었다.
// 오타 하나가 나면 그 클래스는 영영 «판정하지 않음»으로 지나가는데, 그건 조용하다 —
// 관리 화면 유형 필터가 없는 값 4개로 HTTP 500 을 내던 것과 같은 계열이고, 그때도
// 화면이 500 을 내기 전까지 아무도 몰랐다.
func TestAnchorTypeTableNamesRealTypes(t *testing.T) {
	for qid, types := range anchorExpectedType {
		if len(types) == 0 {
			t.Errorf("%s: 유형이 비었다", qid)
		}
		for _, typ := range types {
			if !ValidEntityType(typ) {
				t.Errorf("%s → %q 는 없는 유형이다 — 이 클래스는 영영 판정되지 않는다", qid, typ)
			}
		}
	}
}

// TestKoreanPublicInstitutionClassesAreCovered — 새 유형 앵커 레인이 실제로 만난
// 클래스가 표에 있어야 한다.
//
// ★후보 131건을 위키데이터에 전건 조회하니 63건이 이름까지 맞는 항목을 갖고 있었는데,
// 첫 dry-run 이 붙인 건 40건 중 6건뿐이었다. 최다 사유가 «표에 없는 P31» 이었다 —
// 위키데이터가 답을 갖고 있는데 우리 표가 좁아서 지나갔다.
func TestKoreanPublicInstitutionClassesAreCovered(t *testing.T) {
	// 아래는 전부 그 조회에서 실제로 관측된 (클래스, 우리 유형, 예시) 다.
	for _, c := range []struct{ qid, typ, ex string }{
		{"Q136542063", "government_body", "고용노동부"},
		{"Q136542088", "government_body", "국세청"},
		{"Q35535", "government_body", "전북경찰청"},
		{"Q781132", "government_body", "공군"},
		{"Q16168183", "government_body", "국민건강보험공단"},
		{"Q15936437", "school", "서울대학교"},
		{"Q265662", "school", "한국체육대"},
		{"Q43229", "organization", "한국방송협회"},
		{"Q1478443", "organization", "대한축구협회"},
		{"Q183288", "organization", "대한체육회"},
		{"Q22687", "company", "우리금융지주"},
	} {
		if allowed, known := AnchorTypeAllowed(c.qid, c.typ); !allowed || !known {
			t.Errorf("%s(%s → %s): allowed=%v known=%v — 이 후보는 앵커를 못 받는다",
				c.ex, c.qid, c.typ, allowed, known)
		}
	}
	// 도시는 기관이 **아니다.** 부산시가 government_body 로 앉아 있는 것은 우리 쪽 오류이고,
	// 표가 그것을 «어긋남»으로 말해 줘야 유형 감사가 옮길 수 있다.
	if allowed, known := AnchorTypeAllowed("Q515", "government_body"); allowed || !known {
		t.Errorf("Q515(city)→government_body: allowed=%v known=%v — 도시를 기관으로 인정하면 안 된다", allowed, known)
	}
	if allowed, _ := AnchorTypeAllowed("Q515", "brand_place"); !allowed {
		t.Error("Q515(city)→brand_place 가 막혔다 — 옮겨 갈 곳이 있어야 감사가 말을 할 수 있다")
	}
}
