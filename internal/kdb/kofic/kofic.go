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
		Base: "http://www.kobis.or.kr/kobisopenapi/webservice/rest",
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
	list := mr.MovieListResult.MovieList
	if len(list) == 0 {
		return map[string][]string{}, nil
	}
	// 정확히 같은 한국어 제목 우선, 없으면 첫 결과.
	pick := list[0]
	for _, m := range list {
		if strings.TrimSpace(m.MovieNm) == strings.TrimSpace(ko) {
			pick = m
			break
		}
	}
	en := strings.TrimSpace(pick.MovieNmEn)
	if en == "" {
		return map[string][]string{}, nil
	}
	return map[string][]string{"en": {en}}, nil
}
