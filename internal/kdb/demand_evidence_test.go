package kdb

import (
	"os"
	"strings"
	"testing"
)

// 수요 문턱은 **보수적이어야** 한다. 한 매체의 오식이나 하루 폭주로 열리면 안 된다.
func TestDemandThresholdsAreConservative(t *testing.T) {
	if DemandMinSources < 2 {
		t.Error("출처 1곳으로 열면 그 매체의 오식이 그대로 들어온다")
	}
	if DemandMinDays < 3 {
		t.Error("하루 폭주(같은 기사 재시도)를 수요로 세면 안 된다")
	}
	if DemandWindowDays > 90 {
		t.Error("오래된 수요는 지금의 근거가 아니다")
	}
}

// 수요는 **존재의 근거**이지 표기의 근거가 아니다 — active 로 올리지 않는다.
//
// ★왜 이 선이 중요한가 (2026-09-15).
//   운영자가 반례를 짚었다: "청경채는 야채인데 이런 것도 올라오네."
//   확인해 보니 청경채는 **개그맨 랄랄의 부캐릭터**였고 소비자가 character 로
//   정확히 보냈는데 기각돼 있었다. 겉꼴로는 못 가른다.
//
//   반대쪽도 같다. 수요 문턱에 걸리는 '일반어 기각' 22건을 보니 대부분이 진짜
//   고유명사였다 — 아이들((G)I-DLE) · 있지(ITZY) · 아파트(로제 APT.) · 연인(드라마).
//   철자가 일반명사와 같을 뿐이다.
//
//   그래서 수요는 **후보로만** 연다. 서빙되지 않으니 틀려도 오염이 아니고,
//   표기 근거를 찾는 평소 경로가 진짜인지 가린다. 이것이 "틀린 값보다 빈칸" 이다.
func TestDemandOpensCandidatesNotActive(t *testing.T) {
	b, err := os.ReadFile("demand_evidence.go")
	if err != nil {
		t.Skip(err)
	}
	src := string(b)
	if strings.Contains(src, "status='active'") && !strings.Contains(src, "AND a.status='active'") {
		t.Error("수요 근거로 active 를 쓰고 있다 — 표기 근거 없이 서빙하면 안 된다")
	}
	for _, want := range []string{"'candidate'", "operator_locked=false"} {
		if !strings.Contains(src, want) {
			t.Errorf("%s 가 없다 — 후보로만 열고 운영자 잠금을 지켜야 한다", want)
		}
	}
	// 유형이 붙은 것만 센다. 유형 없는 낱말은 소비자가 분류하지 않은 것이다.
	if !strings.Contains(src, "COALESCE(term_type,'') <> ''") {
		t.Error("유형 없는 요청까지 수요로 세고 있다")
	}
}
