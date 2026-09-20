package kdb

import "testing"

// ★이 시험이 지키는 것 (2026-09-20 실측).
//
//	중국어 칸이 로마자인데 번체 칸에는 한자가 있는 active 행이 25건이었다:
//
//	    김민정   zh=Winter       zh_hant=金玟廷   ← Winter 는 다른 사람 예명이다
//	    스텔라장 zh=Stella Jang  zh_hant=張星銀
//	    호시     zh=Hoshi        zh_hant=權順榮
//
//	원인은 우선순위였다. zh.wikipedia 표제는 prio 6, wikidata-label 은 prio 5 —
//	위키데이터 라벨이 로마자면 중국어 칸의 로마자가 **영구히** 남는다.
//	그래서 «라틴 전용 값은 한자 표제가 덮는다»를 열었다. 아래는 그 규칙의 경계다.

// zhWikiLatinReplaceable — 레인의 SQL 조건과 같은 판정을 Go 로 재현한 것.
// SQL 과 Go 가 다른 것을 보면 한쪽으로 오염이 샌다(9/19 qaCharsetOK 와 같은 계열).
func zhWikiLatinReplaceable(current, title string) bool {
	hasHan := func(s string) bool {
		for _, r := range s {
			if r >= 0x4E00 && r <= 0x9FFF {
				return true
			}
		}
		return false
	}
	if current == "" {
		return true
	}
	return !hasHan(current) && hasHan(title)
}

func TestZhWikiLatin_로마자값은_한자표제가_덮는다(t *testing.T) {
	for _, c := range []struct{ cur, title string }{
		{"Stella Jang", "張星銀"},
		{"Hoshi", "權順榮"},
		{"Winter", "金玟廷"},
		{"Home Alone", "我獨自生活"},
	} {
		if !zhWikiLatinReplaceable(c.cur, c.title) {
			t.Errorf("%q ← %q 를 덮어야 한다", c.cur, c.title)
		}
	}
}

func TestZhWikiLatin_한자값을_로마자로_덮지_않는다(t *testing.T) {
	// b16194b 가 막은 방향. 중국어 위키백과는 K-팝 그룹 문서를 라틴 제목으로 단다.
	for _, c := range []struct{ cur, title string }{
		{"大爆炸樂隊", "BIGBANG"},
		{"金珉錫", "Xiumin"},
		{"樂童音樂家", "AKMU"},
	} {
		if zhWikiLatinReplaceable(c.cur, c.title) {
			t.Errorf("%q 를 %q 로 덮으면 안 된다 — 한자 문화권 칸의 로마자는 틀린 표기다", c.cur, c.title)
		}
	}
}

func TestZhWikiLatin_표제도_라틴이면_손대지_않는다(t *testing.T) {
	// 실측: 룰라(Roo'ra) · 멜로디데이(Melody Day) · STUDIO CHOOM · Sleepy · EJAE ·
	// Chanmina 는 zh.wikipedia 표제 자체가 라틴이다. 이건 그 대상의 중국어 표기가
	// 로마자라는 뜻이므로 고칠 것이 없다.
	for _, c := range []struct{ cur, title string }{
		{"Roo'ra", "Roo'ra"},
		{"Melody Day", "Melody Day"},
		{"STUDIO CHOOM", "STUDIO CHOOM"},
	} {
		if zhWikiLatinReplaceable(c.cur, c.title) {
			t.Errorf("%q ← %q : 표제가 라틴이면 바꿀 근거가 없다", c.cur, c.title)
		}
	}
}

func TestZhWikiSimplified_번체표제는_간체로_바꿔_쓴다(t *testing.T) {
	// zh.wikipedia 표제는 번체가 많다. 그대로 간체 칸에 쓰면 9/17 에 87건 치운
	// «간체 칸의 번체»를 다시 만든다.
	for _, c := range []struct{ in, want string }{
		{"張星銀", "张星银"},
		{"權順榮", "权顺荣"},
		{"我獨自生活", "我独自生活"},
	} {
		if got := zhWikiSimplified(c.in); got != c.want {
			t.Errorf("zhWikiSimplified(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestZhWikiSimplified_바꿀것이_없으면_원문(t *testing.T) {
	for _, in := range []string{"Roo'ra", "STARSHIP", "曹承衍"} {
		if got := zhWikiSimplified(in); got != in {
			t.Errorf("zhWikiSimplified(%q) = %q — 바꿀 번체 전용 글자가 없으면 원문이어야 한다", in, got)
		}
	}
}
