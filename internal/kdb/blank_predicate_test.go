package kdb

// canonical_* 는 nullable·기본값 없음이라 실제 빈칸의 대다수가 NULL 이다
// (2026-08-05 실측 active 기준: canonical_zh NULL 1,584 / '' 17,
//  canonical_zh_hant NULL 1,599 / '' 18, canonical_ja NULL 1,034 / '' 1).
// SQL 에서 `NULL = ''` 는 TRUE 가 아니므로, 빈칸을 `canonical_x = ''` 로만 판정하는
// 선택 쿼리는 그 행을 **통째로 못 본다** — 레인은 정상 동작하는 것처럼 로그를 남기고
// 백로그만 안 줄어든다. 실제로 DrainAnchoredRefill(권위 QID refill) 이 이 결함으로
// 앵커 보유 CJK 갭 249건 중 11건만 보고 있었다(나머지 238건 = 95.6% 가 사정거리 밖).
//
// 이건 리뷰로 잡히지 않는 종류의 결함이라 정적 가드로 고정한다. 새 쿼리가 빈칸을
// 판정하려면 `COALESCE(canonical_x,'')=''` 를 쓰거나 같은 줄에 `canonical_x IS NULL`
// 을 함께 적어야 한다.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	// canonical_<locale> = '' 형태의 빈칸 판정.
	blankEqRe = regexp.MustCompile(`canonical_(en|ja|vi|id|es|pt_br|zh_hant|zh)\s*=\s*''`)
	// 같은 줄에서 NULL 을 함께 다루면 안전하다.
	nullSafeRe = regexp.MustCompile(`COALESCE\s*\(\s*canonical_|canonical_[a-z_]+\s+IS\s+NULL`)
)

// repoRoot — 테스트 실행 디렉터리에서 go.mod 를 찾을 때까지 거슬러 올라간다.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod 를 못 찾음 — 리포 루트 판별 실패")
		}
		dir = parent
	}
}

func TestNoNullBlindBlankPredicate(t *testing.T) {
	root := repoRoot(t)
	var offenders []string

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 접근 불가 경로는 건너뛴다(로그·백업 디렉터리 등).
		}
		if info.IsDir() {
			switch info.Name() {
			case ".git", "vendor", "node_modules", "logs", "pre-migration-backup", "snap":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(b), "\n") {
			if !blankEqRe.MatchString(line) || nullSafeRe.MatchString(line) {
				continue
			}
			offenders = append(offenders, rel+":"+itoa(i+1)+"  "+strings.TrimSpace(line))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}

	if len(offenders) > 0 {
		t.Fatalf("NULL 을 못 보는 빈칸 판정 %d 곳 — NULL 빈칸이 선택에서 통째로 빠진다.\n"+
			"COALESCE(canonical_x,'')='' 를 쓰거나 같은 줄에 canonical_x IS NULL 을 함께 적을 것:\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// itoa — strconv 하나 때문에 import 를 늘리지 않는다(테스트 전용, 양수만).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
