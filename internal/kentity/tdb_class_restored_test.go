package kentity

import (
	"context"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestTDBClassConflictAgainstRestoredSnapshot(t *testing.T) {
	s := &Store{Pool: testdb.Restored(t)}
	ctx := context.Background()
	rows, err := s.TDBShadows(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	for _, r := range rows {
		if !r.ClassConflict {
			continue
		}
		conflicts++
		m, err := s.TDBMapping(ctx, r.ID)
		if err != nil || m.Fresh || m.Current || !m.ClassConflict {
			t.Fatal("stored conflicting source still eligible", m, err)
		}
	}
	t.Logf("read-only restored source observations=%d; class-conflicting sources blocked=%d", len(rows), conflicts)
}
