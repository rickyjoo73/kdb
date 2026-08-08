package kdb

import (
	"strings"
	"testing"
)

// 라틴 원제 허용목록 회귀 — **전부 2026-08-08 DB 실측값**이다.
//
// 종전 가드(`^[ -~]+$`)가 20건 중 18건을 막고 있었고, 그 18건의 비ASCII 문자를 전수로
// 뽑아 이 표를 만들었다. 가짜 예시를 쓰면 다음에 가드를 만질 때 무엇을 지켜야 하는지
// 알 수 없다.
func TestIsLatinOrigin(t *testing.T) {
	cases := []struct {
		ko   string
		want bool
		why  string
	}{
		// ── 통과해야 하는 것 (종전 가드가 막던 실측 18건) ──
		{"It’s 2PM", true, "곡선 아포스트로피 U+2019 — K-pop 표제의 표준 표기"},
		{"Don’t Save Me", true, "동상"},
		{"CHAN’S", true, "동상"},
		{"’Till I Die", true, "선두가 곡선 아포스트로피"},
		{"You’re My Favorite Song", true, "동상"},
		{"DáFF", true, "라틴-1 á"},
		{"ENHYPEN WORLD TOUR ‘BLOOD SAGA’ IN SÃO PAULO", true, "Ã + 곡선 따옴표"},
		{"SYNK : COMPLæXITY", true, "라틴-1 æ"},
		{"BOYNEXTDOOR TOUR ‘KNOCK ON Vol.2’", true, "곡선 따옴표 쌍"},
		{"KARD 2026 WORLD TOUR ‘NOW HERE’ IN SEOUL", true, "동상"},
		{"Mẹ Ơi, Về Nhà", true, "베트남어 — Latin Extended Additional"},
		{"Truy Tìm Long Diên Hương", true, "동상"},
		{"One Last Day ～Japan Special Edition～", true, "전각 물결표는 일본 음반 표기법 — ja 칸에 그대로가 정답"},
		{"2024 Hongseok Fanmeeting［Photo by Hongseok］", true, "전각 대괄호"},
		{"PRESS START♥︎", true, "♥ + 이체자 선택자 U+FE0E"},

		// ── 종전에도 통과하던 것 (순수확장 확인 — 회귀 0) ──
		{"VOYAGER", true, "기존 통과분"},
		{"To Be Continued", true, "기존 통과분"},
		{"KBS Joy", true, "Wikidata 가 zh/ja 라벨로 확인해 준 계층"},

		// ── 막아야 하는 것 ──
		{"BE (Delلuxe Edition)", false, "★Deluxe 의 l 자리에 아랍문자 ل(U+0644) — 오염 제목"},
		{"블랙핑크", false, "한글은 승계 대상이 아니다(mt-fill 담당)"},
		{"찬(灿/Lucid)", false, "한자 혼입 — 어디까지가 이름인지 정할 방법이 없다"},
		{"티빙(TVING)", false, "한글 병기는 DrainParenLatinToEN 담당"},
		{"ドラマ", false, "가타카나"},
		{"Радио", false, "키릴"},
		{"2024", false, "라틴 글자가 없다 — 연도만인 잡음"},
		{"♥", false, "기호만"},
		{"A", false, "1자는 잡음과 구분이 안 된다"},
		{"", false, "빈 문자열"},

		// ── 보이지 않는 문자: 일부러 뺐다 ──
		{"Hello World", false, "NBSP — 눈에 안 보이는 공백은 제목 오염 신호"},
		{"Hello World", false, "narrow NBSP — 동상"},
		{"Hello World", false, "줄바꿈이 제목에 들어갈 이유가 없다"},
		{"Hello‮World", false, "★bidi 제어(RLO) — 표시 순서를 뒤집어 사람 눈과 저장값을 어긋나게 한다"},
	}
	for _, c := range cases {
		if got := IsLatinOrigin(c.ko); got != c.want {
			t.Errorf("IsLatinOrigin(%q) = %v, want %v — %s", c.ko, got, c.want, c.why)
		}
	}
}

// 허용목록은 종전 가드의 **상위집합**이어야 한다. 아니면 이미 채운 500건에 회귀가 난다.
func TestLatinOriginIsSupersetOfASCII(t *testing.T) {
	for r := rune(0x20); r <= 0x7e; r++ {
		s := "Ab" + string(r)
		if !IsLatinOrigin(s) {
			t.Errorf("ASCII 인쇄가능 %q(U+%04X)가 허용목록에서 빠졌다 — 기존 통과분에 회귀", r, r)
		}
	}
}

// MR 가드는 Go regexp 와 SQL 문자열 **두 벌**로 존재한다. 갈라지면 한쪽만 막힌다 —
// 이 저장소가 반복해서 고쳐 온 결함 유형이라(noCurrentCJKVerdict ↔ FillRetryPredicate)
// 같은 문자 집합을 들고 있는지 못 박는다.
func TestMcuneReischauerGuardsAgree(t *testing.T) {
	for _, r := range []rune{'ŏ', 'ŭ', 'Ŏ', 'Ŭ'} {
		if !mcuneReischauerRe.MatchString(string(r)) {
			t.Errorf("Go regexp 가 MR 표지 %q 를 안 잡는다", r)
		}
		if !strings.ContainsRune(latinPropagateSQLGuard, r) {
			t.Errorf("SQL 가드에 MR 표지 %q 가 빠졌다 — 두 벌이 갈라졌다", r)
		}
	}
	// 개정 로마자는 통과해야 한다 — 가드가 정상값을 죽이면 안 된다.
	for _, ok := range []string{"Song Kang-ho", "Yang Soo Kyung", "Lee Su-hyung", "An Yeong-min"} {
		if mcuneReischauerRe.MatchString(ok) {
			t.Errorf("개정 로마자 %q 가 MR 가드에 걸렸다", ok)
		}
	}
	// MR 은 라틴 스크립트라 latinOriginClass 자체는 통과한다 — 그래서 별도 가드가 필요했다.
	if !IsLatinOrigin("Yang Su-kyŏng") {
		t.Error("MR 값이 허용목록에서 걸린다면 별도 가드의 전제가 틀린 것이다")
	}
}

// 기각 사유에 문제 문자가 그대로 남아야 한다 — 이번 진단이 가능했던 이유가 그것이다.
func TestNonLatinChars(t *testing.T) {
	if got := nonLatinChars("BE (Delلuxe Edition)"); got != "ل=U+0644" {
		t.Errorf("nonLatinChars = %q, want %q", got, "ل=U+0644")
	}
	if got := nonLatinChars("2024"); got != "라틴 글자 없음 또는 2자 미만" {
		t.Errorf("전부 허용목록인데 전체일치 실패한 경우의 사유가 틀렸다: %q", got)
	}
}
