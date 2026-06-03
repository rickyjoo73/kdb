// Package wikidata — Wikidata wbsearchentities + wbgetentities client.
//
// Bootstrap 용. 신규 entity 등록 시 9 locale label + aliases + 현지 Wikipedia URL
// 을 한 번에 가져옴. Wikidata 값은 priority 5 (W) — 현지 매체 표기(L)나 권위
// API(O)가 도착하면 덮임.
package wikidata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	apiEndpoint = "https://www.wikidata.org/w/api.php"
	defaultUA   = "kdb-bootstrap/0.1 (https://kdb.aiinplanet.com)"
)

// Client — Wikidata API client. 인증 불필요, UA 만 권장.
type Client struct {
	HTTPClient *http.Client
	UserAgent  string
}

// New — 기본 timeout 10초.
func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		UserAgent:  defaultUA,
	}
}

// Candidate — wbsearchentities 결과 1건.
type Candidate struct {
	QID         string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description"`
	URL         string `json:"url"`
}

// Entity — wbgetentities 결과. KDB 컬럼명 (ko/en/ja/vi/zh/zh_hant/es/id/pt_br) 으로 매핑된 값.
type Entity struct {
	QID        string
	Labels     map[string]string   // ko/en/ja/vi/zh/zh_hant/es/id/pt_br → 값
	Aliases    map[string][]string // 같은 키
	Sitelinks  map[string]string   // wiki code (kowiki/enwiki/jawiki/…) → URL
	SiteTitles map[string]string   // wiki code → 문서 제목(=각 언어판 통용 표기, langlink)
}

// Search — 주어진 query 를 language 로 검색. K-Wave description filter 통과한
// 후보만 반환. filterKWave=false 면 raw 결과 그대로.
func (c *Client) Search(ctx context.Context, query, language string, limit int, filterKWave bool) ([]Candidate, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	q := url.Values{}
	q.Set("action", "wbsearchentities")
	q.Set("search", query)
	q.Set("language", language)
	q.Set("format", "json")
	q.Set("limit", fmt.Sprintf("%d", limit))

	body, err := c.get(ctx, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Search []Candidate `json:"search"`
		Error  *struct {
			Code string `json:"code"`
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("wbsearchentities decode: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("wbsearchentities: %s — %s", resp.Error.Code, resp.Error.Info)
	}
	if !filterKWave {
		return resp.Search, nil
	}
	out := make([]Candidate, 0, len(resp.Search))
	for _, c := range resp.Search {
		if IsKWaveDescription(c.Description) {
			out = append(out, c)
		}
	}
	return out, nil
}

// Fetch — Q-ID 로 9 locale labels + aliases + sitelinks 가져옴.
func (c *Client) Fetch(ctx context.Context, qid string) (*Entity, error) {
	qid = strings.TrimSpace(qid)
	if qid == "" {
		return nil, errors.New("empty qid")
	}
	q := url.Values{}
	q.Set("action", "wbgetentities")
	q.Set("ids", qid)
	q.Set("props", "labels|aliases|sitelinks/urls")
	q.Set("languages", strings.Join(wikidataLangs, "|"))
	q.Set("sitefilter", strings.Join(wikidataSiteFilter, "|"))
	q.Set("format", "json")

	body, err := c.get(ctx, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Language, Value string
			} `json:"labels"`
			Aliases map[string][]struct {
				Language, Value string
			} `json:"aliases"`
			Sitelinks map[string]struct {
				Site, Title string
				URL         string `json:"url"`
			} `json:"sitelinks"`
		} `json:"entities"`
		Error *struct {
			Code string `json:"code"`
			Info string `json:"info"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("wbgetentities decode: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("wbgetentities: %s — %s", resp.Error.Code, resp.Error.Info)
	}
	raw, ok := resp.Entities[qid]
	if !ok {
		return nil, fmt.Errorf("wbgetentities: no entity %s in response", qid)
	}

	e := &Entity{
		QID:        qid,
		Labels:     map[string]string{},
		Aliases:    map[string][]string{},
		Sitelinks:  map[string]string{},
		SiteTitles: map[string]string{},
	}
	// 고정 우선순위 순회 — raw.Labels 는 맵이라 순회 순서가 비결정적이었고,
	// pt/pt-br→pt_br, zh-tw/zh-hant→zh_hant 처럼 여러 lang 이 한 KDB 키로 접히는
	// 경우 어느 변종이 first-write-wins 로 채택되는지 run 마다 달라졌다.
	// wikidataLabelOrder 는 선호 변종(pt-br, zh-hant)을 앞에 둔다.
	for _, lang := range wikidataLabelOrder {
		lab, ok := raw.Labels[lang]
		if !ok {
			continue
		}
		kdbKey := wikidataLangToKDB(lang)
		if kdbKey == "" {
			continue
		}
		if _, exists := e.Labels[kdbKey]; !exists {
			e.Labels[kdbKey] = lab.Value
		}
	}
	for _, lang := range wikidataLabelOrder {
		alist, ok := raw.Aliases[lang]
		if !ok {
			continue
		}
		kdbKey := wikidataLangToKDB(lang)
		if kdbKey == "" {
			continue
		}
		for _, a := range alist {
			e.Aliases[kdbKey] = append(e.Aliases[kdbKey], a.Value)
		}
	}
	for site, sl := range raw.Sitelinks {
		e.Sitelinks[site] = sl.URL
		e.SiteTitles[site] = sl.Title
	}
	return e, nil
}

// LanglinkTitles — 각 언어판 위키피디아 문서 제목을 KDB locale → [표기] 로 변환.
// 위키데이터 라벨이 비어도 위키 문서만 있으면 현지 통용 표기를 확보한다(2026-06-01).
// disambiguation 괄호("(배우)" 등)는 제거. ko 는 제외(기준어).
func (e *Entity) LanglinkTitles() map[string][]string {
	out := map[string][]string{}
	for site, title := range e.SiteTitles {
		loc := sitelinkLocale(site)
		if loc == "" || loc == "ko" {
			continue
		}
		if t := cleanLanglinkTitle(title); t != "" {
			out[loc] = []string{t}
		}
	}
	return out
}

// sitelinkLocale — 위키 사이트 코드 → KDB locale 키. zhwiki 는 번체 경향이라 zh_hant.
func sitelinkLocale(site string) string {
	switch site {
	case "enwiki":
		return "en"
	case "jawiki":
		return "ja"
	case "viwiki":
		return "vi"
	case "eswiki":
		return "es"
	case "idwiki":
		return "id"
	case "ptwiki":
		return "pt_br"
	case "zhwiki":
		return "zh_hant"
	case "kowiki":
		return "ko"
	}
	return ""
}

// cleanLanglinkTitle — 문서 제목에서 disambiguation 괄호 이하를 제거.
// "이름 (배우)" / "이름（가수）" → "이름". 결과가 비면 원본 trim 유지.
func cleanLanglinkTitle(t string) string {
	t = strings.TrimSpace(t)
	for _, open := range []string{" (", " （", "（", "("} {
		if i := strings.Index(t, open); i > 0 {
			if c := strings.TrimSpace(t[:i]); c != "" {
				return c
			}
		}
	}
	return t
}

// SearchAndFetch — Search 결과 중 query 와 이름이 실제로 일치하는 후보의 Q-ID 로
// Fetch. 후보 없거나 일치 후보 없으면 nil, nil.
//
// ★오매칭 방지 (2026-06-01): 과거엔 cands[0] 를 무검증 채택했다. "박보검" 검색의
// 첫 hit 가 엉뚱한 인물(예: 허성진)이어도 그 entity 의 ja 라벨(ホ・ソンジン)을
// 박보검에 써버려 canonical_ja 가 오염됐다. 이제 후보의 label/aliases(ko·en)가
// query 와 정규화 일치하는 첫 후보만 채택하고, 일치가 없으면 채택을 거부한다.
// (KOFIC/TMDb list[0] 폴백 제거와 같은 정공법.)
func (c *Client) SearchAndFetch(ctx context.Context, query string) (*Entity, *Candidate, error) {
	// 1) ko 우선, 그래도 hit 없으면 en 으로 재시도.
	for _, lang := range []string{"ko", "en"} {
		cands, err := c.Search(ctx, query, lang, 5, true)
		if err != nil {
			return nil, nil, err
		}
		if len(cands) == 0 {
			continue
		}
		for i := range cands {
			cand := cands[i]
			ent, err := c.Fetch(ctx, cand.QID)
			if err != nil {
				return nil, &cand, err
			}
			if entityMatchesQuery(query, ent) {
				return ent, &cand, nil
			}
		}
		// 이 lang 의 후보들이 모두 이름 불일치 → 다음 lang 시도(없으면 채택 거부).
	}
	return nil, nil, nil
}

// entityMatchesQuery — fetch 한 entity 의 label/alias(전 locale) 중 하나라도 query
// 와 정규화 일치하면 true. 동명이인 후보 중 진짜를 고르고, 무관한 후보를 거른다.
func entityMatchesQuery(query string, ent *Entity) bool {
	if ent == nil {
		return false
	}
	want := normalizeName(query)
	if want == "" {
		return false
	}
	for _, v := range ent.Labels {
		if normalizeName(v) == want {
			return true
		}
	}
	for _, list := range ent.Aliases {
		for _, v := range list {
			if normalizeName(v) == want {
				return true
			}
		}
	}
	return false
}

// normalizeName — 이름 비교용 정규화: 소문자 + 공백/중점/하이픈/마침표 제거.
// "Park Bo-gum" / "park bo gum" / "パク・ボゴム" 등 표기차를 흡수한다.
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

func (c *Client) get(ctx context.Context, q url.Values) ([]byte, error) {
	u := apiEndpoint + "?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
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
		return nil, fmt.Errorf("wikidata http: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wikidata http status %d", resp.StatusCode)
	}
	// Cap body to 2 MiB; 읽기 에러는 삼키지 않고 surface 한다 — 옛 루프는 모든
	// 에러를 EOF 처럼 break 해 중간 네트워크 끊김의 '부분 응답'을 정상으로 오인했다.
	body, err := io.ReadAll(http.MaxBytesReader(nil, resp.Body, 2<<20))
	if err != nil {
		return nil, fmt.Errorf("wikidata read body: %w", err)
	}
	return body, nil
}

// wikidataLangs — wbgetentities 의 languages 파라미터 (KDB 가 사용하는 9 locale + 변종).
var wikidataLangs = []string{
	"ko", "en", "ja", "vi", "zh", "zh-tw", "zh-hant", "es", "id", "pt", "pt-br",
}

// wikidataSiteFilter — 가져올 sitelinks. KDB 9 locale 의 wiki 만.
var wikidataSiteFilter = []string{
	"kowiki", "enwiki", "jawiki", "viwiki", "zhwiki",
	"zh_yuewiki", "eswiki", "idwiki", "ptwiki",
}

// wikidataLabelOrder — 라벨/alias 를 KDB 키로 접을 때의 고정 순회 순서(결정성).
// 같은 KDB 키로 접히는 변종은 선호 변종을 앞에 둔다: zh-hant > zh-tw (zh_hant),
// pt-br > pt (pt_br). first-write-wins 가 항상 선호 변종을 채택하도록.
var wikidataLabelOrder = []string{
	"ko", "en", "ja", "vi", "zh", "zh-hant", "zh-tw", "es", "id", "pt-br", "pt",
}

// wikidataLangToKDB — wikidata language code → KDB canonical 컬럼 키.
// 첫 매칭 우선 (zh-tw 가 zh-hant 대표). 미지원 lang 은 빈 문자열.
func wikidataLangToKDB(lang string) string {
	switch lang {
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
	case "zh-tw", "zh-hant":
		return "zh_hant"
	case "es":
		return "es"
	case "id":
		return "id"
	case "pt-br":
		return "pt_br"
	case "pt":
		// pt 는 pt_br 비어있을 때만 fallback.
		return "pt_br"
	}
	return ""
}

// kwaveKeywords — K-Wave entity 판별용 description 키워드 (소문자 매칭).
var kwaveKeywords = []string{
	"south korean",
	"korean ",
	"k-pop",
	"k-drama",
	"k-content",
	"한국",
	"남한",
	"대한민국",
	"케이팝",
}

// IsKWaveDescription — wbsearchentities description 에 K-Wave 단서가 있는지.
// description 비어있으면 false (운영자가 따로 검토).
func IsKWaveDescription(desc string) bool {
	if desc == "" {
		return false
	}
	low := strings.ToLower(desc)
	for _, kw := range kwaveKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

// --- 동명이인 구분용 claims (P264/P463/P108/P569/P800), 2026-05-29 ---------

// claimSnak — claim mainsnak 의 datavalue 구조 (재사용).
type claimSnak struct {
	Mainsnak struct {
		DataValue struct {
			Value json.RawMessage `json:"value"`
			Type  string          `json:"type"`
		} `json:"datavalue"`
	} `json:"mainsnak"`
}

// PersonClaims — Wikidata 인물 claim 에서 추출한 동명이인 구분 신호.
type PersonClaims struct {
	Agency       string   // P264 record label / P463 member of / P108 employer (첫 라벨)
	BirthYear    int      // P569 date of birth
	NotableWorks []string // P800 notable work (라벨, 최대 5)
}

// LookupClaims — qid 의 claims 를 가져와 agency/birth_year/notable_works 추출.
// person enrich 전용. claim 없으면 zero value. 라벨 해석은 한 번의 batch 로 처리.
func (c *Client) LookupClaims(ctx context.Context, qid string) (*PersonClaims, error) {
	qid = strings.TrimSpace(qid)
	if qid == "" {
		return nil, errors.New("empty qid")
	}
	q := url.Values{}
	q.Set("action", "wbgetentities")
	q.Set("ids", qid)
	q.Set("props", "claims")
	q.Set("format", "json")
	body, err := c.get(ctx, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Entities map[string]struct {
			Claims map[string][]claimSnak `json:"claims"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("claims decode: %w", err)
	}
	ent, ok := resp.Entities[qid]
	if !ok {
		return nil, nil
	}
	out := &PersonClaims{}

	// P569 birth date — value is {"time":"+1990-01-01T00:00:00Z",...}
	if claims := ent.Claims["P569"]; len(claims) > 0 {
		var v struct {
			Time string `json:"time"`
		}
		if json.Unmarshal(claims[0].Mainsnak.DataValue.Value, &v) == nil {
			out.BirthYear = parseYear(v.Time)
		}
	}

	// agency: prefer P264 (record label), then P463 (member of), then P108 (employer).
	agencyQID := firstItemQID(ent.Claims["P264"])
	if agencyQID == "" {
		agencyQID = firstItemQID(ent.Claims["P463"])
	}
	if agencyQID == "" {
		agencyQID = firstItemQID(ent.Claims["P108"])
	}
	workQIDs := itemQIDs(ent.Claims["P800"], 5)

	resolveIDs := append([]string{}, workQIDs...)
	if agencyQID != "" {
		resolveIDs = append([]string{agencyQID}, resolveIDs...)
	}
	if len(resolveIDs) > 0 {
		labels, _ := c.fetchLabelsFor(ctx, resolveIDs)
		if agencyQID != "" {
			out.Agency = labels[agencyQID]
		}
		for _, wq := range workQIDs {
			if l := labels[wq]; l != "" {
				out.NotableWorks = append(out.NotableWorks, l)
			}
		}
	}
	return out, nil
}

// fetchLabelsFor — 여러 QID 의 en(없으면 ko) 라벨을 한 번에 가져온다.
func (c *Client) fetchLabelsFor(ctx context.Context, qids []string) (map[string]string, error) {
	q := url.Values{}
	q.Set("action", "wbgetentities")
	q.Set("ids", strings.Join(qids, "|"))
	q.Set("props", "labels")
	q.Set("languages", "en|ko")
	q.Set("format", "json")
	body, err := c.get(ctx, q)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Entities map[string]struct {
			Labels map[string]struct {
				Value string `json:"value"`
			} `json:"labels"`
		} `json:"entities"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for id, e := range resp.Entities {
		if l, ok := e.Labels["en"]; ok {
			out[id] = l.Value
		} else if l, ok := e.Labels["ko"]; ok {
			out[id] = l.Value
		}
	}
	return out, nil
}

// firstItemQID — claim 배열 첫 wikibase-item QID.
func firstItemQID(claims []claimSnak) string {
	if ids := itemQIDs(claims, 1); len(ids) > 0 {
		return ids[0]
	}
	return ""
}

// itemQIDs — claim 배열에서 wikibase-item QID 들을 최대 max 개 추출.
func itemQIDs(claims []claimSnak, max int) []string {
	out := []string{}
	for _, cl := range claims {
		if cl.Mainsnak.DataValue.Type != "wikibase-entityid" {
			continue
		}
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(cl.Mainsnak.DataValue.Value, &v) == nil && v.ID != "" {
			out = append(out, v.ID)
			if len(out) >= max {
				break
			}
		}
	}
	return out
}

// parseYear — "+1990-01-01T00:00:00Z" → 1990. 실패 시 0.
func parseYear(t string) int {
	t = strings.TrimPrefix(t, "+")
	if len(t) < 4 {
		return 0
	}
	y := 0
	for i := 0; i < 4; i++ {
		if t[i] < '0' || t[i] > '9' {
			return 0
		}
		y = y*10 + int(t[i]-'0')
	}
	if y < 1900 || y > 2100 {
		return 0
	}
	return y
}
