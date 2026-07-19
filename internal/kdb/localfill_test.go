package kdb

import (
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

func TestBuildLocalFillQueryUsesOnlyProperNoun(t *testing.T) {
	q := buildLocalFillQuery(localFillEntity{
		ko:    "예스24 라이브홀",
		etype: "brand_place",
	}, "en")

	if q != `"예스24 라이브홀"` {
		t.Fatalf("query = %q, want exact proper noun only", q)
	}
}

func TestBuildLocalFillQueriesAddsUnquotedProperNounFallback(t *testing.T) {
	queries := buildLocalFillQueries(localFillEntity{
		ko:    "예스24 라이브홀",
		etype: "brand_place",
	}, "en")
	if len(queries) < 2 {
		t.Fatalf("expected relaxed fallback query, got %#v", queries)
	}
	if queries[0] != `"예스24 라이브홀"` || queries[1] != "예스24 라이브홀" {
		t.Fatalf("queries = %#v, want quoted and unquoted proper noun only", queries)
	}
}

func TestBuildLocalFillQueryDoesNotAddTypeOrLocaleHints(t *testing.T) {
	q := buildLocalFillQuery(localFillEntity{
		ko:    "지은오",
		etype: "character",
		works: []string{"화려한 날들"},
	}, "ja")

	if q != `"지은오"` {
		t.Fatalf("query = %q, want exact proper noun only", q)
	}
}

func TestExactSearchTermLeavesQuotesAlone(t *testing.T) {
	if got := exactSearchTerm(`A "quoted" name`); got != `A "quoted" name` {
		t.Fatalf("exactSearchTerm escaped quoted input: %q", got)
	}
}

func TestShouldFetchLocalFillEvidenceOfficialOnly(t *testing.T) {
	if !shouldFetchLocalFillEvidence("https://kbsworld.kbs.co.kr/program/view.php?pg_seq=2428") {
		t.Fatal("expected KBS World official page to be fetched")
	}
	if !shouldFetchLocalFillEvidence("https://yes24livehall.com/") {
		t.Fatal("expected YES24 LIVE HALL official page to be fetched")
	}
	if shouldFetchLocalFillEvidence("https://namu.moe/w/example") {
		t.Fatal("community pages must not be fetched as official evidence")
	}
	for _, spoof := range []string{
		"http://127.0.0.1/?next=kbs.co.kr",
		"https://kbs.co.kr.evil.example/path",
		"https://evil.example/?next=https://kbs.co.kr",
		"file:///etc/passwd?kbs.co.kr",
		"https://user@kbs.co.kr/path",
		"https://kbs.co.kr:8443/path",
	} {
		if shouldFetchLocalFillEvidence(spoof) {
			t.Errorf("spoofed/private evidence URL accepted: %s", spoof)
		}
	}
}

func TestFilterLocalFillResultsKeepsOnlyProperNounEvidence(t *testing.T) {
	got := filterLocalFillResults("호텔 델 루나", "", []websearch.Result{
		{Title: "Chrome Web Store", Snippet: "unrelated", URL: "https://example.com/chrome"},
		{Title: "호텔 델 루나", Snippet: "tvN drama information", URL: "https://example.com/hotel-del-luna"},
	})
	if len(got) != 1 || got[0].Title != "호텔 델 루나" {
		t.Fatalf("filtered results = %#v, want only the result containing the proper noun", got)
	}
}

func TestFilterLocalFillResultsKeepsOfficialEvidenceURL(t *testing.T) {
	got := filterLocalFillResults("예스24 라이브홀", "", []websearch.Result{
		{Title: "Venue", Snippet: "official site", URL: "https://yes24livehall.com/"},
	})
	if len(got) != 1 {
		t.Fatalf("official evidence URL was filtered out: %#v", got)
	}
}

func TestSummarizeLocalFillEvidenceDoesNotPromoteUnrelatedLateName(t *testing.T) {
	longPrefix := strings.Repeat("한국어 안내 ", 500)
	got := summarizeLocalFillEvidence(longPrefix + " footer YES24 LIVE HALL Corp.")
	if strings.Contains(got, "YES24 LIVE HALL") {
		t.Fatalf("unrelated late page token was elevated into evidence summary: %q", got)
	}
}
