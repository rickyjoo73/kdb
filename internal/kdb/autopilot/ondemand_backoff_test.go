package autopilot

import (
	"os"
	"strings"
	"testing"
)

// TestOnDemandRetryFollowsDemand — 재시도 주기가 수요를 보는지.
//
// 실측(2026-09-18): candidate 1,346건 중 1,126건(84%)이 7일 쿨다운에 묶여 있었고
// 레인이 한 번에 뽑을 수 있는 것은 6건이었다. 8초마다 돌아도 일감이 없어서 실제
// 발굴 능력이 하루 20건이었다. 스프링 레인은 7일간 13번 요청됐는데 재시도는 7일 뒤였다.
func TestOnDemandRetryFollowsDemand(t *testing.T) {
	src, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatalf("sweep.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Sweeper) ResolveOnDemand(")
	if i < 0 {
		t.Fatal("ResolveOnDemand 가 없다")
	}
	win := body[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	if !strings.Contains(win, "kwave_kdb_request_terms") {
		t.Error("재시도 조건이 요청 기록을 보지 않는다 — 수요가 반영되지 않는다")
	}
	if !strings.Contains(win, "rt.created_at > kwave_entities.last_enriched_at") {
		t.Error("«마지막 시도 이후 새 요청» 조건이 없다")
	}
}

// TestOnDemandKeepsAFloor — 수요가 몰려도 바닥이 있는지.
//
// 쿨다운은 '아리랑' 무한 공회전(22일간 23만 회)을 막으려 넣은 가드다. 수요 조건만
// 달고 바닥을 없애면 그 사고가 그대로 돌아온다. 한 행이 하루 4회를 넘지 않아야 한다.
func TestOnDemandKeepsAFloor(t *testing.T) {
	src, err := os.ReadFile("sweep.go")
	if err != nil {
		t.Fatalf("sweep.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Sweeper) ResolveOnDemand(")
	win := body[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	if !strings.Contains(win, "interval '6 hours'") {
		t.Error("수요 경로에 바닥(6시간)이 없다 — 공회전이 재발할 수 있다")
	}
	// 수요가 없는 행의 기존 7일 주기는 그대로여야 한다.
	if !strings.Contains(win, "interval '7 days'") {
		t.Error("아무도 안 찾는 행의 7일 주기가 사라졌다")
	}
}
