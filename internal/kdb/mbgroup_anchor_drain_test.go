package kdb

// MusicBrainz 그룹 앵커 레인의 **질의어 선정** 회귀 테스트.
//
// 이 레인이 붙이는 ref 는 authoritativeIdentityProviders 라 앵커 하나가 곧바로
// 최상위 등급이다. 그래서 "무엇을 물어보는가" 자체가 가드다 — 아래 케이스는 전부
// 2026-08-17 전수 실행에서 실제로 나온 값이다.

import "testing"

func TestQueryTerms(t *testing.T) {
	eq := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if got[i] != want[i] {
				return false
			}
		}
		return true
	}

	t.Run("한글 ko + 로마자 en — 둘 다 묻는다", func(t *testing.T) {
		// `티에프앤` 은 ko 로 안 걸리고 en `TFN` 으로 걸렸다. en 을 버리면 정답을 잃는다.
		if got := queryTerms("티에프앤", "TFN"); !eq(got, "티에프앤", "TFN") {
			t.Fatalf("got %v", got)
		}
		// 반대로 `호라이즌` 은 en `HORI7ON` 으로 걸리고 한글 별칭으로 증명됐다.
		if got := queryTerms("호라이즌", "HORI7ON"); !eq(got, "호라이즌", "HORI7ON") {
			t.Fatalf("got %v", got)
		}
	})

	// ★이 케이스가 이 가드를 만든 이유다. `MIDZY` 는 ITZY 의 **팬덤명**인데
	// canonical_en 이 `Itzy` 로 채워져 있었고, 그 en 으로 물어 ITZY(country=KR)에
	// 앵커가 붙었다. 라틴 ko 는 그 자체가 정식 표기이므로 다른 en 은 못 믿는다.
	t.Run("라틴 ko 인데 en 이 딴 이름 — en 을 버린다", func(t *testing.T) {
		if got := queryTerms("MIDZY", "Itzy"); !eq(got, "MIDZY") {
			t.Fatalf("got %v — en 은 버려야 한다", got)
		}
	})

	t.Run("라틴 ko 와 en 이 같으면 그대로", func(t *testing.T) {
		// 표기차(대소문자·구두점)는 같은 이름으로 본다 — NameMatches 규칙을 그대로 쓴다.
		if got := queryTerms("JUSTB", "JUSTB"); !eq(got, "JUSTB") {
			t.Fatalf("got %v", got)
		}
		if got := queryTerms("Team H", "team-h"); !eq(got, "Team H") {
			t.Fatalf("got %v — 같은 이름이면 중복 제거만 된다", got)
		}
	})

	t.Run("괄호 병기 ko 는 한글이 있으므로 en 을 유지한다", func(t *testing.T) {
		// `루시(LUCY)` `UDTT(우당탕탕 소년단)` `방탄소년단(BTS)` 이 이 형태로 걸렸다.
		if got := queryTerms("루시(LUCY)", "LUCY"); !eq(got, "루시(LUCY)", "LUCY") {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("짧은 이름은 묻지 않는다", func(t *testing.T) {
		// 1~2글자는 정확일치가 곧 우연이다(iTunes 레인의 `BE`→은혁 전례).
		if got := queryTerms("AB", ""); len(got) != 0 {
			t.Fatalf("got %v — 조회 대상이 없어야 한다", got)
		}
		if got := queryTerms("있지", "ITZY"); !eq(got, "ITZY") {
			t.Fatalf("got %v — 짧은 ko 는 빠지고 en 만 남아야 한다", got)
		}
	})

	t.Run("빈 값은 조용히 빠진다", func(t *testing.T) {
		if got := queryTerms("", ""); len(got) != 0 {
			t.Fatalf("got %v", got)
		}
	})
}

func TestHasHangulRunes(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{
		{"호라이즌", true},
		{"루시(LUCY)", true},
		{"MIDZY", false},
		{"Team H", false},
		{"", false},
	} {
		if got := hasHangulRunes(c.in); got != c.want {
			t.Fatalf("hasHangulRunes(%q)=%v want %v", c.in, got, c.want)
		}
	}
}
