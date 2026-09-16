package corrections

import (
	"os"
	"strings"
	"testing"
)

// ★**빈칸이 «정확》할 수는 없다** (2026-09-16 실측).
//
//	종전엔 현재 값이 비었는지 보지 않고 verdict=="current" 면 기각했다. 소비자가
//	일본어 표기를 보내 줬는데 "현재 값이 정확합니다" 로 거절하고 그 자리를 빈칸으로
//	남겼다. 기각 통보를 받은 소비자는 다시 보내지 않는다.
//
//	실측: 기각 중 현재 값이 비었던 394건 가운데 지금도 비어 있는 것이 18건,
//	그중 17건이 ja 다(도시의 거리·아미새·여우비·봉숭아학당…). 6월 24일 것도
//	아직 빈칸이다.
func TestEmptyCurrentIsNeverRejected(t *testing.T) {
	b, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatalf("verify.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	guard := strings.Index(src, `case v.Verdict == "current" && strings.TrimSpace(cur) == "":`)
	reject := strings.Index(src, `case v.Verdict == "current" && v.Confidence >= 0.7:`)
	if guard < 0 {
		t.Fatal("빈칸 가드가 없다 — 소비자가 준 답을 버리고 빈칸을 유지한다")
	}
	if reject < 0 {
		t.Fatal("기각 분기를 못 찾았다")
	}
	if guard > reject {
		t.Error("빈칸 가드가 기각 분기보다 뒤에 있다 — switch 는 위에서 걸린다, 가드가 무력하다")
	}
	// 빈칸일 때는 기각이 아니라 보류여야 한다.
	blk := src[guard:minI(guard+400, len(src))]
	if strings.Contains(blk, `"rejected"`) {
		t.Error("빈칸인데 기각한다 — 빈칸이 정확하다는 말이 된다")
	}
	if !strings.Contains(blk, `"pending"`) {
		t.Error("빈칸일 때 운영자에게 안 넘긴다 — 신고가 그냥 사라진다")
	}
}

// ★원장에 적는 모델 이름이 **실제로 판정한 모델**이어야 한다.
//
//	2026-09-16 에 codex 를 폐기했는데 이 파일은 계속 "codex 검증" 이라 적고 있었다 —
//	원장에 608건, 폐기 당일에도 20건. 이름표가 사실과 다르면 다음 사람이 그것을 믿고
//	엉뚱한 곳을 판다.
func TestVerdictLabelIsNotHardcodedCodex(t *testing.T) {
	b, err := os.ReadFile("verify.go")
	if err != nil {
		t.Fatalf("verify.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, line := range strings.Split(src, "\n") {
		l := strings.TrimSpace(line)
		if strings.HasPrefix(l, "//") {
			continue // 주석은 사연이다
		}
		if strings.Contains(l, `"codex 검증`) || strings.Contains(l, `'codex 검증`) {
			t.Errorf("판정 라벨에 codex 가 박혀 있다 — 판정하는 것은 gemma 다: %s", l)
		}
	}
	if !strings.Contains(src, "func modelLabel()") {
		t.Error("모델 이름을 실제 라우팅에서 가져오지 않는다")
	}
}

func minI(a, b int) int {
	if a < b {
		return a
	}
	return b
}
