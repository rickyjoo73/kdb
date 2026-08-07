package tmdb

// 앵커 부착 가드 회귀 테스트. active 작품에 붙는 앵커는 틀리면 tmdb-locale 이 곧바로
// 7개 로케일을 오염시키므로(승급 경로는 en 1칸이었다) 가드가 느슨해지는 변경을 여기서 막는다.
//
// ★핵심 두 가지를 못으로 박는다.
//  1. SearchExactKoreanID 는 SearchExactID 보다 **엄격하기만** 하다 — 유일성 판정은 ko
//     필터 이전 전체 매치 집합에서 하므로, 외국작이 섞여 2건이면 그대로 보류다.
//  2. SearchExactID 자체의 동작은 리팩터(searchExactMatches 분리) 전후로 동일하다 —
//     승급 드레인이 그대로 의존하고 있다.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tmdbStub — /search/* 는 준비된 결과를, /{media}/{id}/alternative_titles 는 alt 표기를 준다.
func tmdbStub(t *testing.T, results []map[string]any, alts map[string][]string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/alternative_titles") {
			seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			id := seg[len(seg)-2]
			var out []map[string]any
			for _, tt := range alts[id] {
				out = append(out, map[string]any{"iso_3166_1": "KR", "title": tt})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"titles": out, "results": out})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.Base = srv.URL
	return c
}

func TestSearchExactKoreanID(t *testing.T) {
	ctx := context.Background()

	t.Run("정확·유일·원작ko — 채택", func(t *testing.T) {
		cl := tmdbStub(t, []map[string]any{
			{"id": 110316, "name": "오징어 게임", "original_name": "오징어 게임", "original_language": "ko"},
			{"id": 999001, "name": "오징어 게임: 더 챌린지", "original_name": "Squid Game: The Challenge", "original_language": "en"},
		}, nil)
		id, amb, foreign, err := cl.SearchExactKoreanID(ctx, "tok", "오징어 게임", "drama")
		if err != nil || id != 110316 || amb || foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — 110316 채택이어야 한다", id, amb, foreign, err)
		}
	})

	// ★이 케이스가 이 가드를 만든 이유다. language=ko-KR 검색 결과의 title 은 외국작의
	// **한국어 개봉제목**일 수 있고, 그게 유일 일치면 종전 가드(SearchExactID)는 통과한다.
	// 그대로 앵커를 붙이면 외국작의 ja/zh 제목이 한국 작품 칸에 들어간다("아몬드"→프랑스 영화).
	t.Run("정확·유일하지만 원작이 ko 가 아니다 — 보류", func(t *testing.T) {
		cl := tmdbStub(t, []map[string]any{
			{"id": 555001, "title": "아몬드", "original_title": "Amandier", "original_language": "fr"},
		}, nil)
		id, amb, foreign, err := cl.SearchExactKoreanID(ctx, "tok", "아몬드", "movie")
		if err != nil || id != 0 || amb || !foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — foreign 보류여야 한다", id, amb, foreign, err)
		}
		// 종전 가드는 이걸 통과시킨다는 사실 자체를 고정해 둔다(그래서 조건을 더 얹었다).
		oid, oamb, oerr := cl.SearchExactID(ctx, "tok", "아몬드", "movie")
		if oerr != nil || oid != 555001 || oamb {
			t.Fatalf("SearchExactID id=%d amb=%v err=%v — 종전 동작(통과)이 유지되어야 한다", oid, oamb, oerr)
		}
	})

	// ko 가 섞여 있어도 전체 매치가 2건이면 보류다 — ko 만 남겨 유일성을 따지면 종전보다
	// **느슨해진다**. 핸드오프 §5 의 "일치 조건을 절대 느슨하게 하지 말 것".
	t.Run("동명작 2건(하나는 ko) — ko 만 골라 통과시키지 않는다", func(t *testing.T) {
		cl := tmdbStub(t, []map[string]any{
			{"id": 700001, "title": "화차", "original_title": "화차", "original_language": "ko"},
			{"id": 700002, "title": "화차", "original_title": "火車", "original_language": "ja"},
		}, nil)
		id, amb, foreign, err := cl.SearchExactKoreanID(ctx, "tok", "화차", "movie")
		if err != nil || id != 0 || !amb || foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — 동명작 보류여야 한다", id, amb, foreign, err)
		}
	})

	t.Run("부분일치뿐 — 무매칭", func(t *testing.T) {
		cl := tmdbStub(t, []map[string]any{
			{"id": 800001, "name": "사랑의 불시착 특별편", "original_name": "사랑의 불시착 특별편", "original_language": "ko"},
		}, nil)
		id, amb, foreign, err := cl.SearchExactKoreanID(ctx, "tok", "사랑의 불시착", "drama")
		if err != nil || id != 0 || amb || foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — 무매칭이어야 한다", id, amb, foreign, err)
		}
	})

	// 주제목 무매칭 시 alternative_titles 로 회수하는 관용화(2026-07-23)가 리팩터 후에도
	// 살아 있는지. 실측 사례 그대로: 우리 표기 "폭삭 속았수다" ↔ TMDb 주표기 "폭싹 속았수다".
	t.Run("alt-title 로만 일치 — 원작ko 면 채택", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 900001, "name": "폭싹 속았수다", "original_name": "폭싹 속았수다", "original_language": "ko"}},
			map[string][]string{"900001": {"폭삭 속았수다"}},
		)
		id, amb, foreign, err := cl.SearchExactKoreanID(ctx, "tok", "폭삭 속았수다", "drama")
		if err != nil || id != 900001 || amb || foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — alt-title 회수로 채택이어야 한다", id, amb, foreign, err)
		}
	})

	t.Run("빈 토큰·빈 이름 — 조용히 무매칭", func(t *testing.T) {
		cl := tmdbStub(t, nil, nil)
		if id, _, _, err := cl.SearchExactKoreanID(ctx, "", "오징어 게임", "drama"); id != 0 || err != nil {
			t.Fatalf("빈 토큰: id=%d err=%v", id, err)
		}
		if id, _, _, err := cl.SearchExactKoreanID(ctx, "tok", "   ", "drama"); id != 0 || err != nil {
			t.Fatalf("빈 이름: id=%d err=%v", id, err)
		}
	})
}
