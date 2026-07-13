// Package musicbrainz — MusicBrainz API client for music groups/artists.
//
// 인증 불필요 (UA + 1 req/s rate limit). K-pop 그룹/가수의 다국어 alias
// (특히 ja/en) 보강용. vi/pt-br/es/id 는 약함 — 보조 layer.
package musicbrainz

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

const (
	apiBase    = "https://musicbrainz.org/ws/2"
	defaultUA  = "kdb-bootstrap/0.1 (https://kdb.aiinplanet.com)"
	minBetween = 1100 * time.Millisecond // 1 req/s 정중하게.
)

// Client — MusicBrainz HTTP client. 단일 인스턴스 권장. limiter 가 1 req/s 페이싱을
// 동시성 안전하게 보장하므로 병렬 goroutine 이 공유해도 안전하다.
type Client struct {
	HTTPClient *http.Client
	UserAgent  string
	limiter    *httpx.Limiter
}

func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		UserAgent:  defaultUA,
		limiter:    httpx.NewLimiter(minBetween),
	}
}

// Artist — wbsearch 결과 1건.
type Artist struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type,omitempty"`
	Country        string `json:"country,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Score          int    `json:"score,omitempty"`
}

// Search — name + 한국 (country=KR) 우선 매칭. K-pop 외 매체 후보는 제외.
func (c *Client) Search(ctx context.Context, name string) ([]Artist, error) {
	return c.search(ctx, name, "")
}

// SearchGroups searches only MusicBrainz artist resources whose subtype is
// Group. The /artist endpoint also returns solo people; an exact name alone
// cannot prove that a KDB group is the same identity.
func (c *Client) SearchGroups(ctx context.Context, name string) ([]Artist, error) {
	return c.search(ctx, name, "group")
}

func (c *Client) search(ctx context.Context, name, artistType string) ([]Artist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	q := url.Values{}
	query := `artist:"` + name + `" AND country:KR`
	if artistType != "" {
		query += ` AND type:` + artistType
	}
	q.Set("query", query)
	q.Set("fmt", "json")
	q.Set("limit", "5")
	body, err := c.get(ctx, "/artist?"+q.Encode())
	if err != nil {
		return nil, err
	}
	var resp struct {
		Artists []Artist `json:"artists"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mb search decode: %w", err)
	}
	return resp.Artists, nil
}

// AliasByLocale — MBID 로 locale 별 alias 가져옴. KDB 컬럼 키로 매핑.
type AliasByLocale map[string][]string // ko/en/ja/zh/...

// artistDetail — /artist/{mbid}?inc=aliases 파싱 결과. byLocale() 로 KDB 키
// 매핑 alias 를, matchesQuery() 로 동명이인 오매칭 가드를 제공한다.
type artistDetail struct {
	Name    string
	Aliases []struct {
		Name    string `json:"name"`
		Locale  string `json:"locale"`
		Type    string `json:"type"`
		Primary bool   `json:"primary"`
	}
}

func (c *Client) fetchDetail(ctx context.Context, mbid string) (*artistDetail, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, nil
	}
	body, err := c.get(ctx, "/artist/"+mbid+"?inc=aliases&fmt=json")
	if err != nil {
		return nil, err
	}
	var d artistDetail
	if err := json.Unmarshal(body, &d); err != nil {
		return nil, fmt.Errorf("mb fetch decode: %w", err)
	}
	return &d, nil
}

// byLocale — KDB canonical 키 별 alias 맵. primary name 도 en 으로 보존.
func (d *artistDetail) byLocale() AliasByLocale {
	out := AliasByLocale{}
	for _, a := range d.Aliases {
		kdbKey := mbLocaleToKDB(a.Locale)
		if kdbKey == "" {
			continue
		}
		if !contains(out[kdbKey], a.Name) {
			out[kdbKey] = append(out[kdbKey], a.Name)
		}
	}
	// MusicBrainz 의 primary name 도 보존 (보통 en).
	if d.Name != "" && !contains(out["en"], d.Name) {
		out["en"] = append(out["en"], d.Name)
	}
	return out
}

// matchesQuery — primary name 또는 alias(locale 무관, 전체) 중 하나라도 query 와
// 정규화 일치하면 true. wikidata.entityMatchesQuery 와 동일한 오매칭 가드.
func (d *artistDetail) matchesQuery(want string) bool {
	if want == "" {
		return false
	}
	if normalizeName(d.Name) == want {
		return true
	}
	for _, a := range d.Aliases {
		if normalizeName(a.Name) == want {
			return true
		}
	}
	return false
}

// FetchAliases — inc=aliases. (검증 없는 raw fetch — 가능하면 FindAliases 사용.)
func (c *Client) FetchAliases(ctx context.Context, mbid string) (AliasByLocale, error) {
	d, err := c.fetchDetail(ctx, mbid)
	if err != nil || d == nil {
		return nil, err
	}
	return d.byLocale(), nil
}

// FindAliases — name 검색 후, 반환된 artist 의 name/alias 가 query 와 정규화
// 일치하는(= 실제로 그 인물/그룹인) 첫 후보의 alias 만 반환한다. country:KR
// 검색이 fuzzy 매칭으로 엉뚱한 아티스트의 top hit 을 돌려줄 때 그 표기가
// canonical 로 흘러드는 오염(박보검-class)을 막는다. 검증 통과 후보가 없으면
// nil, nil.
func (c *Client) FindAliases(ctx context.Context, name string) (AliasByLocale, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	artists, err := c.Search(ctx, name)
	if err != nil {
		return nil, err
	}
	want := normalizeName(name)
	if want == "" {
		return nil, nil
	}
	const maxProbe = 3 // top hit 이 오매칭이면 몇 후보만 더 확인 (rate-limit 비용).
	for i, art := range artists {
		if i >= maxProbe {
			break
		}
		d, err := c.fetchDetail(ctx, art.ID)
		if err != nil || d == nil {
			continue
		}
		// 검색 결과의 name 자체도 후보 (fetch 가 누락한 표기 보완).
		if normalizeName(art.Name) == want || d.matchesQuery(want) {
			out := d.byLocale()
			if len(out) > 0 {
				return out, nil
			}
		}
	}
	return nil, nil
}

// normalizeName — 이름 비교용 정규화: 소문자 + 공백/중점/하이픈/마침표 제거.
// wikidata.normalizeName 와 동일 규칙 (표기차 흡수).
func normalizeName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '·', '・', '-', '.', '_', '\'', '"', ',':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NameMatches reports whether two artist names are the same after applying
// MusicBrainz's conservative spelling normalization. Search scores alone are
// not an identity proof: a song title can receive a high score against an
// artist with the same text. Candidate promotion callers must require this
// exact normalized-name check in addition to their entity/resource contract.
func NameMatches(a, b string) bool {
	na := normalizeName(a)
	return na != "" && na == normalizeName(b)
}

// --- helpers --------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	// rate limit — 동시성 안전 페이싱(1 req/s). 병렬 goroutine 이 c 를 공유해도
	// 호출이 minBetween 간격으로 직렬화된다.
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+path, nil)
	if err != nil {
		return nil, err
	}
	ua := c.UserAgent
	if ua == "" {
		ua = defaultUA
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")
	resp, err := httpx.Do(c.HTTPClient, req, 2)
	if err != nil {
		return nil, fmt.Errorf("mb http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("mb not found")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mb status %d", resp.StatusCode)
	}
	limited := io.LimitReader(resp.Body, 1<<20) // 1 MiB cap
	return io.ReadAll(limited)
}

// mbLocaleToKDB — MusicBrainz locale → KDB canonical key.
// 무관 locale (de/fr/it 등) 은 빈 문자열 → drop.
func mbLocaleToKDB(loc string) string {
	switch strings.ToLower(loc) {
	case "ko":
		return "ko"
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "vi":
		return "vi"
	case "zh":
		return "zh"
	case "zh-hant", "zh_hant":
		return "zh_hant"
	case "es":
		return "es"
	case "id":
		return "id"
	case "pt-br", "pt_br":
		return "pt_br"
	}
	return ""
}

func contains(arr []string, s string) bool {
	for _, x := range arr {
		if x == s {
			return true
		}
	}
	return false
}
