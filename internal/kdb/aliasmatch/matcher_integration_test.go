package aliasmatch

import (
	"context"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestPostgresTrigramOperatorAndErrors(t *testing.T) {
	pool := testdb.New(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(canonical_ko) VALUES('한국국제문화교류재단')`); err != nil {
		t.Fatal(err)
	}
	matches, err := Find(ctx, pool, "한국국제문화교류재딘")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range matches {
		if m.Kind == KindTypo {
			found = true
		}
	}
	if !found {
		t.Fatal("typo SQL silently failed", matches)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := Find(cancelled, pool, "시험"); err == nil {
		t.Fatal("database error hidden as no matches")
	}
}
