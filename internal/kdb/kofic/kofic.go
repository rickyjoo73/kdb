// Package kofic — KOFIC(영화진흥위원회 KOBIS) enrich 클라이언트.
//
// 국내 영화의 한국어 제목으로 검색해 영어 제목(movieNmEn)을 얻는다. KOFIC 은
// 국내 영화 권위 소스(박스오피스·영화/인물)이며 다국어는 영어 위주 — 영화(movie)
// 타입의 canonical_en 보강에 사용. enrich orchestrator L2 에서 movie 일 때 호출.
package kofic

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
		Base: "https://www.kobis.or.kr/kobisopenapi/webservice/rest",
	}
}

type movieListResp struct {
	MovieListResult struct {
		MovieList []struct {
			MovieCd   string `json:"movieCd"`
			MovieNm   string `json:"movieNm"`
			MovieNmEn string `json:"movieNmEn"`
			PrdtYear  string `json:"prdtYear"`
		} `json:"movieList"`
	} `json:"movieListResult"`
}

// Enrich — ko 로 KOFIC 영화 검색 → 영어 제목 map{"en":[...]}. 매치 없으면 빈 map.
func (c *Client) Enrich(ctx context.Context, key, ko string) (map[string][]string, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(ko) == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("key", key)
	q.Set("movieNm", ko)
	q.Set("itemPerPage", "10")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.Base+"/movie/searchMovieList.json?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kofic http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kofic status %d", resp.StatusCode)
	}
	var mr movieListResp
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, err
	}
	// 오매칭 방지: 한국어 제목이 정확히 일치하는 영화만 채택(없으면 매치 없음).
	// list[0] 폴백 금지 — "아몬드"가 엉뚱한 영화로 매칭되던 문제.
	want := strings.TrimSpace(ko)
	for _, m := range mr.MovieListResult.MovieList {
		if strings.TrimSpace(m.MovieNm) != want {
			continue
		}
		if en := strings.TrimSpace(m.MovieNmEn); en != "" {
			return map[string][]string{"en": {en}}, nil
		}
	}
	return map[string][]string{}, nil
}
