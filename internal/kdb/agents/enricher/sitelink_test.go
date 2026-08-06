package enricher

import "testing"

// fieldsCoveredBy 가 없으면 applyLocaleMap 이 want 전체에 tried 를 찍어, 소스가 값을
// 갖지도 않은 칸이 "시도했으나 실패"로 원장에 남는다 — a022567 이 고친 결함과 같은 것.
func TestFieldsCoveredBy_소스가_값을_가진_칸만(t *testing.T) {
	want := []string{"canonical_ja", "canonical_zh", "canonical_vi", "aliases_ko"}
	m := map[string][]string{"ja": {"李知恩"}}
	got := fieldsCoveredBy(want, m)
	if len(got) != 1 || got[0] != "canonical_ja" {
		t.Fatalf("fieldsCoveredBy = %v, want [canonical_ja]", got)
	}
}

func TestFieldsCoveredBy_빈값은_제외(t *testing.T) {
	want := []string{"canonical_ja"}
	if got := fieldsCoveredBy(want, map[string][]string{"ja": {}}); len(got) != 0 {
		t.Fatalf("빈 값 리스트는 커버로 치면 안 된다: %v", got)
	}
}

// aliases_ko 는 locale 컬럼이 아니라 localeToCode 에 없다. langlink 맵에 ko 가 있어도
// 이 경로로 새면 안 된다(별칭은 L2/L3 담당).
func TestFieldsCoveredBy_로케일_아닌_칸은_제외(t *testing.T) {
	got := fieldsCoveredBy([]string{"aliases_ko"}, map[string][]string{"ko": {"아이유"}})
	if len(got) != 0 {
		t.Fatalf("aliases_ko 는 이 경로로 채우지 않는다: %v", got)
	}
}
