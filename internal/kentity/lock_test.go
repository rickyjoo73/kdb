package kentity

import (
	"context"
	"errors"
	"testing"
)

func TestCommonLockPreservesLegacySafetyAndRevision(t *testing.T) {
	s, in := ownershipFixture(t)
	ctx := context.Background()
	if err := s.SetLock(ctx, "fixture", in.ID, in.Revision, true, "전환하지 않은 기존 소유권 보호 검증"); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	if err := s.AdoptLegacy(ctx, "fixture", in); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetLock(ctx, "fixture", in.ID, e.Revision, true, "공통 검수 작업을 잠시 중지하는 합성 요청"); err != nil {
		t.Fatal(err)
	}
	if err = s.SetLock(ctx, "fixture", in.ID, e.Revision, false, "오래된 화면을 통한 잠금 해제 방지 검증"); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetLock(ctx, "fixture", in.ID, e.Revision, false, "기존 KDB 잠금 중 우회 해제 방지 검증"); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=false WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, in.ID)
	if err != nil || !e.Locked {
		t.Fatal(e, err)
	}
	if err = s.SetLock(ctx, "fixture", in.ID, e.Revision, false, "기존 잠금 해제 후 공통 잠금 별도 해제 검증"); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RequestResearch(ctx, "fixture", in.ID, e.Revision, "잠금 해제 이후 현재 버전의 조사 재요청 검증"); err != nil {
		t.Fatal(err)
	}
	j, err := s.Resolution(ctx, in.ID)
	if err != nil || j.Revision != e.Revision {
		t.Fatal(j, err)
	}
	var n int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_audit_events WHERE entity_id=$1 AND action='operator_lock_changed'`, in.ID).Scan(&n); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}
