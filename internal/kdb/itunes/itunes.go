// Package itunes — Apple iTunes Search API 클라이언트(무키·공개). 국가별 스토어프론트의
// 공식 트랙/앨범 제목 + 아티스트명을 제공한다. song_album 의 현지표기 확인(confirm) 및
// ★아티스트 앵커(artistName) 확보용 — KDB song_album 엔티티엔 아티스트 필드가 없어 그동안
// MusicBrainz 등 앵커 기반 소스를 못 썼는데, iTunes 검색결과가 그 앵커를 공급한다.
//
// 안전: 검색은 제목 기반이라 동명곡 오매칭 위험이 있으므로, 소비자(drain)는 ① 반환 trackName
// 이 현재값과 정규화 일치(confirm)할 때만 권위 승급하고 ② 비ASCII(번역제목)는 아티스트
// corroborate 없이는 변경하지 않는다(빈칸>틀린값). 공식 API·저볼륨·throttle 준수.
package itunes

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

// Track — iTunes Search 결과 1건(필요 필드만).
type Track struct {
	TrackName      string `json:"trackName"`
	ArtistName     string `json:"artistName"`
	CollectionName string `json:"collectionName"`
	TrackID        int64  `json:"trackId"`
	ArtistID       int64  `json:"artistId"`
	Kind           string `json:"kind"`
	// ★View URL 은 **되짚을 수 있는 지시대상**이다(2026-08-16 추가). trackId 만 저장하면
	// "왜 이 트랙인지"를 사람이 확인할 수 없다 — 08-16 의 retrievable 신호가 요구하는 게
	// 정확히 이것이고, 앵커 레인은 이 URL 을 source_urls 에도 남긴다.
	TrackViewURL      string `json:"trackViewUrl"`
	CollectionViewURL string `json:"collectionViewUrl"`
}

// ViewURL — 되짚기용 공개 URL. 트랙 URL 이 없으면 앨범 URL 로 떨어진다.
func (t Track) ViewURL() string {
	if t.TrackViewURL != "" {
		return t.TrackViewURL
	}
	return t.CollectionViewURL
}

// Client — iTunes Search API 클라이언트.
type Client struct{ HTTP *http.Client }

// New — 기본 클라이언트(12s timeout).
func New() *Client { return &Client{HTTP: &http.Client{Timeout: 12 * time.Second}} }

const endpoint = "https://itunes.apple.com/search"

// CountryFor — KDB locale → iTunes 스토어프론트 국가코드. 빈값이면 미지원.
func CountryFor(loc string) string {
	switch loc {
	case "ja":
		return "jp"
	case "zh":
		return "cn"
	case "zh_hant":
		return "tw"
	case "ko":
		// ★KR 스토어(2026-08-16 추가). 종전엔 현지표기 확인용이라 해외 스토어만 필요했다.
		// 앵커 레인은 반대로 **한국 원곡**을 찾으므로 KR 이 주 스토어다.
		return "kr"
	}
	return ""
}

// Search — country 스토어에서 term(곡/앨범 제목) 검색. 음악 한정, 최대 limit.
// 저볼륨 원칙: 호출부가 throttle(권장 ~1req/3s) 책임. 비200/파싱실패는 error.
func (c *Client) Search(ctx context.Context, term, country string, limit int) ([]Track, error) {
	if strings.TrimSpace(term) == "" || country == "" {
		return nil, fmt.Errorf("itunes: empty term/country")
	}
	if limit <= 0 {
		limit = 5
	}
	q := url.Values{}
	q.Set("term", term)
	q.Set("country", country)
	q.Set("media", "music")
	q.Set("limit", fmt.Sprintf("%d", limit))
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
		return nil, fmt.Errorf("itunes: status %d", resp.StatusCode)
	}
	var sr struct {
		ResultCount int     `json:"resultCount"`
		Results     []Track `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&sr); err != nil {
		return nil, err
	}
	return sr.Results, nil
}
