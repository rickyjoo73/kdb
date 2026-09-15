package corrections

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// 앵커 없는 대상은 **교차검증하지 않는다.**
//
// ★2026-09-15 실측 사고. 우리 `에반`(외부ID 0건)에 교정 신고가 들어오자 이름 검색이
// Q105717901 을 찾았다. 그 항목은 한국어 라벨이 정확히 `에반` 인데 실제로는
// ENHYPEN 희승이다. 그래서 희승의 라벨이 "Wikidata 교차검증 일치 — 강제 자동 반영"으로
// 박혔다: en=Heeseung · ja=ヒスン · es=`Heeseung love`.
// 살아 있는 사람에게 다른 사람 이름이 authoritative 로 나가고 있었다.
//
// 이름이 같다는 것은 같은 대상이라는 증거가 아니다 — 그것이 동명 함정 그 자체다.
type searchOnlyWD struct{ searched bool }

func (w *searchOnlyWD) Fetch(context.Context, string) (*wikidata.Entity, error) { return nil, nil }
func (w *searchOnlyWD) SearchAndFetch(context.Context, string) (*wikidata.Entity, *wikidata.Candidate, error) {
	w.searched = true
	return &wikidata.Entity{QID: "Q105717901", Labels: map[string]string{"es": "Heeseung love"}}, nil
}

func TestCorroborateNeedsTheEntitysOwnAnchor(t *testing.T) {
	wd := &searchOnlyWD{}
	s := &Service{WD: wd}
	// Pool 이 없으면 QID 조회가 실패해 ent==nil 로 떨어진다 — 그때 이름 검색으로
	// 넘어가면 안 된다(그것이 이 시험의 전부다).
	ok, ev := s.corroborate(context.Background(), uuid.New(), "에반", "es", "Heeseung love")
	if ok {
		t.Fatalf("앵커가 없는데 교차검증이 통과했다 (%s) — 동명 함정에 그대로 들어간다", ev)
	}
	if wd.searched {
		t.Fatal("이름 검색을 호출했다 — 이름이 같다는 것은 같은 대상이라는 증거가 아니다")
	}
}
