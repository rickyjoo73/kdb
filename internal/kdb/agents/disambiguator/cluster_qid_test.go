package disambiguator

// 같은-QID 군집 소스(4) 회귀 테스트.
//
// ★왜 이 소스가 생겼나(2026-08-15 실측): 기존 군집 소스 셋은 전부 **이름 표기**로 묶는다
// — 같은 ko / 유사 ko(trigram) / 같은 en. 그래서 본명↔활동명 중복은 셋 다 놓쳤다:
// `정호석`/`제이홉`, `김남준`/`RM`, `도경수`/`디오` 는 ko 도 en 도 안 겹치고 trigram 도 0 이다.
// 실측 46쌍 중 34쌍이 어떤 소스에도 안 걸렸다.
//
// ★같이 못으로 박는 것: buildClusters(Select) 와 clustersFromIDs(Run) 은 **짝이 맞아야
// 한다.** Select 가 QID 로 고른 멤버를 Run 이 다시 묶지 못하면 선정만 되고 아무 일도
// 일어나지 않는다 — 이 저장소가 다른 레인에서 이미 밟은 모양이다(선정 조건이 기록/처리
// 대상보다 좁아 대상이 영영 처리되지 않음).

import (
	"testing"

	"github.com/google/uuid"
)

func qidMember(ko, qid, etype string) member {
	return member{id: uuid.New(), ko: ko, qid: qid, entityType: etype, status: "active"}
}

func TestGroupByQID(t *testing.T) {
	t.Run("본명과 활동명을 묶는다 — 이름으로는 안 잡히는 중복", func(t *testing.T) {
		got := groupByQID([]member{
			qidMember("정호석", "Q16236223", "person"),
			qidMember("제이홉", "Q16236223", "person"),
		})
		if len(got) != 1 || len(got[0]) != 2 {
			t.Fatalf("군집 = %v, want 2인 군집 1개", got)
		}
	})

	t.Run("QID 가 다르면 묶지 않는다 — 동음이인 보호", func(t *testing.T) {
		// 이제훈/이재훈 은 ja 로는 둘 다 イ・ジェフン 이지만 별인이다. QID 가 다르면
		// 절대 한 군집에 넣지 않는다.
		if got := groupByQID([]member{
			qidMember("이제훈", "Q489021", "person"),
			qidMember("이재훈", "Q12591234", "person"),
		}); len(got) != 0 {
			t.Fatalf("군집 = %v, want 없음", got)
		}
	})

	t.Run("QID 가 없는 멤버는 제외한다", func(t *testing.T) {
		// 빈 문자열끼리 뭉치면 무관한 엔티티가 한 군집이 된다.
		if got := groupByQID([]member{
			qidMember("김하나", "", "person"),
			qidMember("박두리", "", "person"),
			qidMember("이세미", "", "person"),
		}); len(got) != 0 {
			t.Fatalf("군집 = %v, want 없음 — 빈 QID 는 키가 될 수 없다", got)
		}
	})

	t.Run("같은 QID 라도 entity_type 이 다르면 나눈다", func(t *testing.T) {
		if got := groupByQID([]member{
			qidMember("아이유", "Q483447", "person"),
			qidMember("아이유", "Q483447", "group"),
		}); len(got) != 0 {
			t.Fatalf("군집 = %v, want 없음", got)
		}
	})

	t.Run("단독은 군집이 아니다", func(t *testing.T) {
		if got := groupByQID([]member{qidMember("혼자", "Q1", "person")}); len(got) != 0 {
			t.Fatalf("군집 = %v, want 없음", got)
		}
	})

	t.Run("여러 군집을 동시에 낸다", func(t *testing.T) {
		got := groupByQID([]member{
			qidMember("정호석", "Q16236223", "person"),
			qidMember("제이홉", "Q16236223", "person"),
			qidMember("김남준", "Q22355433", "person"),
			qidMember("RM", "Q22355433", "person"),
			qidMember("외톨이", "Q999", "person"),
		})
		if len(got) != 2 {
			t.Fatalf("군집 수 = %d, want 2", len(got))
		}
		for _, g := range got {
			if len(g) != 2 {
				t.Errorf("군집 크기 = %d, want 2", len(g))
			}
		}
	})
}
