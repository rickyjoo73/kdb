package kdbapi

// provenance_always — 값이 어디서 왔는지는 **묻지 않아도 말해 준다**.
//
// ★왜 (2026-09-15 실측).
//
//	활성 원장에서 실제로 서빙되는 영문 표기의 **33%(4,204칸)가 기계번역**이다.
//	일본어도 18.9%(2,218칸)가 그렇다.
//
//	  gtranslate 4,204 (33.0%) · wikidata-label 3,209 (25.2%) · tmdb 1,673 (13.1%)
//
//	우리 문서는 "추측으로 채워 보내지 않습니다 — 기계번역으로 채웠더니 수제천 →
//	手工制作的 처럼 40%가 틀렸습니다" 라고 약속한다. 그 약속은 `verified_only:true`
//	를 쓸 때만 지켜지는데, **그걸 쓰는 소비자는 presslocale 하나뿐이고 그마저
//	22%의 요청에서만** 쓴다. 나머지 소비자(미디어파인·issuetalk·kstory)는
//	기계번역을 검증된 값과 구분 없이 받아 그대로 발행하고 있었다.
//
//	값을 갑자기 비우면 지금 그 값으로 발행하는 쪽이 하루아침에 빈칸을 받는다.
//	그건 통보 없이 할 일이 아니다. 대신 **구분할 수 있게** 만든다 —
//	locale_provenance 를 요청 여부와 무관하게 항상 붙인다. 지금까지 소비자는
//	"이 영문이 구글 번역"이라는 사실을 **알 방법이 없었다**.
//
// ★값은 건드리지 않는다. 이 파일은 라벨만 붙인다.

import "strings"

// attachLocaleProvenance — 채워진 locale 값마다 출처 라벨을 붙인다.
// 이미 붙어 있으면(verified_only 게이트가 붙였다) 그대로 둔다.
func attachLocaleProvenance(e *Entity) {
	if e == nil || len(e.LocaleProvenance) > 0 {
		return
	}
	prov := map[string]string{}
	for _, f := range localeValueFields(e) {
		if strings.TrimSpace(*f.val) == "" {
			continue
		}
		prov[f.loc] = localeProvenanceLabel(*e, localeSourceFor(*e, f.loc))
	}
	if len(prov) > 0 {
		e.LocaleProvenance = prov
	}
}

type localeField struct {
	loc string
	val *string
}

// localeValueFields — 서빙되는 locale 칸 목록. applyLocaleVerifiedGate 와 **같은 자리**를
// 봐야 한다 — 한쪽에만 locale 이 추가되면 그 칸은 출처 없이 나간다.
func localeValueFields(e *Entity) []localeField {
	return []localeField{
		{"en", &e.CanonicalEN}, {"ja", &e.CanonicalJA}, {"vi", &e.CanonicalVI},
		{"zh", &e.CanonicalZH}, {"zh_hant", &e.CanonicalZHHant}, {"es", &e.CanonicalES},
		{"id", &e.CanonicalID}, {"pt_br", &e.CanonicalPTBR},
	}
}
