package kdb

import (
	"os"
	"strings"
	"testing"
)

// 앵커 검수는 레인이 한다 — 기본 켜짐, 배선 목록 등재, autopilot 두 경로에 붙음.
// 하나라도 빠지면 감사·판정이 다시 «손으로 돌리는 명령»으로 돌아간다.
func TestAnchorReviewWired(t *testing.T) {
	t.Setenv("KDB_ANCHOR_REVIEW_ENABLED", "")
	if !AnchorReviewEnabled() {
		t.Error("기본값이 꺼짐이다")
	}
	t.Setenv("KDB_ANCHOR_REVIEW_ENABLED", "0")
	if AnchorReviewEnabled() {
		t.Error("0 으로 꺼지지 않는다")
	}
	found := false
	for _, l := range WiredLanes {
		found = found || l == "anchor-review"
	}
	if !found {
		t.Error("anchor-review 가 WiredLanes 에 없다")
	}
	b, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go: %v", err)
	}
	if strings.Count(string(b), "runAutonomousAnchorReview(ctx, pool)") < 2 {
		t.Error("autopilot 분리·일반 두 경로에 다 붙지 않았다")
	}
}

// 판정 모델은 한 번 본 어긋남을 30일 동안 다시 묻지 않는다 — unclear 가 매 회차 맨 앞에 나와
// 같은 10건에 GPT 를 태우며 맴도는 것을 막는다(격리 DB 실측: 10건 → 1건 기록 → 9건).
func TestAnchorJudgeRemembersWhatItJudged(t *testing.T) {
	b, err := os.ReadFile("anchor_judge.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	if !strings.Contains(src, "StoredAnchorVerdictsToJudge(ctx, pool, limit)") {
		t.Error("anchor-judge 가 판정 기록을 거르지 않는 선정(StoredAnchorVerdicts)을 쓴다")
	}
	if !strings.Contains(src, "markAnchorJudged(ctx, pool, m, a.Verdict, by)") {
		t.Error("판정 뒤에 기록하지 않는다")
	}
	e, err := os.ReadFile("anchor_verdict_enforce.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(e), "a.judged_at IS NULL OR a.judged_at < now() - $3::interval") {
		t.Error("판정 대상 선정에 판정 기록 조건이 없다")
	}
	if AnchorJudgeFreshness != AnchorAuditFreshness {
		t.Errorf("판정 주기(%v)와 감사 주기(%v)가 다르다", AnchorJudgeFreshness, AnchorAuditFreshness)
	}
}

// 감사는 안 본 것·낡은 것부터 — 가나다순이면 limit 이 전량보다 작을 때 늘 같은 앞머리만 본다.
func TestAnchorAuditPicksUnseenFirst(t *testing.T) {
	b, err := os.ReadFile("person_anchor_audit.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	i := strings.Index(src, "func AuditPersonAnchors(")
	j := strings.Index(src[i:], "LIMIT $1")
	if i < 0 || j < 0 {
		t.Fatal("감사 선정 쿼리를 못 찾았다")
	}
	q := src[i : i+j]
	if !strings.Contains(q, "a.checked_at NULLS FIRST") || strings.Contains(q, "ORDER BY e.canonical_ko\n") {
		t.Error("감사 선정이 안 본 것부터가 아니다")
	}
}
