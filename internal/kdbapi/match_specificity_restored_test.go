package kdbapi

import (
	"context"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 실측 신고에서 나온 결함을 고정한다 (2026-09-14, presslocale).
//
//	"드라마 사랑이 온다 가 방영된다" → match locale=ja 가 이 순서로 답했다:
//	  1) 온다        confidence 0.75 (2자)
//	  2) 사랑        confidence 0.72 (2자)
//	  3) 사랑이 온다 confidence 0.70 (6자)   ← 정답이 꼴찌
//	소비자가 1등을 집어 일본어판에 『オンダ』를 발행했다. 실제로 발행됐다.
//
// 원인은 `ORDER BY confidence DESC` 였다 — 그 confidence 는 매칭 점수가 아니라
// 대상 자체의 품질 점수다. 본문과 무관한 값이 1순위였다.
//
// ★이 시험은 특정 이름에 기대지 않는다. 데이터는 매일 바뀐다.
// **불변식**을 검사한다: 반환 순서는 "본문에서 실제로 맞은 조각의 길이" 내림차순이어야 한다.
func TestRestoredMatchRanksLongestMatchFirst(t *testing.T) {
	pool := testdb.Restored(t)
	store := &Store{Pool: pool}
	ctx := context.Background()

	texts := []string{
		"드라마 사랑이 온다 가 방영된다",
		"아이유와 방탄소년단이 시상식에 참석했다. 정국도 함께했다.",
		"봉준호 감독의 기생충이 부산국제영화제에서 상영된다.",
		"기쁜 우리 좋은 날 이 좋은 날 에 방송된다",
	}

	for _, txt := range texts {
		for _, loc := range []string{"ja", "en"} {
			rows, err := store.MatchEntitiesForLocale(ctx, MatchEntitiesRequest{
				SourceText: txt, Locale: loc, Limit: 20,
			})
			if err != nil {
				t.Fatalf("match(%q, %s): %v", txt, loc, err)
			}
			prev := 1 << 30
			for i, m := range rows {
				got := matchedSpan(m, txt)
				if got == 0 {
					// 맞은 조각을 Go 쪽에서 못 찾는 경우(정규화 차이 등)는 순서 판정에서 뺀다.
					// 있지도 않은 근거로 실패를 만들지 않는다.
					continue
				}
				if got > prev {
					t.Errorf("본문 %q (%s): %d번째 %q(맞은 길이 %d)가 앞선 %d보다 길다 — 긴 매칭이 뒤로 밀렸다",
						txt, loc, i, m.KO, got, prev)
				}
				prev = got
			}
		}
	}
}

// matchedSpan — 이 대상이 본문에서 실제로 맞은 조각의 길이(글자 수).
// api.go 의 matchSpecificityExpr 과 **같은 정의**여야 한다: 정본 또는 별칭 중 본문에
// 들어 있는 가장 긴 것.
func matchedSpan(m MatchedEntity, text string) int {
	n := 0
	if m.KO != "" && strings.Contains(text, m.KO) {
		n = len([]rune(m.KO))
	}
	lower := strings.ToLower(text)
	for _, a := range m.SourceAliases {
		if len([]rune(a)) < 2 || !strings.Contains(lower, strings.ToLower(a)) {
			continue
		}
		if l := len([]rune(a)); l > n {
			n = l
		}
	}
	return n
}
