package kdb

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestReopenNoteCarriesTheClockMarker — 되살림 노트에 시계 표시가 들어간다.
func TestReopenNoteCarriesTheClockMarker(t *testing.T) {
	now := time.Date(2026, 9, 16, 10, 4, 0, 0, time.UTC)
	got := ReopenNote(now, "[scope-reopen] 범위 확대")
	if !strings.HasPrefix(got, "[reopened:2026-09-16]") {
		t.Errorf("시계 표시가 앞에 없다: %q", got)
	}
	if !strings.Contains(got, "범위 확대") {
		t.Errorf("사유가 사라졌다: %q", got)
	}
	if ReopenNote(now, "") != "[reopened:2026-09-16]" {
		t.Errorf("사유가 없을 때 모양이 틀렸다: %q", ReopenNote(now, ""))
	}
	// candidate_ttl 이 읽는 정규식과 **같은 모양**이어야 한다. 하나라도 어긋나면
	// 표시를 붙여도 시계가 안 돌아간다 — 장치가 있는데 안 켜지는 상태가 된다.
	ttlRe := regexp.MustCompile(`\[reopened:([0-9]{4}-[0-9]{2}-[0-9]{2})\]`)
	if !ttlRe.MatchString(ReopenedMarker(now)) {
		t.Errorf("candidate_ttl 의 정규식이 이 표시를 못 읽는다: %q", ReopenedMarker(now))
	}
}

// TestEveryReviveWritesTheClockMarker — **되살리는 모든 코드가 시계를 다시 시작시킨다.**
//
// ★실측 (2026-09-16). candidate_ttl 은 `[reopened:…]` 를 시계의 새 출발점으로 읽도록
// 이미 만들어져 있었다. 그런데 그 표시를 쓰는 곳이 **시험 하나뿐**이었다 —
// 되살리는 세 레인이 전부 자기 문구만 적었다.
//
//	09:39  scope-reopen 이 오세훈을 candidate 로 되살림
//	10:04  candidate_ttl 이 "103일 미결"로 다시 기각   ← 25분
//
// 장치가 있는데 아무도 안 켜면 장치가 없는 것보다 나쁘다. 있다고 믿게 하기 때문이다.
func TestEveryReviveWritesTheClockMarker(t *testing.T) {
	reviveRe := regexp.MustCompile(`SET status='candidate'`)
	var missing []string
	root := "."
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		body := string(b)
		if !reviveRe.MatchString(body) {
			return nil
		}
		// 되살리는 파일은 ReopenNote/ReopenedMarker 를 써야 한다.
		if !strings.Contains(body, "ReopenNote(") && !strings.Contains(body, "ReopenedMarker(") {
			missing = append(missing, path)
		}
		return nil
	})
	if len(missing) > 0 {
		t.Errorf("되살리면서 TTL 시계를 다시 시작시키지 않는 곳이 있다 — 되살린 행이 곧 다시 기각된다:\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestNotATombstoneCoversAllThreeFamilies — 이름을 묻지 못하는 기각 **세 계열**이
// 한 조건에 다 들어 있는지.
//
// ★TTL 을 빼먹으면 이런 일이 난다 (2026-09-16 실측): TTL 이 되살린 행을 다시
// 기각하고, 그 기각 행을 근거로 요청이 `existing_rejected_entity` 로 종결된다.
// TTL 은 설계상 "재요청 시 재발굴"인데 **영구 차단으로 세탁**된다.
func TestNotATombstoneCoversAllThreeFamilies(t *testing.T) {
	for _, alias := range []string{"", "e"} {
		sql := NotATombstoneSQL(alias)
		for _, want := range []string{"[revert-term:reject]", "[ttl-expire:reject]", ScopeRejectionNotePattern} {
			if !strings.Contains(sql, want) {
				t.Errorf("alias=%q: %q 계열이 빠졌다:\n%s", alias, want, sql)
			}
		}
		col := "COALESCE(notes,'')"
		if alias != "" {
			col = "COALESCE(e.notes,'')"
		}
		if !strings.Contains(sql, col) {
			t.Errorf("alias=%q: 칼럼 참조가 %q 가 아니다:\n%s", alias, col, sql)
		}
	}
}
