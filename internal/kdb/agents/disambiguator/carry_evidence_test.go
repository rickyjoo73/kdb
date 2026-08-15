package disambiguator

import "testing"

// TestTierRank — 병합 승계에서 "더 강한 등급"을 고르는 서열. 이게 뒤집히면 근거 있는
// authoritative 가 unverified 로 덮인다.
func TestTierRank(t *testing.T) {
	if !(tierRank("authoritative") > tierRank("evidenced")) {
		t.Error("authoritative 는 evidenced 보다 높아야 한다")
	}
	if !(tierRank("evidenced") > tierRank("unverified")) {
		t.Error("evidenced 는 unverified 보다 높아야 한다")
	}
	if !(tierRank("unverified") > tierRank("")) {
		t.Error("unverified 는 미설정보다 높아야 한다")
	}
	if tierRank("nonsense") != 0 {
		t.Error("모르는 값은 0(미설정)으로 본다 — 임의 값이 승계를 트리거하면 안 된다")
	}
}

// TestCarryLocalesExcludesKo — `ko` 는 절대 승계하지 않는다.
//
// 승자의 canonical_ko 는 그 엔티티의 정체다. 패자의 ko 는 applyMerge 가 이미
// aliases_ko 로 넣으므로, 여기서 또 옮기면 승자의 이름이 패자 이름으로 바뀐다.
func TestCarryLocalesExcludesKo(t *testing.T) {
	for _, loc := range carryLocales {
		if loc == "ko" {
			t.Fatal("carryLocales 에 ko 가 있으면 승자의 이름이 덮인다")
		}
	}
	// 나머지 8개 로케일은 모두 있어야 한다 — 빠지면 그 칸의 근거가 조용히 사라진다.
	want := map[string]bool{
		"en": true, "ja": true, "zh": true, "zh_hant": true,
		"vi": true, "es": true, "id": true, "pt_br": true,
	}
	got := map[string]bool{}
	for _, loc := range carryLocales {
		got[loc] = true
	}
	for loc := range want {
		if !got[loc] {
			t.Errorf("carryLocales 에 %q 가 빠졌다 — 병합 때 그 로케일 값이 버려진다", loc)
		}
	}
	if len(carryLocales) != len(want) {
		t.Errorf("carryLocales = %v; 예상 %d개", carryLocales, len(want))
	}
}
