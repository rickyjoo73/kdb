package kentity

import (
	"context"
	"strings"
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

// 찾는 사람이 원한 것에 가까운 순으로 나오는지 고정한다.
//
// ★2026-09-15 실측 신고. '익산역' 을 찾으면 이렇게 나왔다:
//
//	정관장 익산역점(8, rejected) · GS25 익산역점(9, rejected)
//	티바두마리치킨 익산역점(12, rejected) · 익산역 (철도체험학습장)(13, candidate)
//
// 정확일치가 없으니 그 우선순위가 안 걸리고, 글자 수 순이라 **지점 가게가 앞을 다 차지**했다.
// 게다가 앞의 셋은 전부 기각된 것이다 — 죽은 것이 산 것보다 먼저 나왔다.
func TestRestoredSearchPrefersPrefixAndLiveRows(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool}

	// 접두 일치와 중간 일치가 **둘 다** 있는 이름을 실데이터에서 찾는다.
	var q string
	if err := pool.QueryRow(ctx, `
SELECT p.tok FROM (
  SELECT regexp_replace(canonical_ko, '^(.{2,6})역.*$', '\1역') AS tok
    FROM kentity_entities WHERE canonical_ko ~ '역' AND char_length(canonical_ko) BETWEEN 4 AND 20
) p
 WHERE p.tok ~ '역$'
   AND EXISTS (SELECT 1 FROM kentity_entities a WHERE a.canonical_ko LIKE p.tok || '%' AND a.canonical_ko <> p.tok)
   AND EXISTS (SELECT 1 FROM kentity_entities b WHERE b.canonical_ko LIKE '%' || p.tok || '%' AND b.canonical_ko NOT LIKE p.tok || '%')
 LIMIT 1`).Scan(&q); err != nil {
		t.Skipf("접두/중간 일치가 함께 있는 표본을 못 찾았다: %v", err)
	}

	got, err := s.Search(ctx, q, "", "", 30)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 {
		t.Skipf("%q 결과가 %d건뿐", q, len(got))
	}
	seenNonPrefix := false
	for _, e := range got {
		prefix := strings.HasPrefix(strings.ToLower(e.KO), strings.ToLower(q))
		if !prefix {
			seenNonPrefix = true
			continue
		}
		if seenNonPrefix {
			t.Fatalf("%q: 중간일치가 접두일치보다 먼저 나왔다 (%q 가 뒤에 있다)", q, e.KO)
		}
	}
	// 기각된 것이 산 것보다 먼저 나오면 안 된다(같은 접두 단계 안에서).
	seenLive := false
	for _, e := range got {
		if e.Status != "rejected" {
			seenLive = true
			continue
		}
		if !seenLive && len(got) > 1 {
			// 첫 행이 기각이고 뒤에 산 것이 있으면 정렬이 뒤집힌 것이다.
			for _, o := range got[1:] {
				if o.Status != "rejected" {
					t.Fatalf("%q: 기각(%q)이 살아 있는 것(%q)보다 먼저 나왔다", q, e.KO, o.KO)
				}
			}
		}
	}
	t.Logf("%q → %d건, 첫 행 %q(%s)", q, len(got), got[0].KO, got[0].Status)
}
