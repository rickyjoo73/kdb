// Package googlenews — Google News RSS 검색 클라이언트.
//
// ★왜 만들었나(2026-08-16). 근거 수집의 1차 소스가 네이버 news API 였고, 그 **1,000콜/일**
// 한도가 시스템 전체 처리 속도의 천장이었다 — `evidenced` 무근거 재조사 3,365건에 34일
// (배치를 1,000 으로 올려도 다른 레인이 쿼터를 먼저 쓰므로 실효 ~600/일).
//
// 오너 질문 "굳이 네이버 API 가 필요한가?" 에 실측으로 답한 결과가 이 패키지다.
// Google News RSS 는 **키도 쿼터도 없고** 한국어 뉴스를 질의당 100건까지 준다. 네이버가
// 오탐한 것들이 여기선 정확히 나온다:
//
//	니쥬     → "니쥬, 日 첫 베스트 앨범 오리콘 1위 등극"(알파경제)
//	BTS 진   → "BTS 진, 전 세계 매료시킨 '환상의 왕자님'"(스포츠동아)
//	멜론티켓 → "'붕괴:스타레일' 몰입형 체험 전시 1차 예매 멜론티켓 오픈"(매경 게임진)
//	부산MBC  → "제81주년 광복절 부산 경축식 개최"(부산MBC)
//
// SearXNG 는 대체재가 아니다(같은 날 실측: `니쥬` 에 헝가리어 크롬 도움말). 이유는
// 품질이 아니라 **모양**이다 — 웹검색은 홈페이지를 주고 뉴스검색은 기사 제목을 준다.
// 적합성 게이트(filterRelevantHits)가 "엔티티명이 본문에 나오는가"를 보므로 기사 제목
// 형태여야 통과한다.
//
// 이 저장소는 Google News RSS 를 이미 **키 불필요 소스로 등록·검증**해 두고 있었다
// (apikeys.go 의 IssueURL, probe.go 의 `<item>` 프로브). 검색 클라이언트만 없었다.
package googlenews

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

// Item — 기사 1건.
type Item struct {
	Title  string // "제목 - 매체명" 에서 제목만 분리해 둔 값
	Link   string
	Source string // <source> 요소(매체명). 없으면 제목 꼬리에서 뽑는다.
}

// Client — Google News RSS 클라이언트. 키 없음.
type Client struct {
	HTTP    *http.Client
	Limiter *httpx.Limiter
}

// New — 기본 클라이언트. 예의상 최소 간격 700ms(공개 RSS 라 명시 한도는 없지만
// 저볼륨 원칙을 지킨다 — 이 저장소의 다른 무키 소스와 같은 취급).
func New() *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 15 * time.Second},
		Limiter: httpx.NewLimiter(700 * time.Millisecond),
	}
}

type rssFeed struct {
	Items []struct {
		Title  string `xml:"title"`
		Link   string `xml:"link"`
		Source string `xml:"source"`
	} `xml:"channel>item"`
}

// titleTailRe — Google News 제목은 "본문 제목 - 매체명" 꼴이다. 꼬리의 매체명을 분리한다.
var titleTailRe = regexp.MustCompile(`\s+-\s+([^-]+)$`)

// Search — 한국어 뉴스에서 query 를 검색한다. max 개까지.
//
// hl/gl/ceid 를 ko/KR 로 고정하는 이유: 같은 질의라도 로케일에 따라 전혀 다른 결과가
// 온다(`니쥬` 를 영어 로케일로 던지면 일본/영어 기사가 섞인다). K-엔티티 근거 수집이
// 목적이므로 한국어 판을 고정한다.
func (c *Client) Search(ctx context.Context, query string, max int) ([]Item, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, fmt.Errorf("googlenews: empty query")
	}
	if max <= 0 {
		max = 5
	}
	if c.Limiter != nil {
		if err := c.Limiter.Wait(ctx); err != nil {
			return nil, err
		}
	}
	u := "https://news.google.com/rss/search?q=" + url.QueryEscape(q) +
		"&hl=ko&gl=KR&ceid=KR:ko"
	return c.searchURL(ctx, u, max)
}

// searchURL — 주어진 URL 에서 RSS 를 읽어 파싱한다. Search 의 파싱부이고 테스트가 이걸
// 직접 부른다(질의 조립과 파싱을 나눠야 파서를 실제 피드로 검증할 수 있다).
func (c *Client) searchURL(ctx context.Context, u string, max int) ([]Item, error) {
	if max <= 0 {
		max = 5
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	// UA 를 붙인다 — 없으면 간헐적으로 빈 피드가 온다.
	req.Header.Set("User-Agent", "kdb-evidence/1.0 (+https://aiinad.com)")
	resp, err := httpx.Do(c.HTTP, req, 3)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("googlenews: status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	var feed rssFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("googlenews: parse: %w", err)
	}
	out := make([]Item, 0, max)
	for _, it := range feed.Items {
		title := strings.TrimSpace(it.Title)
		// ★링크 없는 항목은 버린다. 근거로 못 쓰기 때문이다 — recordEvidenceRefs 는
		// URL 이 빈 근거를 건너뛰므로, 이런 항목이 "근거 2건" 문턱만 넘겨주면 판정은
		// 통과하고 대장 적재는 0건이 되어 **강등**으로 이어진다(08-16 결함 계열:
		// 소스가 답한 것과 근거가 성립하는 것은 다르다).
		if title == "" || strings.TrimSpace(it.Link) == "" {
			continue
		}
		src := strings.TrimSpace(it.Source)
		if m := titleTailRe.FindStringSubmatch(title); m != nil {
			if src == "" {
				src = strings.TrimSpace(m[1])
			}
			title = strings.TrimSpace(title[:len(title)-len(m[0])])
		}
		out = append(out, Item{Title: title, Link: strings.TrimSpace(it.Link), Source: src})
		if len(out) >= max {
			break
		}
	}
	return out, nil
}
