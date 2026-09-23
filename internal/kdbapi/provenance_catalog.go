package kdbapi

// provenance_catalog — 출처 등급의 **단일 목록**. 문서(/docs)가 이 목록에서 만들어진다.
//
// ★왜 생겼나 (2026-09-23). 소비자 세 곳이 같은 날 각자 신고했다: 문서 §6-3 이 라벨
//
//	7종을 열거하는데 실제 응답에는 더 온다. 글로벌 미디어파인이 수치를 냈다 —
//	인물 181명·로케일 1,440칸 중 **346칸(24%)이 문서에 없는 라벨**이었다
//	(romanization 231 · opencc 93 · machine-translation 14 · rule-transliteration 6 ·
//	llm-provisional 2). 문서대로 "검증 4종"만 신뢰하도록 게이팅한 소비자는 결정적
//	변환(opencc·rule-transliteration)까지 근거 없이 버리고, 반대로 기본을 신뢰로
//	두면 미검증 값을 발행한다. 어느 쪽으로 짜도 손해다.
//
// ★그래서 표를 손으로 두 벌 적지 않는다. 라벨을 늘리는 자리는 localeProvenanceLabel
//
//	하나이고, 그 결과가 여기 없으면 시험이 깨진다(provenance_catalog_test.go).
//	changelog 를 changelog.go 한 곳에서 만드는 것과 같은 규칙이다.

import (
	"html"
	"strings"
)

// ProvenanceGrade — 라벨 하나. Verified 는 `verified_only:true` 에서 살아남는가이고,
// 이 값은 verifiedProvenances 와 시험으로 묶여 있다(둘이 갈리면 문서가 거짓말을 한다).
type ProvenanceGrade struct {
	Label    string
	Verified bool
	Meaning  string
}

// provenanceCatalog — 신뢰 높은 순. 소비자는 보통 «어디까지 쓸 것인가»의 선을
// 이 순서 위에서 긋는다.
var provenanceCatalog = []ProvenanceGrade{
	{"operator-locked", true, "운영자가 못 박은 값. 자동 갱신이 덮지 않습니다"},
	{"wikidata-label", true, "Wikidata 해당 언어 라벨"},
	{"external-db", true, "권위 DB(TMDb·MusicBrainz·KOFIC·KMDb·네이버인물·iTunes·Discogs 등)"},
	{"media-consensus", true, "독립된 현지 매체 2곳 이상이 같은 표기를 썼습니다"},
	{"wikipedia-langlinks", false, "위키백과 언어판 연결(해당 언어판 문서 제목)"},
	{"community-db", false, "커뮤니티 DB(MyDramaList 등)"},
	{"media-single", false, "현지 매체 1곳의 관측 — 합의 전입니다"},
	{"romanization", false, "한글 → 로마자 규칙 변환(결정적). 공식 영문명이 없을 때 씁니다"},
	{"rule-transliteration", false, "규칙 기반 가나 변환(결정적). 기계번역이 아닙니다"},
	{"opencc", false, "번체 ↔ 간체 결정적 변환(OpenCC). 글자 변환이라 환각이 없습니다"},
	{"machine-translation", false, "기계번역(게이트 통과분)"},
	{"machine-translation-ungated", false, "기계번역인데 우리 게이트가 흠을 잡은 값. 가장 약한 기계값입니다"},
	{"llm-only", false, "LLM 합성 — 근거 검색 **전**의 추측입니다"},
	{"llm-provisional", false, "근거를 찾다 실패한 뒤 «빈칸보다는 낫다»로 채운 잠정값. 무엇에든 밀립니다"},
}

// provenanceSlot — docs 본문의 자리표.
const provenanceSlot = "<!--PROVENANCE-->"

// renderProvenanceHTML — 등급표. 「verified_only 에서 살아남는가」를 라벨마다 적는다 —
// 소비자가 게이팅을 문서만 보고 짤 수 있어야 한다.
func renderProvenanceHTML() string {
	var b strings.Builder
	b.WriteString(`<table>
<tr><th>provenance</th><th><code>verified_only:true</code></th><th>뜻</th></tr>
`)
	for _, g := range provenanceCatalog {
		cls, keep := "", "제외됨"
		if g.Verified {
			cls, keep = ` class="ok"`, "<b>남습니다</b>"
		}
		b.WriteString("<tr><td" + cls + "><code>" + html.EscapeString(g.Label) + "</code></td><td>" +
			keep + "</td><td>" + g.Meaning + "</td></tr>\n")
	}
	b.WriteString("</table>\n")
	return b.String()
}
