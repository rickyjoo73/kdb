package enrich

import (
	"os"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// ★이 시험이 지키는 것 (2026-09-23).
//
//	잠정 채움은 «지정한 언어에만, 지정하지 않은 유형에는 안 간다»가 전부다. 새면 근거 없는
//	추측이 다른 언어로 퍼지고, 사람 한자처럼 «읽기는 같고 글자가 틀린» 값이 권위처럼 나간다.

func TestKeepOnlyLocales(t *testing.T) {
	miss := []string{"en", "ja", "zh", "zh_hant"}
	if got := keepOnlyLocales(miss, nil); len(got) != 4 {
		t.Errorf("지정이 없으면 그대로여야 한다: %v", got)
	}
	got := keepOnlyLocales(miss, kdb.ProvisionalLocalesFrom("zh,zh_hant"))
	if len(got) != 2 || got[0] != "zh" || got[1] != "zh_hant" {
		t.Errorf("지정 언어만 남아야 한다: %v", got)
	}
	if len(keepOnlyLocales([]string{"en", "ja"}, kdb.ProvisionalLocalesFrom("zh"))) != 0 {
		t.Error("지정 언어가 빈칸이 아니면 LLM 을 부를 이유가 없다")
	}
}

// TestProvisionalBranchIsGated — 요청 경로의 잠정 갈래가 세 문을 다 거치는지.
//
// 자동 레인에만 있으면 **소비자 요청이 부르는 길**은 계속 빈칸이다. 그래서 이 경로에도
// 넣되, 켜짐(로케일 지정)·strict 가 막은 칸·유형 제외 셋을 전부 봐야 한다.
func TestProvisionalBranchIsGated(t *testing.T) {
	src, err := os.ReadFile("orchestrator.go")
	if err != nil {
		t.Fatalf("orchestrator.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "kdb.SourceLLMProvisional")
	if i < 0 {
		t.Fatal("요청 경로에 잠정 채움이 없다 — 수요 있는 행이 빈칸으로 남는다")
	}
	gate := body[max0(i-700):i]
	for _, want := range []string{"kdb.ProvisionalLocales()", "kdb.EnrichGroundStrict()", "kdb.ProvisionalTypeExcluded(snap.EntityType)", "groundHandled"} {
		if !strings.Contains(gate, want) {
			t.Errorf("잠정 갈래가 %s 를 안 본다", want)
		}
	}
	if !strings.Contains(body, "o.runLLMFill(ctx, snap, wd, nil, kdb.SourceCodexFallback)") {
		t.Error("평소 L4 가 codex-fallback 출처를 잃었다 — 잠정과 섞이면 안 된다")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
