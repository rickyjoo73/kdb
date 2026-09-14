package enrich

import "testing"

// 2026-09-14 실측: mt-fill 이 버린 사유 1위가 "문장 종결부호(제목 아님)" 였다
// (zh 146 · ja 83 = 229건). 구글이 제목을 문장으로 옮기며 끝에 부호를 붙인 것뿐인데
// 그 한 글자 때문에 멀쩡한 번역이 통째로 버려지고 있었다.
func TestMTTrimSentenceEnd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"爱情来了。", "爱情来了"},          // 중국어 구두점
		{"愛が来る．", "愛が来る"},           // 전각 마침표
		{"Good Day.", "Good Day"},       // 반각
		{"よく遊ぶと何が起きる！", "よく遊ぶと何が起きる"}, // 느낌표
		{"本当に？", "本当に"},             // 물음표
		{"爱情来了。。。", "爱情来了"},        // 연속
		{"爱情来了 。 ", "爱情来了"},        // 공백 섞임
		{"爱情来了", "爱情来了"},           // 부호 없음 — 그대로
		{"Mr.", "Mr"},                   // 짧아도 규칙은 같다
		{"。。。", "。。。"},                // 전부 부호면 원값 유지(게이트가 bad 로 잡을 몫)
		{"", ""},
	}
	for _, c := range cases {
		if got := mtTrimSentenceEnd(c.in); got != c.want {
			t.Errorf("mtTrimSentenceEnd(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// 값은 바뀌면 안 된다 — 부호만 뗀다. 가운데 마침표는 제목의 일부일 수 있다.
func TestMTTrimSentenceEndKeepsInnerPunctuation(t *testing.T) {
	for _, s := range []string{"M.I.L.K", "U.N. 본부", "1.5 거리", "No.5"} {
		if got := mtTrimSentenceEnd(s); got != s {
			t.Errorf("가운데 부호를 건드렸다: %q → %q", s, got)
		}
	}
}
