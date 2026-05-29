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
)

const (
	apiBase    = "https://musicbrainz.org/ws/2"
	defaultUA  = "kdb-bootstrap/0.1 (https://kdb.aiinplanet.com)"
	minBetween = 1100 * time.Millisecond // 1 req/s 정중하게.
)

// Client — MusicBrainz HTTP client. 단일 인스턴스 권장 (마지막 호출 시각 기억).
type Client struct {
	HTTPClient *http.Client
	UserAgent  string
	lastCall   time.Time
}

func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		UserAgent:  defaultUA,
	}
}

// Artist — wbsearch 결과 1건.
type Artist struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Country       string `json:"country,omitempty"`
	Disambiguation string `json:"disambiguation,omitempty"`
	Score         int    `json:"score,omitempty"`
}

// Search — name + 한국 (country=KR) 우선 매칭. K-pop 외 매체 후보는 제외.
func (c *Client) Search(ctx context.Context, name string) ([]Artist, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("query", `artist:"`+name+`" AND country:KR`)
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

// FetchAliases — inc=aliases.
func (c *Client) FetchAliases(ctx context.Context, mbid string) (AliasByLocale, error) {
	if strings.TrimSpace(mbid) == "" {
		return nil, nil
	}
	body, err := c.get(ctx, "/artist/"+mbid+"?inc=aliases&fmt=json")
	if err != nil {
		return nil, err
	}
	var resp struct {
		Name    string `json:"name"`
		Aliases []struct {
			Name    string `json:"name"`
			Locale  string `json:"locale"`
			Type    string `json:"type"`
			Primary bool   `json:"primary"`
		} `json:"aliases"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("mb fetch decode: %w", err)
	}
	out := AliasByLocale{}
	for _, a := range resp.Aliases {
		kdbKey := mbLocaleToKDB(a.Locale)
		if kdbKey == "" {
			continue
		}
		if !contains(out[kdbKey], a.Name) {
			out[kdbKey] = append(out[kdbKey], a.Name)
		}
	}
	// MusicBrainz 의 primary name 도 보존 (보통 en).
	if resp.Name != "" && !contains(out["en"], resp.Name) {
		out["en"] = append(out["en"], resp.Name)
	}
	return out, nil
}

// --- helpers --------------------------------------------------------------

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	// rate limit (단순) — 마지막 호출 후 1.1s 미만이면 sleep.
	if !c.lastCall.IsZero() {
		elapsed := time.Since(c.lastCall)
		if elapsed < minBetween {
			select {
			case <-time.After(minBetween - elapsed):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}
	c.lastCall = time.Now()

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
	resp, err := c.HTTPClient.Do(req)
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
