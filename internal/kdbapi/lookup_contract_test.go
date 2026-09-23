package kdbapi

import "testing"

// 2026-09-23 에 소비자 두 곳이 각자 신고한 것을 **그 이름 그대로** 고정한다.
// 이 시험이 깨지면 조회가 다시 «부분일치를 찾았다고 답하는» 상태로 돌아간 것이다.
func TestPartialMatchIsNotFound(t *testing.T) {
	// global.nbntv — «카카오» 를 물었더니 카카오뱅크·카카오벤처스가 found 로 왔다.
	// 카카오 본체는 원장에 없다. 계약대로 믿으면 기사의 "카카오" 가 KakaoBank 가 된다.
	kakao := []Entity{
		{KID: "K0022248", CanonicalKO: "카카오뱅크", CanonicalEN: "KakaoBank"},
		{KID: "K0022940", CanonicalKO: "카카오벤처스"},
	}
	exact, related := splitExactMatches(kakao, "카카오")
	if len(exact) != 0 || len(related) != 2 {
		t.Fatalf("부분일치가 답으로 샜다: exact=%d related=%d", len(exact), len(related))
	}
	if got := lookupStatusFor(exact); got != "miss" {
		t.Errorf("status=%q, 기대 miss — 없는 것을 있다고 답하면 안 된다", got)
	}

	// PressLocale — «서울시» 로 물으면 서울시립대학교가 found 로 왔다.
	seoul := []Entity{{CanonicalKO: "서울시립대학교", EntityType: "school"}}
	exact, related = splitExactMatches(seoul, "서울시")
	if len(exact) != 0 || len(related) != 1 {
		t.Fatalf("짧은 이름 퍼지 매칭이 답으로 샜다: exact=%d related=%d", len(exact), len(related))
	}
}

// 동명이인은 단건에서도 고르지 않는다(M06). 종전엔 단건만 `found` 였고 묶음만
// `ambiguous` 였다 — 단건만 쓰는 소비자는 동명이인을 모른 채 첫 후보를 저장했다.
func TestHomonymIsAmbiguousOnSingleLookup(t *testing.T) {
	chaeyoung := []Entity{
		{CanonicalKO: "채영", Disambig: "(TWICE)"},
		{CanonicalKO: "채영", Disambig: "(CLC)"},
		{CanonicalKO: "김채영"}, // 부분일치 — 답이 아니다
	}
	exact, related := splitExactMatches(chaeyoung, "채영")
	if len(exact) != 2 || len(related) != 1 {
		t.Fatalf("정확일치 2·참고 1 이어야 한다: exact=%d related=%d", len(exact), len(related))
	}
	if got := lookupStatusFor(exact); got != "ambiguous" {
		t.Errorf("status=%q, 기대 ambiguous", got)
	}
}

// 이름은 캐노니컬만이 아니다 — 별칭·다른 로케일 표기로 물어도 그 대상의 이름이다.
// 조회 SQL 이 훑는 칸과 여기 판정이 어긋나면 정확일치가 부분일치로 강등된다.
func TestExactHitCoversAliasesAndLocales(t *testing.T) {
	iu := Entity{
		CanonicalKO: "아이유", CanonicalEN: "IU", CanonicalJA: "アイユー",
		Aliases: AliasSets{KO: []string{"이지은"}, EN: []string{"Lee Ji-eun"}},
	}
	for _, q := range []string{"아이유", "IU", "iu", "アイユー", "이지은", "Lee Ji-eun", "lee jieun"} {
		if !isExactHit(iu, q) {
			t.Errorf("%q 는 이 대상의 이름인데 부분일치로 밀렸다", q)
		}
	}
	// 정규화는 공백·문장부호·대소문자만 무시한다. 다른 이름은 다른 이름이다.
	for _, q := range []string{"아이", "IU 팬", "지은"} {
		if isExactHit(iu, q) {
			t.Errorf("%q 를 이름으로 쳤다 — 부분일치가 답으로 샌다", q)
		}
	}
}

// 유형 필터가 «보유한 이름»을 가리면, 없다고만 답하지 않는다.
//
// ★2026-09-23 원장 실측. 소비자가 «없다»고 신고한 삼성전자·네이버·카카오는 전부
// 원장에 있었다 — 유형이 달라 필터에 걸렸을 뿐이다(brand_place · event_tour).
// 그냥 miss 로 답하면 소비자는 prepare 로 다시 등록을 요청하고, 같은 대상이 둘이 된다.
func TestTypeFilterHidingTheNameIsReported(t *testing.T) {
	all := []Entity{
		{KID: "K0000192", CanonicalKO: "삼성전자", EntityType: "brand_place"},
		{KID: "K9999999", CanonicalKO: "삼성전자서비스", EntityType: "company"},
	}
	hidden := hiddenByTypeFilter(all, "삼성전자")
	if len(hidden) != 1 || hidden[0].KID != "K0000192" {
		t.Fatalf("가려진 같은 이름을 못 찾았다: %+v", hidden)
	}
	if hidden[0].EntityType == "company" {
		t.Error("가려진 이유(우리 유형이 다르다)가 응답에 드러나야 한다")
	}
}

// 표기 변형(공백)은 같은 이름이다. 이 폴백이 없으면 "쇼 미 더 머니" 가 miss 로
// 갈리는데 인테이크는 existing_entity 로 접수를 무시해 영원한 preparing 교착이 된다.
func TestNormalizedVariantStaysExact(t *testing.T) {
	e := Entity{CanonicalKO: "6시 내고향"}
	if !isExactHit(e, "6시내고향") {
		t.Error("공백만 다른 같은 이름을 부분일치로 밀었다")
	}
}
