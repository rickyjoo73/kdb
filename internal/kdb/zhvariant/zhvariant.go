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
