package kentity

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ID 로 물으면 답이 하나라는 모델은 **ID 를 고를 수 있어야** 성립한다.
// 이름으로 찾으면 `중앙동` 이 여러 건 나온다. 구분값(인천 제물포구 / 경남 창원시 성산구 …)이
// 응답에 없으면 어느 것인지 가릴 수가 없고, 그러면 ID 를 고를 수 없다.
//
// ★시험이 특정 데이터를 요구하지 않는다. 회귀 DB 는 운영 사본이지만 **언제 뜬 사본인지**
//
//	모른다 — 처음엔 '중앙동에 구분값이 있어야 한다'고 썼다가, 구분값을 채우기 전에 뜬
//	사본에서 실패했다. 코드 결함이 아니라 시험이 환경에 기댄 것이다.
//	그래서 **원장에 구분값이 있는 대상을 찾아** 그것이 응답에 실리는지 본다.
func TestRestoredSearchCarriesQualifier(t *testing.T) {
	pool := testdb.Restored(t)
	s := &Store{Pool: pool}
	ctx := context.Background()

	var ko, want string
	err := pool.QueryRow(ctx, `SELECT canonical_ko, qualifier_ko FROM kentity_entities
	 WHERE COALESCE(qualifier_ko,'') <> '' AND canonical_ko <> '' LIMIT 1`).Scan(&ko, &want)
	if err != nil {
		t.Skipf("이 사본에는 구분값이 채워진 대상이 없다: %v", err)
	}

	items, err := s.Search(ctx, ko, "", "", 50)
	if err != nil {
		t.Fatalf("Search(%q): %v", ko, err)
	}
	found := false
	for _, it := range items {
		if it.KO == ko && it.Qualifier == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("%q 의 구분값 %q 가 응답에 실리지 않았다 — ID 를 고를 수 없다", ko, want)
	}
}

// 이름이 정확히 같은 것을 먼저 준다. 종전엔 updated_at 순이라 '중앙동' 을 찾으면
// 'CU 송탄중앙동점'·'이디야커피 마산중앙동점' 이 먼저 나왔다. 데이터에 기대지 않는다 —
// 정확일치가 하나라도 있으면 그것이 맨 앞이어야 한다.
func TestRestoredSearchPutsExactMatchFirst(t *testing.T) {
	pool := testdb.Restored(t)
	s := &Store{Pool: pool}
	ctx := context.Background()

	var ko string
	if err := pool.QueryRow(ctx, `SELECT canonical_ko FROM kentity_entities
	 WHERE char_length(canonical_ko) BETWEEN 2 AND 6
	   AND EXISTS (SELECT 1 FROM kentity_entities o
	                WHERE o.canonical_ko <> kentity_entities.canonical_ko
	                  AND strpos(o.canonical_ko, kentity_entities.canonical_ko) > 0)
	 LIMIT 1`).Scan(&ko); err != nil {
		t.Skipf("부분일치 상대가 있는 이름을 못 찾았다: %v", err)
	}

	items, err := s.Search(ctx, ko, "", "", 50)
	if err != nil {
		t.Fatalf("Search(%q): %v", ko, err)
	}
	if len(items) == 0 {
		t.Skipf("%q 결과 없음", ko)
	}
	if items[0].KO != ko {
		t.Errorf("%q 를 찾았는데 첫 결과가 %q — 정확일치가 먼저 나와야 한다", ko, items[0].KO)
	}
}
