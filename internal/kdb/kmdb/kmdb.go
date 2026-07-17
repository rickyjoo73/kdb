// Package kmdb — KMDb(한국영상자료원 한국영화데이터베이스) Open API 클라이언트.
//
// 한국어 제목으로 검색해 영문 제목(titleEng)·별칭(titleEtc)·제작연도를 얻는다.
// 국내 영화 권위 소스 — movie 후보 승급 근거 + canonical_en 보강에 사용.
// ★쿼터: 일 100건(오너 발급 키, 등록 5659) — 호출측이 반드시 상한을 지킬 것.
package kmdb

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

type Client struct {
	HTTP *http.Client
	Base string
}

func New() *Client {
	return &Client{
		HTTP: &http.Client{Timeout: 15 * time.Second},
		Base: "http://api.koreafilm.or.kr/openapi-data2/wisenut/search_api/search_json2.jsp",
	}
}

// Movie — 검색 결과 1건 (드레인에 필요한 필드만).
type Movie struct {
	DocID    string
	Title    string   // 하이라이트 마커(!HS/!HE) 제거·정규화된 한국어 제목
	TitleEng string   // 영문 제목 (로마자 병기형 "(...)" 는 빈값 처리)
	TitleEtc []string // 별칭들(^ 구분)
	ProdYear string
}

type searchResp struct {
	Data []struct {
		Result []struct {
			DOCID    string `json:"DOCID"`
			Title    string `json:"title"`
			TitleEng string `json:"titleEng"`
			TitleEtc string `json:"titleEtc"`
			ProdYear string `json:"prodYear"`
		} `json:"Result"`
	} `json:"Data"`
}

// cleanTitle — KMDb 제목의 검색 하이라이트 마커(!HS/!HE)와 중복 공백 제거.
func cleanTitle(s string) string {
	s = strings.ReplaceAll(s, "!HS", " ")
	s = strings.ReplaceAll(s, "!HE", " ")
	return strings.Join(strings.Fields(s), " ")
}

// cleanTitleEng — 영문 제목 정제. "(Gisaengchungeul yebanghaja)" 처럼 괄호로만 감싼
// 값은 로마자 병기이지 영문 제목이 아님 → 빈값.
func cleanTitleEng(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		return ""
	}
	return s
}

// Search — 한국어 제목 검색 (호출 1건 = 쿼터 1건).
func (c *Client) Search(ctx context.Context, key, ko string) ([]Movie, error) {
	if strings.TrimSpace(key) == "" || strings.TrimSpace(ko) == "" {
		return nil, nil
	}
	q := url.Values{}
	q.Set("collection", "kmdb_new2")
	q.Set("detail", "Y")
	q.Set("title", ko)
	q.Set("listCount", "10")
	q.Set("ServiceKey", key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := httpx.Do(c.HTTP, req, 2)
	if err != nil {
		return nil, fmt.Errorf("kmdb http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kmdb status %d", resp.StatusCode)
	}
	var sr searchResp
	if err := json.NewDecoder(resp.Body).Decode(&sr); err != nil {
		return nil, fmt.Errorf("kmdb decode: %w", err)
	}
	var out []Movie
	for _, d := range sr.Data {
		for _, r := range d.Result {
			m := Movie{
				DocID:    strings.TrimSpace(r.DOCID),
				Title:    cleanTitle(r.Title),
				TitleEng: cleanTitleEng(r.TitleEng),
				ProdYear: strings.TrimSpace(r.ProdYear),
			}
			for _, alias := range strings.Split(r.TitleEtc, "^") {
				if a := cleanTitle(alias); a != "" {
					m.TitleEtc = append(m.TitleEtc, a)
				}
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// ExactMatch — 오매칭 방지: 한국어 제목(또는 별칭)이 정확히 일치하는 결과만.
// 서로 다른 영문 제목의 정확일치가 2건 이상이면 동명작 모호 → nil(채택 안 함).
func ExactMatch(results []Movie, ko string) *Movie {
	want := strings.Join(strings.Fields(ko), " ")
	var found *Movie
	for i := range results {
		m := &results[i]
		hit := m.Title == want
		if !hit {
			for _, a := range m.TitleEtc {
				if a == want {
					hit = true
					break
				}
			}
		}
		if !hit {
			continue
		}
		if found != nil && found.TitleEng != m.TitleEng && m.TitleEng != "" && found.TitleEng != "" {
			return nil // 동명작 상이 영문 — 모호, 채택 금지
		}
		if found == nil || (found.TitleEng == "" && m.TitleEng != "") {
			found = m
		}
	}
	return found
}
