package kdbapi

// absent_reason — **왜 값이 없고 그래서 무엇을 해야 하는가**를 한 곳에서 계산한다.
//
// ★한 곳에 두는 이유(2026-09-15). 이 계산은 `/v1/entities/match` 에만 있었다.
//   그런데 우리는 문서에서 "본문을 보내지 말고 lookup/bulk 를 쓰라"고 권한다 —
//   **권하는 문에 그 필드가 없었다.** 소비자가 "옵션을 켜도 빈 이유 필드가 없다"고
//   신고한 것이 정확한 관찰이었다.
//
//   같은 계열이 오늘만 네 번째다: verified_only · status · 이름별 type · 빈칸 이유.
//   전부 "단건/핫패스에는 있고 우리가 권하는 문에는 없던" 것이다.
//   그래서 계산을 함수 하나로 두고 세 문이 같은 것을 쓰게 한다 — 사본을 두면 또 갈라진다.

import (
	"strings"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// LocaleAbsence — 한 대상의 한 locale 에 대한 "없음"의 내용.
type LocaleAbsence struct {
	Reason   string `json:"locale_absent,omitempty"` // no_value · fallback_en · llm_only · unverified_source
	FillHint string `json:"fill_hint,omitempty"`     // transliterate · translate_title
}

// absenceFor — 값과 그 출처를 보고 없음의 이유를 정한다. 값이 있고 검증된 출처면 빈 구조체.
//
//	value       그 locale 의 현재 값("" 이면 빈칸)
//	source      그 값의 raw source
//	entityType  채우는 방법을 가른다(사람=음역 / 작품=번역)
//	fellBackEN  영문으로 대체해 돌려주는 중인가
func absenceFor(value, source, entityType string, fellBackEN bool) LocaleAbsence {
	var reason string
	switch {
	case strings.TrimSpace(value) == "":
		reason = "no_value"
	case fellBackEN:
		// ★영어 폴백도 "없음"이다. 값이 비어 있지 않아 처음엔 아무 안내도 안 붙였는데,
		//   소비자가 조치해야 하는 자리가 바로 여기다 — 그대로 쓰면 베트남어 기사에
		//   영어가 박힌다.
		reason = "fallback_en"
	case source == "codex-fallback":
		reason = "llm_only"
	case source != "" && !provenanceIsVerified(localeProvenanceLabel(Entity{}, source)):
		reason = "unverified_source"
	default:
		return LocaleAbsence{}
	}
	return LocaleAbsence{Reason: reason, FillHint: kdb.LocaleFillHint(entityType)}
}

// absentLocalesFor — 한 Entity 에서 요청 locale 들의 없음 이유를 모은다.
// lookup/bulk/prepare 가 쓴다. locale 키는 KDB 표기(en·ja·vi·zh·zh_hant·es·id·pt_br).
func absentLocalesFor(e Entity, locales []string) map[string]LocaleAbsence {
	if len(locales) == 0 {
		return nil
	}
	out := map[string]LocaleAbsence{}
	for _, loc := range locales {
		loc = normalizeLocale(loc)
		if loc == "" || loc == "ko" {
			continue
		}
		val := localeValueFor(e, loc)
		if a := absenceFor(val, localeSourceFor(e, loc), e.EntityType, false); a.Reason != "" {
			out[loc] = a
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// localeValueFor — Entity 의 locale 값. localeSourceFor 와 **같은 키 체계**를 쓴다.
func localeValueFor(e Entity, loc string) string {
	switch loc {
	case "en":
		return e.CanonicalEN
	case "ja":
		return e.CanonicalJA
	case "vi":
		return e.CanonicalVI
	case "zh":
		return e.CanonicalZH
	case "zh_hant":
		return e.CanonicalZHHant
	case "es":
		return e.CanonicalES
	case "id":
		return e.CanonicalID
	case "pt_br":
		return e.CanonicalPTBR
	}
	return ""
}
