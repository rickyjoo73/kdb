package disambiguator

import "testing"

// 군집 키 회귀 — **전부 2026-08-09 DB 실측 중복쌍**이다.
//
// active 상태로 동시에 살아 있던 13쌍을 전수로 뽑았더니 전부 공백 아니면 따옴표 변형만
// 다른 같은 엔티티였다. 종전 키(TrimSpace)로는 같은 군집에 못 들어갔고, 폴백인
// trigramClose 도 공백이 만드는 가짜 bigram 때문에 임계 미달이었다.
func TestNormKeyGroupsMeasuredDuplicates(t *testing.T) {
	pairs := [][2]string{
		{"스트레이 키즈", "스트레이키즈"},
		{"라디오 스타", "라디오스타"},
		{"오은영의 금쪽 상담소", "오은영의 금쪽상담소"},
		{"닥터 섬보이", "닥터섬보이"},
		{"신병4: 사보타주", "신병4 : 사보타주"},
		{"킬러들의 쇼핑몰 2", "킬러들의 쇼핑몰2"},
		{"와일드 씽", "와일드씽"},
		{"표인: 풍기대막", "표인:풍기대막"},
		{"THE SIN: BLISS", "THE SIN : BLISS"},
		{"Time’s Tickin’", "Time’s Tickin'"}, // U+2019 ↔ U+0027
		{"걱정 말아요 그대", "걱정말아요 그대"},
		{"하이브 재팬", "하이브재팬"},
		{"채영", "채영"}, // 완전 동일 — 종전에도 잡히던 것(회귀 확인)
	}
	for _, p := range pairs {
		if normKey(p[0]) != normKey(p[1]) {
			t.Errorf("normKey(%q)=%q ≠ normKey(%q)=%q — 같은 군집에 못 들어간다",
				p[0], normKey(p[0]), p[1], normKey(p[1]))
		}
	}
}

// 서로 다른 엔티티는 합치면 안 된다. 공백을 지우는 키라 과합침 위험이 실재하므로
// 실제로 DB 에 함께 존재하는 이름들로 못 박는다.
func TestNormKeyKeepsDistinctApart(t *testing.T) {
	distinct := [][2]string{
		{"엔시티", "엔시티 드림"},          // 실측: 별개 엔티티 2건
		{"보이넥스트도어", "BOYNEXTDOOR"}, // 한글명 ↔ 라틴명은 별개 행
		{"논스톱", "논스톱3"},
		{"프로듀스 101", "프로듀스 1"}, // 44차 §1 의 숫자 토큰 함정과 같은 계열
		{"샤크라", "써클 차트"},
		{"Time’s Tickin’", "Times Tickin Pt. 2"},
	}
	for _, p := range distinct {
		if normKey(p[0]) == normKey(p[1]) {
			t.Errorf("normKey 가 서로 다른 %q 와 %q 를 같은 키(%q)로 만든다 — 과합침",
				p[0], p[1], normKey(p[0]))
		}
	}
}

// 라벨은 정규화하지 않는다 — 프롬프트에 붙여쓴 이름이 가면 판정이 흔들린다.
func TestNormKeepsReadableLabel(t *testing.T) {
	if got := norm("  스트레이 키즈 "); got != "스트레이 키즈" {
		t.Errorf("norm 은 양끝 공백만 잘라야 한다: %q", got)
	}
	if normKey("스트레이 키즈") == norm("스트레이 키즈") {
		t.Error("키와 라벨이 같아졌다 — 둘을 분리한 이유가 사라진다")
	}
}
