// Package zhvariant — 중국어 간체↔번체 결정적 변환. Wikidata zh 라벨이 번체(邊佑錫)
// 만 줄 때, 간체 칸(canonical_zh)이 비던 문제를 해결한다. MediaWiki(zhwiki)의
// LanguageConverter 를 호출 — 규칙 기반 결정적 변환이라 환각이 없다(LLM 변환과 달리).
// 무인증·표준 라이브러리만. 같은 텍스트는 프로세스 메모리에 캐시한다.
//
// 실측(2026-06-13): 朴寶劍→朴宝剑, 張娜拉→张娜拉, 邊佑錫→边佑锡 — 통용 표기와 일치.
package zhvariant

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const endpoint = "https://zh.wikipedia.org/w/api.php"

var (
	cache   sync.Map // "<variant>\x00<text>" → 변환결과
	tagRE   = regexp.MustCompile(`<[^>]+>`)
	httpClt = &http.Client{Timeout: 12 * time.Second}
)

// ToSimplified — 간체(zh-cn)로. ToTraditional — 번체(zh-tw)로.
func ToSimplified(ctx context.Context, s string) string { return convert(ctx, s, "zh-cn") }
func ToTraditional(ctx context.Context, s string) string { return convert(ctx, s, "zh-tw") }

// HasHan — 한자를 포함하는가(변환 대상 판별).
func HasHan(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Han, r) {
			return true
		}
	}
	return false
}

// LooksTransliterated — 한자 표기이지만 **이름을 음차한 것**으로 보이는가.
//
// ★판별 근거는 가운뎃점·붙임표 하나다 (2026-09-23). 중국어는 외국 이름을 음차할 때
//
//	음절을 가운뎃점으로 끊는다(`基姆·申-洛克` = "Kim Sin-rock" 을 소리대로 옮긴 것).
//	한국 이름의 **한자**에는 그 구분자가 들어가지 않는다 — `金信祿` 이다.
//	그래서 구분자 유무는 «음차인가 한자 이름인가»를 규칙만으로 가르는 신호가 된다.
//	추측이 아니라 글자 판정이므로 환각이 없다.
//
// ★왜 필요한가. 위키데이터 `zh` 라벨에는 자동 생성된 음차가 섞여 있다. 김신록
//
//	(Q107109045)의 zh 라벨이 `基姆·申-洛克` 인데 같은 대상의 zh-Hant 라벨은
//	`金信祿` 이다. 우리는 zh 를 먼저 보고 그것을 변환 기준으로 삼았으므로, 음차가
//	간체 칸에 남고 번체 칸까지 음차로 덮일 수 있었다. 중국어 간체 기사에 사람
//	이름이 «基姆·申-洛克» 로 나가면 독자에게는 이름으로 읽히지 않는다
//	(글로벌 미디어파인 실측: 인물 181명 중 3명).
func LooksTransliterated(s string) bool {
	return strings.ContainsAny(s, "·•‧・-‐－")
}

// PreferNativeHan — 간체·번체 두 값 중 **변환 기준으로 삼을 것**을 고른다.
// 한쪽만 음차로 보이면 다른 쪽이 기준이다. 둘 다이거나 둘 다 아니면 종전대로
// 간체 칸을 우선한다(기존 동작 보존).
func PreferNativeHan(zh, zhHant string) string {
	zh, zhHant = strings.TrimSpace(zh), strings.TrimSpace(zhHant)
	switch {
	case zh == "":
		return zhHant
	case zhHant == "":
		return zh
	case LooksTransliterated(zh) && !LooksTransliterated(zhHant):
		return zhHant
	case LooksTransliterated(zhHant) && !LooksTransliterated(zh):
		return zh
	default:
		return zh
	}
}

// convert — text 를 variant(zh-cn/zh-tw)로 변환. 한자가 없으면 원문 그대로(no-op
// passthrough). **변환 실패(네트워크/타임아웃/파싱) 시 빈 문자열 ""를 반환**하고
// 캐시하지 않는다 — 호출측이 ""를 "변환 못함"으로 인지해 쓰기를 건너뛰게 한다
// (실패를 원문=틀린 변종으로 캐논에 기록하는 오염 방지). 성공 시 결과(간체==번체인
// 이름이면 입력과 같을 수 있음 — 정당한 값)를 캐시·반환.
func convert(ctx context.Context, text, variant string) string {
	text = strings.TrimSpace(text)
	if text == "" || !HasHan(text) {
		return text
	}
	key := variant + "\x00" + text
	if v, ok := cache.Load(key); ok {
		return v.(string)
	}
	out := fetch(ctx, text, variant)
	if out == "" {
		return "" // 실패 → 캐시 안 함, 호출측이 스킵.
	}
	cache.Store(key, out)
	return out
}

func fetch(ctx context.Context, text, variant string) string {
	q := url.Values{}
	q.Set("action", "parse")
	q.Set("text", text)
	q.Set("contentmodel", "wikitext")
	q.Set("prop", "text")
	q.Set("variant", variant)
	q.Set("disablelimitreport", "1")
	q.Set("format", "json")
	reqCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, endpoint+"?"+q.Encode(), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "KDB/1.0 (K-content entity DB)")
	resp, err := httpClt.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var r struct {
		Parse struct {
			Text struct {
				Star string `json:"*"`
			} `json:"text"`
		} `json:"parse"`
	}
	if json.Unmarshal(body, &r) != nil {
		return ""
	}
	// parse.text 는 HTML — 태그 제거 후 텍스트만.
	clean := strings.TrimSpace(tagRE.ReplaceAllString(r.Parse.Text.Star, ""))
	// 단일 표기(이름/제목)만 기대 — 줄바꿈/여분 공백 정리.
	clean = strings.TrimSpace(strings.ReplaceAll(clean, "\n", " "))
	if len(clean) > 200 { // 비정상(긴 HTML) → 버림
		return ""
	}
	return clean
}
