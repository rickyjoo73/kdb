package kdb

// locale_fill_hint — 그 언어 표기가 **없을 때 소비자가 무엇을 해야 하는가**.
//
// ★왜 필요한가 (2026-09-14).
//   빈칸을 받은 소비자가 한글을 그대로 발행했다(presslocale 일본어 32건 중 11건).
//   운영자: "상대방이 empty 로 왔을때, 어떻게든 액션을 해야겠지."
//
// ★왜 "직역하세요" 하나로 끝내면 안 되는가.
//   그게 바로 우리 원장을 망가뜨린 규칙이다. 실측:
//     이후   → en "After"          (gtranslate) — 사람 이름을 뜻으로 옮겼다
//     좋은 날 → en "One Sunny Day"              — 같은 병
//     김도하 → en "Gimdoha"                     — 성씨 김은 Kim 이다
//   active 인물 **1,101명**의 영문 이름이 기계번역이었다.
//   사람·그룹 이름은 **소리를 옮기는 것**이지 뜻을 옮기는 게 아니다.
//
//   KDB 는 유형을 안다. 소비자는 대개 모른다. 그러니 우리가 말해 준다.
//
// ★한 벌만 둔다. 제목/이름 구분은 enrich 의 채움 게이트가 쓰는 것과 **같은 정의**여야
//   한다 — 채울 때와 안내할 때가 다른 기준을 쓰면 원장과 안내가 어긋난다.

import "strings"

// TitleTypes — 제목류. 뜻-번역이 서빙값으로 허용되는 유형(오너 승인 2026-07-26).
// enrich 의 채움 게이트가 이 맵을 그대로 쓴다. 여기가 원본이다.
var TitleTypes = map[string]bool{
	"drama": true, "movie": true, "show": true, "song_album": true,
	"event_tour": true, "agency": true, "brand_place": true,
	"channel_outlet": true, "term": true,
}

// 소비자에게 주는 지시.
const (
	// FillHintTransliterate — 소리를 옮겨라. 뜻을 옮기지 마라.
	// 인물·그룹·캐릭터. 이후→イフ 이지 After 가 아니다.
	FillHintTransliterate = "transliterate"
	// FillHintTranslateTitle — 공식 현지 제목이 있으면 그것을, 없으면 뜻을 옮겨라.
	FillHintTranslateTitle = "translate_title"
	// FillHintUseStandardName — **소리도 뜻도 아니다.** 그 언어의 표준 통용명을 써라.
	// 없으면 학명을 그대로 둬라.
	//
	// ★왜 셋째가 필요한가 (2026-09-20). 종명은 두 지시 중 어느 것으로도 못 옮긴다:
	//
	//	전어 → 뜻으로 옮기면 鲭鱼(고등어). 소리로 옮기면 ジョノ — 일본어 표기가 아니다.
	//	       정답은 コノシロ · 窩斑鰶 이고, 그건 각 언어의 **표준 통용명**이다.
	//
	//   그런데 종명이 들어오는 유형 `term` 은 TitleTypes 에 있어 `translate_title`,
	//   즉 «뜻을 옮겨라»가 나간다. 우리가 틀린 지시를 보내고 있었다 — 소비자가
	//   그대로 따르면 위 다섯 건이 그대로 재현된다.
	FillHintUseStandardName = "use_standard_name"
)

// LocaleFillHint — 이 유형의 표기가 없을 때 소비자가 할 일.
//
// 유형만 보는 판이다. 대상 자체에 대해 아는 것이 있으면 LocaleFillHintFor 를 쓴다.
func LocaleFillHint(entityType string) string {
	return LocaleFillHintFor(entityType, "")
}

// LocaleFillHintFor — 유형 + **그 대상에 대한 관측**으로 지시를 정한다.
//
// taxonName 은 위키데이터 P225(학명)다. 있으면 이 대상은 분류군이므로 유형이 무엇이든
// `use_standard_name` 이 이긴다 — 유형 표를 통째로 옮기지 않는 이유는 `term` 에 분류군이
// 아닌 것도 들어 있어서다(0150 주석).
func LocaleFillHintFor(entityType, taxonName string) string {
	if strings.TrimSpace(taxonName) != "" {
		return FillHintUseStandardName
	}
	if TitleTypes[entityType] {
		return FillHintTranslateTitle
	}
	return FillHintTransliterate
}
