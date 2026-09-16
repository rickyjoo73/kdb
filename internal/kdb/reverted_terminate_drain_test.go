package kdb

import "testing"

// TestRevertTermKeepsKoreanSubjects — **되살린 것을 다시 죽이지 않는다.**
//
// 실측(2026-09-15~16): 오세훈 Q494239 "South Korean politician" 을 scope-reopen 이
// candidate 로 되살린 그 날, 이 드레인이 "직업이 비-엔터"로 다시 rejected 로 내렸다.
// 다음 날 소비자 요청은 그 기각 행에 막혀 out_of_scope 로 나갔다.
// 되살리는 레인과 죽이는 레인이 같은 기준을 쓰는지 여기서 고정한다.
func TestRevertTermKeepsKoreanSubjects(t *testing.T) {
	keep := []string{
		"South Korean politician",                   // 오세훈 Q494239
		"South Korean association football player",  // 차범근 Q346751
		"South Korean businessman",                  // 이재용 계열
		"South Korean curler",                       // 김은정 Q6408614
		"South Korean basketball player",            // 황윤서 Q135354657
		"South Korean entrepreneur",                 // 이혜원 Q12613186
		"political party in South Korea",            // 국민의힘 계열
		"semiconductor company in South Korea",      // SK하이닉스 계열
		"national research university in Seoul, South Korea", // 서울대학교 Q391028
	}
	for _, d := range keep {
		if v, _ := revertTermVerdict(d, false, "", "Q1"); v != revertTermKeep {
			t.Errorf("한국 대상을 종결했다: %q → %v", d, v)
		}
	}

	// 해외 대상은 범위가 넓어져도 그대로 범위 밖이다.
	for _, d := range []string{
		"Japanese singer", "American actor", "Chinese businessman",
		"North Korean politician",
	} {
		if v, _ := revertTermVerdict(d, false, "", "Q1"); v != revertTermReject {
			t.Errorf("해외 대상을 종결하지 않았다: %q → %v", d, v)
		}
	}

	// 이름요소 항목은 어떤 대상의 근거도 못 된다.
	if v, _ := revertTermVerdict("Korean unisex given name", true, "Q3409032", "Q69509682"); v != revertTermReject {
		t.Errorf("이름요소 항목을 종결하지 않았다: %v", v)
	}

	// ★근거 없이는 죽이지 않는다 — 빈 설명, 그리고 한국/해외 어느 표시도 없는 설명.
	for _, d := range []string{"", "   ", "professional wrestler", "software engineer"} {
		if v, _ := revertTermVerdict(d, false, "", "Q1"); v != revertTermHold {
			t.Errorf("근거 없는 건을 보류하지 않았다: %q → %v", d, v)
		}
	}
}
