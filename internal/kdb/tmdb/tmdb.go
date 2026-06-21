// Package tmdb — TMDb(themoviedb.org) enrich 클라이언트.
//
// 영화(movie) / 드라마·예능(tv) 의 한국어 제목으로 검색해, TMDb translations 에서
// KDB 9개 로케일 제목을 모은다. enrich orchestrator L2(권위 API) 에서 호출.
// 인증: API Read Access Token(v4) Bearer. 키 해석은 호출측(apikeys.Resolve).
package tmdb

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
		HTTP: &http.Client{Timeout: 12 * time.Second},
		Base: "https://api.themoviedb.org/3",
	}
}

type searchResult struct {
	ID            int    `json:"id"`
	Title         string `json:"title"`         // movie
	Name          string `json:"name"`          // tv
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	OrigLang      string `json:"original_language"`
}

type searchResp struct {
	Results []searchResult `json:"results"`
}

type translationsResp struct {
	Translations []struct {
		ISO639  string `json:"iso_639_1"`
		ISO3166 string `json:"iso_3166_1"`
		Data    struct {
			Title string `json:"title"` // movie
			Name  string `json:"name"`  // tv
		} `json:"data"`
	} `json:"translations"`
}

// altTitlesResp — /alternative_titles. movie 는 "titles", tv 는 "results" 키.
// 언어코드 없이 국가코드(iso_3166_1)만 있다 — 국가→locale 매핑(altLocale)으로 변환.
type altTitlesResp struct {
	Titles  []altTitle `json:"titles"`  // movie
	Results []altTitle `json:"results"` // tv
}
type altTitle struct {
	ISO3166 string `json:"iso_3166_1"`
	Title   string `json:"title"`
}

// Enrich — ko 로 TMDb 검색 → 검증된 매치의 translations → KDB 로케일별 제목 map.
// 반환: (로케일맵, 매치된 TMDb id, error). 신뢰 매치 없으면 빈 map + id=0(에러 없음).
//
// 검증(오매칭 방지): "아몬드"가 프랑스 영화 "아몬드 나무 사이"로 잘못 매칭되던 문제 →
// 한국어 제목이 정규화 일치하거나, 일치 없으면 원작 언어=ko 인 최상위만 채택. 그 외엔
// 매치 없음으로 처리(Wikidata/codex 가 담당).
func (c *Client) Enrich(ctx context.Context, token, ko, entityType string) (map[string][]string, int, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(ko) == "" {
		return nil, 0, nil
	}
	media := "movie"
	if entityType == "drama" || entityType == "show" {
		media = "tv"
	}

	// 1) 검색
	sq := url.Values{}
	sq.Set("query", ko)
	sq.Set("language", "ko-KR")
	var sr searchResp
	if err := c.get(ctx, token, "/search/"+media+"?"+sq.Encode(), &sr); err != nil {
		return nil, 0, err
	}
	id := pickMatch(sr.Results, ko)
	if id == 0 {
		return map[string][]string{}, 0, nil
	}

	// 2) translations
	var tr translationsResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/translations", media, id), &tr); err != nil {
		return nil, 0, err
	}
	// en 제목 먼저 확보 — 비영어 locale 의 "영어 복사" 판별 기준. TMDb 는 번역이
	// 없는 country 변종(예: es-ES)에 영어 제목을 그대로 채워두기도 하는데, 그게
	// 우리 es 칸에 들어가면 또 영어복사가 된다. 그래서: ① 영어와 다른 번역 변종
	// (es-MX 등)을 우선하고, ② 끝까지 영어와 같으면 그 locale 은 반환하지 않는다
	// (codex 개선 프롬프트 또는 빈칸이 처리 — 빈칸 > 영어복사).
	enTitle := ""
	for _, t := range tr.Translations {
		if t.ISO639 == "en" {
			s := strings.TrimSpace(t.Data.Title)
			if s == "" {
				s = strings.TrimSpace(t.Data.Name)
			}
			if s != "" {
				enTitle = s
				break
			}
		}
	}
	out := map[string][]string{}
	for _, t := range tr.Translations {
		loc := kdbLocale(t.ISO639, t.ISO3166)
		if loc == "" {
			continue
		}
		title := strings.TrimSpace(t.Data.Title)
		if title == "" {
			title = strings.TrimSpace(t.Data.Name)
		}
		if title == "" {
			continue
		}
		isEnCopy := loc != "en" && enTitle != "" && normTitle(title) == normTitle(enTitle)
		if cur, exists := out[loc]; exists {
			// 이미 값이 있고, 그게 영어복사인데 새 값이 진짜 번역이면 교체.
			if normTitle(cur[0]) == normTitle(enTitle) && !isEnCopy {
				out[loc] = []string{title}
			}
			continue
		}
		if isEnCopy {
			continue // 영어복사는 보류(뒤에 진짜 번역 변종이 오면 채택)
		}
		out[loc] = []string{title}
	}

	// 3) alternative_titles — 국가별 공식/대체 제목. translations 에 없는 현지문자
	//    변종을 보강한다(예: TW "魷魚遊戲" → zh_hant). translations(out)가 이미 채운
	//    locale 은 권위 우선이라 건드리지 않고, 빈 locale 만 채운다. 영어복사는 제외.
	var at altTitlesResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/alternative_titles", media, id), &at); err == nil {
		alts := at.Titles
		if len(alts) == 0 {
			alts = at.Results
		}
		for _, a := range alts {
			loc := altLocale(a.ISO3166)
			title := strings.TrimSpace(a.Title)
			if loc == "" || title == "" {
				continue
			}
			if _, exists := out[loc]; exists {
				continue // translations 우선
			}
			if loc != "en" && enTitle != "" && normTitle(title) == normTitle(enTitle) {
				continue // 영어복사 제외
			}
			out[loc] = []string{title}
		}
	}
	return out, id, nil
}

// altLocale — alternative_titles 의 국가코드(iso_3166_1) → KDB locale.
// translations 와 달리 언어코드가 없어 국가로 추정한다(현지문자 변종 보강용).
func altLocale(country string) string {
	switch country {
	case "JP":
		return "ja"
	case "VN":
		return "vi"
	case "ID":
		return "id"
	case "BR":
		return "pt_br"
	case "TW", "HK", "MO":
		return "zh_hant"
	case "CN", "SG":
		return "zh"
	case "ES", "MX", "AR", "CO", "CL", "PE":
		return "es"
	}
	return ""
}

// pickMatch — 오매칭 방지. 한국어 제목(또는 원제)이 정규화 일치하는 결과만 채택.
// 일치 없으면 0(매치 없음).
//
// 과거엔 "일치 없으면 OrigLang==ko 인 results[0]" fallback 이 있었으나, 같은 질의
// 부분문자열을 공유하는 다른 한국 작품/리메이크의 인기 1위가 엉뚱하게 채택돼
// canonical 을 오염시켰다(KOFIC 의 exact-only 정책과 불일치). normTitle 이 공백/
// 구두점 차이는 이미 흡수하므로, 정규화 일치가 없으면 진짜 다른 작품으로 보고 거른다.
func pickMatch(results []searchResult, ko string) int {
	nk := normTitle(ko)
	for _, r := range results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		orig := r.OriginalTitle
		if orig == "" {
			orig = r.OriginalName
		}
		if normTitle(title) == nk || normTitle(orig) == nk {
			return r.ID
		}
	}
	return 0
}

// normTitle — 공백/구두점 제거 + 소문자. "범죄도시 5" == "범죄도시5" 매칭용.
func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r == ' ' || r == '\t' || r == ':' || r == '-' || r == '·' || r == '.' || r == ',' || r == '!' || r == '?' || r == '\'' || r == '"':
			continue
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (c *Client) get(ctx context.Context, token, path string, dst any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("accept", "application/json")
	resp, err := httpx.Do(c.HTTP, req, 2)
	if err != nil {
		return fmt.Errorf("tmdb http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

// kdbLocale — TMDb (iso_639_1, iso_3166_1) → KDB 로케일 코드. 매핑 없으면 "".
func kdbLocale(lang, country string) string {
	switch lang {
	case "en":
		return "en"
	case "ja":
		return "ja"
	case "vi":
		return "vi"
	case "id":
		return "id"
	case "es":
		return "es"
	case "pt":
		if country == "BR" {
			return "pt_br"
		}
	case "zh":
		switch country {
		case "TW", "HK", "MO":
			return "zh_hant"
		case "CN", "SG":
			return "zh"
		}
	}
	return ""
}
