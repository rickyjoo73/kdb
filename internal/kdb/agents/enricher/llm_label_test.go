package enricher

import (
	"os"
	"strings"
	"testing"
)

// ★원장에 적는 이름은 **실제로 일한 곳**이어야 한다 (2026-09-16).
//
//	2026-09-15 에 codex 를 라우팅에서 걷어냈고 2026-09-16 에 실행 경로까지 폐기했다.
//	그 뒤로 이 일을 한 것은 전부 gemma 인데, 원장은 "gpt-5.5" 로 22,731건을 적어 뒀다.
//
//	숫자가 거짓이면 그 위에 세우는 판단이 전부 거짓이다. 실제로 그 원장을 보고
//	"앱이 codex 를 하루 834번 부른다"고 읽었다 — 앱은 한 번도 부르지 않았다.
func TestLedgerRecordsWhoActuallyDidTheWork(t *testing.T) {
	if llmSourceLabel != "gemma" {
		t.Errorf("원장 이름표가 %q — KDB 의 LLM 은 gemma 하나다", llmSourceLabel)
	}
	for _, f := range []string{"layers.go", "person.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 를 못 읽었다: %v", f, err)
		}
		if strings.Contains(string(b), `"gpt-5.5"`) {
			t.Errorf("%s 에 없는 모델 이름이 박혀 있다 — 원장이 거짓을 적는다", f)
		}
	}
}
