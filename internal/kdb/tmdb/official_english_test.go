package tmdb

// en 재동기 회귀 테스트. 이 경로는 **이미 값이 있는 칸을 덮으므로** 가드가 느슨해지면
// 손해가 즉시 난다 — 08-15 실측에서 가드 없이 돌렸다면 오부착 앵커 3건이 멀쩡한 en 을
// 덮어썼다.
//
// 못으로 박는 것 둘:
//  1. 기본 응답 name 과 translations 의 en 이 **일치할 때만** 값을 준다. TMDb 는 en
//     번역이 없으면 name 에 원제를 되비추는데(`고딩형사`), 그게 en 칸에 들어가면 원제
//     복사가 된다.
//  2. original_language 를 그대로 돌려준다 — 호출측의 오부착 가드가 이것에 의존한다.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// detailStub — /{media}/{id} 는 detail 을, /{media}/{id}/translations 는 번역 목록을 준다.
func detailStub(t *testing.T, detail map[string]any, translations []map[string]any) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/translations") {
			_ = json.NewEncoder(w).Encode(map[string]any{"translations": translations})
			return
		}
		_ = json.NewEncoder(w).Encode(detail)
	}))
	t.Cleanup(srv.Close)
	c := New()
	c.Base = srv.URL
	return c
}

func enTr(country, name string) map[string]any {
	return map[string]any{"iso_639_1": "en", "iso_3166_1": country,
		"data": map[string]any{"name": name}}
}

func TestOfficialEnglish(t *testing.T) {
	ctx := context.Background()

	t.Run("일치하면 채택 — 유부녀 킬러 유형", func(t *testing.T) {
		c := detailStub(t,
			map[string]any{"name": "A Bona Fide Killer", "original_name": "유부녀 킬러", "original_language": "ko"},
			[]map[string]any{enTr("US", "A Bona Fide Killer")})
		got, lang, err := c.OfficialEnglish(ctx, "tok", 294095, "drama")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "A Bona Fide Killer" {
			t.Fatalf("제목 = %q, want %q", got, "A Bona Fide Killer")
		}
		if lang != "ko" {
			t.Fatalf("원작언어 = %q, want ko", lang)
		}
	})

	t.Run("en 번역이 없으면 원제 되비침을 거른다 — 고딩형사 유형", func(t *testing.T) {
		c := detailStub(t,
			map[string]any{"name": "고딩형사", "original_name": "고딩형사", "original_language": "ko"},
			[]map[string]any{{"iso_639_1": "ko", "iso_3166_1": "KR",
				"data": map[string]any{"name": "고딩형사"}}})
		got, _, err := c.OfficialEnglish(ctx, "tok", 1603413, "drama")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "" {
			t.Fatalf("제목 = %q, want 빈 문자열(채택 안 함)", got)
		}
	})

	t.Run("기본명과 en 번역이 어긋나면 채택하지 않는다", func(t *testing.T) {
		c := detailStub(t,
			map[string]any{"name": "The Criminal", "original_name": "犯罪者", "original_language": "ja"},
			[]map[string]any{enTr("US", "Presumed Guilty")})
		got, lang, err := c.OfficialEnglish(ctx, "tok", 322571, "movie")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "" {
			t.Fatalf("제목 = %q, want 빈 문자열(교차확인 실패)", got)
		}
		if lang != "ja" {
			t.Fatalf("원작언어 = %q, want ja — 가드용이라 불일치해도 돌려줘야 한다", lang)
		}
	})

	t.Run("en-US 가 없으면 다른 en 변종을 받는다", func(t *testing.T) {
		c := detailStub(t,
			map[string]any{"title": "Hunt", "original_title": "헌트", "original_language": "ko"},
			[]map[string]any{enTr("GB", "Hunt")})
		got, _, err := c.OfficialEnglish(ctx, "tok", 727340, "movie")
		if err != nil {
			t.Fatalf("err: %v", err)
		}
		if got != "Hunt" {
			t.Fatalf("제목 = %q, want Hunt", got)
		}
	})
}
