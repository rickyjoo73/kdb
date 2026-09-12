package disambiguator

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestDistinctReviewStopsReselection(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	a := &Agent{}
	x, y := uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,disambig) VALUES ($1,'김동명','배우'),($2,'김동명','선수')`, x, y)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id    uuid.UUID
		label string
	}{{x, "배우"}, {y, "선수"}} {
		got := a.applyDistinct(ctx, pool, member{id: c.id}, memberResult{Disambig: &c.label})
		if got.Action != agents.ActionNoop {
			t.Fatalf("existing verdict = %s, want noop", got.Action)
		}
	}
	var reviewed time.Time
	if err := pool.QueryRow(ctx, `SELECT disambig_reviewed_at FROM kwave_entities WHERE id=$1`, x).Scan(&reviewed); err != nil {
		t.Fatal(err)
	}
	ids, err := a.Select(ctx, pool, 30)
	if err != nil || len(ids) != 0 {
		t.Fatalf("reviewed pair reselected: %v %v", ids, err)
	}
	// A fresh partner must reopen a cluster without waiting for both old rows
	// to leave cooldown. The old pre-GROUP-BY filter lost this new conflict.
	z := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'김동명')`, z); err != nil {
		t.Fatal(err)
	}
	ids, err = a.Select(ctx, pool, 30)
	if err != nil || len(ids) != 3 {
		t.Fatalf("fresh partner: got %v %v, want all 3", ids, err)
	}
}

func TestDistinctVerdictIdempotenceAndWriteFailures(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	a := &Agent{}
	id := uuid.New()
	label := "선수"
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,needs_disambig) VALUES($1,'테스트 인물',true)`, id); err != nil {
		t.Fatal(err)
	}
	if got := a.applyDistinct(ctx, pool, member{id: id}, memberResult{Disambig: &label}); got.Action != agents.ActionSplit {
		t.Fatal(got)
	}
	if got := a.applyDistinct(ctx, pool, member{id: id}, memberResult{Disambig: &label}); got.Action != agents.ActionNoop {
		t.Fatal(got)
	}
	var notes string
	if err := pool.QueryRow(ctx, `SELECT notes FROM kwave_entities WHERE id=$1`, id).Scan(&notes); err != nil {
		t.Fatal(err)
	}
	if strings.Count(notes, "disambiguator: distinct") != 1 {
		t.Fatalf("duplicate audit breadcrumb: %s", notes)
	}
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := a.applyDistinct(ctx, pool, member{id: id}, memberResult{Disambig: &label}); got.Action != agents.ActionSkipped {
		t.Fatal(got)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if got := a.applyDistinct(cancelled, pool, member{id: id}, memberResult{Disambig: &label}); got.Action != agents.ActionErrored {
		t.Fatal(got)
	}
}
