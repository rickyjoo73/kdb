package kdbapi

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ★이 시험은 지금 **회귀에서 돌지 않는다.** 사실대로 적어 둔다.
//
//	testdb.New 는 `KDB_TEST_DATABASE_URL` 을 보는데 그 변수를 거는 곳이
//	저장소에 하나도 없다 — 회귀 스크립트도 CI 도 안 건다. 그래서 testdb 를 쓰는
//	시험 20파일 59개가 전부 조용히 t.Skip 된다(2026-09-16 실측).
//	회귀가 쓰는 DB 는 `kdb_platform_migration_test` 인데 testdb 는
//	`kdb_workflow_test` 만 받으므로, 이름을 맞추거나 변수를 걸어야 살아난다.
//
//	그래서 이 파일에서 **실제로 회귀를 지키는 것은 아래 정적 시험 둘**이다
//	(조건이 existing_entity 인가 · 조회가 요청 경로 밖인가). 오늘 만든 결함
//	둘을 잡은 것도 그 둘이다. 여기 DB 시험은 손으로 돌릴 때를 위한 것이다.
//
// ★요청 훅이 고르는 행이 **소비자가 기다리는 그 행**인지 본다.
//
//	이 판정을 틀리면 두 가지로 나빠진다. 좁으면 훅이 아무것도 못 찾아 "장치는
//	있는데 아무 일도 안 일어나는" 자리가 하나 더 생기고, 넓으면 요청과 무관한
//	행(동명이인 분기·중복)을 요청 예산으로 밀게 된다.
func TestWaitingCandidateID(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	s := &Store{Pool: pool}

	mk := func(ko, etype, status string) string {
		var id string
		if err := pool.QueryRow(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, status) VALUES ($1,$2,$3) RETURNING id::text`,
			ko, etype, status).Scan(&id); err != nil {
			t.Fatalf("삽입 실패 %s: %v", ko, err)
		}
		return id
	}

	// ① candidate 만 있다 → 그 행을 고른다. 소비자가 기다리는 행이다.
	wantID := mk("리뉴뮤직", "agency", "candidate")
	if got := s.waitingCandidateID(ctx, "리뉴뮤직", "agency"); got != wantID {
		t.Fatalf("candidate 단독인데 못 찾았다: got=%q want=%q", got, wantID)
	}

	// ② 유형을 안 보낸 소비자도 같은 답을 받아야 한다. 게이트가 existing_entity 를
	//    낼 때 유형을 강제하지 않으므로, 훅만 강제하면 게이트와 어긋난다.
	if got := s.waitingCandidateID(ctx, "리뉴뮤직", ""); got != wantID {
		t.Fatalf("유형 없이 물었을 때 못 찾았다: got=%q", got)
	}

	// ③ 같은 이름에 active 가 있으면 **아무것도 하지 않는다.** 그건 이미 답이
	//    나가는 낱말이고, candidate 쪽은 동명이인 분기이거나 중복이다.
	mk("쿼터뮤직", "agency", "active")
	mk("쿼터뮤직", "person", "candidate")
	if got := s.waitingCandidateID(ctx, "쿼터뮤직", "agency"); got != "" {
		t.Fatalf("active 가 있는데 candidate 를 밀려 했다: got=%q", got)
	}

	// ④ 운영자가 잠근 행은 건드리지 않는다.
	lockedID := mk("잠긴이름", "organization", "candidate")
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1::uuid`, lockedID); err != nil {
		t.Fatal(err)
	}
	if got := s.waitingCandidateID(ctx, "잠긴이름", "organization"); got != "" {
		t.Fatalf("운영자 잠금 행을 골랐다: got=%q", got)
	}

	// ⑤ 없는 이름은 빈 문자열. 오류가 아니다 — 훅을 안 걸 뿐이다.
	if got := s.waitingCandidateID(ctx, "없는이름", "person"); got != "" {
		t.Fatalf("없는 이름에 id 를 냈다: got=%q", got)
	}

	// ⑥ 별칭으로도 찾는다. 게이트의 existing_entity 판정이 aliases_ko 를 보므로
	//    훅이 canonical 만 보면 게이트가 맞다고 한 행을 못 찾는다.
	aliasID := mk("정식이름", "person", "candidate")
	if _, err := pool.Exec(ctx,
		`UPDATE kwave_entities SET aliases_ko=ARRAY['딴이름'] WHERE id=$1::uuid`, aliasID); err != nil {
		t.Fatal(err)
	}
	if got := s.waitingCandidateID(ctx, "딴이름", "person"); got != aliasID {
		t.Fatalf("별칭으로 못 찾았다: got=%q want=%q", got, aliasID)
	}
}

// 훅이 배선 안 된 경우(nil)에도 적재 경로가 그대로 돌아야 한다. 서버 모드가
// 아닌 호출자(시험·CLI)가 이 필드를 안 넣는다.
func TestDemandHookOptionalWiring(t *testing.T) {
	s := &Store{Pool: nil}
	if s.onDemandCandidate != nil {
		t.Fatal("기본값이 nil 이 아니다")
	}
	if got := s.waitingCandidateID(context.Background(), "아무거나", "person"); got != "" {
		t.Fatalf("pool 이 없는데 id 를 냈다: got=%q", got)
	}
}

// ★훅이 **불릴 수 있는 조건**에 걸려 있는가.
//
//	처음엔 `decision.ReasonCode == "existing_entity"` 로 걸었다. 그런데 그 판정은
//	`status='active'` 가 정확히 1건일 때만 켜진다 — candidate 에는 절대 안 걸린다.
//	빌드도 통과하고 시험도 통과하는데 훅은 **한 번도 안 불린다.** 배포했으면
//	"장치는 있는데 아무도 안 켠" 자리가 하나 더 생겼을 것이고, 0건인 이유를
//	한참 뒤에 찾았을 것이다.
//
//	그래서 조건 자체를 시험으로 고정한다.
func TestDemandHookIsNotGatedOnExistingEntity(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("api.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	idx := strings.Index(src, "s.onDemandCandidate(id)")
	if idx < 0 {
		t.Fatal("요청 훅 호출이 없다")
	}
	// 호출 직전 한 뭉치만 본다.
	head := src[max0(idx-600):idx]
	if strings.Contains(head, `ReasonCode == "existing_entity"`) {
		t.Error("훅이 existing_entity 에 걸려 있다 — 그 판정은 active 에만 켜져서 candidate 엔 평생 안 불린다")
	}
	if !strings.Contains(head, "activeMatches == 0") {
		t.Error("active 가 있는데도 훅을 걸고 있다 — 답이 나가는 낱말을 요청 예산으로 민다")
	}
	if !strings.Contains(head, "gatekeeper.IntakeReject") {
		t.Error("기각 판정에도 훅을 건다 — 범위 밖 낱말에 외부 호출을 쓴다")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ★훅의 조회가 **요청 경로 밖**에 있는가.
//
//	정규화 키에 함수 색인이 없어 이 조회는 순차 스캔 44ms 다(실측 2026-09-16,
//	14,569행). 동기로 두면 miss 응답마다 44ms 가 붙고 50낱말 bulk 는 2.2초가 된다.
//	오늘 아침 메뉴 배지에서 같은 실수를 해 회귀가 잡았다(e2b1281) — 같은 자리를
//	두 번 밟지 않게 시험으로 고정한다.
func TestDemandHookLookupIsOffTheRequestPath(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("api.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	call := strings.Index(src, "s.waitingCandidateID(")
	if call < 0 {
		t.Fatal("훅 조회를 못 찾았다")
	}
	head := src[max0(call-400):call]
	if !strings.Contains(head, "go func()") {
		t.Error("훅 조회가 동기다 — miss 응답마다 순차 스캔 한 번이 붙는다")
	}
	if !strings.Contains(head, "context.Background()") {
		t.Error("요청 컨텍스트를 물려받는다 — 소비자가 끊으면 훅도 같이 죽는다")
	}
}
