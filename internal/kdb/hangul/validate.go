// Package hangul — 한글 정규성 검증 / 자소 분리 오타 검출.
//
// 운영자 정공법: 자소 단독 (ㄱ-ㅎ, ㅏ-ㅣ) 또는 완성형 외 깨진 글자 포함된
// ko 는 RSS extractor / OCR / typo 의 결과. autopilot 자동 처리 차단 + 운영자
// 검토 큐 또는 자동 typo merge (정상 ko 후보 있을 때).
package hangul

import (
	"strings"
	"unicode"
)

// 한글 완성형 음절: U+AC00 ~ U+D7A3
const (
	syllableStart = 0xAC00
	syllableEnd   = 0xD7A3
	jamoCompatStart = 0x3130 // ㄱ ㄲ ... 자소 호환 영역
	jamoCompatEnd   = 0x318F
	jamoInitialStart = 0x1100 // ᄀ ... 한글 초성 영역
	jamoFinalEnd     = 0x11FF
)

// IsSyllable — 완성형 한글 음절 (정상).
func IsSyllable(r rune) bool { return r >= syllableStart && r <= syllableEnd }

// IsLoneJamo — 자소 단독 (ㄱ-ㅎ, ㅏ-ㅣ). 정상 ko 에는 등장 X.
func IsLoneJamo(r rune) bool {
	if r >= jamoCompatStart && r <= jamoCompatEnd {
		return true
	}
	if r >= jamoInitialStart && r <= jamoFinalEnd {
		return true
	}
	return false
}

// IsCleanKorean — ko 문자열에 자소 단독 / 깨진 글자 없음.
// 허용: 완성형 한글 + 영문 + 숫자 + 공백 + `.,'-:!?()/&` 등 일반 punctuation + 콜론 등.
// 거부: 자소 단독 / 제어문자 / private use area.
func IsCleanKorean(s string) bool {
	for _, r := range s {
		if IsLoneJamo(r) {
			return false
		}
		if unicode.IsControl(r) {
			return false
		}
		if r >= 0xE000 && r <= 0xF8FF { // PUA
			return false
		}
	}
	return true
}

// StripLoneJamo — 자소 단독 글자 제거. typo 정정 후보 만들기.
//
// 예: "임ㅇ원희" → "임원희".
func StripLoneJamo(s string) string {
	var b strings.Builder
	for _, r := range s {
		if !IsLoneJamo(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// CountLoneJamo — 자소 단독 개수. typo 정도 측정용.
func CountLoneJamo(s string) int {
	n := 0
	for _, r := range s {
		if IsLoneJamo(r) {
			n++
		}
	}
	return n
}
