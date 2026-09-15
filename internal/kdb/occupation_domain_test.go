package kdb

import "testing"

// 영역 표는 **QID 로만** 판정한다 — QID 는 식별자라 모호하지 않다.
//
// ★같은 날 한자 성씨표로 같은 일을 하려다 1,287건을 잘못 잡았다. 강은 姜·康·強 이
// 다 쓰이고 간체·번체까지 갈려 표 자체가 틀렸다. QID 에는 그 문제가 없다 —
// Q82955 는 politician 이지 다른 무엇도 아니다.
func TestOccupationDomainMapsKnownQIDs(t *testing.T) {
	for _, c := range []struct{ qid, want string }{
		{"Q177220", DomainEntertainment},  // singer
		{"Q33999", DomainEntertainment},   // actor
		{"Q937857", DomainSports},         // association football player
		{"Q10871364", DomainSports},       // baseball player
		{"Q82955", DomainPolitics},        // politician
		{"Q131524", DomainBusiness},       // entrepreneur
		{"Q1930187", DomainMedia},         // journalist
		{"Q1622272", DomainAcademia},      // university teacher
		{"Q36180", DomainArts},            // writer
	} {
		if got := OccupationDomain([]string{c.qid}); got != c.want {
			t.Errorf("%s → %q, 기대 %q", c.qid, got, c.want)
		}
	}
}

// 모르는 QID 는 **판정하지 않는다**(D-37). 지어내면 소비자가 그것을 저장한다.
func TestUnknownOccupationIsNotGuessed(t *testing.T) {
	for _, in := range [][]string{
		nil,
		{},
		{"Q999999999"},
		{"Q999999999", "Q888888888"},
	} {
		if got := OccupationDomain(in); got != "" {
			t.Errorf("%v → %q, 기대 빈 문자열", in, got)
		}
	}
}

// 여럿이면 **가장 흔한 것**, 동수면 고정 순서. 같은 입력에 같은 답이 나와야 한다.
func TestMultipleOccupationsResolveDeterministically(t *testing.T) {
	// 가수 둘 + 정치인 하나 → 연예(최다)
	got := OccupationDomain([]string{"Q177220", "Q488205", "Q82955"})
	if got != DomainEntertainment {
		t.Errorf("최다 영역이 안 골라졌다: %q", got)
	}
	// 배우 하나 + 정치인 하나(동수) → 고정 순서로 연예
	got = OccupationDomain([]string{"Q82955", "Q33999"})
	if got != DomainEntertainment {
		t.Errorf("동수 가름이 고정이 아니다: %q", got)
	}
	// 순서를 바꿔도 같아야 한다
	if OccupationDomain([]string{"Q33999", "Q82955"}) != got {
		t.Error("입력 순서에 따라 답이 달라졌다")
	}
	// 알려진 것이 정치인뿐이면 정치
	if d := OccupationDomain([]string{"Q999999999", "Q82955"}); d != DomainPolitics {
		t.Errorf("모르는 QID 가 섞이면 판정이 흔들린다: %q", d)
	}
}

// 표의 값은 **선언된 영역 상수**여야 한다. 오타 하나가 소비자 필터를 조용히 깨뜨린다.
func TestEveryMappedDomainIsADeclaredConstant(t *testing.T) {
	ok := map[string]bool{
		DomainEntertainment: true, DomainSports: true, DomainPolitics: true,
		DomainBusiness: true, DomainAcademia: true, DomainMedia: true, DomainArts: true,
	}
	for qid, d := range occupationDomains {
		if !ok[d] {
			t.Errorf("%s 이 선언되지 않은 영역 %q 로 간다", qid, d)
		}
	}
	if len(occupationDomains) < 50 {
		t.Errorf("표가 %d개뿐이다 — 흔한 직업을 덮지 못한다", len(occupationDomains))
	}
}
