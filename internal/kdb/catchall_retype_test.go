package kdb

import "testing"

// TestSoleAnchorTypeOnlyDecidesWhenItCan — **못 가르는 것을 가른다고 하지 않는다.**
//
// 이 드레인은 유형을 바꾼다 — 틀리면 엉뚱한 유형으로 서빙된다. 그래서 P31 이
// 한 유형만 가리킬 때에만 결론을 낸다.
func TestSoleAnchorTypeOnlyDecidesWhenItCan(t *testing.T) {
	// 한 유형만 가리키는 경우.
	for _, c := range []struct {
		p31  []string
		want string
	}{
		{[]string{"Q7278"}, "political_party"},           // 국민의힘
		{[]string{"Q6881511"}, "company"},                // 기업
		{[]string{"Q3918"}, "school"},                    // 대학
		{[]string{"Q476028"}, "sports_team"},             // 축구단
		{[]string{"Q7889"}, "game"},                      // 비디오게임
		{[]string{"Q3918", "Q875538"}, "school"},         // 둘 다 school
		{[]string{"Q7278", "Q99999999"}, "political_party"}, // 모르는 클래스는 무시
	} {
		got, ok := soleAnchorType(c.p31)
		if !ok || got != c.want {
			t.Errorf("%v → (%q,%v), 기대 %q", c.p31, got, ok, c.want)
		}
	}

	// ★클래스가 못 가르면 결론 없음 — 대상은 그냥 둔다.
	for _, p31 := range [][]string{
		{"Q4830453"},           // business: agency 인지 company 인지 모른다
		{"Q1004"},              // comic: webtoon 인지 publication 인지 모른다
		{"Q4438121"},           // sports organization: 협회인지 구단인지 모른다
		{"Q3918", "Q7278"},     // 학교이자 정당? 갈린다
		{"Q7889", "Q7725634"},  // 게임이자 문학작품? 갈린다
	} {
		got, ok := soleAnchorType(p31)
		if got != "" || !ok {
			t.Errorf("%v 는 결론 없음이어야 한다 — (%q,%v)", p31, got, ok)
		}
	}

	// 아는 클래스가 하나도 없으면 «판정 안 함»이다 — 갈림과 구분된다.
	if got, ok := soleAnchorType([]string{"Q99999999", "Q88888888"}); ok || got != "" {
		t.Errorf("모르는 클래스만 있을 때 판정했다 — (%q,%v)", got, ok)
	}
	if got, ok := soleAnchorType(nil); ok || got != "" {
		t.Errorf("빈 P31 에 판정했다 — (%q,%v)", got, ok)
	}
}

// TestCatchallTypesAreOnlyTheDumpingGrounds — 사람이나 근거가 **골라서** 붙인 유형은
// 여기서 안 건드린다. person 을 P31 하나로 뒤집으면 동명이인 오매칭이 유형까지 바꾼다.
func TestCatchallTypesAreOnlyTheDumpingGrounds(t *testing.T) {
	allowed := map[string]bool{"brand_place": true, "term": true, "unknown": true}
	for _, ty := range CatchallTypes {
		if !allowed[ty] {
			t.Errorf("%q 는 잡동사니 칸이 아니다 — 여기서 꺼내면 안 된다", ty)
		}
	}
	if len(CatchallTypes) != len(allowed) {
		t.Errorf("잡동사니 칸이 %d개다 — 늘리려면 이유를 여기 적는다", len(CatchallTypes))
	}
}
