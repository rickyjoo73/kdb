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
	// ★검사 구간을 **그 case 안으로** 끊는다. 넓게 잡으면 바로 다음 case 의
	//   `rejected` 를 읽고 엉뚱하게 실패한다(처음에 그렇게 짰다).
	blk := src[guard:]
	if k := strings.Index(blk[len(`case v.Verdict == "current" && strings.TrimSpace(cur) == "":`):], "\n\tcase "); k > 0 {
		blk = blk[:k]
	}
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

// TestEveryLedgerBranchNamesTheJudge — 원장에 쓰는 모든 분기가 «누가 판정했는지》를
// 적는지 소스에서 확인한다.
//
// 왜 소스를 읽는 테스트인가: 이 결함은 **분기를 빠뜨려서** 생긴다. 한 분기만 라벨이
// 없어도 그 경로로 나간 건은 판정자를 영영 알 수 없다. 실제 피해가 두 번 있었다.
//
//	2026-06-13~09-16  "codex 검증:" 을 고정 문자열로 박아 610건이 거짓 라벨
//	2026-09-17        고친 줄 알았는데 verify.go 의 current 분기 하나가 라벨 자체
//	                  없이 남아 있었다 — codex 를 켜고 첫 판정에서 드러났다
//
// by 는 «실제로 답한 공급자»(RunP 반환값)이고 modelLabel() 은 «어디로 보내라고 설정돼
// 있는가»다. 원장에는 반드시 전자를 적어야 한다.
func TestEveryLedgerBranchNamesTheJudge(t *testing.T) {
	for _, f := range []string{"verify.go", "review.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 읽기 실패: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.Contains(trimmed, `s.finalize(ctx,`) &&
				!strings.Contains(trimmed, `s.finalizeApply(ctx,`) {
				continue
			}
			// 상태만 바꾸고 문구를 쓰지 않는 호출은 대상이 아니다.
			if !strings.Contains(trimmed, `"`) {
				continue
			}
			if strings.Contains(trimmed, "by+") || strings.Contains(trimmed, "by +") {
				continue
			}
			// 판정자가 없는 종결 문구가 남아 있다 — 다만 «검증» 이라는 말이 없는
			// 순수 상태 전이(예: verifying 표시)는 허용한다.
			if strings.Contains(trimmed, "검증") || strings.Contains(trimmed, "정확") {
				t.Errorf("%s:%d 판정자 이름 없이 원장에 쓴다 — by+ 를 붙여라\n  %s",
					f, i+1, trimmed)
			}
		}
	}
}

// TestModelLabelIsNotWrittenToLedger — 설정값(modelLabel)을 원장에 적지 못하게 한다.
func TestModelLabelIsNotWrittenToLedger(t *testing.T) {
	for _, f := range []string{"verify.go", "review.go"} {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 읽기 실패: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			if !strings.Contains(line, "modelLabel()") {
				continue
			}
			if strings.Contains(line, "s.finalize") || strings.Contains(line, "resolution") {
				t.Errorf("%s:%d 설정값을 원장에 적는다 — 실제 판정자(by)를 써라\n  %s",
					f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
