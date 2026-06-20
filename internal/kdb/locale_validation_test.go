package kdb

import "testing"

// 영문/베트남어 등 비-CJK 칸에 한글이 들어가는 오염(예: MusicBrainz 가 canonical_en
// 에 "김대호" 를 넣던 버그)을 IsValidSpellingForLocale 가 막는지 검증.
func TestIsValidSpellingForLocale(t *testing.T) {
	cases := []struct {
		locale, spelling string
		want             bool
	}{
		{"en", "김대호", false}, // 영문 칸 한글 → 거부 (보고된 버그)
		{"en", "Kim Dae-ho", true},
		{"en", "BTS", true},
		{"vi", "박보검", false},
		{"es", "이정은", false},
		{"id", "기안84", false}, // 한글+숫자 → 거부
		{"ja", "김대호", false}, // ja 칸에 한글 → 거부
		{"ja", "キム・デホ", true},
		{"ja", "防弾少年団", true},
		{"zh", "防弾少年団", true},
		{"zh", "BTS", false},     // zh 는 한자 필수
		{"pt_br", "이연", false},   // underscore 변종도 정규화돼 검증
		{"pt-br", "Lee Yeon", true},
		{"en", "", false},
		// ko 칸 오염 차단(2026-06-20): 일본어/순수한자가 canonical_ko 에 들어가는 손상.
		{"ko", "허준", true},
		{"ko", "방탄소년단", true},
		{"ko", "아이브(IVE)", true}, // 한글+라틴 혼용 허용
		{"ko", "IVE", true},        // 라틴 official 허용
		{"ko", "常田大希", false},     // 순수 한자(한글 없음) → 거부
		{"ko", "ホジュン～伝説の心医～", false}, // 가나 포함 → 거부
		{"ko", "100日の郎君様", false},      // 가나 포함 → 거부
	}
	for _, c := range cases {
		if got := IsValidSpellingForLocale(c.locale, c.spelling); got != c.want {
			t.Errorf("IsValidSpellingForLocale(%q, %q) = %v; want %v", c.locale, c.spelling, got, c.want)
		}
	}
}
