package hangul

import "testing"

func TestIsCleanKorean(t *testing.T) {
	clean := []string{
		"임원희", "안은진", "오징어 게임", "Park Shin-hye",
		"BTS", "방탄소년단 (BTS)", "콩 심은 데 콩 나고",
	}
	dirty := []string{
		"임ㅇ원희", "ㅁ방탄", "안ㅡ은진",
	}
	for _, s := range clean {
		if !IsCleanKorean(s) {
			t.Errorf("clean=%q rejected", s)
		}
	}
	for _, s := range dirty {
		if IsCleanKorean(s) {
			t.Errorf("dirty=%q accepted", s)
		}
	}
}

func TestStripLoneJamo(t *testing.T) {
	cases := []struct{ in, want string }{
		{"임ㅇ원희", "임원희"},
		{"ㅁ방탄", "방탄"},
		{"안ㅡ은진", "안은진"},
		{"임원희", "임원희"},
	}
	for _, c := range cases {
		if got := StripLoneJamo(c.in); got != c.want {
			t.Errorf("StripLoneJamo(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

func TestCountLoneJamo(t *testing.T) {
	if CountLoneJamo("임ㅇ원희") != 1 {
		t.Errorf("expected 1")
	}
	if CountLoneJamo("임원희") != 0 {
		t.Errorf("expected 0")
	}
}
