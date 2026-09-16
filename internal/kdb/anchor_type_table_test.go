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
