package kdb

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoPromptStillSpeaksTheDeadScope — **프롬프트에 죽은 범위 명제가 남아 있으면 실패한다.**
//
// ★왜 저장소 전체를 훑나 (2026-09-21). 같은 결함을 네 번 찾았다:
//
//	게이트(gatekeeper/intake.go)   2026-09-15 고침
//	verify_evidence.go            2026-09-20 고침
//	type_retrace.go               2026-09-20 고침
//	keyword_triage.go             2026-09-21 고침  ← 세 번 고치고도 빠뜨렸다
//
//	그 사이 triage 는 기각 회수 레인이 되살린 것을 그 자리에서 되죽였다 —
//	국민의힘·SK하이닉스·더불어민주당이 "대중문화 고유명사가 아니다"로 다시 죽었다.
//
//	scope_phrases.go 가 **원장 문구**에 한 일(한 자리에 모으기)을 프롬프트에는 안 했다.
//	프롬프트는 파일마다 흩어져 있어 모을 수 없으니, 대신 **없어야 할 말이 없는지**를
//	기계가 전수로 본다. 다섯 번째가 나오면 이 시험이 먼저 실패한다.
//
// ★무엇을 금지하나. LLM 에게 주는 문자열에서 «한국 대중문화/K-콘텐츠/K-엔터테인먼트»를
//
//	**범위의 정의로** 쓰는 것. 0143 이 정치·경제·시사·스포츠를 넣었으므로 그 정의는
//	죽었다. 반대로 «그것은 기각 사유가 아니다»라고 **부정하는 문장은 허용**한다 —
//	모델에게 옛 틀을 쓰지 말라고 말하려면 그 낱말을 입에 올려야 한다.
func TestNoPromptStillSpeaksTheDeadScope(t *testing.T) {
	// 범위를 정의하는 자리에 쓰인 옛 낱말. "한국 대중문화 …입니다/이다/판별" 꼴.
	deadFrame := regexp.MustCompile(`(한국 )?대중문화\(?K-?콘텐츠\)?|K-엔터테인먼트|한국 대중문화`)
	// 부정문·해설은 면제한다(«…는 기각 사유가 아니다» / 주석의 사고 기록).
	exempt := regexp.MustCompile(`아니다|아닙니다|아니라|않는다|않습니다|금지|기각 사유가|고쳤다|남아 있었다|였다|범위 확대|종전`)

	root := ".."
	type hit struct{ file, line, text string }
	var bad []hit
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		// ★소비자 문서와 변경이력은 프롬프트가 아니다. 거기서는 «범위가 넓어졌다»를
		//   설명하려고 옛 낱말을 **일부러** 쓴다 — 그것이 곧 소비자에게 주는 정보다.
		base := filepath.Base(path)
		if base == "docs.go" || base == "changelog.go" {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			// 주석은 대상이 아니다 — 사고 기록에 옛 문구가 인용된다.
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") ||
				strings.HasPrefix(trimmed, "*") {
				continue
			}
			// LLM 에게 나가는 문자열만 본다: WriteString·프롬프트 리터럴.
			if !strings.Contains(trimmed, `"`) {
				continue
			}
			if !deadFrame.MatchString(trimmed) {
				continue
			}
			if exempt.MatchString(trimmed) {
				continue
			}
			bad = append(bad, hit{path, itoaLocal(i + 1), truncLine(trimmed)})
		}
		return nil
	})
	if len(bad) > 0 {
		var b strings.Builder
		for _, h := range bad {
			b.WriteString("\n  " + h.file + ":" + h.line + "  " + h.text)
		}
		t.Errorf("프롬프트가 아직 죽은 범위(한국 대중문화/K-콘텐츠)를 범위의 정의로 말한다 — "+
			"0143 은 정치·경제·시사·스포츠를 범위 안에 넣었다. 네 번 같은 결함을 찾았으니 "+
			"다섯 번째를 여기서 막는다:%s", b.String())
	}
}

func truncLine(s string) string {
	r := []rune(s)
	if len(r) <= 110 {
		return s
	}
	return string(r[:110]) + "…"
}
