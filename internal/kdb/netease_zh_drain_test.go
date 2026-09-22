package kdb

import "testing"

// ★이 시험이 지키는 것 (2026-09-23).
//
//	동명곡. 「안녕」·「동행」·「제일 잘 나가」는 제목만으로 찾으면 다른 가수의 곡이 먼저 나온다.
//	그리고 플랫폼 번역제목에는 영문이 섞여 있다 — 그것을 zh 칸에 쓰면 중국어가 아니다.

func TestNeteaseTitleEq(t *testing.T) {
	cases := []struct {
		ko, name string
		want     bool
	}{
		{"댕댕", "댕댕 (dangdang)", true},
		{"나 같은 건 없는 건가요", "나 같은건 없는건가요", true},
		{"에필로그", "에필로그(Epilogue)", true},
		{"봄날", "봄날 (Spring Day) - Remix", false},
		{"안녕", "안녕하세요", false},
		{"", "안녕", false},
	}
	for _, c := range cases {
		if got := neteaseTitleEq(c.ko, c.name); got != c.want {
			t.Errorf("neteaseTitleEq(%q,%q)=%v, want %v", c.ko, c.name, got, c.want)
		}
	}
}

func TestNeteaseZhTitle(t *testing.T) {
	if got := neteaseZhTitle([]string{"不要幸福"}); got != "不要幸福" {
		t.Errorf("중국어 번역제목을 못 골랐다: %q", got)
	}
	if got := neteaseZhTitle([]string{"一杯的回忆 (原唱 : 李长熙)"}); got != "一杯的回忆" {
		t.Errorf("주석을 안 뗐다: %q", got)
	}
	if got := neteaseZhTitle([]string{"作者未定", "作者未定(Inst.)"}); got != "作者未定" {
		t.Errorf("같은 제목의 변형을 둘로 셌다: %q", got)
	}
	for _, tns := range [][]string{
		{"Ghosting"},                  // 영문 번역제목은 zh 가 아니다
		{"Wanna go get some abalone"}, // 〃
		{"怦然心动", "心动"},                // 서로 다른 제목 둘 — 어느 쪽인지 말할 수 없다
		{"안녕"},                        // 한글이 섞이면 중국어 제목이 아니다
		{},
	} {
		if got := neteaseZhTitle(tns); got != "" {
			t.Errorf("%v 에서 %q 를 골랐다 — 골라선 안 된다", tns, got)
		}
	}
}

func TestNeteasePick_가수가_맞아야_쓴다(t *testing.T) {
	ours := []itunesArtist{{ko: "티아라", names: []string{"티아라", "tara", "t-ara"}}}
	rows := []neteaseSongRow{
		{ID: 1, Name: "왜 이러니", TNS: []string{"为什么这样"}, Ar: []neteaseArtistRow{{Name: "다른가수"}}},
		{ID: 2, Name: "왜 이러니", TNS: []string{"为什么这样"}, Ar: []neteaseArtistRow{{Name: "T-ara"}}},
	}
	hit, zh, why := neteasePick("왜 이러니", rows, ours)
	if hit == nil || hit.ID != 2 || zh != "为什么这样" {
		t.Fatalf("기사 가수의 곡을 골라야 한다: hit=%v zh=%q why=%s", hit, zh, why)
	}
	// 같은 제목인데 우리 가수가 없으면 아무것도 쓰지 않는다.
	if hit, _, why := neteasePick("왜 이러니", rows[:1], ours); hit != nil || why != "가수 불일치" {
		t.Errorf("동명곡을 가져왔다: hit=%v why=%s", hit, why)
	}
	// 가수는 맞는데 중국어 제목이 없으면 쓸 값이 없다.
	rows[1].TNS = []string{"Ghosting"}
	if hit, _, why := neteasePick("왜 이러니", rows, ours); hit != nil || why != "중국어 제목 없음" {
		t.Errorf("영문 번역제목을 zh 로 썼다: hit=%v why=%s", hit, why)
	}
	// 제목이 아예 다르면 no_match.
	if _, _, why := neteasePick("없는곡", rows, ours); why != "no_match" {
		t.Errorf("why=%s", why)
	}
}

// TestNeteaseLaneIsWired — 원장에 배선했는지. 배선 안 하면 도는지조차 알 수 없다(19회차).
func TestNeteaseLaneIsWired(t *testing.T) {
	found := false
	for _, l := range WiredLanes {
		if l == "netease-zh" {
			found = true
		}
	}
	if !found {
		t.Error("netease-zh 가 WiredLanes 에 없다")
	}
}
