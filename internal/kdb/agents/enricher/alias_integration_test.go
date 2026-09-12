package enricher

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestAliasFillDoesNotFightCleanup(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	a := &Agent{}
	id, owner := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,confidence,aliases_ko) VALUES
 ($1,'동상이몽-너는 내 운명',.7,'{}'),($2,'동상이몽2 - 너는 내 운명',.95,ARRAY['동상이몽2'])`, id, owner)
	if err != nil {
		t.Fatal(err)
	}
	r := &record{id: id, ko: "동상이몽-너는 내 운명"}
	for i := 0; i < 2; i++ {
		if a.appendAliasesKo(ctx, pool, r, []string{"동상이몽2"}) {
			t.Fatal("reported a fill that cleanup would remove")
		}
		if r.writeErr != nil {
			t.Fatal(r.writeErr)
		}
	}
	if !a.appendAliasesKo(ctx, pool, r, []string{"별도 표기"}) {
		t.Fatal("non-conflicting alias was not saved")
	}
	// A stale in-memory snapshot cannot count the same DB value as a new fill.
	stale := &record{id: id, ko: r.ko}
	if a.appendAliasesKo(ctx, pool, stale, []string{"별도 표기"}) {
		t.Fatal("duplicate write reported as new fill")
	}
	var stored []string
	if err := pool.QueryRow(ctx, `SELECT aliases_ko FROM kwave_entities WHERE id=$1`, id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if len(stored) != 1 || stored[0] != "별도 표기" {
		t.Fatalf("unexpected stored aliases: %v", stored)
	}
}

func TestAliasStorageFailureIsNotSourceExhaustion(t *testing.T) {
	pool := testdb.New(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := &record{id: uuid.New(), ko: "원문"}
	if (&Agent{}).appendAliasesKo(ctx, pool, r, []string{"다른 표기"}) || r.writeErr == nil {
		t.Fatal("DB failure must be recorded as retryable storage error")
	}
}
