package kdb

import "testing"

// 권위 정답(wikidata ja) 대조 — Go 이식이 Python 프로토타입과 동일 동작함을 고정.
func TestNameToKatakana(t *testing.T) {
	cases := []struct{ ko, want string }{
		{"안재현", "アン・ジェヒョン"},
		{"탁재훈", "タク・ジェフン"},   // 이름 첫음절 유성(규칙) — 정답 チェ 와 다를 수 있으나 규칙 고정
		{"김향기", "キム・ヒャンギ"},
		{"최진실", "チェ・ジンシル"},
		{"정지소", "チョン・ジソ"},
		{"이종원", "イ・ジョンウォン"},
		{"박준면", "パク・ジュンミョン"},
		{"홍광호", "ホン・グァンホ"},
	}
	for _, c := range cases {
		if got := NameToKatakana(c.ko); got != c.want {
			t.Errorf("NameToKatakana(%q)=%q want %q", c.ko, got, c.want)
		}
	}
}

func TestKanaIsKatakanaGuard(t *testing.T) {
	if !kanaIsKatakana("アン・ジェヒョン") {
		t.Error("valid katakana rejected")
	}
	if kanaIsKatakana("アンxジェ") {
		t.Error("latin-containing accepted")
	}
	if kanaIsKatakana("") {
		t.Error("empty accepted")
	}
}

func TestIsPureHangul(t *testing.T) {
	if !isPureHangul("김철수") || !isPureHangul("매기 강") {
		t.Error("hangul rejected")
	}
	if isPureHangul("M!LK") || isPureHangul("IU아이유") || isPureHangul("") {
		t.Error("non-pure-hangul accepted")
	}
}
