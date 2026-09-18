package kdb

import "testing"

// TestExtractsRealContextSpellings — 실제 요청 문맥에서 표기를 뽑는지.
//
// 아래 문맥은 전부 2026-09 발굴 큐의 **실물**이고, 전부 no_match 로 끝난 건이다.
func TestExtractsRealContextSpellings(t *testing.T) {
	cases := []struct {
		term, ctx, wantLoc, wantVal string
	}{
		{
			"스프링 레인",
			"타이틀곡 '카케라-운메이노피스-'를 포함한 '톡-토크'(Tock-Talk), '스프링 레인'(Spring Rain) 등 일본 오리지널 신곡 3곡",
			"en", "Spring Rain",
		},
		{
			"서머 매드니스 2026: 코어",
			"'서머 매드니스 2026: 코어'(SUMMER MADNESS 2026: CORE) 개최",
			"en", "SUMMER MADNESS 2026: CORE",
		},
		{
			"도시테",
			"신곡 '도시테'(どうして)를 발표했다",
			"ja", "どうして",
		},
		{
			"청사포",
			"부산 '청사포'(靑蛇浦) 일대",
			"han", "靑蛇浦",
		},
	}
	for _, c := range cases {
		got := ExtractContextSpelling(c.term, c.ctx)
		if len(got) == 0 {
			t.Errorf("%q: 아무것도 못 뽑았다 — 답이 문맥에 있다", c.term)
			continue
		}
		if got[0].Locale != c.wantLoc || got[0].Value != c.wantVal {
			t.Errorf("%q: %s=%q, 기대 %s=%q", c.term, got[0].Locale, got[0].Value, c.wantLoc, c.wantVal)
		}
	}
}

// TestDoesNotMineExplanations — 괄호 안이 표기가 아니라 설명일 때 뽑지 않는지.
//
// 실물: "닥터X : 하얀 마피아의 시대(이하 '닥터X')", "'틈만나면,'(연출 최보필/작가 채진아)".
// 한글이 섞인 괄호는 표기가 아니다. 여기서 뽑으면 «틀린값»을 만든다.
func TestDoesNotMineExplanations(t *testing.T) {
	bad := []struct{ term, ctx string }{
		{"닥터X : 하얀 마피아의 시대", "닥터X : 하얀 마피아의 시대(이하 '닥터X')가 방영된다"},
		{"틈만나면,", "SBS 예능 '틈만나면,'(연출 최보필/작가 채진아)은"},
		{"최혜인", "Shot & Directed by 최혜인(감독)"},
		{"아이유", "아이유(1993년생)"},
		{"방탄소년단", "방탄소년단(BTS)"}, // 이건 좋은 값 — 아래에서 별도 확인
	}
	for _, c := range bad[:4] {
		if got := ExtractContextSpelling(c.term, c.ctx); len(got) > 0 {
			t.Errorf("%q: 설명을 표기로 뽑았다 → %s=%q", c.term, got[0].Locale, got[0].Value)
		}
	}
	// 정상 케이스는 뽑혀야 한다.
	if got := ExtractContextSpelling("방탄소년단", "방탄소년단(BTS)의 신곡"); len(got) != 1 || got[0].Value != "BTS" {
		t.Errorf("방탄소년단(BTS) 를 못 뽑았다: %+v", got)
	}
}

// TestNeverInventsWhenAbsent — 문맥에 없으면 아무것도 만들지 않는지.
func TestNeverInventsWhenAbsent(t *testing.T) {
	for _, c := range []struct{ term, ctx string }{
		{"스프링 레인", "온유가 일본에서 네 번째 싱글을 발매한다"},
		{"없는용어", "전혀 관계없는 문장"},
		{"", "빈 용어"},
		{"가", "한 글자"},
	} {
		if got := ExtractContextSpelling(c.term, c.ctx); len(got) > 0 {
			t.Errorf("%q: 없는 표기를 지어냈다 → %+v", c.term, got)
		}
	}
}
