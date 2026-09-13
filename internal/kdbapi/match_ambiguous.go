package kdbapi

import "strings"

// MatchStatusAmbiguous — 문맥 없이 이름만 온 요청이 동명 후보 둘 이상으로 갈릴 때의 신호.
// 계약(M06 / I06 / I07): 서버는 유명도·신뢰도 1위·첫 행으로 대신 고르지 않는다.
const MatchStatusAmbiguous = "ambiguous"

// ambiguousCandidates — 요청이 "문맥 없는 이름"인지 판정하고, 그렇다면 그 이름으로
// 갈리는 후보를 모두 돌려준다. 아니면 nil(=기존 동작 그대로).
//
// 문맥의 정의를 넓게 잡으면 기사 본문 매칭까지 ambiguous 로 바뀌어 번역 핫패스가
// 통째로 막힌다. 그래서 판정을 가장 좁게 둔다 — **요청 본문 전체가 후보의 표기와
// 정확히 같을 때만** 문맥이 없다고 본다. "인물-G" 는 문맥 없음, "인물-G 가 신곡을
// 냈다" 는 문맥 있음이다. 후자는 종전처럼 entities 만 돌려주고, 소비자가 원하면
// disambiguate=true 로 본문 판별을 켜면 된다.
//
// 후보가 1건이면 갈릴 것이 없으므로 ambiguous 가 아니다.
func ambiguousCandidates(sourceText string, ents []MatchedEntity) []MatchedEntity {
	term := strings.TrimSpace(sourceText)
	if term == "" || len(ents) < 2 {
		return nil
	}
	out := make([]MatchedEntity, 0, len(ents))
	for _, e := range ents {
		if matchesWholeTerm(term, e) {
			out = append(out, e)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// matchesWholeTerm — 요청 본문 전체가 이 엔티티의 한국어 정본 또는 별칭과 같은가.
// 라틴 별칭의 대소문자는 무시한다(BTS↔bts) — 매칭 쿼리의 D-9 규칙과 같은 기준.
//
// entity_type 은 보지 않는다. person "인물-G" 와 work "인물-G" 가 함께 걸려도
// 문맥 없는 요청자는 여전히 고를 수 없고, 유형이 다르다는 이유로 한쪽을 자동
// 선택하는 것이야말로 M06 이 막으려는 동작이다.
func matchesWholeTerm(term string, e MatchedEntity) bool {
	if strings.EqualFold(term, strings.TrimSpace(e.KO)) {
		return true
	}
	for _, a := range e.SourceAliases {
		if a != "" && strings.EqualFold(term, strings.TrimSpace(a)) {
			return true
		}
	}
	return false
}
