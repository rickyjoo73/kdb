package kentity

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestCommonCatalogAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool}
	var oldCount, newCount, confirmed int
	if err := pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM kwave_entities),(SELECT count(*) FROM kentity_entities c JOIN kwave_entities e ON e.id=c.id WHERE c.origin_system='kdb'),(SELECT count(*) FROM kentity_crosswalks WHERE status='confirmed')`).Scan(&oldCount, &newCount, &confirmed); err != nil {
		t.Fatal(err)
	}
	if oldCount != newCount || confirmed != 0 {
		t.Fatal("projection/mapping mismatch", oldCount, newCount, confirmed)
	}
	es, err := s.Search(ctx, "", "person", "", 5)
	if err != nil || len(es) == 0 {
		t.Fatal(es, err)
	}
	if _, err = s.Get(ctx, es[0].ID); err != nil {
		t.Fatal(err)
	}
	mappings, err := s.Mappings(ctx, "review", 5)
	if err != nil || len(mappings) == 0 {
		t.Fatal("restored mapping listing", err)
	}
	for _, m := range mappings {
		if _, err = s.Mapping(ctx, m.SourceID); err != nil {
			t.Fatal("restored mapping detail", err)
		}
	}
	key := uuid.NewString()
	p, err := s.CreateCandidate(ctx, "restore-fixture", key, CandidateInput{KO: "공통구조격리검증", Type: "person", Domains: []string{"politics", "sports"}, Reason: "격리 DB 합성 검사"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"kentity_candidate_requests", "kentity_audit_events", "kentity_entity_domains", "kentity_names"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE entity_id=$1", p.ID); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_entities WHERE id=$1`, p.ID); err != nil {
			t.Error(err)
		}
	})
	if p.Status != "candidate" || p.Origin != "native" || len(p.Domains) != 2 {
		t.Fatal(p)
	}
	t.Logf("legacy UUID projection %d/%d; no name-only confirmed mappings", newCount, oldCount)
}
