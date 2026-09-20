package kdb

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 이 결함의 원인은 «세는 자리»와 «집행하는 자리»가 다른 것을 봤다는 것이다.
// backlog-watch 는 495건을 세는데 anchor-withdraw 는 0건을 봤다.
// 둘이 다시 갈리면 경보만 울리고 아무 일도 안 일어난다 — 그래서 시험으로 묶는다.

// anchorInvariantWhere — backlog_watch.go 의 anchor-contradicts-type 불변식 Where 절.
func anchorInvariantWhere(t *testing.T) string {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "kdb", "backlog_watch.go"))
	if err != nil {
		t.Fatalf("read backlog_watch.go: %v", err)
	}
	// 이름을 먼저 찾고, 그 뒤에 오는 첫 Where 블록을 집는다.
	// (Rationale 문구로 찾으면 문구를 고칠 때마다 시험이 깨진다 — 실제로 한 번 깨졌다.)
	idx := strings.Index(string(src), `Name: "anchor-contradicts-type"`)
	if idx < 0 {
		t.Fatal("anchor-contradicts-type 불변식을 못 찾았다 — 시험이 무엇을 지키는지 말할 수 없다")
	}
	m := regexp.MustCompile("(?s)Where:\\s*`([^`]*)`").FindSubmatch(src[idx:])
	if m == nil {
		t.Fatal("anchor-contradicts-type 의 Where 절을 못 찾았다")
	}
	return string(m[1])
}

// TestAnchorEnforcementMatchesInvariant — 경보가 세는 조건 그대로를 집행기가 본다.
func TestAnchorEnforcementMatchesInvariant(t *testing.T) {
	inv := anchorInvariantWhere(t)
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "kdb", "anchor_verdict_enforce.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	enforce := string(src)

	// 불변식이 쓰는 술어 셋. 하나라도 집행기에 없으면 둘이 다른 모집단을 본다.
	for _, pred := range []struct{ name, needle string }{
		{"활성만", "e.status = 'active'"},
		{"판정이 있는 것만", "a.verdict <> ''"},
		{"그 판정이 지금 유형에 대한 것", "a.entity_type = e.entity_type::text"},
		// 이것이 빠지면 고친 것도 계속 센다 — 2026-09-21 에 32건을 철회하고도
		// 경보가 495 그대로였던 이유다.
		{"그 ref 가 아직 붙어 있는 것만", "x.external_id = a.external_id"},
	} {
		// 두 자리의 공백 습관이 다르다(`status='active'` vs `status = 'active'`).
		// 공백을 전부 지우고 비교한다 — 지키려는 것은 술어이지 서식이 아니다.
		norm := func(s string) string {
			return strings.Map(func(r rune) rune {
				if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
					return -1
				}
				return r
			}, s)
		}
		if !strings.Contains(norm(inv), norm(pred.needle)) {
			t.Fatalf("불변식에 %q(%s)가 없다 — 시험의 기준이 낡았다", pred.needle, pred.name)
		}
		if !strings.Contains(norm(enforce), norm(pred.needle)) {
			t.Fatalf("집행기가 %q(%s)를 안 본다 — 경보가 세는 것과 다른 모집단을 집행한다", pred.needle, pred.name)
		}
	}
}

// TestAnchorAutoWithdrawIsNameElementOnly — 자동으로 떼는 것은 name-element 하나뿐이라는
// 2026-09-15 정책이 새 경로에서도 그대로여야 한다. 넓히면 «맞는 근거를 지우고 틀린 유형을
// 남기는» 사고가 난다(씨스타19·엠블랙이 그 예였다).
func TestAnchorAutoWithdrawIsNameElementOnly(t *testing.T) {
	src, err := os.ReadFile(filepath.Join(repoRoot(t), "internal", "kdb", "anchor_verdict_enforce.go"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	s := string(src)
	if !strings.Contains(s, "m.Verdict != AnchorNameElement") {
		t.Fatal("자동 철회를 name-element 로 좁히는 조건이 없다")
	}
	for _, v := range []string{"AnchorTypeMismatch", "AnchorNotHuman", "AnchorHumanOnChar", "AnchorFictional"} {
		if strings.Contains(s, v) {
			t.Fatalf("%s 가 집행 경로에 등장한다 — 이 계열은 사람이 가려야 한다(P4.13)", v)
		}
	}
	// ref 가 여럿이면 어느 것이 표기 출처인지 못 가린다 — 근거 없이 지우지 않는다(D-37).
	if !strings.Contains(s, "refCount != 1") {
		t.Fatal("wikidata ref 가 정확히 1개일 때만 떼는 조건이 없다")
	}
	if !strings.Contains(s, "locked") {
		t.Fatal("운영자 잠금을 건너뛰는 조건이 없다")
	}
}
