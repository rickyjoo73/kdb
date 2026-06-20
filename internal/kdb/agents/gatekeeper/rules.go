// Package gatekeeper — the CandidateGatekeeper role agent
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.2, owner request #4).
//
// ~31% of status='candidate' rows are junk: full sentences, particle/verb
// fragments, multi-word phrases, over-length strings, lone jamo, and
// mixed-script non-name patterns. This agent decides, per candidate, USE
// (keep → eligible for promote) vs REJECT (junk / non-proper-noun) so that
// downstream classification / promotion only works on real entities.
//
// Two stages:
//
//  1. A CHEAP deterministic rule pre-gate (this file). It HARD-REJECTS obvious
//     junk and HARD-KEEPS obviously clean short names, with NO LLM call. It is
//     intentionally CONSERVATIVE: anything that could plausibly be a real name
//     falls through to the gray band rather than being rejected.
//  2. A gpt-5.5 gray-band decision (agent.go) for the ambiguous remainder, via
//     a tight role prompt + strict JSON schema. `uncertain` → quarantine for
//     operator review (never a silent drop).
package gatekeeper

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/rickyjoo73/kdb/internal/kdb/hangul"
)

// MaxNameRunes is the length ceiling for a plausible proper noun. The design
// measured only 7 candidates over 20 chars, all junk; we use 20 runes.
const MaxNameRunes = 20

// PreVerdict is the outcome of the deterministic pre-gate.
type PreVerdict int

const (
	// PreGray — ambiguous; needs the gpt-5.5 gray-band decision.
	PreGray PreVerdict = iota
	// PreReject — deterministic hard reject (obvious junk).
	PreReject
	// PreKeep — deterministic hard keep (obviously a clean short proper noun).
	PreKeep
)

// PreResult carries the pre-gate verdict plus a short machine reason and the
// heuristic flags that fired (passed to the gray-band prompt as context).
type PreResult struct {
	Verdict PreVerdict
	Reason  string
	Flags   []string
}

// josaTails are common Korean particle / verb-ending suffixes. A bare token
// ending in one of these is almost always a fragment or conjugated phrase,
// not a proper noun. Kept short and high-precision to avoid nuking real names
// (e.g. we do NOT include "다" alone, which ends many given names indirectly —
// only multi-char endings that are unambiguous grammar).
var josaTails = []string{
	"하게", "하다", "이다", "였다", "었다", "있다", "없다", "린다", "한다",
	"하는", "이라", "라고", "에서", "으로", "처럼", "보다", "까지", "부터",
	"께서", "에게", "한테", "이며", "거나", "지만", "는데", "ㅂ니다", "습니다",
	// 명령형/요청(챗 프롬프트 노이즈: "점심메뉴추천해줘", "날씨알려줘"). 실제 작품
	// 제목은 이런 어시스턴트 명령형으로 거의 끝나지 않는다(2글자+ 만 매칭해 "안아줘"
	// 류 1글자 줘는 제외).
	"해줘", "추천해줘", "알려줘", "검색해줘", "찾아줘", "보여줘", "정리해줘",
	"주세요", "해주세요", "하세요", "할래", "을까요", "ㄹ까요",
}

// PreGate runs the cheap deterministic rules over a single candidate term and
// returns whether it is a hard reject, a hard keep, or must go to the LLM.
//
// Conservative by design: real names pass through (PreGray or PreKeep);
// only unambiguous junk is PreReject.
// mashSeqs — 키보드 행 시퀀스/연속 숫자 부분문자열. 실제 고유명사엔 거의 안 나오나
// 난수/마구입력엔 흔하다. 소문자 비교.
var mashSeqs = []string{
	"qwer", "wert", "erty", "rtyu", "tyui", "yuio", "uiop",
	"asdf", "sdfg", "dfgh", "fghj", "ghjk", "hjkl",
	"zxcv", "xcvb", "cvbn", "vbnm",
	"poiu", "lkjh", "mnbv", "abcd",
	"1234", "2345", "3456", "4567", "5678", "6789", "7890", "0987",
}

// LooksLikeMash — 키보드 난수/마구입력인가. ① 키보드 행 시퀀스(qwer/asdf/zxcv) 또는
// 연속 숫자(1234) 포함, 또는 ② 모음 없는 긴 라틴 자음 덩어리(≥7) → 마구입력.
func LooksLikeMash(s string) bool {
	low := strings.ToLower(s)
	for _, seq := range mashSeqs {
		if strings.Contains(low, seq) {
			return true
		}
	}
	// 모음 없는 라틴 자음 연속 길이(BTS/NCT 같은 약어는 ≤5 라 무관, 7+ 만 마구입력).
	run := 0
	for _, r := range low {
		if r >= 'a' && r <= 'z' && !strings.ContainsRune("aeiouy", r) {
			run++
			if run >= 7 {
				return true
			}
		} else {
			run = 0
		}
	}
	return false
}

// fanHonorificSuffixes — 이름에 결합되는 팬 호칭. "고은언니"(고은+언니)처럼 이름+호칭이
// 한 토큰으로 결합되고 그 뒤에 또 다른 이름 토큰이 붙는 형태("고은언니 한고은")는 명백한
// 오염이다 — 실제 엔티티 이름은 이런 형태가 없다. (이름+분리호칭 "오드리 누나" 류는
// 오탐 위험이 있어 하드 차단 대신 운영자 검수 대시보드로 surfacing 한다.)
var fanHonorificSuffixes = []string{"언니", "오빠", "누나", "형아"}

func PreGate(term string) PreResult {
	t := strings.TrimSpace(term)
	flags := []string{}

	if t == "" {
		return PreResult{Verdict: PreReject, Reason: "empty", Flags: []string{"empty"}}
	}

	// Lone jamo (broken RSS/OCR) — never a real name.
	if hangul.CountLoneJamo(t) > 0 {
		return PreResult{Verdict: PreReject, Reason: "lone jamo (broken)", Flags: []string{"lone_jamo"}}
	}
	// 키보드 난수/마구입력(qwertyzxcv12345 류) — 수집 자체를 막는다. 정당한 제목
	// (EV6/2PM/NCT 127)은 짧고 구조가 있어 무관.
	if LooksLikeMash(t) {
		return PreResult{Verdict: PreReject, Reason: "keyboard/random mash", Flags: []string{"mash"}}
	}
	// Control chars / PUA — broken input.
	if !hangul.IsCleanKorean(t) {
		return PreResult{Verdict: PreReject, Reason: "control/PUA char", Flags: []string{"unclean"}}
	}

	runes := utf8.RuneCountInString(t)

	// Over-length — phrases/descriptions, not names.
	if runes > MaxNameRunes {
		return PreResult{Verdict: PreReject, Reason: "over length", Flags: []string{"len_gt_max"}}
	}

	// Sentence punctuation — a proper noun does not end a sentence.
	if strings.ContainsAny(t, ".!?…") {
		flags = append(flags, "sentence_punct")
	}

	// Internal whitespace → multi-word. A few real names contain a space
	// (romanized brands, "레이 아미"), so spacing alone is NOT a hard reject;
	// it is a strong gray-band signal UNLESS combined with other junk.
	hasSpace := strings.ContainsAny(t, " \t 　")
	wordCount := len(strings.Fields(t))
	if hasSpace {
		flags = append(flags, "has_space")
	}

	// 4+ whitespace-separated words is a phrase/sentence — hard reject.
	if wordCount >= 4 {
		return PreResult{Verdict: PreReject, Reason: "phrase (4+ words)", Flags: append(flags, "many_words")}
	}

	// 팬호칭 결합 오염: 첫 토큰이 이름+팬호칭(고은언니·X오빠·X누나)으로 끝나고 그 뒤에
	// 또 다른 토큰(보통 실제 이름)이 붙은 "고은언니 한고은" 류 — 실재 엔티티에 없는 형태.
	// 정상 제목(아는 형님/나의 아저씨)은 첫 토큰이 호칭으로 끝나지 않아 무관.
	if hasSpace && wordCount >= 2 {
		first := strings.Fields(t)[0]
		for _, h := range fanHonorificSuffixes {
			if strings.HasSuffix(first, h) && utf8.RuneCountInString(first) > utf8.RuneCountInString(h)+1 {
				return PreResult{Verdict: PreReject, Reason: "fan-honorific concat: " + first,
					Flags: append(flags, "fan_honorific")}
			}
		}
	}

	// Josa / verb-ending tail on a single token → grammatical fragment.
	if !hasSpace {
		for _, tail := range josaTails {
			if strings.HasSuffix(t, tail) && runes > utf8.RuneCountInString(tail) {
				return PreResult{Verdict: PreReject, Reason: "josa/verb tail: " + tail,
					Flags: append(flags, "josa_tail")}
			}
		}
	}

	// Mixed hangul + latin/digit. Some real brands mix (e.g. "BTS다") but most
	// mixed-script candidates are junk. Conservative: a SHORT, all-caps/latin
	// brand-like token mixed with hangul is gray; otherwise (digits present, or
	// long) reject.
	hasHangul := containsHangul(t)
	hasLatin := containsLatin(t)
	hasDigit := containsDigit(t)
	if hasHangul && (hasLatin || hasDigit) {
		flags = append(flags, "mixed_script")
		// digits embedded in a hangul string are almost always junk.
		if hasDigit && hasHangul && !looksLikeBrand(t) {
			return PreResult{Verdict: PreReject, Reason: "hangul+digit mix", Flags: append(flags, "hangul_digit")}
		}
	}

	// Everything that survived the hard-reject rules goes to the gpt-5.5 gray
	// band. We deliberately do NOT cheap-KEEP: a clean short Hangul token can
	// still be a common noun ("세계일주", "건강") that only the LLM can tell
	// apart from a real name ("아이유"). Cheap-reject is conservative
	// (don't nuke real names); the keep/reject call for clean tokens is the
	// model's job. PreKeep stays in the type for forward-compat / explicit
	// allowlisting but is not produced by the default rules.
	if len(flags) == 0 {
		flags = append(flags, "clean_token")
	}
	return PreResult{Verdict: PreGray, Reason: "ambiguous — needs LLM", Flags: flags}
}

func containsHangul(s string) bool {
	for _, r := range s {
		if hangul.IsSyllable(r) {
			return true
		}
	}
	return false
}

func containsLatin(s string) bool {
	for _, r := range s {
		if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
			return true
		}
	}
	return false
}

func containsDigit(s string) bool {
	for _, r := range s {
		if unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

// looksLikeBrand allows a small set of brand-like mixed patterns through the
// digit check (e.g. "U+", "2NE1", "f(x)", "GOT7" — short tokens combining
// latin + digit are common K-pop group names). Pure hangul + digit is NOT a
// brand.
func looksLikeBrand(s string) bool {
	if strings.ContainsAny(s, " ") {
		return false
	}
	if utf8.RuneCountInString(s) > 8 {
		return false
	}
	// Latin+digit (no hangul) → brand-like (2NE1, GOT7).
	return containsLatin(s) && !containsHangul(s)
}
