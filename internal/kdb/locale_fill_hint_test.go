package kdb

import "testing"

// 실측에서 나온 사고를 고정한다 (2026-09-14).
// 사람 이름을 **번역**해서 이런 값이 원장에 들어갔다:
//
//	이후   → "After"          좋은 날 → "One Sunny Day"      김도하 → "Gimdoha"
//
// active 인물 1,101명의 영문 이름이 기계번역이었다. 빈칸일 때 소비자에게 "직역하세요"
// 라고 안내하면 같은 사고를 밖에서 반복시킨다. 유형을 보고 말해야 한다.
func TestLocaleFillHintNeverTranslatesNames(t *testing.T) {
	for _, ty := range []string{"person", "group", "character"} {
		if got := LocaleFillHint(ty); got != FillHintTransliterate {
			t.Errorf("%s 에 %q 를 안내한다 — 이름은 소리를 옮겨야 한다", ty, got)
		}
	}
	for _, ty := range []string{"drama", "movie", "show", "song_album", "event_tour"} {
		if got := LocaleFillHint(ty); got != FillHintTranslateTitle {
			t.Errorf("%s 에 %q 를 안내한다 — 제목은 공식 제목/뜻옮김이다", ty, got)
		}
	}
	// 모르는 유형은 **음역으로 기울인다.** 틀린 음역은 읽기 불편할 뿐이지만,
	// 틀린 번역은 다른 뜻의 단어를 사람 이름 자리에 놓는다.
	if got := LocaleFillHint("완전히새로운유형"); got != FillHintTransliterate {
		t.Errorf("미지 유형에 %q — 기본은 음역이어야 한다", got)
	}
}
