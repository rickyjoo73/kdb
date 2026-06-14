package enrich

import "testing"

// dataqa 가 비운 값과 enrich 후보값은 표기차(공백/하이픈/중점/대소문자)가 있어도
// 같은 이름이면 suppression 으로 막혀야 한다 — 안 그러면 정규화 틈으로 핑퐁 재발.
func TestIsSuppressed(t *testing.T) {
	snap := &snapshot{
		Suppressed: map[string]map[string]bool{
			"es": {normForSuppress("Ella Gross"): true},
			"ja": {normForSuppress("エラ・マッケンジー・グロス"): true},
		},
	}
	cases := []struct {
		loc, val string
		want     bool
	}{
		{"es", "Ella Gross", true},      // 정확 일치
		{"es", "ella  gross", true},     // 대소문자/공백차 → 정규화 후 일치
		{"es", "Ella-Gross", true},      // 하이픈차
		{"ja", "エラ・マッケンジー・グロス", true}, // 중점 포함
		{"es", "Navi", false},           // 올바른 값은 통과
		{"id", "Ella Gross", false},     // 다른 locale 은 영향 없음
		{"es", "", false},               // 빈 값
	}
	for _, c := range cases {
		if got := snap.isSuppressed(c.loc, c.val); got != c.want {
			t.Errorf("isSuppressed(%q,%q)=%v want %v", c.loc, c.val, got, c.want)
		}
	}
}

// Suppressed 미설정(nil) snapshot 도 panic 없이 false.
func TestIsSuppressedNilSafe(t *testing.T) {
	snap := &snapshot{}
	if snap.isSuppressed("es", "Ella Gross") {
		t.Fatal("nil Suppressed should yield false")
	}
}
