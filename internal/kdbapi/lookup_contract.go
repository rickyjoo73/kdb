package kdbapi

// lookup_contract — 조회 응답의 **status 계약**을 한 자리에 둔다.
//
// ★왜 생겼나 (2026-09-23, 소비자 두 곳이 같은 날 각자 신고했다).
//
//	global.nbntv  «카카오» 로 물었더니 `found` 에 카카오뱅크·카카오벤처스가 왔다.
//	              카카오 본체는 원장에 없다. 계약대로 found 를 믿으면 기사의
//	              "카카오" 가 "KakaoBank" 로 번역된다.
//	PressLocale   «채영» 단건 조회가 (TWICE)·(CLC) 둘을 주면서 status 는 `found`.
//	              같은 이름을 bulk 로 물으면 `ambiguous`. 단건만 쓰는 소비자는
//	              동명이인을 모른 채 첫 후보의 id 를 저장한다.
//	              «서울시» → 서울시립대학교가 `found`.
//
// 두 신고는 한 결함의 두 얼굴이다. **조회 SQL 은 `ILIKE '%질의%'` 로 부분일치를
// 함께 걷어 오는데**(표기 변형·별칭을 놓치지 않으려고 그렇게 짰다) **status 는
// "행이 하나라도 있으면 found"** 였다. 그래서 «부분일치밖에 없다»와 «정확히 그
// 대상을 찾았다»가 같은 한 글자로 나갔다.
//
// ★고친 방식. 걷어 오는 것은 그대로 두고(별칭·정규화 동치를 놓치면 안 된다),
//
//	나갈 때 **정확일치와 부분일치를 갈라 담는다.**
//	  matches — 질의가 그 대상의 «이름»인 것(캐노니컬·별칭, 전 로케일, 정규화 동치).
//	  related — 이름에 질의가 들어 있을 뿐인 것. 참고용이고, status 를 만들지 않는다.
//	status 는 matches 만 보고 정한다: 0건 miss · 1건 found · 2건 이상 ambiguous.
//
// ★발굴 신호는 이미 이 기준으로 돌고 있었다. `hasNormalizedHit` 이 false 면
//
//	(=부분일치뿐이면) 발굴 큐에 넣는다. 즉 **안쪽은 «없다»로 처리하면서 바깥에는
//	«찾았다»고 답하고 있었다.** 서빙만 거짓말을 한 자리다.

import (
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// entityNames — 이 대상을 부르는 모든 이름(캐노니컬 9 + 별칭 9벌).
// 조회 SQL 이 훑는 칸과 **같은 목록**이어야 한다. 한쪽만 늘리면, SQL 은 걷어 왔는데
// 여기서는 이름으로 안 쳐서 정확일치가 부분일치로 강등된다.
func entityNames(e Entity) []string {
	names := []string{
		e.CanonicalKO, e.CanonicalEN, e.CanonicalJA, e.CanonicalVI,
		e.CanonicalZH, e.CanonicalZHHant, e.CanonicalES, e.CanonicalID, e.CanonicalPTBR,
	}
	for _, set := range [][]string{
		e.Aliases.KO, e.Aliases.EN, e.Aliases.JA, e.Aliases.VI,
		e.Aliases.ZH, e.Aliases.ZHHant, e.Aliases.ES, e.Aliases.ID, e.Aliases.PTBR,
	} {
		names = append(names, set...)
	}
	return names
}

// isExactHit — 질의가 이 대상의 이름인가. 정규화(공백·문장부호·대소문자 무시)는
// 인테이크 dedup·조회 SQL 과 같은 규칙을 쓴다 — "쇼 미 더 머니"와 "쇼미더머니"는
// 같은 이름이고, "카카오"와 "카카오뱅크"는 다른 이름이다.
func isExactHit(e Entity, query string) bool {
	key := gatekeeper.NormalizedKey(query)
	if key == "" {
		return false
	}
	for _, n := range entityNames(e) {
		if n != "" && gatekeeper.NormalizedKey(n) == key {
			return true
		}
	}
	return false
}

// splitExactMatches — 정확일치(답)와 부분일치(참고)로 가른다. 순서는 보존한다
// (조회 SQL 이 이미 정확일치·신뢰도 순으로 정렬해 준다).
func splitExactMatches(matches []Entity, query string) (exact, related []Entity) {
	for _, m := range matches {
		if isExactHit(m, query) {
			exact = append(exact, m)
		} else {
			related = append(related, m)
		}
	}
	return exact, related
}

// lookupStatusFor — 정확일치 건수로 status 를 정한다. 여기서 miss 를 내면 호출측이
// tombstone(out_of_scope)·번역 재매칭·발굴 큐로 이어 간다.
//
// ★2건 이상이면 고르지 않는다(M06). 문맥 없이 KDB 가 하나를 고르면 소비자는 틀린
// 대상의 id 를 저장하고, 그 위에 근거가 쌓인다(I05).
func lookupStatusFor(exact []Entity) string {
	switch {
	case len(exact) == 0:
		return "miss"
	case len(exact) > 1:
		return "ambiguous"
	default:
		return "found"
	}
}

// hiddenByTypeFilter — 유형 필터가 **가린 같은 이름**을 돌려준다(필터 없이 다시 조회).
//
// ★왜 (2026-09-23 원장 실측). 소비자가 「삼성전자·네이버·기획재정부·카카오가 없다」고
//
//	신고했는데, **넷 다 원장에 있었다.** 없던 것이 아니라 유형이 달라서 필터에 걸린
//	것이다 — 삼성전자·네이버는 `brand_place`(company 유형이 생기기 전에 만들어졌다),
//	카카오는 `event_tour`(소비자가 처음 보낸 잘못된 type 힌트가 그대로 굳었다).
//
//	그 답이 그냥 `miss` 로 나가면 소비자는 «없구나» 하고 prepare 로 다시 등록을
//	요청한다. 그렇게 같은 대상이 둘이 된다 — 사당귀(show)와 사장님 귀는 당나귀 귀
//	(drama)가 정확히 그렇게 생겼다. 그래서 **«있는데 유형이 다르다»를 말해 준다.**
//	그 이름의 대상을 answer(matches)로 올리지는 않는다 — 소비자가 유형으로 좁힌 것을
//	우리가 무를 수는 없다. 보여 주고 고르게 한다.
func hiddenByTypeFilter(all []Entity, query string) []Entity {
	exact, _ := splitExactMatches(all, query)
	return exact
}

// consumerTypeFilter — 소비자가 보낸 type 을 **조회 필터로 쓸 값**으로 바꾼다.
// 반환 = (필터값, 유효한가).
//
// ★미상 표시는 필터가 아니다. `unknown`·`term` 은 "무엇인지 모르겠다"는 뜻인데
//
//	(entity_types.go PlaceholderTypes), 그대로 필터에 넣으면 «미상으로 등록된 것만»
//	을 찾게 된다. 소비자가 «유형을 모르겠다»고 말한 것을 «유형이 미상인 대상을
//	달라»로 읽은 셈이라, 박보검이 miss 로 나갔다(PressLocale 신고 3).
//
// ★목록에 없는 값은 조용히 지나가면 안 된다. `persson` 같은 오타가 200 miss 로
//
//	나가면 소비자는 "KDB 에 없구나" 하고 이미 있는 대상을 prepare 로 새로 등록
//	요청한다 — 문서 7-1 이 경계한 오염이 바로 이 문으로 들어온다.
func consumerTypeFilter(t string) (string, bool) {
	if t == "" {
		return "", true
	}
	if !validEntityType(t) {
		return "", false
	}
	if kdb.PlaceholderTypes[t] {
		return "", true
	}
	return t, true
}
