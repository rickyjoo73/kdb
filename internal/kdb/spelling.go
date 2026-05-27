// Package kdb — spelling normalize (Agent NLP 권고).
//
// 운영자 정공법:
//   - 추출 단계: raw spelling 그대로 보존 (관측 사실).
//   - 합의 평가: normalized 기준 grouping (변종을 같은 entity 로 통합).
//   - canonical UPDATE: 다수결 raw spelling 사용 (현지 매체 표기 그대로).
//
// 정규화 룰 (Go 측, application 정합):
//  1. NFC 적용 (Unicode 정규화)
//  2. lower-case (라틴 알파벳만 영향, CJK 무관)
//  3. hyphen (-/-/⁃/‐ 등 모두 0x2D) → 공백
//  4. 일본어 중점 (・ U+30FB) → 공백
//  5. 다중 공백 → 단일 공백
//  6. trim
package kdb

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeSpelling — 합의 평가용 정규화. raw 형태 변경 안 함 (caller 가 별도 저장).
func NormalizeSpelling(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// NFC
	s = norm.NFC.String(s)
	// hyphen 변종 모두 공백 — 운영자 정공법 (hyphenless = SEO)
	s = strings.NewReplacer(
		"-", " ", // ASCII hyphen
		"‐", " ", // hyphen
		"‑", " ", // non-breaking hyphen
		"‒", " ", // figure dash
		"–", " ", // en dash
		"—", " ", // em dash
		"⁃", " ", // hyphen bullet
		"・", " ", // japanese middle dot
		"·", " ", // middle dot
	).Replace(s)
	// lower-case (라틴 알파벳만)
	s = strings.ToLower(s)
	// 다중 공백 → 단일
	var b strings.Builder
	b.Grow(len(s))
	prevSpace := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if !prevSpace {
				b.WriteRune(' ')
			}
			prevSpace = true
			continue
		}
		b.WriteRune(r)
		prevSpace = false
	}
	return strings.TrimSpace(b.String())
}
