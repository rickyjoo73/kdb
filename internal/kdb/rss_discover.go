// Package kdb — RSS URL auto-discovery from media homepage.
//
// 운영자가 매체별 RSS URL 을 일일이 찾는 부담 줄이기:
//  1. 매체 홈페이지 GET (e.g. https://soompi.com/es/)
//  2. <link rel="alternate" type="application/rss+xml" href="..."> 추출
//  3. 없으면 일반 경로 폴백 (/feed, /rss, /rss.xml, /atom.xml)
//  4. fetch + RSS parse 성공하면 URL 반환
//
// 정공법: 5 candidate URL 시도 cap. 매 fetch 5s timeout. 실패해도 silent.
package kdb

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// DiscoveryResult — 자동 발견 결과.
type DiscoveryResult struct {
	URL     string // 발견된 RSS URL (parse 성공한 첫 candidate)
	Title   string // <link title="..."> 또는 RSS channel title
	Tried   []string
	ParseOK bool
}

// RSSDiscoverer — 매체 도메인에서 RSS URL 발견.
type RSSDiscoverer struct {
	HTTPClient *http.Client
	UserAgent  string
}

// NewRSSDiscoverer — 5s timeout default.
func NewRSSDiscoverer() *RSSDiscoverer {
	return &RSSDiscoverer{
		HTTPClient: &http.Client{Timeout: 5 * time.Second},
		UserAgent:  "mediafine-kdb-discover/1.0 ( rickyjoo@aiinad.com )",
	}
}

// linkRSSRE — <link rel="alternate" type="application/rss+xml" ...> 또는 atom+xml.
// 단순 정규식 — full HTML parser 회피 (표준 lib only).
var linkRSSRE = regexp.MustCompile(
	`(?i)<link\s+[^>]*?(?:rel=["']alternate["'][^>]*?type=["']application/(?:rss|atom)\+xml["']|type=["']application/(?:rss|atom)\+xml["'][^>]*?rel=["']alternate["'])[^>]*>`)
var hrefRE = regexp.MustCompile(`(?i)href=["']([^"']+)["']`)
var titleRE = regexp.MustCompile(`(?i)title=["']([^"']+)["']`)

// Discover — domain (e.g. "soompi.com" 또는 "soompi.com/es") 의 RSS URL 시도.
//
// 정공법:
//   - https:// 우선, http:// 폴백 (1회).
//   - homepage HTML 파싱 → <link> 자동 발견.
//   - 못 찾으면 /feed, /rss, /rss.xml, /atom.xml, /feed.xml 순서로 직접 시도.
//   - 각 candidate 마다 GET → ParseFeed 성공해야 채택.
func (d *RSSDiscoverer) Discover(ctx context.Context, domain string) (*DiscoveryResult, error) {
	result := &DiscoveryResult{}
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return result, fmt.Errorf("empty domain")
	}
	domain = strings.TrimSuffix(domain, "/")

	// 1. homepage 에서 <link> 자동 발견 시도.
	homepages := []string{
		"https://" + domain + "/",
		"https://www." + stripWWW(domain) + "/",
	}
	for _, hp := range homepages {
		links := d.discoverFromHomepage(ctx, hp)
		for _, l := range links {
			result.Tried = append(result.Tried, l.URL)
			if d.verifyRSSURL(ctx, l.URL) {
				result.URL = l.URL
				result.Title = l.Title
				result.ParseOK = true
				return result, nil
			}
		}
		if len(links) > 0 {
			break
		}
	}

	// 2. 일반 경로 폴백.
	base := "https://" + domain
	commonPaths := []string{
		"/feed", "/feed/", "/rss", "/rss/", "/rss.xml",
		"/atom.xml", "/feed.xml", "/index.xml",
	}
	for _, p := range commonPaths {
		u := base + p
		result.Tried = append(result.Tried, u)
		if d.verifyRSSURL(ctx, u) {
			result.URL = u
			result.ParseOK = true
			return result, nil
		}
	}

	return result, fmt.Errorf("no RSS feed discovered (tried %d candidates)", len(result.Tried))
}

type homepageLink struct {
	URL   string
	Title string
}

// discoverFromHomepage — homepage HTML fetch + <link> 추출.
func (d *RSSDiscoverer) discoverFromHomepage(ctx context.Context, homepageURL string) []homepageLink {
	req, err := http.NewRequestWithContext(ctx, "GET", homepageURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB cap
	if err != nil {
		return nil
	}
	html := string(body)
	matches := linkRSSRE.FindAllString(html, -1)
	if len(matches) == 0 {
		return nil
	}

	base, _ := url.Parse(homepageURL)
	var out []homepageLink
	seen := make(map[string]bool)
	for _, m := range matches {
		href := hrefRE.FindStringSubmatch(m)
		if len(href) < 2 {
			continue
		}
		title := ""
		if t := titleRE.FindStringSubmatch(m); len(t) > 1 {
			title = t[1]
		}
		// resolve relative URL
		u, err := url.Parse(href[1])
		if err != nil {
			continue
		}
		abs := base.ResolveReference(u).String()
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, homepageLink{URL: abs, Title: title})
		if len(out) >= 5 {
			break
		}
	}
	return out
}

// verifyRSSURL — candidate URL GET + RSS parse 성공 검증.
func (d *RSSDiscoverer) verifyRSSURL(ctx context.Context, candidate string) bool {
	req, err := http.NewRequestWithContext(ctx, "GET", candidate, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", d.UserAgent)
	req.Header.Set("Accept", "application/rss+xml, application/atom+xml, application/xml, text/xml")
	resp, err := d.HTTPClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return false
	}
	items, err := ParseFeed(resp.Body)
	return err == nil && len(items) > 0
}

func stripWWW(d string) string {
	return strings.TrimPrefix(d, "www.")
}
