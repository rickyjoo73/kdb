package kdb

import (
	"os"
	"strings"
	"testing"
)

// ★칸도 표도 있는데 1.4% 만 채워져 있었다 (2026-09-16 실측).
//
//	활성 인물 5,407 중 직업 영역 보유 78건. 칸(0142)과 판정표(occupation_domain.go)를
//	어제 만들었는데, 그 값을 쓰는 곳이 enrich 캐스케이드 한 군데뿐이라 그 경로를
//	탄 것만 채워졌다. 3,600여 건은 앵커를 이미 갖고 있는데 아무도 P106 을 묻지 않았다.
//
// 이 시험이 지키는 것은 **이 레인이 이름을 검색하지 않는다**는 것이다.
// 확정된 QID 로만 물어야 동명이인 위험이 구조적으로 없다.
func TestOccupationBackfillNeverSearchesByName(t *testing.T) {
	src := occupationSource(t)
	for _, forbidden := range []string{".Search(", "SearchAndFetch", "EntityMatchesQuery"} {
		if strings.Contains(src, forbidden) {
			t.Errorf("이름 검색 경로(%s)가 있다 — 동명이인이 섞인다", forbidden)
		}
	}
	if !strings.Contains(src, "BatchClaims") {
		t.Error("확정 QID 묶음 조회를 안 쓴다")
	}
	// QID 모양을 SQL 에서 먼저 거른다. 하나가 틀리면 묶음이 통째로 빈 응답이 된다.
	if !strings.Contains(src, `^Q[1-9][0-9]*$`) {
		t.Error("QID 모양 가드가 없다 — 모양 틀린 하나가 50건을 조용히 날린다")
	}
}

// ★조회 실패를 "직업 없음"으로 적으면 안 된다. 같은 날 앵커 레인에서 이미 데었다
// (CA 인증서가 없어 120건 전부가 거짓 «검색없음» 이었다).
func TestOccupationFetchFailureIsCountedSeparately(t *testing.T) {
	src := occupationSource(t)
	if !strings.Contains(src, "r.Failed +=") {
		t.Error("묶음 조회 실패를 따로 세지 않는다 — 못 한 것이 없는 것으로 적힌다")
	}
	if strings.Contains(src, "if cerr != nil {\n\t\t\tcontinue") {
		t.Error("조회 실패를 그냥 건너뛴다 — 아무 데도 안 남는다")
	}
}

// ★원자료는 영역이 안 나와도 적는다. 0142 가 칸을 둘로 나눈 이유가 그것이다 —
// 표가 늘어나면 다시 판정할 수 있어야 한다.
func TestRawOccupationIsStoredEvenWhenDomainIsUnknown(t *testing.T) {
	src := occupationSource(t)
	if !strings.Contains(src, "occupation_qids   = CASE WHEN cardinality($2::text[]) > 0") {
		t.Error("원자료를 저장하지 않는다 — 표가 늘어도 다시 판정할 수 없다")
	}
	if !strings.Contains(src, "r.UnknownQIDs[qid]++") {
		t.Error("표에 없는 P106 을 세지 않는다 — 표를 늘릴 근거가 안 남는다")
	}
}

// TopUnknownOccupations — 많이 나온 순, 동수면 QID 오름차순. 순서가 고정이어야
// 같은 입력에 같은 보고가 나온다.
func TestTopUnknownOccupationsIsDeterministic(t *testing.T) {
	m := map[string]int{"Q3": 5, "Q1": 9, "Q2": 5, "Q4": 1}
	got := strings.Join(TopUnknownOccupations(m, 3), " ")
	if got != "Q1×9 Q2×5 Q3×5" {
		t.Errorf("순서가 %q — 기대 \"Q1×9 Q2×5 Q3×5\"", got)
	}
	if len(TopUnknownOccupations(map[string]int{}, 5)) != 0 {
		t.Error("빈 입력에 무언가를 돌려줬다")
	}
}

func occupationSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("occupation_backfill.go")
	if err != nil {
		t.Fatalf("occupation_backfill.go 를 못 읽었다: %v", err)
	}
	return string(b)
}
