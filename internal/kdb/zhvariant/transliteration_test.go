package zhvariant

import "testing"

// 음차 판별은 규칙 하나로 끝나야 한다 — 가운뎃점·붙임표.
func TestLooksTransliterated(t *testing.T) {
	yes := []string{"基姆·申-洛克", "金·凯瑞", "朴·寶劍", "李-英愛"}
	for _, s := range yes {
		if !LooksTransliterated(s) {
			t.Errorf("%q 는 음차 꼴인데 아니라고 했다", s)
		}
	}
	no := []string{"金信祿", "金信禄", "朴寶劍", "少女時代", "寄生蟲"}
	for _, s := range no {
		if LooksTransliterated(s) {
			t.Errorf("%q 는 한자 이름인데 음차라고 했다", s)
		}
	}
}

// 김신록(Q107109045) — 위키데이터 zh 라벨이 로마자 음차이고 zh-Hant 가 한자 이름이다.
// 기준을 간체 칸으로 고정하면 음차가 살아남아 중국어 기사에 «基姆·申-洛克» 가 나간다.
func TestPreferNativeHanPicksTheHanjaName(t *testing.T) {
	if got := PreferNativeHan("基姆·申-洛克", "金信祿"); got != "金信祿" {
		t.Errorf("기준 = %q, 기대 金信祿", got)
	}
	// 반대 방향도 막는다(번체 칸이 음차인 경우).
	if got := PreferNativeHan("金信禄", "基姆·申-洛克"); got != "金信禄" {
		t.Errorf("기준 = %q, 기대 金信禄", got)
	}
	// 둘 다 멀쩡하면 종전대로 간체 칸이 기준이다(동작 보존).
	if got := PreferNativeHan("金信禄", "金信祿"); got != "金信禄" {
		t.Errorf("기준 = %q, 기대 金信禄(기존 동작)", got)
	}
	// 한쪽이 비면 있는 쪽.
	if got := PreferNativeHan("", "金信祿"); got != "金信祿" {
		t.Errorf("기준 = %q, 기대 金信祿", got)
	}
	if got := PreferNativeHan("金信禄", ""); got != "金信禄" {
		t.Errorf("기준 = %q, 기대 金信禄", got)
	}
}
