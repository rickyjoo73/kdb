package kdb

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ★되살린 행은 TTL 시계를 다시 시작한다 (2026-09-15).
//
//	`JTBC` 를 기각에서 후보로 되살렸더니 28분 만에 다시 기각됐다 — created_at 이
//	83일 전이라 TTL 이 즉시 걸렸다. 그 83일 중 대부분은 **기각돼 있던 기간**이고,
//	기각은 미결이 아니라 결론이다. 그 시간을 "기한 내 실증 실패"로 세면 오래된 행은
//	되살릴 기회를 한 번도 못 얻는다.
func TestRestoredTTLClockRestartsOnReopen(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	old := uuid.New()
	reopened := uuid.New()
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_kdb_recheck_log WHERE entity_id = ANY($1)`,
			[]uuid.UUID{old, reopened}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entities WHERE id = ANY($1)`,
			[]uuid.UUID{old, reopened}); err != nil {
			t.Error(err)
		}
	})
	ago := time.Now().AddDate(0, 0, -83)
	mk := func(id uuid.UUID, ko, notes string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entities(id, canonical_ko, entity_type, status, created_at, notes)
VALUES ($1,$2,'person','candidate',$3,$4)`, id, ko, ago, notes); err != nil {
			t.Fatal(err)
		}
	}
	mk(old, "티티엘오래된"+old.String()[:8], "")
	mk(reopened, "티티엘되살림"+reopened.String()[:8],
		"[reopened:"+time.Now().Format("2006-01-02")+"]")

	// 반환값은 (rejected, checked) 둘이다 — 오류를 돌려주지 않는다.
	DrainExpireStaleCandidates(ctx, pool, 500)

	st := func(id uuid.UUID) string {
		var s string
		if err := pool.QueryRow(ctx, `SELECT status FROM kwave_entities WHERE id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := st(old); got != "rejected" {
		t.Fatalf("83일 미결인데 종결이 안 됐다: %s", got)
	}
	if got := st(reopened); got != "candidate" {
		t.Fatalf("오늘 되살린 행이 즉시 종결됐다 — 되살릴 기회를 못 얻는다: %s", got)
	}
}
