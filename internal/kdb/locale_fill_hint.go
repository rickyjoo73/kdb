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
)

// LocaleFillHint — 이 유형의 표기가 없을 때 소비자가 할 일.
func LocaleFillHint(entityType string) string {
	if TitleTypes[entityType] {
		return FillHintTranslateTitle
	}
	return FillHintTransliterate
}
