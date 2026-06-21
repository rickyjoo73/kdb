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

// typeContextWords — requested_entity_type 별 검색어 보강. bare 모호 토큰(예
// "펜트하우스"=드라마/일반어, "마크"=NCT멤버/일반어)이 일반어 뉴스로 검색돼 분류가
// 오판되는 것을 막고, 소비자가 요청한 type 의 맥락(드라마/아이돌 등)으로 검색을
// 특정한다. 실측(2026-06-21): 이름만 따옴표 + 이 맥락어 비따옴표(`"펜트하우스" 드라마`)
// 검색 시 펜트하우스→drama 1.0, 마크→person 1.0 으로 해결. 오염방어는 분류기의
// 비엔티티/K범위 필터가 유지(틀린 힌트로 junk 가 통과하지 않음).
var typeContextWords = map[string]string{
	"person":         "배우 가수 아이돌",
	"group":          "그룹 케이팝",
	"drama":          "드라마",
	"movie":          "영화",
	"show":           "예능",
	"song_album":     "노래 앨범",
	"agency":         "기획사",
	"event_tour":     "콘서트",
	"channel_outlet": "방송",
}

// SearchNewsContext — query 로 Google News RSS 를 검색해 관련 기사 제목을 최대
// max 개 반환. 한국어 로케일(ko-KR) 고정 — 대상이 K-content 고유명사이기 때문.
// query 전체를 exact phrase 로 검색한다(기존 동작 유지).
// best-effort: 실패(네트워크/파싱/비-200)는 nil 반환, 호출측 분류엔 영향 없음.
func SearchNewsContext(ctx context.Context, query string, max int) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	return searchNewsRaw(ctx, `"`+q+`"`, max)
}

// SearchNewsContextTyped — requested_entity_type 가 있으면 이름은 exact phrase 로
// 고정(따옴표)하고 그 type 의 맥락어를 비따옴표로 덧붙여 검색을 특정한다. type 이
// 없거나 맥락어 없는 type(brand_place·character·term·unknown)은 bare 검색과 동일.
func SearchNewsContextTyped(ctx context.Context, query, reqType string, max int) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	raw := `"` + q + `"`
	if tw := typeContextWords[strings.TrimSpace(reqType)]; tw != "" {
		raw += " " + tw
	}
	return searchNewsRaw(ctx, raw, max)
}

// searchNewsRaw — rawQuery 를 그대로 q 파라미터로 보낸다(따옴표/맥락어 조립은 호출측
// 책임). SearchNewsContext / SearchNewsContextTyped 의 공용 코어.
func searchNewsRaw(ctx context.Context, rawQuery string, max int) []string {
	if strings.TrimSpace(rawQuery) == "" {
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
	vals.Set("q", rawQuery)
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
		log.Printf("kdb.news_search: non-200 status=%d query=%q (결과 없음과 구분)", resp.StatusCode, rawQuery)
		return nil
	}
	items, err := ParseFeed(resp.Body)
	if err != nil {
		log.Printf("kdb.news_search: feed parse err=%v query=%q", err, rawQuery)
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
