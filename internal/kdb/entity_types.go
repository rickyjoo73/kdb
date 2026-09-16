package kdb

// entity_types — **유형 목록의 유일한 원본.**
//
// ★왜 만들었나 (2026-09-16). 같은 목록을 세 곳이 따로 들고 있었고, 그중 하나가
//   몇 달째 틀려 있었다. 관리 화면의 필터 어휘:
//
//	person group work place organization brand event term unknown
//
//   실제 DB enum:
//
//	person group show drama movie song_album agency channel_outlet brand_place
//	event_tour character term unknown  + political_party government_body company
//	organization sports_team school game musical_play webtoon publication
//
//   두 가지가 동시에 잘못돼 있었다.
//     ① **없는 값 4개**(work·place·brand·event) — 고르면 `invalid input value for
//        enum kwave_entity_type` 로 화면이 500 이 된다.
//     ② **있는 값 14개 누락** — show·drama·movie·song_album·agency·character 와
//        0143·0146 으로 늘린 새 유형 10종 **전부**.
//
//   그래서 운영자는 새 유형(정당·기업·게임…)을 **화면에서 고를 수도, 인박스에서
//   승격시킬 수도 없었다**(isValidEntityType 이 같은 목록을 본다). 문서와 API 는
//   새 유형을 받는데 사람이 쓰는 화면만 옛 세상에 있었다.
//
// ★목록을 두 벌 적지 않는다. 여기 하나만 두고 API·관리화면이 같이 본다.
//   마이그레이션의 enum 과 어긋나면 회귀가 실패한다(entity_types_test.go).

// EntityTypes — kwave_entity_type enum 전체. **enum 정렬 순서와 같게** 둔다.
var EntityTypes = []string{
	// K-웨이브 원래 유형
	"person", "group", "show", "drama", "movie", "song_album", "agency",
	"channel_outlet", "brand_place", "event_tour", "character", "term", "unknown",
	// 0143 — 정치·경제·시사·스포츠
	"political_party", "government_body", "company", "organization",
	"sports_team", "school",
	// 0146 — 기각 더미에서 실제로 들어오던 것들
	"game", "musical_play", "webtoon", "publication",
}

// PlaceholderTypes — "무엇인지 모르겠다"는 뜻의 칸. 대상의 성질이 아니라 **미상 표시**다.
// 화면의 «유형 지정» 목록에서는 빼고, 필터에서는 남긴다(미상만 골라 봐야 하니까).
var PlaceholderTypes = map[string]bool{"unknown": true, "term": true}

// ValidEntityType — DB enum 에 있는 값인가.
func ValidEntityType(s string) bool {
	for _, v := range EntityTypes {
		if v == s {
			return true
		}
	}
	return false
}

// AssignableEntityTypes — 운영자가 **골라서 붙일 수 있는** 유형(미상 칸 제외).
func AssignableEntityTypes() []string {
	out := make([]string, 0, len(EntityTypes))
	for _, v := range EntityTypes {
		if !PlaceholderTypes[v] {
			out = append(out, v)
		}
	}
	return out
}
