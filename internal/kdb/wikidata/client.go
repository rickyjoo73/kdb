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
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

const (
	apiEndpoint = "https://www.wikidata.org/w/api.php"
	defaultUA   = "kdb-bootstrap/0.1 (https://kdb.aiinplanet.com)"
	// minBetween — 병렬 enrich 가 단일 Client 를 공유할 때 호출 페이싱(≈10 req/s).
	// Wikidata 엔 명시 req/s 제한은 없지만 동시 다발 호출 시 429 위험이 있어 버스트를
	// 평탄화한다. codex(수십 초)가 파이프라인을 지배하므로 처리량 영향은 미미.
	minBetween = 100 * time.Millisecond
)

// Client — Wikidata API client. 인증 불필요, UA 만 권장. limiter 가 동시성 안전
// 페이싱을 보장하므로 병렬 goroutine 이 공유해도 안전하다.
type Client struct {
	HTTPClient *http.Client
	UserAgent  string
	limiter    *httpx.Limiter
}

// New — 기본 timeout 10초.
func New() *Client {
	return &Client{
		HTTPClient: &http.Client{Timeout: 10 * time.Second},
		UserAgent:  defaultUA,
		limiter:    httpx.NewLimiter(minBetween),
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
	QID          string
	Labels       map[string]string   // ko/en/ja/vi/zh/zh_hant/es/id/pt_br → 값
	SourceLabels map[string]string   // 원천 locale→원문 라벨. 변종 접기/괄호 제거 없이 보존.
	Aliases      map[string][]string // 같은 키
	Sitelinks    map[string]string   // wiki code (kowiki/enwiki/jawiki/…) → URL
	SiteTitles   map[string]string   // wiki code → 문서 제목(=각 언어판 통용 표기, langlink)
	InstanceOf   []string            // P31(instance of) QID 목록 — 이름요소/동음이의 판별용
	// Occupations — P106(occupation) QID 목록. **원자료 그대로** 둔다(D-37).
	// 영역(연예·정치·스포츠…)으로 접는 것은 kdb 쪽 표가 한다 — 여기서 접으면
	// 그 표가 틀릴 때 되짚을 원자료가 안 남는다.
	Occupations []string
	// GenderQIDs — P21(sex or gender) QID 목록. 원자료 그대로 둔다(D-37).
	// 값 해석(남·여·그 밖)은 kdb 쪽 표가 한다.
	GenderQIDs []string
	// CountryQIDs — P17(country) + P495(country of origin) QID 목록. **원자료 그대로**(D-37).
	//
	// ★왜 여는가 (2026-09-16). 조직·기관·학교·구단이 새 유형으로 들어오면서 "이것이
	//   한국 것인가"를 물어야 하는데, 그때까지 그 물음에 답하는 것은 description 문자열
	//   뿐이었다(IsKWaveDescription). 문자열은 있으면 맞지만 **없다고 아닌 것이 아니다** —
	//   설명이 비었거나 한국어 설명뿐인 항목이 그대로 «근거 없음»이 된다.
	//   P17 은 그 물음에 직접 답한다. 사람에겐 P27(국적)이 따로 있어 person 은 이 값이
	//   비는 것이 정상이다 — 없다고 해외로 읽으면 안 된다.
	CountryQIDs []string
	// Descriptions — 언어별 항목 설명("South Korean singer" 등). 직업 판별의 1차 근거다.
	// ★2026-07-31 추가: 그전까지 description 은 Candidate(이름검색 결과)에만 있어서, QID 를
	// 이미 아는 상태에서 "이 항목이 무엇인가"를 물으려면 이름검색을 다시 돌아야 했다 —
	// 동명이인이 섞이는 경로다. audit-revert candidate 191건을 판정할 때 캐시된 description
	// 이 186건(97%) 비어 있어 직업 판별이 불가능했던 것도 같은 원인.
	Descriptions map[string]string // ko/en/ja/… → 설명
}

// nameElementClasses — "실존 엔티티"가 아니라 **이름 그 자체**를 가리키는 Wikidata 클래스.
// 한국어 인명 요소는 Q695xxxxx 대역에 대량 등재돼 있다("만원"=Korean male given name,
// "인형"=Korean given name, "경남"=Korean unisex given name). 이런 항목은 "그 이름을 쓸 수
// 있다"는 사전적 사실일 뿐 실존 인물·작품의 근거가 아니므로, 승급 앵커로 쓰면 일반명사가
// active 로 들어온다(2026-07-29 실측: 이 경로로 87건 오염).
var nameElementClasses = map[string]bool{
	"Q202444":    true, // given name
	"Q12308941":  true, // male given name
	"Q11879590":  true, // female given name
	"Q3409032":   true, // unisex given name
	"Q101352":    true, // family name
	"Q1243157":   true, // double name
	"Q4167410":   true, // Wikimedia disambiguation page
	"Q13406463":  true, // Wikimedia list article
	"Q4167836":   true, // Wikimedia category
	"Q17442446":  true, // Wikimedia internal item
	"Q15184295":  true, // Wikimedia module
	"Q11266439":  true, // Wikimedia template
	"Q66087861":  true, // Wikimedia name disambiguation page
	"Q22808320":  true, // Wikimedia human name disambiguation page
	"Q106589819": true, // Wikimedia surname disambiguation page
}

// IsNameElementClass — QID 하나가 "이름 그 자체" 클래스인지. Entity 없이도 판정해야
// 하는 곳(활성 원장 감사 P4.12)이 생겨서 연다. 목록은 nameElementClasses 하나뿐이다 —
// 사본을 두면 인입 가드와 감사가 서로 다른 것을 막게 된다.
func IsNameElementClass(qid string) bool { return nameElementClasses[qid] }

// NameElementClassCount — 목록 크기. 목록이 비거나 줄면 88건짜리 오염을 통째로
// 못 잡으므로 시험이 이 값을 지킨다.
func NameElementClassCount() int { return len(nameElementClasses) }

// IsNameElement — 이 항목이 실존 엔티티가 아니라 이름 요소/동음이의 문서인지.
// true 면 승급 앵커로 인정해선 안 된다(빈 InstanceOf 는 판단 불가라 false — 기존 동작 유지).
func (e *Entity) IsNameElement() (bool, string) {
	if e == nil {
		return false, ""
	}
	for _, q := range e.InstanceOf {
		if nameElementClasses[q] {
			return true, q
		}
	}
	return false, ""
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
	// claims 는 P31(instance of) 판별용 — 이름요소/동음이의 항목을 승급 앵커에서 제외한다.
	q.Set("props", "labels|aliases|descriptions|sitelinks/urls|claims")
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
			Descriptions map[string]struct {
				Language, Value string
			} `json:"descriptions"`
			Sitelinks map[string]struct {
				Site, Title string
				URL         string `json:"url"`
			} `json:"sitelinks"`
			// value 의 형태는 속성마다 다르다(entity-id 는 객체, IMDb ID 등은 문자열).
			// 전체 claims 를 강타입으로 받으면 문자열 value 에서 디코드가 깨지므로
			// RawMessage 로 받고 P31 만 entity-id 형태로 시도 파싱한다.
			Claims map[string][]struct {
				MainSnak struct {
					DataValue struct {
						Value json.RawMessage `json:"value"`
					} `json:"datavalue"`
				} `json:"mainsnak"`
			} `json:"claims"`
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
		QID:          qid,
		Labels:       map[string]string{},
		SourceLabels: map[string]string{},
		Aliases:      map[string][]string{},
		Sitelinks:    map[string]string{},
		SiteTitles:   map[string]string{},
		Descriptions: map[string]string{},
	}
	for lang, label := range raw.Labels {
		e.SourceLabels[lang] = label.Value
	}
	for _, lang := range wikidataLabelOrder {
		d, ok := raw.Descriptions[lang]
		if !ok {
			continue
		}
		kdbKey := wikidataLangToKDB(lang)
		if kdbKey == "" {
			continue
		}
		if _, exists := e.Descriptions[kdbKey]; !exists {
			e.Descriptions[kdbKey] = d.Value
		}
	}
	for _, cl := range raw.Claims["P31"] {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(cl.MainSnak.DataValue.Value, &v) == nil && v.ID != "" {
			e.InstanceOf = append(e.InstanceOf, v.ID)
		}
	}
	// P21(sex or gender) — 동명이인 가름과 현지 표기(경칭·호칭)에 쓴다.
	// 값이 여럿일 수 있어(드물다) **첫 것만** 쓰지 않고 다 담는다 — 고르는 것은 kdb 쪽 표다.
	for _, cl := range raw.Claims["P21"] {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(cl.MainSnak.DataValue.Value, &v) == nil && v.ID != "" {
			e.GenderQIDs = append(e.GenderQIDs, v.ID)
		}
	}
	// P106(occupation) — 같은 응답에 이미 들어 있다(props 에 claims 가 있다).
	// 정치인·운동선수·기업인도 서빙하기로 하면서(운영자 결정 2026-09-15) **무슨
	// 영역의 사람인가**가 필요해졌다. 유형(person)은 그대로 두고 직업을 따로 든다 —
	// 정치인도 가수도 존재론적으로 person 이고, 다른 것은 영역이지 종류가 아니다.
	for _, cl := range raw.Claims["P106"] {
		var v struct {
			ID string `json:"id"`
		}
		if json.Unmarshal(cl.MainSnak.DataValue.Value, &v) == nil && v.ID != "" {
			e.Occupations = append(e.Occupations, v.ID)
		}
	}
	// P17(country) · P495(country of origin) — 조직엔 P17, 창작물엔 P495 가 붙는다.
	// 둘을 한 자리에 담되 **순서는 P17 먼저** — 같은 값이면 더 강한 쪽이 앞에 온다.
	for _, prop := range []string{"P17", "P495"} {
		for _, cl := range raw.Claims[prop] {
			var v struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(cl.MainSnak.DataValue.Value, &v) == nil && v.ID != "" {
				e.CountryQIDs = append(e.CountryQIDs, v.ID)
			}
		}
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
			e.Labels[kdbKey] = StripDisambig(lab.Value)
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

// StripDisambig — 위키데이터 **레이블**에 섞여 들어온 동음이의 괄호를 뗀다.
//
// ★왜 필요한가(2026-08-07 실측). 문서 제목이 아니라 레이블 자체에 괄호가 들어있다:
//
//	Q6099788 아이비  ja "アイビー (歌手)"   zh "Ivy (韓國歌手)"
//	Q7413792 산이    ja "San E（ラッパー）"
//
// 봇이 위키백과 문서 제목을 레이블로 그대로 복사한 흔적이다. 이게 canonical_ja/zh 로
// 들어와 실측 80칸이 오염됐다(ja 8 · zh 38 · zh_hant 34, opencc/zh-variant 파생 포함).
// 오너 원칙 "빈칸 > 틀린값"에서 이건 틀린값이다 — 이름이 아니라 분류 딱지가 붙어 있다.
//
// ★괄호를 무조건 떼면 안 된다. 정상 표기에도 괄호가 쓰인다 — `티빙(TVING)` 은 맞는 값이고
// 이 저장소가 괄호 라틴 추출 규칙을 만들 때 확인한 사실이다.
//
// ★"괄호 안이 CJK 면 분류 딱지"도 틀린 규칙이다. 실데이터로 확인했다 — 그 조건으로 걸리는
// 것의 대부분은 분류가 아니라 **독음/한자 병기**였다:
//
//	4WARD(フォワード) · BINGO(ビンゴ) · Gnarly（ナーリー） · HAWWAH（夏渦）
//
// 이건 이름의 일부이고 떼면 정보가 사라진다. 그래서 분류어 **닫힌 목록**으로만 판정한다.
// 목록에 없는 괄호는 무조건 보존한다 — 모르면 손대지 않는 쪽이 안전하다.
//
// 문서 제목은 사정이 다르다 — 위키백과 제목의 괄호는 정의상 동음이의 표기라
// cleanLanglinkTitle 은 그대로 둔다.
func StripDisambig(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasSuffix(s, ")") && !strings.HasSuffix(s, "）") {
		return s
	}
	i := strings.LastIndexAny(s, "(（")
	if i <= 0 {
		return s // 여는 괄호가 없거나 맨 앞 — 이름 전체가 괄호면 손대지 않는다
	}
	inner := strings.Trim(strings.TrimSpace(s[i:]), "(（)）")
	if inner == "" || !isDisambigWord(inner) {
		return s
	}
	if head := strings.TrimSpace(s[:i]); head != "" {
		return head
	}
	return s
}

// disambigWords — 위키백과/위키데이터가 동음이의 구분에 쓰는 분류어. 괄호 안에 이 중
// 하나라도 있으면 이름이 아니라 딱지로 본다. 실측 오염값에서 뽑았고, 단독 '曲'/'団' 같은
// 짧은 조각은 일부러 뺐다 — 독음에 우연히 섞일 수 있어 오탐 위험이 크다.
var disambigWords = []string{
	// ja
	"歌手", "俳優", "女優", "声優", "映画", "ドラマ", "グループ", "バンド", "ラッパー",
	"アイドル", "タレント", "テレビ番組", "楽曲", "アルバム",
	// zh / zh-hant
	"組合", "组合", "團體", "团体", "樂隊", "乐队", "電視劇", "电视剧", "網路劇", "网络剧",
	"電影", "电影", "藝人", "艺人", "專輯", "专辑", "歌曲", "綜藝", "综艺",
	// ko (kowiki 제목이 섞여 들어오는 경우)
	"가수", "배우", "영화", "드라마", "그룹", "밴드", "아이돌",
}

func isDisambigWord(inner string) bool {
	for _, w := range disambigWords {
		if strings.Contains(inner, w) {
			return true
		}
	}
	return false
}

// cleanLanglinkTitle — 문서 제목에서 disambiguation 괄호 이하를 제거.
// "이름 (배우)" / "이름（가수）" → "이름". 결과가 비면 원본 trim 유지.
//
// ★괄호가 **이름의 일부**인 경우를 지킨다 (2026-09-15). 종전엔 여는 괄호를 만나면
//
//	무조건 잘라 `f(x)` 가 `f` 가 됐다. 위키 계열의 동음이의 괄호는 규칙이 있다 —
//	**맨 끝에 있고, 반각이면 앞에 빈칸이 있다.** 그 꼴일 때만 뗀다.
//	이 함수를 위키데이터 라벨 전체에 쓰기 시작하면서(권위값 업그레이드) 드러났다.
func cleanLanglinkTitle(t string) string {
	t = strings.TrimSpace(t)
	// 전각 괄호는 이름에 거의 안 쓰인다 — 끝에 있으면 뗀다("이름（가수）").
	if strings.HasSuffix(t, "）") {
		if i := strings.LastIndex(t, "（"); i > 0 {
			if c := strings.TrimSpace(t[:i]); c != "" {
				return c
			}
		}
	}
	// 반각은 **앞에 빈칸이 있을 때만.** `f(x)`·`Ne(o)mu` 같은 이름을 지킨다.
	if strings.HasSuffix(t, ")") {
		if i := strings.LastIndex(t, " ("); i > 0 {
			if c := strings.TrimSpace(t[:i]); c != "" {
				return c
			}
		}
	}
	return t
}

// CleanDisambiguator — 라벨/제목 끝의 구분자 괄호를 뗀다. "Going Seventeen (Programa de
// Variedades)" → "Going Seventeen". 위키 계열은 동명 구분을 괄호로 하는데, 그 괄호는
// **그 대상의 이름이 아니다** — 소비자 화면에 그대로 나가면 안 된다.
func CleanDisambiguator(t string) string { return cleanLanglinkTitle(t) }

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
			// ★이름요소 배제(2026-07-29): Wikidata 는 한국어 인명 요소를 대량 등재한다
			// ("만원"=Korean male given name, "인형"·"경남"도 동일). 이름이 일치하는 건
			// 당연하지만(그 이름 자체의 항목이므로) 실존 인물·작품의 근거가 아니다.
			// 승급 앵커·다국어 채움·정정 검증이 모두 이 함수를 타므로 여기서 끊는다
			// (실측: 이 경로로 일반명사 87건이 active 로 유입).
			if isName, cls := ent.IsNameElement(); isName {
				log.Printf("kdb.wikidata: 이름요소 후보 배제 query=%q qid=%s p31=%s", query, ent.QID, cls)
				continue
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

// EntityMatchesQuery — 이름 일치 판정의 외부 공개 래퍼.
//
// ★SearchAndFetch 는 filterKWave=true 가 내장이라 설명문에 한국 단서가 없는 조직
//
//	(대한축구협회·시흥교육지원청)을 아예 못 본다. 그래서 org 앵커 레인은 제 루프를
//	도는데, **이름 일치만은 같은 함수를 써야 한다** — 사본을 두면 한쪽이 느슨해진
//	순간 그쪽으로만 오매칭이 들어온다.
func EntityMatchesQuery(query string, ent *Entity) bool { return entityMatchesQuery(query, ent) }

// SouthKorea — P17/P495 가 한국을 가리키는 QID.
const SouthKorea = "Q884"

// IsSouthKorean — P17/P495 중 하나라도 한국인가. 값이 아예 없으면 false 이지만
// 그것은 "아니다"가 아니라 **"모른다"** 다 — 부르는 쪽이 그 차이를 다뤄야 한다(D-37).
func (e *Entity) IsSouthKorean() bool {
	if e == nil {
		return false
	}
	for _, q := range e.CountryQIDs {
		if q == SouthKorea {
			return true
		}
	}
	return false
}

// NormalizeName — 이름 비교용 정규화의 외부 공개 래퍼(enrich 의 ko-label 앵커 가드 등에서
// 동일 정규화를 재사용). 내부 normalizeName 과 동일.
func NormalizeName(s string) string { return normalizeName(s) }

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
	// 동시성 안전 페이싱 — 병렬 enrich 의 버스트를 평탄화(429 방지).
	if err := c.limiter.Wait(ctx); err != nil {
		return nil, err
	}
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
	resp, err := httpx.Do(c.HTTPClient, req, 2)
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
	"ko", "en", "ja", "vi", "zh", "zh-hans", "zh-tw", "zh-hant", "es", "id", "pt", "pt-br",
}

// wikidataSiteFilter — 가져올 sitelinks. KDB 9 locale 의 wiki 만.
var wikidataSiteFilter = []string{
	"kowiki", "enwiki", "jawiki", "viwiki", "zhwiki",
	"zh_yuewiki", "eswiki", "idwiki", "ptwiki",
}

// wikidataLabelOrder — 라벨/alias 를 KDB 키로 접을 때의 고정 순회 순서(결정성).
// 같은 KDB 키로 접히는 변종은 선호 변종을 앞에 둔다: zh-hant > zh-tw (zh_hant),
// pt-br > pt (pt_br). first-write-wins 가 항상 선호 변종을 채택하도록.
//
// ★zh-hans 는 여기 넣지 않는다 (2026-09-15). Labels 는 **옛 API 계약**이라 zh 키가 raw
//
//	`zh` 라벨을 들고 있어야 하고, 시험이 그것을 고정한다. 간체가 필요한 쪽은
//	SourceLabels["zh-hans"] 를 직접 본다 — 그쪽이 자체를 접지 않고 보존한다.
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
		// zh-hans 를 여기로 접지 않는다 — Labels["zh"] 는 raw zh 라는 옛 계약이고
		// 시험이 고정한다. 간체가 필요한 쪽은 SourceLabels["zh-hans"] 를 본다.
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
//
// ★"south korea" 를 뒤늦게 넣었다 (2026-09-16). 종전엔 형용사형 "south korean" 만
//
//	봤는데, **사람은 그렇게 쓰이지만 조직은 아니다**:
//
//	  사람   "South Korean singer"                    ← 통과했다
//	  기관   "government agency in South Korea"       ← 떨어졌다
//	  학교   "university in Seoul, South Korea"       ← 떨어졌다
//	  단체   "governing body of football in South Korea" ← 떨어졌다
//
//	형용사형은 명사형의 부분문자열이 아니라 그 반대다("south korean" 안에
//	"south korea" 가 들어 있다). 그래서 명사형을 넣으면 종전 통과분은 그대로
//	통과하고, 조직 계열만 새로 들어온다 — 좁히는 변경이 아니라 넓히는 변경이다.
//
//	새 유형 앵커 레인(org_anchor_drain)이 이 구멍 위에 서 있었다. 거기서 걸렸다.
var kwaveKeywords = []string{
	"south korea", // "south korean" 을 포함한다
	"republic of korea",
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

// BatchClaims — 여러 QID 의 item-값 속성을 **한 번의 호출로** 가져온다.
// 반환은 qid → 속성 → QID 목록. 값이 item 이 아닌 속성(날짜·문자열)은 담지 않는다.
//
// ★왜 필요한가 (2026-09-16). 활성 인물 5,407 중 직업 영역이 채워진 것이 78건뿐이었다.
//
//	칸(0142)도 판정표(occupation_domain.go)도 이미 있었는데, 그 값을 쓰는 곳이
//	**enrich 캐스케이드 한 군데뿐**이라 그 경로를 탄 것만 채워졌다.
//
//	뒤채움을 Fetch 로 돌면 앵커 보유 3,600여 건 × 350ms ≈ 21분이고, 9개 locale
//	라벨과 sitelink 까지 매번 받아 온다 — 필요한 건 P106/P21 두 줄인데.
//	wbgetentities 는 ids 를 50개까지 받는다. 그러면 73회면 끝난다.
//
// ids 는 50개씩 끊어 보낸다. 하나라도 모양이 틀리면 **그 묶음이 통째로** 빈 응답이
// 되므로(2026-09-15 에 'Q1ui' 하나로 40건이 조용히 안 돌아왔다) 모양을 먼저 거른다.
func (c *Client) BatchClaims(ctx context.Context, qids []string, props []string) (map[string]map[string][]string, error) {
	out := map[string]map[string][]string{}
	if len(qids) == 0 || len(props) == 0 {
		return out, nil
	}
	want := map[string]bool{}
	for _, p := range props {
		want[p] = true
	}
	clean := make([]string, 0, len(qids))
	for _, q := range qids {
		if qidShape.MatchString(strings.TrimSpace(q)) {
			clean = append(clean, strings.TrimSpace(q))
		}
	}
	const batch = 50
	for i := 0; i < len(clean); i += batch {
		end := i + batch
		if end > len(clean) {
			end = len(clean)
		}
		q := url.Values{}
		q.Set("action", "wbgetentities")
		q.Set("ids", strings.Join(clean[i:end], "|"))
		q.Set("props", "claims")
		q.Set("format", "json")
		body, err := c.get(ctx, q)
		if err != nil {
			return out, err // 부분 결과는 그대로 돌려준다 — 부른 쪽이 "못 했다"를 알아야 한다
		}
		var resp struct {
			Entities map[string]struct {
				Claims map[string][]claimSnak `json:"claims"`
			} `json:"entities"`
			Error *struct {
				Code string `json:"code"`
				Info string `json:"info"`
			} `json:"error"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return out, fmt.Errorf("wbgetentities(batch) decode: %w", err)
		}
		if resp.Error != nil {
			return out, fmt.Errorf("wbgetentities(batch): %s — %s", resp.Error.Code, resp.Error.Info)
		}
		for qid, ent := range resp.Entities {
			for prop, claims := range ent.Claims {
				if !want[prop] {
					continue
				}
				ids := itemQIDs(claims, 50)
				if len(ids) == 0 {
					continue
				}
				if out[qid] == nil {
					out[qid] = map[string][]string{}
				}
				out[qid][prop] = ids
			}
		}
	}
	return out, nil
}

// qidShape — Q + 숫자. 모양이 틀린 것 하나가 묶음 전체를 죽인다.
var qidShape = regexp.MustCompile(`^Q[1-9][0-9]*$`)

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

// SimplifiedZh — 이 항목의 **간체(zh-Hans)** 표기. 근거가 없으면 "" 를 돌려준다.
//
// ★왜 이 함수가 필요한가 (2026-09-17).
//
//	`Labels["zh"]` 는 위키데이터의 raw `zh` 레이블이고 **간체라는 보장이 없다.**
//	실제로 번체가 들어 있는 항목이 흔하다. 그걸 그대로 canonical_zh(간체 칸)에
//	쓰면 중국 본토 사용자에게 번체가 나간다 — 실측 87건이 그 상태였다:
//
//	  KBS        zh=韓國放送公社   (간체라면 韩国放送公社)
//	  국립국악원  zh=韓國國立國樂院
//	  김종국     zh=金鍾國
//
//	판단 규칙은 둘이다:
//	  ① `zh-hans` 레이블이 따로 있으면 **그것이 간체다.** Labels 는 변종을 접은
//	     옛 계약이라 zh 키에 raw zh 가 들어 있으므로 SourceLabels 를 직접 본다.
//	  ② `zh-hans` 가 없고 zh 와 zh_hant 가 **글자까지 같으면**, 그 출처는 두 자체를
//	     구분하지 않은 것이다. 간체의 근거가 못 되므로 "" 를 돌려준다. 비워 두면
//	     opencc 가 zh_hant 에서 결정적으로 변환해 채운다 — 그쪽이 진짜 간체다.
//
//	이 규칙은 enrich 오케스트레이터가 먼저 갖고 있었는데 **대량으로 채우는 로케일
//	드레인은 안 보고 있었다.** 두 곳이 같은 판단을 따로 들고 있으면 반드시 갈라진다
//	— 같은 날 아침에 SELECT/UPDATE 가 갈라져 en 496건이 방치된 것을 고쳤다.
//	그래서 규칙을 **여기 한 곳에** 둔다.
func (e *Entity) SimplifiedZh() string {
	if e == nil {
		return ""
	}
	if hans := strings.TrimSpace(e.SourceLabels["zh-hans"]); hans != "" {
		return hans
	}
	zh := strings.TrimSpace(e.Labels["zh"])
	if zh == "" {
		return ""
	}
	// 출처가 간체/번체를 구분하지 않았다 — 간체의 근거가 아니다.
	if zht := strings.TrimSpace(e.Labels["zh_hant"]); zht != "" && zh == zht {
		return ""
	}
	return zh
}
