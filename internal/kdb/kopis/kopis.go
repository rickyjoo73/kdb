// Package kopis — 공연예술통합전산망(KOPIS) OpenAPI 클라이언트 (2026-07-23 Phase1).
//
// event_tour(콘서트·뮤지컬·연극·무용 등) candidate 의 공식 카탈로그 앵커.
// 키: kwave_kdb_api_settings 'KDB_KOPIS_API_KEY' (apikeys.Resolve, 오너 07-22 발급).
// 라이브 실측: 정체 candidate "쇼맨쉽 시즌2" → "박지현 콘서트: 쇼맨쉽 시즌2 …
// [서울 (앵콜)]" (장르 대중음악) 정확 반환 — 공연명이 장식(아티스트·도시·앵콜)을
// 두르고 오므로 매칭은 정규화 포함(containment) 게이트를 쓴다(드레인 쪽 책임).
package kopis

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

const (
	apiBase    = "http://www.kopis.or.kr/openApi/restful"
	minBetween = 1100 * time.Millisecond // 공공 API 예의(1 req/s)
)

// Event — 공연 목록(pblprfr) 결과 1건.
type Event struct {
	ID    string `xml:"mt20id"`
	Name  string `xml:"prfnm"`
	From  string `xml:"prfpdfrom"`
	To    string `xml:"prfpdto"`
	Venue string `xml:"fcltynm"`
	Genre string `xml:"genrenm"`
	State string `xml:"prfstate"`
}

type listResp struct {
	Events []Event `xml:"db"`
}

// Client — KOPIS HTTP 클라이언트(단일 인스턴스 권장, limiter 공유).
type Client struct {
	HTTPClient *http.Client
	limiter    *httpx.Limiter
}

func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 12 * time.Second},
		limiter:    httpx.NewLimiter(minBetween),
	}
}

// Search — 공연명 검색. 기간은 2024-01-01 ~ 오늘+400일(과거 공연도 이름 검색으로
// 매칭 — 소비자 기사 이벤트는 대부분 최근 2년 내). 최대 rows 건.
func (c *Client) Search(ctx context.Context, serviceKey, name string, rows int) ([]Event, error) {
	serviceKey = strings.TrimSpace(serviceKey)
	name = strings.TrimSpace(name)
	if serviceKey == "" || name == "" {
		return nil, fmt.Errorf("kopis: empty key/name")
	}
	if rows <= 0 || rows > 50 {
		rows = 20
	}
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
	now := time.Now()
	q := url.Values{}
	q.Set("service", serviceKey)
	q.Set("stdate", "20240101")
	q.Set("eddate", now.AddDate(0, 0, 400).Format("20060102"))
	q.Set("rows", fmt.Sprintf("%d", rows))
	q.Set("cpage", "1")
	q.Set("shprfnm", name)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/pblprfr?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "KDB-entity-db/1.0 (kdb.aiinplanet.com)")
	resp, err := httpx.Do(c.HTTPClient, req, 2)
	if err != nil {
		return nil, fmt.Errorf("kopis http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kopis status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	var lr listResp
	if err := xml.Unmarshal(body, &lr); err != nil {
		return nil, fmt.Errorf("kopis xml: %w", err)
	}
	return lr.Events, nil
}

// DetailURL — 운영자/소비자용 공연 상세 페이지.
func DetailURL(id string) string {
	return "https://www.kopis.or.kr/por/db/pblprfr/pblprfrView.do?mt20Id=" + url.QueryEscape(id)
}
