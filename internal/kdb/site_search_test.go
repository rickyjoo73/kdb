package kdb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

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

func TestGoogleNewsLocaleParams(t *testing.T) {
	hl, gl, ceid := googleNewsLocaleParams("vi")
	if hl != "vi" || gl != "VN" || ceid != "VN:vi" {
		t.Fatalf("vi params = %s/%s/%s", hl, gl, ceid)
	}
	hl, gl, ceid = googleNewsLocaleParams("zh_HK")
	if hl != "zh-TW" || gl != "TW" || ceid != "TW:zh-Hant" {
		t.Fatalf("zh-hant params = %s/%s/%s", hl, gl, ceid)
	}
}

func TestSearchDomainParsesRSS(t *testing.T) {
	var gotQuery, gotHL, gotGL, gotCEID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotHL = r.URL.Query().Get("hl")
		gotGL = r.URL.Query().Get("gl")
		gotCEID = r.URL.Query().Get("ceid")
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = w.Write([]byte(`<?xml version="1.0"?>
<rss version="2.0"><channel>
<item><title>BTS article</title><link>https://example.com/news/1</link><description>Alpha</description></item>
</channel></rss>`))
	}))
	defer server.Close()

	svc := &SiteSearchService{
		HTTPClient: server.Client(),
		UserAgent:  "test-agent",
		SearchBase: server.URL,
	}
	items, err := svc.searchDomain(context.Background(), "example.com", "vi", "BTS")
	if err != nil {
		t.Fatalf("searchDomain: %v", err)
	}
	if gotQuery != `site:example.com "BTS"` {
		t.Fatalf("q = %q", gotQuery)
	}
	if gotHL != "vi" || gotGL != "VN" || gotCEID != "VN:vi" {
		t.Fatalf("locale params = %s/%s/%s", gotHL, gotGL, gotCEID)
	}
	if len(items) != 1 || items[0].Title != "BTS article" || !strings.Contains(items[0].Link, "example.com/news/1") {
		t.Fatalf("items = %#v", items)
	}
}
