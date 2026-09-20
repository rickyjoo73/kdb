package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoGateJudgesByTheDeadScope — **판정기 프롬프트에 죽은 범위 명제가 남아 있는가.**
//
// ★이게 오늘 찾은 것 중 가장 비쌌다 (2026-09-20).
//
//	게이트(gatekeeper/intake.go)는 2026-09-15 에 0143 으로 올렸다. 그런데 그 뒤에
//	오는 **판정기 둘**(verify_evidence · type_retrace)은 안 고쳤다. 새 유형이 문을
//	통과해 들어온 뒤 거기서 «K-콘텐츠가 아니다»로 다시 죽었다.
//
//	verify_evidence 의 기각 규칙에 이런 말이 그대로 있었다:
//	  "해외 인물, 일반 단어/명사, **의약품·스포츠·정치 등 무관 분야**"
//	스포츠와 정치는 0143 이 **명시적으로 넣은** 분야다.
//
//	실측: [cand-evidence:review] 1,623행 · 옛 범위 문구가 근거인 것 1,133행(70%).
//	그리고 **하루 100~180건씩 새로 찍히고 있었다**(9/16:182 · 9/17:144 · 9/20:146).
//	막힌 candidate 502 중 147 이 company·organization·school·government_body·
//	political_party·sports_team — 정확히 0143 이 추가한 유형이다.
//
// ★scope_phrases.go 는 «같은 명제를 여러 말로 적는» 문제를 원장 노트에서 풀었다.
//
//	이 시험은 같은 것을 **프롬프트**에서 막는다. 문구가 흩어져 있으면 또 나온다.
func TestNoGateJudgesByTheDeadScope(t *testing.T) {
	// 기각·판별 규칙을 만드는 파일들. 새 판정기를 만들면 여기 더한다.
	files := []string{
		"verify_evidence.go",
		"type_retrace.go",
		filepath.Join("..", "agents", "fillverifier", "agent.go"),
	}
	// 프롬프트 **문자열 안**에 있으면 안 되는 말. 주석에는 있어도 된다(왜 고쳤는지 적는다).
	banned := []struct{ phrase, why string }{
		{"의약품·스포츠·정치 등 무관 분야", "스포츠·정치는 0143 이 넣은 범위다"},
		{"한국 대중문화(K-콘텐츠) 고유명사의 '기사맥락 판별기'", "범위가 좁다"},
		{"한국 대중문화(K-콘텐츠) 고유명사의 '유형 판별기'", "범위가 좁다"},
		{"실존하는 한국 대중문화(K-콘텐츠) 엔티티인가", "범위가 좁다"},
		{"한국 대중문화 엔티티가 아예 아님", "기각 사유가 범위로 좁다"},
	}
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 읽기 실패: %v", f, err)
		}
		for _, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // 주석은 통과 — 왜 고쳤는지 적어 둔다
			}
			if !strings.Contains(line, "WriteString") {
				continue
			}
			for _, b := range banned {
				if strings.Contains(line, b.phrase) {
					t.Errorf("%s 프롬프트에 죽은 범위 명제가 있다 — %s:\n  %s", f, b.why, trimmed)
				}
			}
		}
	}
}

// TestTypeRetraceOffersEveryAssignableType — 판별기가 **적을 칸이 있는지**.
//
// ★종전 목록은 11종이었고 0143·0146 으로 늘린 10종이 하나도 없었다. 그러면 판별기가
// «국민의힘» 을 실존한다고 봐도 적을 칸이 없어 가장 가까운 brand_place 로 민다 —
// catchall_retype 의 노트에 그 흔적이 그대로 있다
// ("사용 가능한 분류 중 brand_place가 가장 가깝습니다").
//
// 목록을 손으로 적으면 또 뒤처진다. 한 자리(kdb.AssignableEntityTypes)를 보는지 확인한다.
func TestTypeRetraceOffersEveryAssignableType(t *testing.T) {
	src, err := os.ReadFile("type_retrace.go")
	if err != nil {
		t.Fatalf("type_retrace.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "kdbroot.AssignableEntityTypes()") {
		t.Error("유형 목록을 손으로 적고 있다 — 유형이 늘면 또 뒤처진다")
	}
}
