// Package appstore — 게임·앱의 **나라별 공식 제목**을 애플 앱스토어에서 가져온다.
//
// ★운영자 지시 (2026-09-15): "게임쪽은 playstore 또는 게임사이트에서 찾아야 될 듯한데."
//
// ★왜 이 출처인가. 게임 제목은 위키데이터에 거의 없다. 그런데 스토어에는 **퍼블리셔가
//   직접 등록한 나라별 공식 제목**이 있다 — 우리가 찾는 바로 그 값이고, 추측이 아니다.
//
//     블루 아카이브     kr 블루 아카이브 · jp ブルーアーカイブ · us Blue Archive · tw 蔚藍檔案
//     승리의 여신: 니케  jp 勝利の女神：NIKKE · us GODDESS OF VICTORY: NIKKE
//
// ★앵커를 먼저 고정한다. 이름 검색만 쓰면 엉뚱한 게임이 잡힌다 — 실측으로 `붉은사막`을
//   jp 에서 검색하니 무관한 앱(`黒猫`)이, `P의 거짓`은 수학 앱이 나왔다.
//   그래서 **한국 스토어에서 이름이 정확히 일치하는 것만** trackId(앵커)로 인정하고,
//   그 id 로 각국을 조회한다. 같은 id 는 같은 게임이다.
//
// ★그 나라에 없으면 **빈 값**이다. 그것은 정직한 "없음"이지 오매칭이 아니다
//   (붉은사막·마비노기 모바일은 해외 미출시라 빈칸이 맞다). 지어내지 않는다(D-37).
package appstore

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

const endpoint = "https://itunes.apple.com"

// Client — 저볼륨 조회자. 호출부가 pacing 을 책임진다(스토어 예의).
type Client struct{ HTTP *http.Client }

func New() *Client {
	return &Client{HTTP: &http.Client{Timeout: 20 * time.Second}}
}

// CountryFor — KDB locale → 앱스토어 국가 코드. 빈 값이면 그 locale 은 안 본다.
func CountryFor(loc string) string {
	switch loc {
	case "ko":
		return "kr"
	case "ja":
		return "jp"
	case "en":
		return "us"
	case "zh_hant", "zh-hant":
		return "tw"
	case "zh":
		return "cn"
	case "vi":
		return "vn"
	case "id":
		return "id"
	case "es":
		return "es"
	case "pt_br", "pt-br":
		return "br"
	}
	return ""
}

type result struct {
	TrackID    int64  `json:"trackId"`
	TrackName  string `json:"trackName"`
	SellerName string `json:"sellerName"`
	Kind       string `json:"kind"`
}

func (c *Client) get(ctx context.Context, path string, q url.Values) ([]result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+path+"?"+q.Encode(), nil)
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
		return nil, fmt.Errorf("appstore: status %d", resp.StatusCode)
	}
	var sr struct {
		Results []result `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&sr); err != nil {
		return nil, err
	}
	return sr.Results, nil
}

// normTitle — 제목 비교용 정규화. 공백·대소문자만 없앤다. 그 이상 지우면 다른 게임이 같아진다.
func normTitle(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(s)), ""))
}

// Anchor — 한국 스토어에서 **이름이 정확히 일치하는** 앱의 id 와 퍼블리셔.
// 일치가 없으면 (0,"",nil) — 첫 결과로 폴백하지 않는다. 그 폴백이 `P의 거짓`에
// 수학 앱을 물렸다.
func (c *Client) Anchor(ctx context.Context, koTitle string) (int64, string, error) {
	koTitle = strings.TrimSpace(koTitle)
	if koTitle == "" {
		return 0, "", fmt.Errorf("appstore: empty title")
	}
	q := url.Values{}
	q.Set("term", koTitle)
	q.Set("country", "kr")
	q.Set("media", "software")
	q.Set("limit", "10")
	rs, err := c.get(ctx, "/search", q)
	if err != nil {
		return 0, "", err
	}
	want := normTitle(koTitle)
	for _, r := range rs {
		if normTitle(r.TrackName) == want {
			return r.TrackID, r.SellerName, nil
		}
	}
	return 0, "", nil
}

// TitleIn — 그 앵커의 **해당 국가 공식 제목**. 그 나라에 없으면 빈 문자열이다.
func (c *Client) TitleIn(ctx context.Context, trackID int64, country string) (string, error) {
	if trackID == 0 || country == "" {
		return "", nil
	}
	q := url.Values{}
	q.Set("id", fmt.Sprintf("%d", trackID))
	q.Set("country", country)
	rs, err := c.get(ctx, "/lookup", q)
	if err != nil {
		return "", err
	}
	if len(rs) == 0 {
		return "", nil // 그 나라 미출시 — 정직한 없음
	}
	return strings.TrimSpace(rs[0].TrackName), nil
}

// URL — 앵커의 스토어 주소. 근거 URL 로 남긴다.
func URL(trackID int64) string {
	return fmt.Sprintf("https://apps.apple.com/kr/app/id%d", trackID)
}
