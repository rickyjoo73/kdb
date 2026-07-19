// Package websearch — KDB 의 on-demand 웹검색 추상화(pluggable provider chain).
//
// 배경(2026-06-22 실측, KDB 서버 IP): 기존 검색(site_search·news_search)이 쓰던
// Google News RSS 는 503 차단. DuckDuckGo 는 단일 쿼리는 200 이나 ~5쿼리 연속 후
// 202 로 IP 가 차단되고 지속됨(데이터센터 IP 봇탐지). Bing/Mojeek/Sogou(zh)/Coccoc(vi)
// 은 200 도달. → 단일 엔진 의존을 버리고 provider chain + 전역 throttle + 차단 시
// provider cooldown 으로 "필요할 때마다(on-demand) 1건씩" 안정 검색한다(벌크 금지).
//
// 정공법:
//   - 전역 throttle: 모든 검색 호출을 최소 간격으로 직렬화(버스트 차단 방지).
//   - provider cooldown: 어떤 provider 가 차단(비200/봇챌린지)되면 일정시간 스킵 →
//     체인의 다음 provider 로. 복구는 자동(쿨다운 만료).
//   - HTML 스크래핑은 best-effort(파서 깨지면 그 provider만 빈 결과). 정식 API
//     (Bing/Brave/Mojeek key)는 Provider 하나 추가로 드롭인 가능(블로킹 영구 제거).
package websearch

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/sourcehealth"
)

// Result — 검색 결과 1건.
type Result struct {
	Title   string
	URL     string
	Snippet string
}

// Provider — 단일 검색 백엔드. locale 은 현지엔진/지역화 힌트(미사용 가능).
type Provider interface {
	Name() string
	Search(ctx context.Context, query, locale string, max int) ([]Result, error)
}

// --- 전역 throttle + provider cooldown -------------------------------------

var (
	mu          sync.Mutex
	lastCall    time.Time
	cooldownEnd = map[string]time.Time{} // provider Name → 차단 해제 시각
)

// minInterval — 모든 웹검색 호출 사이 최소 간격(버스트 차단 방지). env 로 조정.
func minInterval() time.Duration {
	if v := os.Getenv("KDB_WEBSEARCH_MIN_INTERVAL_MS"); v != "" {
		if ms, err := time.ParseDuration(v + "ms"); err == nil && ms > 0 {
			return ms
		}
	}
	return 2500 * time.Millisecond
}

// cooldownDur — provider 차단 감지 시 스킵 기간.
func cooldownDur() time.Duration {
	if v := os.Getenv("KDB_WEBSEARCH_COOLDOWN_MIN"); v != "" {
		if m, err := time.ParseDuration(v + "m"); err == nil && m > 0 {
			return m
		}
	}
	return 15 * time.Minute
}

// gate — 전역 throttle 적용(직전 호출과 minInterval 간격 보장). ctx 취소 존중.
func gate(ctx context.Context) {
	mu.Lock()
	wait := time.Until(lastCall.Add(minInterval()))
	lastCall = time.Now().Add(maxDur(wait, 0)) // 예약(동시 호출도 줄세움)
	mu.Unlock()
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
		}
	}
}

func maxDur(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func onCooldown(name string) bool {
	mu.Lock()
	defer mu.Unlock()
	until, ok := cooldownEnd[name]
	return ok && time.Now().Before(until)
}

func markCooldown(name string) {
	mu.Lock()
	cooldownEnd[name] = time.Now().Add(cooldownDur())
	mu.Unlock()
}

// --- Chain ------------------------------------------------------------------

// Chain — 순서대로 시도하는 provider 모음. 쿨다운 중인 provider 는 스킵, 결과를
// 낸 첫 provider 에서 멈춘다. 전부 빈/차단이면 빈 결과.
type Chain struct{ Providers []Provider }

// Search — on-demand 1건 검색. 전역 throttle 후 체인을 순회한다.
func (c Chain) Search(ctx context.Context, query, locale string, max int) ([]Result, string, error) {
	if strings.TrimSpace(query) == "" {
		return nil, "", fmt.Errorf("empty query")
	}
	if max <= 0 {
		max = 8
	}
	var lastErr error
	for _, p := range c.Providers {
		if onCooldown(p.Name()) {
			continue
		}
		gate(ctx)
		res, err := p.Search(ctx, query, locale, max)
		if err != nil {
			lastErr = err
			markCooldown(p.Name()) // 차단/오류 → 잠시 스킵
			continue
		}
		if len(res) > 0 {
			return res, p.Name(), nil
		}
	}
	return nil, "", lastErr
}

// Default — env KDB_WEBSEARCH_PROVIDERS(CSV, 기본 "searxng,bing,ddg") 로 체인 구성.
// searxng(자체호스팅 메타검색·무카드·무키, 1순위) → bing/ddg(HTML 스크래핑 fallback).
// 지원: searxng, bing, ddg(duckduckgo lite). (mojeek/sogou/brave-api 추후 추가.)
func Default() Chain {
	order := os.Getenv("KDB_WEBSEARCH_PROVIDERS")
	if strings.TrimSpace(order) == "" {
		order = "searxng,bing,ddg"
	}
	var ps []Provider
	for _, name := range strings.Split(order, ",") {
		switch strings.TrimSpace(strings.ToLower(name)) {
		case "searxng":
			ps = append(ps, searxngProvider{base: searxngBase()})
		case "bing":
			ps = append(ps, bingProvider{})
		case "ddg", "duckduckgo":
			ps = append(ps, ddgProvider{})
		}
	}
	if len(ps) == 0 {
		ps = []Provider{searxngProvider{base: searxngBase()}, bingProvider{}, ddgProvider{}}
	}
	return Chain{Providers: ps}
}

// --- HTTP helper ------------------------------------------------------------

var client = &http.Client{Timeout: 12 * time.Second}

const browserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:122.0) Gecko/20100101 Firefox/122.0"

// fetch — GET, 봇 차단(비200·202 챌린지)은 error. 본문 반환.
func fetch(ctx context.Context, rawURL string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-US,en;q=0.8")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20)) // 2MB cap
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d", resp.StatusCode) // 202/403/429/503 = 차단
	}
	return string(body), nil
}

var (
	tagRe = regexp.MustCompile(`<[^>]+>`)
	wsRe  = regexp.MustCompile(`\s+`)
)

// clean — HTML 태그 제거 + 엔티티 일부 복원 + 공백 정리.
func clean(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	r := strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&#x27;", "'",
		"&lt;", "<", "&gt;", ">", "&nbsp;", " ")
	s = r.Replace(s)
	return strings.TrimSpace(wsRe.ReplaceAllString(s, " "))
}

// --- SearXNG (자체 호스팅 메타검색, JSON) -----------------------------------

type searxngProvider struct{ base string }

func (searxngProvider) Name() string { return "searxng" }

// searxngBase — 인스턴스 URL. env KDB_SEARXNG_URL, 기본은 스택 내부 서비스.
func searxngBase() string {
	if v := strings.TrimSpace(os.Getenv("KDB_SEARXNG_URL")); v != "" {
		return v
	}
	return "http://kdb-searxng:8080"
}

func (p searxngProvider) Search(ctx context.Context, query, locale string, max int) ([]Result, error) {
	base := p.base
	if base == "" {
		base = searxngBase()
	}
	q := url.Values{}
	q.Set("q", query)
	q.Set("format", "json")
	if lang := searxngLang(locale); lang != "" {
		q.Set("language", lang)
	}
	// 현지엔진 힌트: 기본 집계(google/brave)는 현지표기 약함. locale 별 현지엔진을
	// SearXNG 내장 파서로 섞는다(zh=baidu — 직접 스크래핑은 302지만 SearXNG 가 처리).
	if eng := searxngEngines(locale); eng != "" {
		q.Set("engines", eng)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(base, "/")+"/search?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		sourcehealth.Record("searxng", 0, err)
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		sourcehealth.Record("searxng", resp.StatusCode, nil)
		return nil, fmt.Errorf("searxng status %d", resp.StatusCode)
	}
	sourcehealth.Record("searxng", http.StatusOK, nil)
	var sr struct {
		Results []struct {
			URL     string `json:"url"`
			Title   string `json:"title"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&sr); err != nil {
		return nil, err
	}
	out := make([]Result, 0, max)
	for _, r := range sr.Results {
		if r.Title == "" || r.URL == "" {
			continue
		}
		out = append(out, Result{Title: r.Title, URL: r.URL, Snippet: r.Content})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

// searxngEngines — locale → SearXNG engines. 부하최소 방침(2026-06-23): rate-limit 을
// 잘 유발하는 google/brave/ddg(상위 스크래핑 엔진)는 빼고, 현지표기에 정확하고 rate-limit
// 에 관대한 wikipedia(언어별 = 현지어 문서제목 = 정식 현지표기) 를 1순위로, bing 을 보조로.
// CJK 는 현지엔진(baidu=중국 간체, wikipedia zh-TW=번체) 추가. searxngLang 으로 언어 유도.
// 빈값이면 SearXNG 가 기본 다엔진(부하↑·rate-limit 위험)을 써서, 모든 fill locale 에 명시.
// 참고: Coccoc(vi)·태국엔진은 SearXNG 내장 미존재 → vi/id 등 라틴 locale 은 wikipedia+bing.
// ★엔진셋 실측 재조정(2026-06-29): SearXNG 경유 엔진별 실측 결과 — wikipedia 는 한국어
// 쿼리(예: '봉준호')에 results=0·infoboxes=0(언어판 위키엔 한글 제목 문서가 없어 cross-lang
// 이름해소에 무용), mojeek 은 CJK recall 0(3회). 실제 working = bing·baidu·sogou(+ ddg/
// brave/google 은 working 이나 ★오너 IP밴 금지 제약으로 미사용). 따라서 native·ban-safe 인
// sogou(zh, 실측 10) 를 zh/zh_hant 에 추가. bing 은 모든 locale 의 주력 유지. wikipedia 는
// 무해(SearXNG 가 병렬 질의, 0이면 무시)하나 영문/라틴 고유명사엔 가끔 기여하므로 잔류.
// ★2026-07-19 수리: 종전 로케일별 고정 엔진 제한(ja=bing,wikipedia 등)이 방금 큐레이트한
// SearXNG 밴-안전 엔진 풀을 못 쓰게 막고, 심지어 bing-ja 는 한국어 쿼리에 완전 노이즈
// (구글문서 도움말 등)를 반환해 fill 이 전부 폐기됐음. 빈 문자열=engines 파라미터 생략 →
// SearXNG 가 settings.yml 의 keep_only 풀(bing/baidu/sogou/mojeek/yahoo/wikipedia/…)을
// 전부 취합·차단엔진 자동 스킵. language 파라미터(searxngLang)가 현지 결과로 유도. 실측:
// engines 생략 시 "트롤리 드라마"→나무위키·넷플릭스 정상, 제한 시→노이즈. 엔진 큐레이션은
// 이제 코드가 아니라 searxng/settings.yml 에서 관리(운영 일원화).
func searxngEngines(loc string) string {
	return ""
}

// searxngLang — locale → SearXNG language 파라미터(현지 결과 유도).
func searxngLang(loc string) string {
	switch loc {
	case "ja":
		return "ja"
	case "vi":
		return "vi"
	case "id":
		return "id"
	case "es":
		return "es"
	case "pt_br":
		return "pt-BR"
	case "zh":
		return "zh-CN"
	case "zh_hant":
		return "zh-TW"
	case "en":
		return "en"
	}
	return ""
}

// --- DuckDuckGo (lite) ------------------------------------------------------

type ddgProvider struct{}

func (ddgProvider) Name() string { return "ddg" }

var ddgLinkRe = regexp.MustCompile(`(?s)<a[^>]*class=['"]result-link['"][^>]*href=['"]([^'"]+)['"][^>]*>(.*?)</a>`)
var ddgLinkRe2 = regexp.MustCompile(`(?s)<a[^>]*href=['"]([^'"]+)['"][^>]*class=['"]result-link['"][^>]*>(.*?)</a>`)

func (ddgProvider) Search(ctx context.Context, query, _ string, max int) ([]Result, error) {
	u := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(query)
	body, err := fetch(ctx, u)
	if err != nil {
		return nil, err
	}
	ms := ddgLinkRe.FindAllStringSubmatch(body, -1)
	if len(ms) == 0 {
		ms = ddgLinkRe2.FindAllStringSubmatch(body, -1)
	}
	out := make([]Result, 0, max)
	for _, m := range ms {
		title := clean(m[2])
		link := m[1]
		if title == "" || link == "" {
			continue
		}
		out = append(out, Result{Title: title, URL: link})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

// --- Bing -------------------------------------------------------------------

type bingProvider struct{}

func (bingProvider) Name() string { return "bing" }

// b_algo 결과 블록 → <h2 ...><a ... href>제목</a> + <p>스니펫.
// 실측(2026-06-22): `<li class="b_algo" data-id iid=...>`, `<h2 class=""><a target=...
// href="https://www.bing.com/ck/a?...">제목</a>` (href 는 Bing 클릭 리다이렉트 —
// 제목/스니펫 텍스트가 추출용이라 그대로 둠).
var bingBlockRe = regexp.MustCompile(`(?s)<li class="b_algo"[^>]*>(.*?)</li>`)
var bingHrefRe = regexp.MustCompile(`(?s)<h2[^>]*>\s*<a[^>]*\shref="([^"]+)"[^>]*>(.*?)</a>`)
var bingSnipRe = regexp.MustCompile(`(?s)<p[^>]*>(.*?)</p>`)

func (bingProvider) Search(ctx context.Context, query, locale string, max int) ([]Result, error) {
	u := "https://www.bing.com/search?q=" + url.QueryEscape(query)
	if mkt := bingMarket(locale); mkt != "" {
		u += "&setlang=" + mkt
	}
	body, err := fetch(ctx, u)
	if err != nil {
		return nil, err
	}
	out := make([]Result, 0, max)
	for _, b := range bingBlockRe.FindAllStringSubmatch(body, -1) {
		hm := bingHrefRe.FindStringSubmatch(b[1])
		if hm == nil {
			continue
		}
		title, link := clean(hm[2]), decodeBingURL(hm[1])
		if title == "" || link == "" {
			continue
		}
		snip := ""
		if sm := bingSnipRe.FindStringSubmatch(b[1]); sm != nil {
			snip = clean(sm[1])
		}
		out = append(out, Result{Title: title, URL: link, Snippet: snip})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}

// decodeBingURL — Bing 클릭 리다이렉트(www.bing.com/ck/a?...&u=a1<base64url>)를
// 실제 목적지 URL 로 복원. site_search 가 결과 페이지를 fetch 하려면 실 URL 필요.
// 복원 실패(다른 형식·직접 링크)면 원본 그대로 반환.
func decodeBingURL(raw string) string {
	raw = strings.ReplaceAll(raw, "&amp;", "&")
	if !strings.Contains(raw, "bing.com/ck/a") {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	enc := u.Query().Get("u")
	if len(enc) > 2 && strings.HasPrefix(enc, "a1") {
		if dec, err := base64.RawURLEncoding.DecodeString(enc[2:]); err == nil && len(dec) > 0 {
			return string(dec)
		}
	}
	return raw
}

// bingMarket — locale → Bing setlang(현지 결과 유도). 미지원은 빈값.
func bingMarket(loc string) string {
	switch loc {
	case "ja":
		return "ja"
	case "vi":
		return "vi"
	case "id":
		return "id"
	case "es":
		return "es"
	case "pt_br":
		return "pt"
	case "zh":
		return "zh-Hans"
	case "zh_hant":
		return "zh-Hant"
	case "en":
		return "en"
	}
	return ""
}
