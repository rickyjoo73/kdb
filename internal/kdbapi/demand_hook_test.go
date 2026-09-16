package kdbapi

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

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
