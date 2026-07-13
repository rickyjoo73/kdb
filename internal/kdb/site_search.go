package kdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

const (
	defaultSiteSearchDomainLimit  = 6
	defaultSiteSearchResultsLimit = 3
	maxSiteSearchDomainLimit      = 20
	maxSiteSearchResultsLimit     = 10
	maxSiteSearchQueries          = 5
)

// ErrNoDiscoverySource 는 요청 locale에 활성 site-discovery 매체가 없음을
// 나타낸다. 호출자는 errors.Is 로 이 상태를 일반 검색 실패와 구분해
// blocked_no_source 결과로 기록할 수 있다.
var ErrNoDiscoverySource = errors.New("no discovery source")

// SiteSearchService performs site-scoped discovery for one entity/locale.
//
// The service intentionally does not update canonical_* directly. It enqueues
// matching result pages into kwave_rss_items_raw with a forced cheap hint for
// the target entity; the standalone kdb-worker sweeper then runs the existing
// Codex extraction and media-consensus pipeline.
type SiteSearchService struct {
	Pool     *pgxpool.Pool
	Searcher webSearcher // 검색 백엔드(테스트 주입 가능). nil → websearch.Default().
}

// webSearcher — searchDomain 백엔드 추상화. websearch.Chain 이 충족(테스트는 fake 주입).
type webSearcher interface {
	Search(ctx context.Context, query, locale string, max int) ([]websearch.Result, string, error)
}

type SiteSearchRequest struct {
	EntityID            uuid.UUID
	Locale              string
	Query               string
	Domains             []string
	LimitDomains        int
	MaxResultsPerDomain int
	DryRun              bool
}

type SiteSearchResponse struct {
	EntityID        string             `json:"entity_id"`
	CanonicalKO     string             `json:"canonical_ko"`
	Locale          string             `json:"locale"`
	Queries         []string           `json:"queries"`
	DomainsSearched int                `json:"domains_searched"`
	ResultsFound    int                `json:"results_found"`
	Enqueued        int                `json:"enqueued"`
	Duplicates      int                `json:"duplicates"`
	Results         []SiteSearchResult `json:"results"`
}

type SiteSearchResult struct {
	Domain      string `json:"domain"`
	Query       string `json:"query"`
	Title       string `json:"title"`
	URL         string `json:"url"`
	Enqueued    bool   `json:"enqueued"`
	AlreadySeen bool   `json:"already_seen"`
	Error       string `json:"error,omitempty"`
}

type siteSearchEntity struct {
	ID          uuid.UUID
	CanonicalKO string
	CanonicalEN string
	Canonical   string
	AliasesKO   []string
	AliasesEN   []string
	Aliases     []string
}

type siteSearchDomain struct {
	Domain string
	Locale string
}

func NewSiteSearchService(pool *pgxpool.Pool) *SiteSearchService {
	return &SiteSearchService{
		Pool:     pool,
		Searcher: websearch.Default(),
	}
}

func (s *SiteSearchService) SearchAndEnqueue(ctx context.Context, req SiteSearchRequest) (*SiteSearchResponse, error) {
	if s == nil || s.Pool == nil {
		return nil, fmt.Errorf("site search store not configured")
	}
	req = req.normalized()
	if req.EntityID == uuid.Nil {
		return nil, fmt.Errorf("entity_id required")
	}
	if !supportedSiteSearchLocale(req.Locale) {
		return nil, fmt.Errorf("unsupported locale: %s", req.Locale)
	}

	ent, err := s.loadEntity(ctx, req.EntityID, req.Locale)
	if err != nil {
		return nil, err
	}
	queries := buildSiteSearchQueries(ent, req.Query)
	if len(queries) == 0 {
		return nil, fmt.Errorf("no search query candidates")
	}
	domains, err := s.loadDomains(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := requireDiscoverySource(domains, req.Locale); err != nil {
		return nil, err
	}

	resp := &SiteSearchResponse{
		EntityID:    req.EntityID.String(),
		CanonicalKO: ent.CanonicalKO,
		Locale:      req.Locale,
		Queries:     queries,
	}
	seenLinks := map[string]struct{}{}
	for _, d := range domains {
		resp.DomainsSearched++
		domainResults := 0
		for _, q := range queries {
			if domainResults >= req.MaxResultsPerDomain {
				break
			}
			items, err := s.searchDomain(ctx, d.Domain, req.Locale, q)
			if err != nil {
				resp.Results = append(resp.Results, SiteSearchResult{
					Domain: d.Domain,
					Query:  q,
					Error:  err.Error(),
				})
				continue
			}
			for _, item := range items {
				if domainResults >= req.MaxResultsPerDomain {
					break
				}
				if item.Link == "" || item.Title == "" {
					continue
				}
				linkKey := d.Domain + "\x00" + item.Link
				if _, ok := seenLinks[linkKey]; ok {
					continue
				}
				seenLinks[linkKey] = struct{}{}
				if !siteSearchItemMentionsEntity(item, ent, q) {
					continue
				}
				result := SiteSearchResult{
					Domain: d.Domain,
					Query:  q,
					Title:  item.Title,
					URL:    item.Link,
				}
				resp.ResultsFound++
				domainResults++
				if !req.DryRun {
					enqueued, err := s.enqueueRaw(ctx, req.EntityID, req.Locale, d.Domain, item)
					if err != nil {
						result.Error = err.Error()
					} else if enqueued {
						result.Enqueued = true
						resp.Enqueued++
					} else {
						result.AlreadySeen = true
						resp.Duplicates++
					}
				}
				resp.Results = append(resp.Results, result)
			}
		}
		time.Sleep(150 * time.Millisecond)
	}
	return resp, nil
}

func (r SiteSearchRequest) normalized() SiteSearchRequest {
	r.Locale = normalizeSearchLocale(r.Locale)
	r.Query = strings.TrimSpace(r.Query)
	if r.LimitDomains <= 0 {
		r.LimitDomains = defaultSiteSearchDomainLimit
	}
	if r.LimitDomains > maxSiteSearchDomainLimit {
		r.LimitDomains = maxSiteSearchDomainLimit
	}
	if r.MaxResultsPerDomain <= 0 {
		r.MaxResultsPerDomain = defaultSiteSearchResultsLimit
	}
	if r.MaxResultsPerDomain > maxSiteSearchResultsLimit {
		r.MaxResultsPerDomain = maxSiteSearchResultsLimit
	}
	r.Domains = uniqueCleanStrings(r.Domains, maxSiteSearchDomainLimit)
	return r
}

func (s *SiteSearchService) loadEntity(ctx context.Context, id uuid.UUID, locale string) (siteSearchEntity, error) {
	targetCol, aliasesCol := siteSearchLocaleColumns(locale)
	q := fmt.Sprintf(`
SELECT id, canonical_ko,
       COALESCE(canonical_en, ''),
       COALESCE(%s, ''),
       COALESCE(aliases_ko, '{}'),
       COALESCE(aliases_en, '{}'),
       COALESCE(%s, '{}')
FROM kwave_entities
WHERE id = $1`, targetCol, aliasesCol)
	var ent siteSearchEntity
	if err := s.Pool.QueryRow(ctx, q, id).Scan(
		&ent.ID, &ent.CanonicalKO, &ent.CanonicalEN, &ent.Canonical,
		&ent.AliasesKO, &ent.AliasesEN, &ent.Aliases,
	); err != nil {
		if err == pgx.ErrNoRows {
			return ent, fmt.Errorf("entity not found")
		}
		return ent, err
	}
	return ent, nil
}

func (s *SiteSearchService) loadDomains(ctx context.Context, req SiteSearchRequest) ([]siteSearchDomain, error) {
	args := []any{req.Locale, req.LimitDomains}
	filter := ""
	if len(req.Domains) > 0 {
		args = append(args, req.Domains)
		filter = " AND domain = ANY($3::text[])"
	}
	rows, err := s.Pool.Query(ctx, `
SELECT domain, locale
FROM kwave_news_whitelist
WHERE discovery_enabled = true
  AND locale = $1`+filter+`
ORDER BY COALESCE(media_trust, trust, 0.8) DESC,
         observations_total DESC,
         domain
LIMIT $2`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []siteSearchDomain
	for rows.Next() {
		var d siteSearchDomain
		if err := rows.Scan(&d.Domain, &d.Locale); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func requireDiscoverySource(domains []siteSearchDomain, locale string) error {
	if len(domains) > 0 {
		return nil
	}
	return fmt.Errorf("%w for locale %s", ErrNoDiscoverySource, locale)
}

// searchDomain — 2026-06-22: Google News RSS(KDB IP 503) → websearch 체인(Bing 주력·
// DDG fallback, 전역 throttle + cooldown). site:domain 연산자로 도메인 스코프.
// Result(Title/URL/Snippet) → FeedItem 으로 매핑해 enqueueRaw 와 호환.
func (s *SiteSearchService) searchDomain(ctx context.Context, domain, locale, query string) ([]FeedItem, error) {
	sr := s.Searcher
	if sr == nil {
		sr = websearch.Default()
	}
	res, _, err := sr.Search(ctx, `site:`+domain+` "`+query+`"`, locale, 10)
	if err != nil {
		return nil, err
	}
	items := make([]FeedItem, 0, len(res))
	for _, r := range res {
		if r.Title == "" || r.URL == "" {
			continue
		}
		items = append(items, FeedItem{Title: r.Title, Link: r.URL, Description: r.Snippet})
	}
	return items, nil
}

func (s *SiteSearchService) enqueueRaw(ctx context.Context, entityID uuid.UUID, locale, domain string, item FeedItem) (bool, error) {
	hints, _ := json.Marshal([]string{entityID.String()})
	tag, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_rss_items_raw
  (source_domain, locale, link, title, description, fetched_at,
   cheap_status, cheap_hints, codex_status)
VALUES ($1, $2, $3, $4, $5, now(), 'hit', $6::jsonb, 'pending')
ON CONFLICT (source_domain, link) DO NOTHING`,
		domain, locale, item.Link, item.Title, item.Description, string(hints))
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func buildSiteSearchQueries(ent siteSearchEntity, override string) []string {
	if strings.TrimSpace(override) != "" {
		return uniqueCleanStrings([]string{override}, 1)
	}
	candidates := []string{
		ent.CanonicalKO,
		ent.CanonicalEN,
		ent.Canonical,
	}
	candidates = append(candidates, ent.AliasesKO...)
	candidates = append(candidates, ent.AliasesEN...)
	candidates = append(candidates, ent.Aliases...)
	return uniqueCleanStrings(candidates, maxSiteSearchQueries)
}

func siteSearchItemMentionsEntity(item FeedItem, ent siteSearchEntity, query string) bool {
	text := strings.ToLower(item.Title + " " + item.Description)
	for _, s := range buildSiteSearchQueries(ent, query) {
		if textMentionsQuery(text, strings.ToLower(strings.TrimSpace(s))) {
			return true
		}
	}
	return false
}

// textMentionsQuery — 오매칭 축소 매칭. 1글자 query 는 거부(너무 흔함). ASCII
// query 는 단어 경계(앞뒤가 영숫자가 아님) 매칭 — 짧은 alias("IU")가 무관한 더 긴
// 단어("taium") 속에 박혀 엉뚱한 기사를 hit 으로 적재하던 것을 막는다. CJK 등
// 비-ASCII 는 공백 단어경계가 없으므로 substring 유지.
func textMentionsQuery(text, q string) bool {
	if utf8.RuneCountInString(q) < 2 {
		return false
	}
	if isASCIIWord(q) {
		return asciiBoundaryContains(text, q)
	}
	return strings.Contains(text, q)
}

func isASCIIWord(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return false
		}
	}
	return true
}

// asciiBoundaryContains — q 가 text 안에 영숫자에 둘러싸이지 않은 위치로 나타나면 true.
func asciiBoundaryContains(text, q string) bool {
	from := 0
	for {
		i := strings.Index(text[from:], q)
		if i < 0 {
			return false
		}
		pos := from + i
		beforeOK := pos == 0 || !isASCIIAlnum(rune(text[pos-1]))
		end := pos + len(q)
		afterOK := end >= len(text) || !isASCIIAlnum(rune(text[end]))
		if beforeOK && afterOK {
			return true
		}
		from = pos + 1
	}
}

func isASCIIAlnum(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func normalizeSearchLocale(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	locale = strings.ReplaceAll(locale, "_", "-")
	switch locale {
	case "pt":
		return "pt-br"
	case "zh-tw", "zh-hk":
		return "zh-hant"
	case "id-id":
		return "id"
	}
	return locale
}

func supportedSiteSearchLocale(locale string) bool {
	switch normalizeSearchLocale(locale) {
	case "en", "ja", "vi", "es", "id", "pt-br", "zh", "zh-hant":
		return true
	default:
		return false
	}
}

func siteSearchLocaleColumns(locale string) (targetCol, aliasesCol string) {
	switch normalizeSearchLocale(locale) {
	case "ja":
		return "canonical_ja", "aliases_ja"
	case "vi":
		return "canonical_vi", "aliases_vi"
	case "es":
		return "canonical_es", "aliases_es"
	case "id":
		return "canonical_id", "aliases_id"
	case "pt-br":
		return "canonical_pt_br", "aliases_pt_br"
	case "zh", "zh-hant":
		return "canonical_zh_hant", "aliases_zh_hant"
	default:
		return "canonical_en", "aliases_en"
	}
}

func uniqueCleanStrings(in []string, limit int) []string {
	if limit <= 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, minInt(len(in), limit))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		key := strings.ToLower(s)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, s)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
