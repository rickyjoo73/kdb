package kdb

// context_spelling — 소비자가 함께 보낸 기사 문맥에서 **괄호 표기**를 뽑는다.
//
// ★왜 (2026-09-18 실측). prepare 요청 6,425건 중 6,370건(99%)이 근거 URL 과 문맥을
// 함께 보낸다. 그런데 문맥은 게이트 판정(pass/review/reject)에만 쓰이고 표기 추출에는
// 쓰이지 않았다. 답이 요청 안에 들어 있는데 버리고 위키데이터에만 물은 뒤 no_match 로
// 끝냈다:
//
//	스프링 레인  "'톡-토크'(Tock-Talk), '스프링 레인'(Spring Rain) 등 일본 오리지널 신곡"
//	카케라       "타이틀곡 '카케라 -운명의 조각-'(KAKERA -運命のピース-)"
//	서머 매드니스 "'서머 매드니스 2026: 코어'(SUMMER MADNESS 2026: CORE)"
//
// 용어 바로 뒤 괄호만 규칙으로 세도 255건이고, 그중 196건이 no_match 로 죽었다.
//
// ★LLM 을 쓰지 않는다. 한국 기사에서 «한글표기(외국어표기)» 는 굳어진 관행이라
// 결정적으로 뽑을 수 있다. 환각 여지가 없고 비용도 0이다. 문맥을 통째로 LLM 에
// 넣는 것은 이 규칙이 못 잡는 것에만 쓴다(다음 단계).
//
// ★뽑기만 한다. 이 파일은 값을 쓰지 않는다 — 호출자가 소스 우선순위와 자체 검증을
// 거쳐 반영한다. «틀린값보다 빈칸» 은 여기서도 유효하다.

import (
	"regexp"
	"strings"
	"unicode"
)

// ContextSpelling — 문맥에서 뽑아낸 한 건.
type ContextSpelling struct {
	Locale string // "en" | "ja" | "zh" (문자 종류로 판정)
	Value  string
}

var (
	// 용어 뒤에 바로 붙는 괄호. 전각·반각 모두.
	ctxParenRE = regexp.MustCompile(`^[\s'"’”]*[（(]\s*([^）)]{1,60})\s*[）)]`)
	ctxLatinRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 .,'’&!?:;#*+\-–—/()]*$`)
	ctxKanaRE  = regexp.MustCompile(`[\p{Hiragana}\p{Katakana}]`)
	ctxHanRE   = regexp.MustCompile(`\p{Han}`)
	ctxHangRE  = regexp.MustCompile(`\p{Hangul}`)
)

// ExtractContextSpelling — 문맥에서 term 바로 뒤 괄호 표기를 뽑는다.
//
// 같은 term 이 문맥에 여러 번 나오면 **처음 성공한 것**을 쓴다. 아무것도 못 뽑으면
// 빈 슬라이스다 — 지어내지 않는다.
func ExtractContextSpelling(term, context string) []ContextSpelling {
	term = strings.TrimSpace(term)
	if term == "" || len(term) < 2 || strings.TrimSpace(context) == "" {
		return nil
	}
	var out []ContextSpelling
	for i := strings.Index(context, term); i >= 0; {
		tail := context[i+len(term):]
		if len(tail) > 90 {
			tail = tail[:90]
		}
		if m := ctxParenRE.FindStringSubmatch(tail); m != nil {
			if cs, ok := classifyContextValue(term, m[1]); ok {
				out = append(out, cs)
				break
			}
		}
		next := strings.Index(context[i+len(term):], term)
		if next < 0 {
			break
		}
		i = i + len(term) + next
	}
	return out
}

// classifyContextValue — 괄호 안의 문자열이 쓸 만한 외국어 표기인지 판정하고 로케일을 정한다.
func classifyContextValue(term, raw string) (ContextSpelling, bool) {
	v := strings.TrimSpace(strings.Trim(strings.TrimSpace(raw), "'\"’”‘“"))
	if v == "" || v == term {
		return ContextSpelling{}, false
	}
	if n := len([]rune(v)); n < 2 || n > 45 {
		return ContextSpelling{}, false
	}
	// 한글이 섞이면 표기가 아니라 설명이다 ("이하 '닥터X'", "연출 최보필").
	if ctxHangRE.MatchString(v) {
		return ContextSpelling{}, false
	}
	// 숫자만/기호만 — 연도·회차 같은 부가정보.
	hasLetter := false
	for _, r := range v {
		if unicode.IsLetter(r) {
			hasLetter = true
			break
		}
	}
	if !hasLetter {
		return ContextSpelling{}, false
	}
	switch {
	case ctxKanaRE.MatchString(v):
		return ContextSpelling{Locale: "ja", Value: v}, true
	case ctxHanRE.MatchString(v):
		// 한자만 — 중국어 표기일 수도, 일본어 한자 표기일 수도 있다. 구분이
		// 안 되므로 로케일을 단정하지 않는다. 호출자가 검증 대상으로 큐잉한다.
		return ContextSpelling{Locale: "han", Value: v}, true
	case ctxLatinRE.MatchString(v):
		return ContextSpelling{Locale: "en", Value: v}, true
	}
	return ContextSpelling{}, false
}
