package corrections

import (
	"os"
	"strings"
	"testing"
)

// TestRepairForLocaleFixesTheStuckCorrection — **실제로 멈춰 있던 #3867**.
//
// 「나 혼자 산다」 zh 에 판정기가 번체 `我獨自生活` 를 제안했다. 간체 칸이라 가드가
// 거절했고 DrainProposed 는 조용히 넘겼다 — 3일 17시간, 로그도 사유도 없이.
// 클라가 보낸 값(`我独自生活`)이 처음부터 옳았다.
func TestRepairForLocaleFixesTheStuckCorrection(t *testing.T) {
	got, fixed := repairForLocale("zh", "我獨自生活")
	if !fixed || got != "我独自生活" {
		t.Errorf("간체 칸의 번체를 제 자체로 못 돌렸다: %q(fixed=%v) — 기대 %q", got, fixed, "我独自生活")
	}
	// 반대 방향도.
	if got, fixed := repairForLocale("zh_hant", "我独自生活"); !fixed || got != "我獨自生活" {
		t.Errorf("번체 칸의 간체를 못 돌렸다: %q(fixed=%v)", got, fixed)
	}
	// 이미 옳은 값은 건드리지 않는다.
	for _, c := range []struct{ loc, val string }{
		{"zh", "我独自生活"}, {"zh_hant", "我獨自生活"}, {"ja", "私は一人で暮らす"}, {"en", "I Live Alone"},
	} {
		if got, fixed := repairForLocale(c.loc, c.val); fixed || got != c.val {
			t.Errorf("옳은 값을 바꿨다: %s %q → %q", c.loc, c.val, got)
		}
	}
	// 못 고치는 것은 **그대로 둔다** — 억지로 다듬지 않는다.
	if got, fixed := repairForLocale("ja", "나 혼자 산다"); fixed || got != "나 혼자 산다" {
		t.Errorf("고칠 수 없는 값을 손댔다: %q", got)
	}
}

// TestCharsetNoteSaysWhy — 「보류」라고만 쓰지 않는다.
//
// 원장에 «미통과» 라고만 남으면 다음에 읽는 사람이 원인을 처음부터 다시 판다.
// 오늘 아침 «건너뜀 17» 이 무엇인지 아무도 몰랐던 것과 같은 모양이다.
func TestCharsetNoteSaysWhy(t *testing.T) {
	n := charsetNote("zh", "我獨自生活")
	if !strings.Contains(n, "번체") || !strings.Contains(n, "간체") {
		t.Errorf("무엇이 왜 안 되는지 안 적혀 있다: %q", n)
	}
	if n := charsetNote("ja", "Hello"); n == "" {
		t.Error("사유가 비어 있다")
	}
}

// TestSilentSkipIsGone — 문자셋 실패를 **조용히 넘기는 자리**가 남아 있지 않은가.
func TestSilentSkipIsGone(t *testing.T) {
	src, err := os.ReadFile("review.go")
	if err != nil {
		t.Fatalf("review.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainProposed")
	if i < 0 {
		t.Fatal("DrainProposed 가 없다")
	}
	win := body[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	if !strings.Contains(win, "repairForLocale") {
		t.Error("제 자체로 되돌려 보지 않는다")
	}
	if !strings.Contains(win, "charsetNote") {
		t.Error("못 쓴 이유를 원장에 남기지 않는다 — 7일 뒤 «클라이언트 미응답» 으로 잘못 기록된다")
	}
	// 「클라가 응답 안 했다」는 사유를 우리 잘못에 붙이지 않는다.
	// 주석은 대상이 아니다 — 원장에 **쓰는 문자열**만 본다.
	for _, line := range strings.Split(win, "\n") {
		code := strings.TrimSpace(line)
		if strings.HasPrefix(code, "//") || strings.HasPrefix(code, "--") {
			continue
		}
		if strings.Contains(code, "클라이언트") && strings.Contains(code, "resolution") {
			t.Errorf("우리 수정안이 막힌 것을 클라이언트 탓으로 적고 있다: %s", code)
		}
	}
}

// TestSelfApplyNeedsRealEvidence — «근거가 명확하면 자체 수정» 의 경계.
//
// 오너 지시는 «근거가 명확하면» 이다. 단일 클라이언트 주장만으로 바꾸지 않는다는
// 이 패키지의 신뢰 모델은 그대로다 — 둘 중 하나여야 한다.
func TestSelfApplyNeedsRealEvidence(t *testing.T) {
	src, err := os.ReadFile("selfapply.go")
	if err != nil {
		t.Fatalf("selfapply.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Service) selfApplyReason")
	if i < 0 {
		t.Fatal("selfApplyReason 이 없다")
	}
	win := body[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	if !strings.Contains(win, "s.corroborate(") {
		t.Error("위키데이터 교차검증 없이 자체 반영한다")
	}
	if !strings.Contains(win, "trustedSourceDomain") {
		t.Error("신뢰 도메인 확인이 없다")
	}
	// 빈칸일 때만 신뢰출처 단독 반영. 값이 있는 칸을 도메인만 보고 덮으면
	// «빈칸 > 틀린값» 의 반대편으로 넘어간다.
	if !strings.Contains(win, `strings.TrimSpace(current) == ""`) {
		t.Error("값이 든 칸을 근거 도메인만 보고 덮는다")
	}
}

// TestPriorDecisionReuseHasAnEscapeHatch — 재사용이 **영구 차단**이 되면 안 된다.
//
// 같은 신고를 18번 받아 18번 판정하는 것은 낭비지만, 새 근거가 왔는데도 옛 답을
// 돌려주면 그것은 소비자의 정정을 막는 벽이 된다.
func TestPriorDecisionReuseHasAnEscapeHatch(t *testing.T) {
	src, err := os.ReadFile("corrections.go")
	if err != nil {
		t.Fatalf("corrections.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "priorDecision(")
	if i < 0 {
		t.Fatal("직전 판정 재사용이 없다 — 같은 신고에 매번 LLM 을 다시 쓴다")
	}
	win := body[max0(i-400) : i+700]
	if !strings.Contains(win, "trustedSourceDomain") {
		t.Error("새 근거가 와도 재판정하지 않는다 — 소비자가 고칠 방법이 없어진다")
	}
	if !strings.Contains(win, "s.record(") {
		t.Error("재사용하면서 신고를 기록하지 않는다 — 문서가 «모든 신고는 접수» 라고 약속했다")
	}

	// 창은 30일이다. 무기한이면 나중에 생긴 공식 제목을 영영 못 받는다.
	sa, err := os.ReadFile("selfapply.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sa), "priorDecisionWindow") {
		t.Error("재사용 기간 제한이 없다")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
