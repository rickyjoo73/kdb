// Package discogs — Discogs 음악 DB Search API 클라이언트(무토큰 search 허용·rate-limit).
// iTunes 와 동일 역할: song_album 의 현지표기 confirm(corroborate) + release/artist 앵커 확보.
//
// ★실측(2026-06-29): Discogs 제목은 대부분 라틴/로마자(K-곡은 일본판도 영어제목 유지) — ja/zh
// 번역 제목 수율은 ≈0(MusicBrainz 와 동일 한계). 따라서 confirm-only 폴백(iTunes 가 놓친 곡을
// 보강) + release-ID/artist external_ref 앵커 용도. 토큰(KDB_DISCOGS_TOKEN) 있으면 rate-limit
// 완화(60/min), 없으면 무인증(25/min). User-Agent 필수.
package discogs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Release — Discogs search 결과 1건(필요 필드만). Title 은 "Artist - ReleaseTitle" 형식.
type Release struct {
	ID      int64    `json:"id"`
	Title   string   `json:"title"`
	Country string   `json:"country"`
	Year    string   `json:"year"`
	Label   []string `json:"label"`
	Format  []string `json:"format"`
}

// Client — Discogs API 클라이언트.
type Client struct {
	HTTP  *http.Client
	Token string
}

// New — 기본 클라이언트(12s timeout). 토큰은 KDB_DISCOGS_TOKEN(선택).
func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 12 * time.Second}, Token: os.Getenv("KDB_DISCOGS_TOKEN")}
}

const endpoint = "https://api.discogs.com/database/search"

// Search — release 검색(term=곡/앨범 제목, 선택 country). 저볼륨 원칙: 호출부가 throttle 책임.
func (c *Client) Search(ctx context.Context, term, country string, limit int) ([]Release, error) {
	if strings.TrimSpace(term) == "" {
		return nil, fmt.Errorf("discogs: empty term")
	}
	if limit <= 0 {
		limit = 5
	}
	q := url.Values{}
	q.Set("q", term)
	q.Set("type", "release")
	q.Set("per_page", fmt.Sprintf("%d", limit))
	if country != "" {
		q.Set("country", country)
	}
	if c.Token != "" {
		q.Set("token", c.Token)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "KDB-entity-db/1.0 (research; contact admin)")
	req.Header.Set("Accept", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("discogs: status %d", resp.StatusCode)
	}
	var sr struct {
		Results []Release `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&sr); err != nil {
		return nil, err
	}
	return sr.Results, nil
}

// SplitTitle — Discogs "Artist - ReleaseTitle" 를 (artist, title) 로 분리. 구분자 부재면 ("", 전체).
func SplitTitle(s string) (artist, title string) {
	if i := strings.Index(s, " - "); i > 0 {
		return strings.TrimSpace(s[:i]), strings.TrimSpace(s[i+3:])
	}
	return "", strings.TrimSpace(s)
}
