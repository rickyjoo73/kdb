// Package tmdb — TMDb(themoviedb.org) enrich 클라이언트.
//
// 영화(movie) / 드라마·예능(tv) 의 한국어 제목으로 검색해, TMDb translations 에서
// KDB 9개 로케일 제목을 모은다. enrich orchestrator L2(권위 API) 에서 호출.
// 인증: API Read Access Token(v4) Bearer. 키 해석은 호출측(apikeys.Resolve).
package tmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	HTTP *http.Client
	Base string
}

func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 12 * time.Second},
		Base: "https://api.themoviedb.org/3",
	}
}

type searchResp struct {
	Results []struct {
		ID            int    `json:"id"`
		Title         string `json:"title"`         // movie
		Name          string `json:"name"`          // tv
		OriginalTitle string `json:"original_title"`
		OriginalName  string `json:"original_name"`
	} `json:"results"`
}

type translationsResp struct {
	Translations []struct {
		ISO639  string `json:"iso_639_1"`
		ISO3166 string `json:"iso_3166_1"`
		Data    struct {
			Title string `json:"title"` // movie
			Name  string `json:"name"`  // tv
		} `json:"data"`
	} `json:"translations"`
}

// Enrich — ko 로 TMDb 검색 → 최상위 매치의 translations → KDB 로케일별 제목 map.
// 매치 없으면 빈 map(에러 없음). HTTP/파싱 실패는 error.
func (c *Client) Enrich(ctx context.Context, token, ko, entityType string) (map[string][]string, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(ko) == "" {
		return nil, nil
	}
	media := "movie"
	if entityType == "drama" || entityType == "show" {
		media = "tv"
	}

	// 1) 검색
	sq := url.Values{}
	sq.Set("query", ko)
	sq.Set("language", "ko-KR")
	var sr searchResp
	if err := c.get(ctx, token, "/search/"+media+"?"+sq.Encode(), &sr); err != nil {
		return nil, err
	}
	if len(sr.Results) == 0 {
		return map[string][]string{}, nil
	}
	id := sr.Results[0].ID

	// 2) translations
	var tr translationsResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/translations", media, id), &tr); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, t := range tr.Translations {
		loc := kdbLocale(t.ISO639, t.ISO3166)
		if loc == "" {
			continue
		}
		title := strings.TrimSpace(t.Data.Title)
		if title == "" {
			title = strings.TrimSpace(t.Data.Name)
		}
		if title == "" {
			continue
		}
		if _, exists := out[loc]; exists { // 첫 값 우선(로케일당 1개)
			continue
		}
		out[loc] = []string{title}
	}
	return out, nil
}

func (c *Client) get(ctx context.Context, token, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// kdbLocale — TMDb (iso_639_1, iso_3166_1) → KDB 로케일 코드. 매핑 없으면 "".
func kdbLocale(lang, country string) string {
	switch lang {
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "vi":
		return "vi"
	case "id":
		return "id"
	case "es":
		return "es"
	case "pt":
		if country == "BR" {
			return "pt_br"
		}
	case "zh":
		switch country {
		case "TW", "HK", "MO":
			return "zh_hant"
		case "CN", "SG":
			return "zh"
		}
	}
	return ""
}
