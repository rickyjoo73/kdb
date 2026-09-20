package kdb

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestEntityTypesMatchTheDatabaseEnum — 목록이 **DB enum 과 정확히 같은지.**
//
// ★왜 (2026-09-16). 같은 목록을 세 곳이 따로 들고 있었고 관리 화면 것이 몇 달째
// 틀려 있었다 — 없는 값 4개(work·place·brand·event)가 있고 있는 값 14개가 빠졌다.
// 없는 값을 고르면 화면이 500 이 되고, 빠진 값은 운영자가 아예 고를 수 없다.
// 새 유형을 마이그레이션으로 늘리면 이 시험이 "목록에도 넣어라"라고 말한다.
func TestEntityTypesMatchTheDatabaseEnum(t *testing.T) {
	added := enumValuesFromMigrations(t)
	have := map[string]bool{}
	for _, v := range EntityTypes {
		if have[v] {
			t.Errorf("목록에 %q 가 중복이다", v)
		}
		have[v] = true
	}
	// 마이그레이션이 추가한 값은 **전부** 목록에 있어야 한다.
	for v := range added {
		if !have[v] {
			t.Errorf("마이그레이션이 %q 를 추가했는데 목록에 없다 — 운영자가 고를 수 없다", v)
		}
	}
	// 기반 enum(레거시)까지 합한 전체는 23개다. 실측으로 못박는다 —
	// 목록이 조용히 줄면 그만큼 화면에서 사라진다.
	if len(EntityTypes) != 24 {
		t.Errorf("유형이 %d개다 — 24개여야 한다. 늘렸으면 이 수를 같이 고쳐라", len(EntityTypes))
	}
	// 옛 목록에 있던 **없는 값**들이 되살아나면 안 된다.
	for _, ghost := range []string{"work", "place", "brand", "event"} {
		if have[ghost] {
			t.Errorf("%q 는 enum 에 없는 값이다 — 고르면 화면이 500 이 된다", ghost)
		}
	}
}

// TestAssignableExcludesOnlyThePlaceholders — «무엇인지 모르겠다»는 유형이 아니다.
func TestAssignableExcludesOnlyThePlaceholders(t *testing.T) {
	got := AssignableEntityTypes()
	if len(got) != len(EntityTypes)-len(PlaceholderTypes) {
		t.Errorf("지정 가능 유형 %d개, 기대 %d개", len(got), len(EntityTypes)-len(PlaceholderTypes))
	}
	for _, v := range got {
		if PlaceholderTypes[v] {
			t.Errorf("%q 는 미상 칸인데 지정 목록에 있다", v)
		}
	}
	// 새 유형은 **전부** 지정 가능해야 한다 — 운영자가 인박스에서 승격시켜야 하니까.
	for _, want := range []string{
		"political_party", "government_body", "company", "organization",
		"sports_team", "school", "game", "musical_play", "webtoon", "publication",
	} {
		if !ValidEntityType(want) {
			t.Errorf("%q 가 유효 유형이 아니다", want)
		}
		found := false
		for _, v := range got {
			if v == want {
				found = true
			}
		}
		if !found {
			t.Errorf("%q 를 운영자가 고를 수 없다", want)
		}
	}
}

// TestNobodyKeepsASecondTypeList — 목록을 **두 벌 적지 못하게** 한다.
func TestNobodyKeepsASecondTypeList(t *testing.T) {
	// 옛 목록의 지문. 이 조합이 어딘가에 다시 나타나면 두 벌이 생긴 것이다.
	ghost := regexp.MustCompile(`"work",\s*"place"|"place",\s*"organization"|"organization",\s*"brand"`)
	var bad []string
	_ = filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") ||
			strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		if ghost.Match(b) {
			bad = append(bad, path)
		}
		return nil
	})
	if len(bad) > 0 {
		t.Errorf("옛 유형 목록이 되살아났다 — kdb.EntityTypes 를 써라:\n  %s", strings.Join(bad, "\n  "))
	}
}

func enumValuesFromMigrations(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join("..", "..", "migrations")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("마이그레이션을 못 읽었다: %v", err)
	}
	re := regexp.MustCompile(`ADD VALUE(?: IF NOT EXISTS)? '([a-z_]+)'`)
	out := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		for _, line := range strings.Split(string(b), "\n") {
			if !strings.Contains(line, "kwave_entity_type") {
				continue
			}
			if m := re.FindStringSubmatch(line); m != nil {
				out[m[1]] = true
			}
		}
	}
	return out
}
