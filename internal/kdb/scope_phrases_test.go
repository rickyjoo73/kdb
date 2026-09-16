package kdb

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestDeadScopeRejectionMatchesRealNotes — 원장에 **실제로 있던** 기각 문구들.
// 다섯 표현을 하나씩 붙이다 다섯 번 새로 알았다. 실측값을 박아 둔다.
func TestDeadScopeRejectionMatchesRealNotes(t *testing.T) {
	dead := []string{
		"[contam:review] 오염/정크 의심: 비-K(범위밖): 반도체 제조 기업",
		"[contam:review] 오염/정크 의심: 비-K",
		"기각: K-엔터테인먼트 인물이 아님",
		"[audit-revert 2026-07-21] wikidata scope 오염(비연예/오링크) — 서빙 홀드",
		`[revert-term:reject] wikidata Q494239 직업이 비-엔터: "South Korean politician"`,
		"K리그 프로축구단 FC서울(스포츠), K-콘텐츠가 아님",
		"범위 밖으로 판정",
	}
	for _, n := range dead {
		if !IsDeadScopeRejection(n) {
			t.Errorf("옛 범위 기각인데 못 알아봤다: %q", n)
		}
	}
	// 해외 표시가 있으면 그 기각은 범위가 넓어져도 그대로 유효하다.
	alive := []string{
		"비-K: 일본 아이돌 그룹",
		"K-엔터가 아님 — 미국 배우",
		"해외 스포츠 선수라 범위 밖",
	}
	for _, n := range alive {
		if IsDeadScopeRejection(n) {
			t.Errorf("해외 대상 기각은 살아 있어야 한다: %q", n)
		}
	}
	// 범위와 무관한 기각은 건드리지 않는다.
	for _, n := range []string{"merged into 다른 대상", "[ttl-expire:reject] 21일 무근거", "일반어"} {
		if IsDeadScopeRejection(n) {
			t.Errorf("범위와 무관한 기각을 범위 기각으로 봤다: %q", n)
		}
	}
}

// TestNoHardcodedScopePhraseOutsideOnePlace — 범위 기각 문구를 **다른 파일에서 직접**
// 적지 못하게 한다.
//
// 이 시험이 있는 이유: 같은 명제를 세 곳에 따로 적었고, 그중 한 곳(intake_autoverify)
// 에만 두 표현이 들어 있었다. 그래서 오세훈은 되살아난 날 밤에 다시 막혔다.
// 여섯 번째 표현이 나오면 scope_phrases.go 만 고치면 되도록 강제한다.
func TestNoHardcodedScopePhraseOutsideOnePlace(t *testing.T) {
	// 문구가 **정규식 리터럴 안에** 나타나는 경우만 잡는다. 사람이 읽는 주석·
	// 화면 문구는 대상이 아니다.
	sqlRe := regexp.MustCompile(`[~!][~] *'[^']*(비-?K|K-엔터|K-콘텐츠|비-?엔터|비연예)`)
	root := ".."
	var bad []string
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, "scope_phrases.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		for i, line := range strings.Split(string(b), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "--") {
				continue
			}
			if sqlRe.MatchString(line) {
				bad = append(bad, path+":"+itoaLocal(i+1)+"  "+trimmed)
			}
		}
		return nil
	})
	if len(bad) > 0 {
		t.Errorf("범위 기각 문구를 직접 적은 곳이 있다 — kdb.ScopeRejectionNotePattern 을 쓴다:\n  %s",
			strings.Join(bad, "\n  "))
	}
}

func itoaLocal(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
