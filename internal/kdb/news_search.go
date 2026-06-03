// Package kdb — SearchNewsContext: classify 의 "모르면 검색" 보조.
//
// classify(gpt)가 needs_search/unknown 을 반환할 때, 이름으로 Google News RSS
// 일반 검색(도메인 제한 없음)을 돌려 관련 기사 제목을 문맥(SearchHits)으로 모은다.
// site_search.go 의 SiteSearchService 는 whitelist 도메인 스코프 + RSS enqueue 용
// 으로 무겁다 — 여기서는 단순히 제목 문맥만 반환한다(부수효과 없음).
package kdb

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var newsSearchClient = &http.Client{Timeout: 12 * time.Second}

// SearchNewsContext — query 로 Google News RSS 를 검색해 관련 기사 제목을 최대
// max 개 반환. 한국어 로케일(ko-KR) 고정 — 대상이 K-content 고유명사이기 때문.
// best-effort: 실패(네트워크/파싱/비-200)는 nil 반환, 호출측 분류엔 영향 없음.
func SearchNewsContext(ctx context.Context, query string, max int) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	if max <= 0 {
		max = 6
	}
	u, err := url.Parse("https://news.google.com/rss/search")
	if err != nil {
		return nil
	}
	vals := u.Query()
	vals.Set("q", `"`+q+`"`)
	vals.Set("hl", "ko")
	vals.Set("gl", "KR")
	vals.Set("ceid", "KR:ko")
	u.RawQuery = vals.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "mediafine-kdb-news-search/1.0 ( rickyjoo@aiinad.com )")
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	resp, err := newsSearchClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// 비-200(특히 429 Google 차단)은 "결과 없음"과 구분되도록 로그 — best-effort
		// 라 동작은 그대로 nil, 관측성만 보강.
		log.Printf("kdb.news_search: non-200 status=%d query=%q (결과 없음과 구분)", resp.StatusCode, query)
		return nil
	}
	items, err := ParseFeed(resp.Body)
	if err != nil {
		log.Printf("kdb.news_search: feed parse err=%v query=%q", err, query)
		return nil
	}
	out := make([]string, 0, max)
	for _, it := range items {
		t := strings.TrimSpace(it.Title)
		// 첫 항목은 보통 검색어 echo("query" - Google 뉴스) — 스킵.
		if t == "" || strings.Contains(t, "Google 뉴스") || strings.Contains(t, "Google News") {
			continue
		}
		out = append(out, t)
		if len(out) >= max {
			break
		}
	}
	return out
}
