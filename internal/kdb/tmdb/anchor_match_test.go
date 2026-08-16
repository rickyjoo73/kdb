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
		id, amb, foreign, _, err := cl.SearchExactKoreanID(ctx, "tok", "오징어 게임", "drama")
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
		id, amb, foreign, _, err := cl.SearchExactKoreanID(ctx, "tok", "아몬드", "movie")
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
		id, amb, foreign, _, err := cl.SearchExactKoreanID(ctx, "tok", "화차", "movie")
		if err != nil || id != 0 || !amb || foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — 동명작 보류여야 한다", id, amb, foreign, err)
		}
	})

	t.Run("부분일치뿐 — 무매칭", func(t *testing.T) {
		cl := tmdbStub(t, []map[string]any{
			{"id": 800001, "name": "사랑의 불시착 특별편", "original_name": "사랑의 불시착 특별편", "original_language": "ko"},
		}, nil)
		id, amb, foreign, _, err := cl.SearchExactKoreanID(ctx, "tok", "사랑의 불시착", "drama")
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
		id, amb, foreign, _, err := cl.SearchExactKoreanID(ctx, "tok", "폭삭 속았수다", "drama")
		if err != nil || id != 900001 || amb || foreign {
			t.Fatalf("id=%d amb=%v foreign=%v err=%v — alt-title 회수로 채택이어야 한다", id, amb, foreign, err)
		}
	})

	t.Run("빈 토큰·빈 이름 — 조용히 무매칭", func(t *testing.T) {
		cl := tmdbStub(t, nil, nil)
		if id, _, _, _, err := cl.SearchExactKoreanID(ctx, "", "오징어 게임", "drama"); id != 0 || err != nil {
			t.Fatalf("빈 토큰: id=%d err=%v", id, err)
		}
		if id, _, _, _, err := cl.SearchExactKoreanID(ctx, "tok", "   ", "drama"); id != 0 || err != nil {
			t.Fatalf("빈 이름: id=%d err=%v", id, err)
		}
	})
}

// TestSeasonCollapseGuard — 프랜차이즈 접힘 가드. 2026-08-07 에 이 가드가 없어서 앵커
// 레인이 시즌 엔티티를 상위 시리즈 항목에 붙였고, tmdb-locale 이 프랜차이즈 제목을
// 시즌 칸에 써넣었다. 아래 케이스는 전부 **실제 TMDb 응답에서 확인한 값**이다.
func TestSeasonCollapseGuard(t *testing.T) {
	ctx := context.Background()

	// tv/71908 "크라임씬"의 KR alt 에 "크라임씬 리턴즈"가 실제로 등록돼 있다.
	// 붙이면 zh 가 프랜차이즈 제목 `犯罪现场` 으로 채워진다(정작 그 항목의 CN alt 에는
	// 시즌용 `犯罪现场归来` 가 따로 있다).
	t.Run("리턴즈가 alt 에만 있으면 보류", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 71908, "name": "크라임씬", "original_name": "크라임씬", "original_language": "ko"}},
			map[string][]string{"71908": {"크라임씬 리턴즈"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "크라임씬 리턴즈", "show")
		if err != nil || id != 0 || !seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 상위 시리즈 보류여야 한다", id, seasonOnly, err)
		}
	})

	// tv/62411 "식샤를 합시다"의 KR alt 에 "식샤를 합시다 3: 비긴즈" 가 등록돼 있다.
	t.Run("숫자+비긴즈가 alt 에만 있으면 보류", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 62411, "name": "식샤를 합시다", "original_name": "식샤를 합시다", "original_language": "ko"}},
			map[string][]string{"62411": {"식샤를 합시다 2", "식샤를 합시다 3: 비긴즈"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "식샤를 합시다 3: 비긴즈", "drama")
		if err != nil || id != 0 || !seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 상위 시리즈 보류여야 한다", id, seasonOnly, err)
		}
	})

	// tv/18495 "논스톱" 의 KR alt 에 논스톱3·4·5 가 전부 들어 있다.
	t.Run("숫자 회차가 alt 에만 있으면 보류", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 18495, "name": "논스톱", "original_name": "논스톱", "original_language": "ko"}},
			map[string][]string{"18495": {"뉴 논스톱", "논스톱3", "논스톱 4", "논스톱5"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "논스톱5", "show")
		if err != nil || id != 0 || !seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 상위 시리즈 보류여야 한다", id, seasonOnly, err)
		}
	})

	// ★가드가 너무 세면 정상 회수를 죽인다. 숫자가 **이름의 일부**인 경우는 상대
	// 주제목에도 그 토큰이 그대로 있으므로 통과해야 한다. 실측 통과 사례 그대로:
	// 우리 "오늘을 버틴 노래-플레이리스트 109" ↔ TMDb "오늘을 버틴 노래: 플레이리스트 109".
	t.Run("숫자가 이름의 일부면 통과", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 329385, "name": "오늘을 버틴 노래: 플레이리스트 109",
				"original_name": "오늘을 버틴 노래: 플레이리스트 109", "original_language": "ko"}},
			map[string][]string{"329385": {"오늘을 버틴 노래-플레이리스트 109"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "오늘을 버틴 노래-플레이리스트 109", "show")
		if err != nil || id != 329385 || seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 329385 채택이어야 한다", id, seasonOnly, err)
		}
	})

	// 회차 표지가 없는 평범한 별칭 회수는 종전대로 살아 있어야 한다(지락실↔뿅뿅 지구오락실).
	t.Run("표지 없는 별칭은 종전대로 채택", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 203508, "name": "뿅뿅 지구오락실", "original_name": "뿅뿅 지구오락실", "original_language": "ko"}},
			map[string][]string{"203508": {"지락실"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "지락실", "show")
		if err != nil || id != 203508 || seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 203508 채택이어야 한다", id, seasonOnly, err)
		}
	})

	// `파트너` 가 `파트` 로 오인되면 정상 작품이 죽는다 — 닫힌 목록에서 파트를 뺀 이유.
	t.Run("파트너는 회차 표지가 아니다", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 243761, "name": "Good Partner", "original_name": "굿파트너", "original_language": "ko"}},
			map[string][]string{"243761": {"굿 파트너"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "굿 파트너", "drama")
		if err != nil || id != 243761 || seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 243761 채택이어야 한다", id, seasonOnly, err)
		}
	})

	// 숫자 토큰 비교가 부분문자열이면 `1` 이 `101` 에 걸린다.
	t.Run("숫자는 토큰 단위로 본다", func(t *testing.T) {
		if seqMarkersCovered("프로듀스1", "프로듀스 101") {
			t.Fatal("`프로듀스1` 이 `프로듀스 101` 에 덮이면 안 된다")
		}
		if !seqMarkersCovered("프로듀스101", "프로듀스 101") {
			t.Fatal("같은 숫자 토큰은 덮여야 한다")
		}
	})
}

// TestSubtitleSiblingGuard — 부제 형제 가드. 위 가드는 회차 표지를 숫자·닫힌 낱말로만
// 보므로, 시즌 이름이 **부제**인 접힘은 그대로 통과한다. 2026-08-16 에 두 건이 실제로
// 통과해 잘못 붙었고(아래 케이스는 그때의 실제 TMDb 응답이다) 그래서 이 가드를 더했다.
func TestSubtitleSiblingGuard(t *testing.T) {
	ctx := context.Background()

	// tv/135094 `더 리슨`(2021, 솔지·김나영·케이시·승희·HYNN)의 KR alt 에 시즌 부제 다섯이
	// 나란히 있다. 우리 `더 리슨: 우리 함께 다시`는 출연진이 전혀 다른 **더 리슨4** 다.
	t.Run("같은 머리에 다른 부제가 또 있으면 보류", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 135094, "name": "더 리슨", "original_name": "더 리슨", "original_language": "ko"}},
			map[string][]string{"135094": {
				"더 리슨: 바람이 분다", "더 리슨: 우리가 사랑한 목소리",
				"더 리슨: 너와 함께한 시간", "더 리슨: 우리 함께 다시", "더 리슨: 오늘, 너에게 닿다",
			}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "더 리슨: 우리 함께 다시", "show")
		if err != nil || id != 0 || !seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 상위 시리즈 보류여야 한다", id, seasonOnly, err)
		}
	})

	// tv/258925 `샤먼` = 시즌1 `샤먼 : 귀신전`(2024) + 시즌2 `샤먼 : 미신전`(2026).
	// 이걸 통과시키면 우리 DB 의 두 엔티티가 같은 id 를 나눠 갖는다(실제로 그렇게 됐다).
	// 콜론 주위 공백이 달라도 같은 머리로 봐야 한다.
	t.Run("콜론 주위 공백이 달라도 형제로 본다", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 258925, "name": "샤먼", "original_name": "샤먼", "original_language": "ko"}},
			map[string][]string{"258925": {"샤먼 : 귀신전", "샤먼 : 미신전", "샤먼 귀신전"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "샤먼: 귀신전", "show")
		if err != nil || id != 0 || !seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 상위 시리즈 보류여야 한다", id, seasonOnly, err)
		}
	})

	// ★가드가 너무 세면 정상 회수를 죽인다. 부제가 하나뿐이면 그 항목은 시리즈가 아니라
	// **짧은 주제목을 가진 그 작품**이다 — 통과해야 한다.
	t.Run("같은 부제뿐이면 통과", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 588108, "title": "한산", "original_title": "한산", "original_language": "ko"}},
			map[string][]string{"588108": {"한산: 용의 출현", "한산 용의 출현"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "한산: 용의 출현", "movie")
		if err != nil || id != 588108 || seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 588108 채택이어야 한다", id, seasonOnly, err)
		}
	})

	// 부제가 없는 제목은 이 규칙 밖이다 — 08-16 에 정답으로 확인한 세 건이 계속 통과해야 한다.
	t.Run("부제 없는 제목은 규칙 밖", func(t *testing.T) {
		cl := tmdbStub(t,
			[]map[string]any{{"id": 64369, "name": "TV 동물농장", "original_name": "TV 동물농장", "original_language": "ko"}},
			map[string][]string{"64369": {"동물농장", "TV Animal Farm"}},
		)
		id, _, _, seasonOnly, err := cl.SearchExactKoreanID(ctx, "tok", "동물농장", "show")
		if err != nil || id != 64369 || seasonOnly {
			t.Fatalf("id=%d seasonOnly=%v err=%v — 64369 채택이어야 한다", id, seasonOnly, err)
		}
	})

	t.Run("머리만 있는 표기는 형제로 세지 않는다", func(t *testing.T) {
		if subtitleSiblingExists("샤먼: 귀신전", []string{"샤먼", "Shaman"}) {
			t.Fatal("시리즈 루트 이름만으로 보류하면 정상 매치가 전부 죽는다")
		}
		if !subtitleSiblingExists("샤먼: 귀신전", []string{"샤먼", "샤먼 : 미신전"}) {
			t.Fatal("다른 부제가 실제로 있으면 보류해야 한다")
		}
	})
}
