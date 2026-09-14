package kentity

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ID 로 물으면 답이 하나라는 모델은 **ID 를 고를 수 있어야** 성립한다.
// 이름으로 찾으면 `중앙동` 이 50건 나온다. 구분값(인천 제물포구 / 경남 창원시 성산구 …)이
// 응답에 없으면 어느 것인지 가릴 수가 없고, 그러면 ID 를 고를 수 없다.
func TestRestoredSearchReturnsQualifierAndExactFirst(t *testing.T) {
	pool := testdb.Restored(t)
	s := &Store{Pool: pool}
	ctx := context.Background()

	items, err := s.Search(ctx, "중앙동", "", "", 50)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) == 0 {
		t.Skip("중앙동 이 이 사본에 없다")
	}

	// ① 이름이 정확히 같은 것이 먼저 나와야 한다.
	if items[0].KO != "중앙동" {
		t.Errorf("첫 결과가 %q — 정확히 같은 이름이 먼저 나와야 한다", items[0].KO)
	}

	// ② 정확일치 결과에는 구분값이 실려야 한다. 없으면 어느 중앙동인지 알 수 없다.
	exact, withQ := 0, 0
	for _, it := range items {
		if it.KO != "중앙동" {
			continue
		}
		exact++
		if it.Qualifier != "" {
			withQ++
		}
	}
	if exact > 1 && withQ == 0 {
		t.Errorf("정확일치 %d건인데 구분값이 하나도 없다 — ID 를 고를 수 없다", exact)
	}
	t.Logf("정확일치 %d건 중 구분값 있음 %d건", exact, withQ)
}
