package aliasmatch

import "testing"

func TestIsAbbreviation(t *testing.T) {
	cases := []struct {
		ko, full string
		want     bool
		note     string
	}{
		{"유퀴즈", "유 퀴즈 온 더 블럭", true, "단어 첫음절 prefix"},
		{"어거스트 디", "어거스트 디 투어", true, "공백 prefix"},
		{"무빙", "무빙 2", true, "공백 제거 prefix"},
		{"모두가", "모두가 자신의 무가치함과 싸우고 있다", true, "공백 제거 prefix"},
		{"굿파트너", "굿파트너2", true, "no-space prefix"},
		{"박신혜", "김민경", false, "전혀 다른 이름"},
		{"BTS", "방탄소년단", false, "음운 매칭 X"},
	}
	for _, c := range cases {
		got := isAbbreviation(c.ko, c.full)
		if got != c.want {
			t.Errorf("isAbbreviation(%q, %q) = %v; want %v (%s)", c.ko, c.full, got, c.want, c.note)
		}
	}
}
