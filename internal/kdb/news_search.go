// Package kdb — SearchNewsContext: classify 의 "모르면 검색" 보조.
//
// classify(gpt)가 needs_search/unknown 을 반환할 때, 이름으로 일반 웹검색을 돌려
// 관련 기사/문서 제목을 문맥(SearchHits)으로 모은다. 부수효과 없음(제목만 반환).
//
// 2026-06-22: 백엔드를 Google News RSS(KDB IP 에서 503 차단) → websearch 체인
// (Bing 주력·DDG fallback, 전역 throttle + 차단 시 cooldown)으로 교체. on-demand
// 단일 호출이라 throttle 안에서 안전. site_search.go(SiteSearchService)는 whitelist
// 도메인 스코프 + RSS enqueue 라 무겁다 — 여기서는 제목 문맥만.
package kdb

import (
	"context"
	"log"
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

// SearchNewsContext — query 로 웹검색해 관련 문서 제목을 최대 max 개 반환.
// best-effort: 실패(네트워크/차단/파싱)는 nil 반환, 호출측 분류엔 영향 없음.
func SearchNewsContext(ctx context.Context, query string, max int) []string {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil
	}
	if max <= 0 {
		max = 6
	}
	res, _, err := websearch.Default().Search(ctx, `"`+q+`"`, "", max)
	if err != nil {
		// 차단/오류는 "결과 없음"과 구분되도록 로그 — best-effort 라 동작은 nil.
		log.Printf("kdb.news_search: websearch err=%v query=%q (결과 없음과 구분)", err, query)
		return nil
	}
	out := make([]string, 0, max)
	for _, r := range res {
		t := strings.TrimSpace(r.Title)
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(out) >= max {
			break
		}
	}
	return out
}
