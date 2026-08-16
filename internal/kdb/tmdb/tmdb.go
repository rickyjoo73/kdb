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
	"unicode/utf8"

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
	out, err := c.EnrichByID(ctx, token, id, entityType)
	return out, id, err
}

// EnrichByID — TMDb id 를 이미 아는 엔티티의 로케일별 제목 map. Enrich 의 검색 단계를
// 건너뛴다.
//
// ★2026-07-31 분리: 로케일 채움 드레인용. external_refs 에 tmdb id 를 이미 보유한
// active 엔티티(실측 192건: show 84·drama 50·movie 58)가 ja/zh 빈칸으로 남아 있었는데,
// 이름 재검색 경로(Enrich)를 태우면 동명작에 오매칭될 위험을 새로 만든다 — 확정된 id 가
// 있으면 그걸 쓰는 게 맞다. 반환 규칙(영어복사 배제, translations 우선, alternative_titles
// 보강)은 Enrich 와 동일하다.
func (c *Client) EnrichByID(ctx context.Context, token string, id int, entityType string) (map[string][]string, error) {
	if strings.TrimSpace(token) == "" || id <= 0 {
		return map[string][]string{}, nil
	}
	media := "movie"
	if entityType == "drama" || entityType == "show" {
		media = "tv"
	}

	// 2) translations
	var tr translationsResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/translations", media, id), &tr); err != nil {
		return nil, err
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
	return out, nil
}

// detailResp — /{media}/{id} 기본 응답 중 en 재동기에 필요한 최소 필드.
type detailResp struct {
	Title         string `json:"title"` // movie
	Name          string `json:"name"`  // tv
	OriginalTitle string `json:"original_title"`
	OriginalName  string `json:"original_name"`
	OrigLang      string `json:"original_language"`
}

// OfficialEnglish — **en 재동기 전용**. TMDb 의 현재 공식 영문명과 원작 언어를 돌려준다.
// 반환: (영문명, 원작언어, error). 채택할 수 없으면 영문명이 빈 문자열이다.
//
// ★왜 이 함수가 따로 필요한가(2026-08-15): TMDb 는 방영 전 **직역 가제**를 달았다가
// 공개 시점에 공식명으로 갈아탄다 — `유부녀 킬러` 의 en-US name 이 2026-06-30 18:24:53
// UTC 에 `Married Woman Killer`→`A Bona Fide Killer` 로 바뀌고 21초 뒤 옛 값은 US
// alternative_title 로 강등됐다. 우리는 06-10 에 읽어 저장했고 그대로 굳었다. 즉
// **값의 출처가 권위여도 시점이 낡는다.** 다시 읽는 경로가 이것이다.
//
// ★두 엔드포인트가 일치할 때만 채택한다. 기본 응답의 name 은 en 번역이 없으면 원제로
// 폴백하므로(실측: `고딩형사`·`너와 함께라면` 이 한글 그대로 나왔다), translations 의
// en 항목과 대조하면 "진짜 영문 제목이 있다"와 "원제를 en 칸에 되비추고 있다"가 갈린다.
// 세션 중 같은 id(322571)가 The Criminal→犯罪者→Presumed Guilty 로 응답이 흔들린 것도
// 봤으므로, 단일 필드 하나만 믿지 않는다.
//
// origLang 은 호출측 가드용이다 — 오부착 앵커를 거르는 데 쓴다(08-15 전수 스윕에서
// `소울메이트`→Soulm8te[en], `헌트`→The Hunt[en], `범죄자`→犯罪者[ja] 를 이 조건이 잡았다).
func (c *Client) OfficialEnglish(ctx context.Context, token string, id int, entityType string) (string, string, error) {
	if strings.TrimSpace(token) == "" || id <= 0 {
		return "", "", nil
	}
	media := "movie"
	if entityType == "drama" || entityType == "show" {
		media = "tv"
	}

	var d detailResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d", media, id), &d); err != nil {
		return "", "", err
	}
	primary := strings.TrimSpace(d.Title)
	if primary == "" {
		primary = strings.TrimSpace(d.Name)
	}
	if primary == "" {
		return "", d.OrigLang, nil
	}

	var tr translationsResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/translations", media, id), &tr); err != nil {
		return "", d.OrigLang, err
	}
	enTitle := ""
	for _, t := range tr.Translations {
		if t.ISO639 != "en" {
			continue
		}
		s := strings.TrimSpace(t.Data.Title)
		if s == "" {
			s = strings.TrimSpace(t.Data.Name)
		}
		if s == "" {
			continue
		}
		if t.ISO3166 == "US" { // en-US 를 우선하되, 없으면 다른 en 변종도 받는다.
			enTitle = s
			break
		}
		if enTitle == "" {
			enTitle = s
		}
	}
	if enTitle == "" || normTitle(enTitle) != normTitle(primary) {
		return "", d.OrigLang, nil // 교차확인 실패 — 채택하지 않는다.
	}
	return primary, d.OrigLang, nil
}

// SearchExactID — 드레인 승급용 엄격 매칭. ko 정규화 제목이 검색결과와 정확히 일치하는
// 건이 딱 1건일 때만 그 TMDb id 를 반환한다. 2건+이면 동명작(예: 리메이크·동명 시리즈)
// 이라 ambiguous=true 로 승급 보류("틀린값보다 빈칸", homonym 오염 방어). media 는
// entityType 으로 결정(drama/show=tv, 그 외=movie). Enrich 의 pickMatch(첫 일치)와 달리
// 유일성을 요구하는 게 차이 — 승급은 정체 단언이라 더 엄격해야 한다.
//
// ★프랜차이즈 접힘(2026-08-07)도 여기서 걸러진다 — 승급은 정체 단언이므로 상위 시리즈
// 항목을 시즌 후보에 붙이면 안 된다. 이 경로는 별도 신호 없이 무매칭으로 접는다(승급
// 보류 = 현상유지라 손해가 없다). 세부 분포가 필요한 쪽은 앵커 경로다.
func (c *Client) SearchExactID(ctx context.Context, token, ko, entityType string) (id int, ambiguous bool, err error) {
	matched, _, err := c.searchExactMatches(ctx, token, ko, entityType)
	if err != nil {
		return 0, false, err
	}
	switch len(matched) {
	case 0:
		return 0, false, nil
	case 1:
		for k := range matched {
			return k, false, nil
		}
	}
	return 0, true, nil // 동명작 다수 — 보류
}

// SearchExactKoreanID — **앵커 부착 전용** 매칭. SearchExactID 의 정규화 정확일치 +
// 유일성 가드를 그대로 통과한 뒤, 그 유일 매치의 **원작 언어가 ko** 일 때만 채택한다.
// 즉 SearchExactID 보다 엄격하기만 하고 느슨해지는 경우가 없다(유일성 판정은 ko 필터
// **이전** 전체 매치 집합에서 하므로, 외국작이 섞여 2건이면 그대로 보류다).
//
// ★왜 조건을 하나 더 다는가(2026-08-07): 이 경로는 이미 active 인 엔티티에 ref 만 붙이고,
// 그 직후 tmdb-locale 레인이 7개 로케일을 자동으로 채운다 — 앵커 1건이 틀리면 8칸이
// 한꺼번에 오염된다(승급 드레인은 en 1칸만 채웠다). 검색은 language=ko-KR 이라 결과의
// title 이 **외국작의 한국어 개봉제목**일 수 있고, 그게 유일 일치면 종전 가드는 통과한다.
// 이 저장소가 실제로 밟은 오염이 그 형태다("아몬드"→프랑스 영화). 한국 작품 DB 이므로
// 원작 언어 ko 요구는 정상 대상을 거의 잃지 않는다.
//
// foreign=true 는 "정확·유일 일치는 있었으나 원작이 한국어가 아니다" — 무매칭과 구분해
// 호출측이 따로 집계·로그할 수 있게 한다(조용한 누락 금지).
//
// seasonOnly=true 는 "일치한 항목이 우리 시즌이 아니라 **상위 시리즈**였다"(프랜차이즈
// 접힘 가드). 이 역시 무매칭과 구분한다 — 이 판정이 쌓이는 제목은 TMDb 에 개별 항목이
// 없다는 뜻이라, 나중에 시즌 단위 출처를 따로 붙일지 판단하는 근거가 된다.
func (c *Client) SearchExactKoreanID(ctx context.Context, token, ko, entityType string) (id int, ambiguous, foreign, seasonOnly bool, err error) {
	matched, seasonDropped, err := c.searchExactMatches(ctx, token, ko, entityType)
	if err != nil {
		return 0, false, false, false, err
	}
	switch len(matched) {
	case 0:
		return 0, false, false, seasonDropped > 0, nil
	case 1:
		for k, lang := range matched {
			if lang != "ko" {
				return 0, false, true, false, nil
			}
			return k, false, false, false, nil
		}
	}
	return 0, true, false, false, nil // 동명작 다수 — 보류
}

// searchExactMatches — ko 정규화 제목과 정확히 일치하는 검색결과 전부를 id→원작언어로
// 돌려준다. SearchExactID / SearchExactKoreanID 의 공통 본체 — 두 곳이 각자 매칭 규칙을
// 들고 있으면 규칙이 갈라진다(이 저장소가 §1 에서 재시도 규칙으로 겪은 그 문제다).
//
// seasonDropped 는 "정확일치는 했으나 프랜차이즈 접힘 가드가 물린 건수"다. 무매칭과
// 구분해 돌려줘야 호출측이 "TMDb 에 없다"와 "상위 시리즈만 있다"를 갈라 셀 수 있다
// (조용한 누락 금지).
func (c *Client) searchExactMatches(ctx context.Context, token, ko, entityType string) (matched map[int]string, seasonDropped int, err error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(ko) == "" {
		return nil, 0, nil
	}
	media := "movie"
	if entityType == "drama" || entityType == "show" {
		media = "tv"
	}
	sq := url.Values{}
	sq.Set("query", ko)
	sq.Set("language", "ko-KR")
	var sr searchResp
	if err := c.get(ctx, token, "/search/"+media+"?"+sq.Encode(), &sr); err != nil {
		return nil, 0, err
	}
	nk := normTitle(ko)
	if nk == "" {
		return nil, 0, nil
	}
	matched = map[int]string{}
	for _, r := range sr.Results {
		title := r.Title
		if title == "" {
			title = r.Name
		}
		orig := r.OriginalTitle
		if orig == "" {
			orig = r.OriginalName
		}
		if normTitle(title) == nk || normTitle(orig) == nk {
			matched[r.ID] = r.OrigLang
		}
	}
	// 2026-07-23 Phase1 관용화: 주제목 무매칭이면 상위 후보의 alternative_titles 도
	// 대조한다(실측: "폭삭 속았수다"는 TMDb 주표기 "폭싹 속았수다"의 alt-title 로만
	// 일치 — 정체 20건 중 7건이 이 관용화로 회수). 유일성 요구는 불변(동명작 보류).
	if len(matched) == 0 {
		probe := 0
		for _, r := range sr.Results {
			if probe >= 3 {
				break
			}
			probe++
			alts, aerr := c.alternativeTitles(ctx, token, media, r.ID)
			if aerr != nil {
				continue
			}
			for _, t := range alts {
				if normTitle(t) != nk {
					continue
				}
				// ★프랜차이즈 접힘 가드(2026-08-07 실측). TMDb 는 시리즈의 시즌·속편을
				// **항목 하나로 접고** 각 시즌의 한국어 제목을 alternative_titles 에
				// 등록한다 — tv/71908 "크라임씬"의 alt 에 "크라임씬 리턴즈"가, tv/62411
				// "식샤를 합시다"의 alt 에 "식샤를 합시다 3: 비긴즈"가 실제로 들어 있다.
				// 그래서 위 관용화가 시즌 엔티티를 **프랜차이즈 항목**에 붙였고,
				// tmdb-locale 이 프랜차이즈 제목을 시즌 칸에 써넣었다(크라임씬 리턴즈에
				// zh `犯罪现场` — 정작 그 항목의 CN alt 에는 `犯罪现场归来`가 따로 있다).
				//
				// §4.5 의 별칭 함정과 같은 계열이다: **alt 표기는 "그 이름으로도 불린다"
				// 이지 "이 항목이 그 작품이다"가 아니다.** 회차 표지가 우리 쪽에만 있고
				// 상대 주제목에 없으면 그 항목은 우리 작품이 아니라 상위 시리즈다.
				if !seqMarkersCovered(nk, r.Name, r.OriginalName, r.Title, r.OriginalTitle) {
					seasonDropped++
					break
				}
				// ★부제 형제 가드(2026-08-16 실측). 위 가드는 회차 표지를 **숫자와 닫힌
				// 낱말**로만 본다. 그런데 접힌 시즌의 이름이 숫자도 `시즌`도 아닌 **부제**면
				// 그대로 통과한다 — 실제로 두 건이 통과했다:
				//   tv/135094 `더 리슨` 의 KR alt = {바람이 분다, 우리가 사랑한 목소리,
				//     너와 함께한 시간, **우리 함께 다시**, 오늘, 너에게 닿다} — 우리 쪽
				//     `더 리슨: 우리 함께 다시`는 출연진이 전혀 다른 **더 리슨4**인데
				//     2021년 시즌1 항목에 붙었다.
				//   tv/258925 `샤먼` 의 KR alt = {샤먼 : 귀신전(시즌1), 샤먼 : 미신전(시즌2)}
				//     — 우리 DB 의 두 엔티티가 **같은 id 를 나눠 갖게** 됐다.
				// 판정: 우리 제목이 `머리: 부제` 꼴이고, 그 항목의 표기 중 **같은 머리에
				// 다른 부제**를 단 것이 또 있으면 그 항목은 우리 작품이 아니라 이들을 묶은
				// 시리즈다. 부제가 없는 제목(`동물농장` `콩콩팥팥`)은 이 규칙 밖이다.
				if subtitleSiblingExists(t, append(alts, r.Name, r.OriginalName, r.Title, r.OriginalTitle)) {
					seasonDropped++
					break
				}
				matched[r.ID] = r.OrigLang
				break
			}
		}
	}
	return matched, seasonDropped, nil
}

// tmdbSeqWords — 회차·속편을 가리키는 **닫힌 목록**. 여는 목록(예: 괄호 안 아무 말)으로
// 잡으면 이름의 일부를 회차로 오인한다 — 0106 이 `4WARD(フォワード)` 류 144칸을 망칠
// 뻔한 것과 같은 함정이다. normTitle 이 소문자화하므로 항목도 소문자로 둔다.
//
// `파트`/`part` 는 일부러 뺐다. `파트너`(굿파트너·수상한 파트너·내 파트너는 악마)가
// 부분문자열로 걸린다. 실제 `듄: 파트 2` 류는 숫자 표지가 같이 있어 이미 잡힌다.
var tmdbSeqWords = []string{"리턴즈", "비긴즈", "시즌", "returns", "begins", "season"}

// seqMarkersCovered — ours(정규화 제목)의 회차 표지가 theirs 중 **어느 하나**에도
// 빠짐없이 나타나면 true. 표지가 없으면 true(대부분의 정상 매치가 여기 해당).
//
// 숫자는 **연속 숫자 토큰 단위**로 비교한다. 부분문자열로 보면 `1`이 `10`에 걸려
// `프로듀스 1`과 `프로듀스 101`이 같아진다. 반대로 `응답하라 1988`처럼 숫자가 이름의
// 일부인 경우는 상대 주제목에도 그 토큰이 그대로 있어 정상 통과한다.
func seqMarkersCovered(ours string, theirs ...string) bool {
	nums := digitRuns(ours)
	var words []string
	for _, w := range tmdbSeqWords {
		if strings.Contains(ours, w) {
			words = append(words, w)
		}
	}
	if len(nums) == 0 && len(words) == 0 {
		return true
	}
	for _, cand := range theirs {
		nc := normTitle(cand)
		if nc == "" {
			continue
		}
		if containsAllTokens(digitRuns(nc), nums) && containsAllWords(nc, words) {
			return true
		}
	}
	return false
}

// subtitleSiblingExists — ours 가 `머리: 부제` 꼴일 때, cands 중 **같은 머리 · 다른 부제**를
// 단 표기가 있으면 true. 있으면 그 항목은 개별 작품이 아니라 여러 부제를 묶은 시리즈다.
//
// 부제가 없으면 항상 false — 규칙을 적용할 근거가 없다. 머리만 있는 표기(`샤먼`)도 형제로
// 세지 않는다. 그건 시리즈 루트 이름일 뿐이고, TMDb 주제목이 짧은 이름인 정상 매치
// (`샤먼: 귀신전` ↔ 주제목 `샤먼`)를 전부 죽인다 — **다른 부제가 실제로 있을 때만** 막는다.
func subtitleSiblingExists(ours string, cands []string) bool {
	oh, ot, ok := splitSubtitle(ours)
	if !ok {
		return false
	}
	for _, c := range cands {
		ch, ct, ok := splitSubtitle(c)
		if !ok || ch != oh {
			continue
		}
		if ct != ot {
			return true
		}
	}
	return false
}

// splitSubtitle — 첫 구분자에서 `머리`/`부제`로 가른다. 둘 다 normTitle 로 정규화해
// 돌려준다(`샤먼 : 귀신전` 과 `샤먼: 귀신전` 이 같아야 한다). 구분자가 없거나 어느 쪽이
// 비면 ok=false.
//
// ★normTitle 을 **가른 뒤에** 적용한다. normTitle 이 `:` 를 지우므로 먼저 정규화하면
// 머리와 부제의 경계가 사라진다.
// 구분자는 콜론(반각·전각)만 쓰는 **닫힌 목록**이다. ` - ` 를 넣으면 제목 안의 하이픈
// (`달의 연인 - 보보경심 려`)이 부제 경계로 잘못 읽힌다 — 실측한 오염 두 건이 모두
// 콜론이라 그것만 막는다.
func splitSubtitle(s string) (head, tail string, ok bool) {
	for i, r := range s {
		if r != ':' && r != '：' {
			continue
		}
		head, tail = normTitle(s[:i]), normTitle(s[i+utf8.RuneLen(r):])
		if head == "" || tail == "" {
			return "", "", false
		}
		return head, tail, true
	}
	return "", "", false
}

// digitRuns — 연속 숫자 토큰 목록. "학교2013" → ["2013"], "감자별2013qr3" → ["2013","3"].
func digitRuns(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r >= '0' && r <= '9' {
			cur += string(r)
			continue
		}
		if cur != "" {
			out = append(out, cur)
			cur = ""
		}
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func containsAllTokens(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsAllWords(have string, want []string) bool {
	for _, w := range want {
		if !strings.Contains(have, w) {
			return false
		}
	}
	return true
}

// alternativeTitles — /{media}/{id}/alternative_titles 의 모든 표기(국가 무관).
func (c *Client) alternativeTitles(ctx context.Context, token, media string, id int) ([]string, error) {
	var ar struct {
		Titles []struct {
			Title string `json:"title"`
		} `json:"titles"`
		Results []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/alternative_titles", media, id), &ar); err != nil {
		return nil, err
	}
	var out []string
	for _, t := range ar.Titles {
		out = append(out, t.Title)
	}
	for _, t := range ar.Results {
		out = append(out, t.Title)
	}
	return out, nil
}

// AllTitles — 매칭된 작품의 locale 별 모든 공식제목(번역 + alt_titles, **영문복사 포함**).
// source 재귀속 전용: codex-fallback 값이 TMDb 공식제목과 일치하면 tmdb 로 승급(값 무변)
// 하기 위한 "확인용". 빈칸 채움에는 쓰지 말 것 — 영문복사 필링은 정책상 금지이고 Enrich 가
// (영문복사 제외) 담당한다. 반환=locale→[모든 공식제목 변종], tmdbID.
func (c *Client) AllTitles(ctx context.Context, token, ko, entityType string) (map[string][]string, int, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(ko) == "" {
		return nil, 0, nil
	}
	media := "movie"
	if entityType == "drama" || entityType == "show" {
		media = "tv"
	}
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
	out := map[string][]string{}
	add := func(loc, title string) {
		if loc == "" || title == "" {
			return
		}
		for _, e := range out[loc] {
			if normTitle(e) == normTitle(title) {
				return // 중복 변종 제거
			}
		}
		out[loc] = append(out[loc], title)
	}
	var tr translationsResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/translations", media, id), &tr); err == nil {
		for _, t := range tr.Translations {
			title := strings.TrimSpace(t.Data.Title)
			if title == "" {
				title = strings.TrimSpace(t.Data.Name)
			}
			add(kdbLocale(t.ISO639, t.ISO3166), title)
		}
	}
	var at altTitlesResp
	if err := c.get(ctx, token, fmt.Sprintf("/%s/%d/alternative_titles", media, id), &at); err == nil {
		alts := at.Titles
		if len(alts) == 0 {
			alts = at.Results
		}
		for _, a := range alts {
			add(altLocale(a.ISO3166), strings.TrimSpace(a.Title))
		}
	}
	return out, id, nil
}

// TitleMatches — 정규화 후 v 가 후보 제목 중 하나와 일치하는지(재귀속 확인용·외부 호출).
func TitleMatches(v string, titles []string) bool {
	nv := normTitle(v)
	if nv == "" {
		return false
	}
	for _, t := range titles {
		if normTitle(t) == nv {
			return true
		}
	}
	return false
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
