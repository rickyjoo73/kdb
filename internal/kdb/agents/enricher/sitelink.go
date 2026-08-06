package enricher

// sitelink.go — Wikidata sitelink 을 채움 값으로 바꾸는 규칙 변환 (2026-08-06).
//
// 배경: cascade 는 L3 에서 Wikidata 엔티티를 가져오면서 sitelinks(위키 코드 → 문서 URL)
// 도 같이 받아왔다. 그런데 그 값은 L4 프롬프트의 힌트로만 넘어가고 버려졌고, L4 는
// ground-strict 로 사실상 상시 꺼져 있다(검색 그라운딩 성공률 6.9%). 결과적으로 ja/zh
// 표기의 가장 확실한 출처를 조회해 놓고 쓰지 않았다.
//
// 라벨과 sitelink 는 다른 신호다. Wikidata 라벨이 비어 있어도 그 언어 위키에 문서가
// 있으면 표제어는 존재한다 — 실제로 CJK 빈칸의 상당수가 이 상태다.

import (
	"net/url"
	"strings"
)

// wikiToLocale — 위키 코드 → KDB 로케일 컬럼 코드.
//
// zhwiki 는 간체·번체가 문서마다 섞여 들어오므로 canonical_zh 로만 넣는다. zh_hant 는
// 바로 뒤 L3.5(fillZhVariants, OpenCC)가 결정적으로 파생하므로 여기서 추측하지 않는다.
// zh_yuewiki(광둥어)·zh_classicalwiki(문언문)는 표준 중국어 표기가 아니라 제외한다.
var wikiToLocale = map[string]string{
	"enwiki": "en",
	"jawiki": "ja",
	"zhwiki": "zh",
	"viwiki": "vi",
	"idwiki": "id",
	"eswiki": "es",
	"ptwiki": "pt_br",
}

// sitelinkSpellings — sitelink 맵을 (로케일 코드 → 표기) 로 바꾼다. 쓸 수 없는 항목은
// 조용히 버린다 — 여기서 애매한 걸 통과시키면 "빈칸 > 틀린값" 이 깨진다.
func sitelinkSpellings(sitelinks map[string]string) map[string][]string {
	out := map[string][]string{}
	for site, rawURL := range sitelinks {
		loc, ok := wikiToLocale[site]
		if !ok {
			continue
		}
		if title := wikiTitleFromURL(rawURL); title != "" {
			out[loc] = []string{title}
		}
	}
	return out
}

// wikiTitleFromURL — 위키 문서 URL 에서 표제어를 뽑는다.
//
//	https://ja.wikipedia.org/wiki/%E6%9D%8E%E7%9F%A5%E6%81%A9 → 李知恩
//
// EscapedPath 에서 마지막 경로를 떼고 디코드한다. u.Path 를 쓰면 이미 디코드된 상태라
// 제목 안의 '/'(AC%2FDC)와 경로 구분자를 구분할 수 없다.
//
// ★괄호 한정어가 붙은 표제어는 버린다. "李知恩 (歌手)" 의 " (歌手)" 는 위키의 동음이의
// 처리 장치이지 이름의 일부가 아니다. 그렇다고 떼어내는 규칙을 세우면 진짜 괄호를 가진
// 제목("Love (Inst.)")을 훼손한다. 둘을 구분할 방법이 없으므로 통째로 비운다 — 이 칸은
// 다른 소스가 채우면 된다.
func wikiTitleFromURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	esc := u.EscapedPath()
	i := strings.LastIndex(esc, "/")
	if i < 0 || i+1 >= len(esc) {
		return ""
	}
	title, err := url.PathUnescape(esc[i+1:])
	if err != nil {
		return ""
	}
	title = strings.TrimSpace(strings.ReplaceAll(title, "_", " "))
	if title == "" {
		return ""
	}
	if strings.ContainsAny(title, "(（") {
		return "" // 동음이의 한정어 — 이름의 일부인지 알 수 없다.
	}
	return title
}

// fieldsCoveredBy — want 중 m 이 실제로 값을 가진 칸만 남긴다.
//
// 왜 필요한가: applyLocaleMap 은 want 전체에 tried 를 찍는다. 소스가 값을 갖지도 않은
// 칸까지 "시도했다"로 기록하면, 원장이 "어떤 소스도 못 채움"이라는 거짓 판정을 남긴다
// (a022567 이 고친 것과 같은 결함 — 정책이 안 물어본 칸을 소진으로 세지 않는다).
func fieldsCoveredBy(want []string, m map[string][]string) []string {
	out := make([]string, 0, len(want))
	for _, col := range want {
		code, ok := localeToCode[col]
		if !ok {
			continue
		}
		if vals, has := m[code]; has && len(vals) > 0 {
			out = append(out, col)
		}
	}
	return out
}
