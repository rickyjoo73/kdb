package kdb

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

func TestRequireDiscoverySourceSentinel(t *testing.T) {
	err := requireDiscoverySource(nil, "vi")
	if !errors.Is(err, ErrNoDiscoverySource) {
		t.Fatalf("error = %v, want errors.Is(..., ErrNoDiscoverySource)", err)
	}
	if !strings.Contains(err.Error(), "locale vi") {
		t.Fatalf("error = %q, want locale context", err)
	}
	if err := requireDiscoverySource([]siteSearchDomain{{Domain: "example.com", Locale: "vi"}}, "vi"); err != nil {
		t.Fatalf("active discovery source returned error: %v", err)
	}
}

func TestBuildSiteSearchQueries(t *testing.T) {
	ent := siteSearchEntity{
		CanonicalKO: "방탄소년단",
		CanonicalEN: "BTS",
		Canonical:   "BTS",
		AliasesKO:   []string{"방탄소년단", "방탄"},
		AliasesEN:   []string{"Bangtan Boys", "bts"},
		Aliases:     []string{"防弾少年団"},
	}
	got := buildSiteSearchQueries(ent, "")
	want := []string{"방탄소년단", "BTS", "방탄", "Bangtan Boys", "防弾少年団"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %#v, want %#v", got, want)
	}

	got = buildSiteSearchQueries(ent, "  NewJeans  ")
	want = []string{"NewJeans"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("override queries = %#v, want %#v", got, want)
	}
}

func TestSiteSearchRequestNormalized(t *testing.T) {
	got := (SiteSearchRequest{
		Locale:              "PT_BR",
		Query:               "  BTS  ",
		Domains:             []string{" example.com ", "example.com", "news.example"},
		LimitDomains:        100,
		MaxResultsPerDomain: 100,
	}).normalized()

	if got.Locale != "pt-br" || got.Query != "BTS" {
		t.Fatalf("normalized locale/query = %q/%q", got.Locale, got.Query)
	}
	if got.LimitDomains != maxSiteSearchDomainLimit {
		t.Fatalf("LimitDomains = %d, want %d", got.LimitDomains, maxSiteSearchDomainLimit)
	}
	if got.MaxResultsPerDomain != maxSiteSearchResultsLimit {
		t.Fatalf("MaxResultsPerDomain = %d, want %d", got.MaxResultsPerDomain, maxSiteSearchResultsLimit)
	}
	if want := []string{"example.com", "news.example"}; !reflect.DeepEqual(got.Domains, want) {
		t.Fatalf("Domains = %#v, want %#v", got.Domains, want)
	}
}

func TestSiteSearchItemMentionsEntity(t *testing.T) {
	ent := siteSearchEntity{
		CanonicalKO: "방탄소년단",
		CanonicalEN: "BTS",
		AliasesEN:   []string{"Bangtan Boys"},
	}

	if !siteSearchItemMentionsEntity(FeedItem{Title: "BTS announces tour"}, ent, "") {
		t.Fatal("expected title mention to match")
	}
	if !siteSearchItemMentionsEntity(FeedItem{Description: "New album from Bangtan Boys"}, ent, "") {
		t.Fatal("expected description alias to match")
	}
	if siteSearchItemMentionsEntity(FeedItem{Title: "Unrelated music news"}, ent, "") {
		t.Fatal("unexpected unrelated match")
	}
}

// fakeSearcher — searchDomain 백엔드 주입용 테스트 더블(websearch 미호출, 네트워크 0).
type fakeSearcher struct {
	gotQuery, gotLocale string
	results             []websearch.Result
	err                 error
}

func (f *fakeSearcher) Search(_ context.Context, query, locale string, _ int) ([]websearch.Result, string, error) {
	f.gotQuery, f.gotLocale = query, locale
	return f.results, "fake", f.err
}

// searchDomain 이 site: 쿼리를 만들고 websearch Result → FeedItem 으로 매핑(빈 제목/URL
// 스킵)하는지 검증. RSS 백엔드 제거(2026-06-22) 후 이 매핑이 계약.
func TestSearchDomainMapsWebsearch(t *testing.T) {
	fake := &fakeSearcher{results: []websearch.Result{
		{Title: "BTS article", URL: "https://example.com/news/1", Snippet: "Alpha"},
		{Title: "", URL: "https://skip.me"}, // 빈 제목 → 스킵
	}}
	svc := &SiteSearchService{Searcher: fake}
	items, err := svc.searchDomain(context.Background(), "example.com", "vi", "BTS")
	if err != nil {
		t.Fatalf("searchDomain: %v", err)
	}
	if fake.gotQuery != `site:example.com "BTS"` {
		t.Fatalf("q = %q", fake.gotQuery)
	}
	if fake.gotLocale != "vi" {
		t.Fatalf("locale = %q", fake.gotLocale)
	}
	if len(items) != 1 || items[0].Title != "BTS article" ||
		items[0].Link != "https://example.com/news/1" || items[0].Description != "Alpha" {
		t.Fatalf("items = %#v", items)
	}
}

func TestTextMentionsQuery(t *testing.T) {
	cases := []struct {
		text, q string
		want    bool
	}{
		{"iu announced her comeback", "iu", true},   // ASCII 단어 경계
		{"the taium project launched", "iu", false}, // 더 긴 단어 속 박힘 → 거부
		{"BTS and IU performed", "bts", true},       // 대소문자 무시(text 는 소문자화 가정)
		{"방탄소년단 신곡 발표", "방탄소년단", true},              // CJK substring
		{"무관한 기사 본문", "아이유", false},                 // 미포함
		{"x marks the spot", "x", false},            // 1글자 거부
	}
	for _, c := range cases {
		got := textMentionsQuery(strings.ToLower(c.text), strings.ToLower(c.q))
		if got != c.want {
			t.Errorf("textMentionsQuery(%q, %q) = %v; want %v", c.text, c.q, got, c.want)
		}
	}
}

// ★검색이 `site:` 를 안 지킨다 (2026-09-15 실측: 최근 30일 3,012건 **전부** 도메인 밖).
//
//	그런데 enqueueRaw 는 요청한 도메인을 source_domain 으로 적었다. 그래서 유튜브
//	한 페이지가 브라질 매체 5곳으로 둔갑했고(구잘 pt-br "GuzalTV"), 스팀 상점
//	페이지가 일본어 표기의 근거가 됐다(쉿(Shhh) ja "Shhh!").
//	매체합의는 "서로 다른 매체가 같은 말을 했다"를 세는 장치다 — 재료가 거짓이면
//	합의도 거짓이다. 그러니 그 매체의 페이지가 아니면 **받지 않는다**.
func TestSearchDomainDropsResultsOffTheRequestedDomain(t *testing.T) {
	fake := &fakeSearcher{results: []websearch.Result{
		{Title: "진짜 그 매체", URL: "https://koari.net/news/1"},
		{Title: "하위 호스트도 그 매체", URL: "https://m.koari.net/news/2"},
		{Title: "나무위키", URL: "https://namu.wiki/w/벡터"},
		{Title: "인스타그램", URL: "https://www.instagram.com/p/abc/"},
		{Title: "스팀 상점", URL: "https://store.steampowered.com/app/1"},
		{Title: "유튜브", URL: "https://www.youtube.com/watch?v=x"},
	}}
	svc := &SiteSearchService{Searcher: fake}
	items, err := svc.searchDomain(context.Background(), "koari.net", "ja", "벡터")
	if err != nil {
		t.Fatalf("searchDomain: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("도메인 밖 결과가 살아남았다: %#v", items)
	}
	for _, it := range items {
		if !strings.Contains(it.Link, "koari.net") {
			t.Fatalf("그 매체의 페이지가 아니다: %q", it.Link)
		}
	}
}

func TestURLIsOnDomain(t *testing.T) {
	cases := []struct {
		url, domain string
		want        bool
	}{
		{"https://koari.net/a", "koari.net", true},
		// www. · m. · 하위 호스트는 같은 매체로 본다.
		{"https://www.koari.net/a", "koari.net", true},
		{"https://m.koari.net/a", "koari.net", true},
		{"https://news.koari.net/a", "koari.net", true},
		// 접두만 같은 남의 도메인은 그 매체가 아니다.
		{"https://koari.net.evil.com/a", "koari.net", false},
		{"https://namu.wiki/w/x", "koari.net", false},
		{"https://www.youtube.com/watch", "daebak.tokyo", false},
		{"", "koari.net", false},
		{"https://koari.net/a", "", false},
	}
	for _, c := range cases {
		if got := URLIsOnDomain(c.url, c.domain); got != c.want {
			t.Fatalf("URLIsOnDomain(%q,%q) = %v, want %v", c.url, c.domain, got, c.want)
		}
	}
}
