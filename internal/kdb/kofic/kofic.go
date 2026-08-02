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

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
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

// MovieURL — movieCd 의 KOBIS 상세 페이지. 빈 코드면 "".
func MovieURL(movieCd string) string {
	movieCd = strings.TrimSpace(movieCd)
	if movieCd == "" {
		return ""
	}
	return "https://www.kobis.or.kr/kobis/business/mast/mvie/searchMovieDtl.do?code=" + movieCd
}

// Enrich — ko 로 KOFIC 영화 검색 → 영어 제목 map{"en":[...]} 와 **movieCd**.
// 매치 없으면 빈 map + 빈 코드.
//
// ★movieCd 를 함께 반환하는 이유(2026-08-03). 종전엔 영어 제목만 돌려줘서
// 호출자가 external_ref 에 넣을 식별자가 없었고, kofic_drain 은 궁여지책으로
// external_id 에 **영어 제목**을 넣고 url 은 검색 첫 페이지를 넣었다(실측 30건 전부
// "Star Wars"·"Aladdin" 같은 제목). ref 행이 있으니 api-source-no-ref 불변식은
// 통과하는데 정작 어느 영화인지는 되짚을 수 없는 장식 ref 였다. movieCd 는 KOBIS
// 의 진짜 기본키이고 상세 URL 이 그 코드로 직접 열린다.
func (c *Client) Enrich(ctx context.Context, key, ko string) (map[string][]string, string, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(ko) == "" {
		return nil, "", nil
	}
	q := url.Values{}
	q.Set("key", key)
	q.Set("movieNm", ko)
	q.Set("itemPerPage", "10")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.Base+"/movie/searchMovieList.json?"+q.Encode(), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := httpx.Do(c.HTTP, req, 2)
	if err != nil {
		return nil, "", fmt.Errorf("kofic http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("kofic status %d", resp.StatusCode)
	}
	var mr movieListResp
	if err := json.NewDecoder(resp.Body).Decode(&mr); err != nil {
		return nil, "", err
	}
	// 오매칭 방지: 한국어 제목이 정확히 일치하는 영화만 채택(없으면 매치 없음).
	// list[0] 폴백 금지 — "아몬드"가 엉뚱한 영화로 매칭되던 문제.
	//
	// ★movieCd 는 영어 제목이 없어도 돌려준다. 정확제목 일치는 그 자체로 "이 KOBIS
	// 영화가 이 엔티티다"라는 식별이고, movieNmEn 유무는 별개 사실이다. 종전엔 en 이
	// 비면 아무것도 못 돌려줘서 식별에 성공하고도 앵커를 버렸다.
	want := strings.TrimSpace(ko)
	for _, m := range mr.MovieListResult.MovieList {
		if strings.TrimSpace(m.MovieNm) != want {
			continue
		}
		code := strings.TrimSpace(m.MovieCd)
		if en := strings.TrimSpace(m.MovieNmEn); en != "" {
			return map[string][]string{"en": {en}}, code, nil
		}
		if code != "" {
			return map[string][]string{}, code, nil
		}
	}
	return map[string][]string{}, "", nil
}
