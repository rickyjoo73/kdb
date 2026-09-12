package kentity

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
	"time"
)

func TestTDBShadowAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	s := &Store{Pool: pool}
	ctx := context.Background()
	id := uuid.New()
	roots, names, links := shadowCount(t, s, "kentity_entities"), shadowCount(t, s, "kentity_names"), shadowCount(t, s, "kentity_crosswalks")
	b := TDBBindingBatch{Policy: TDBShadowPolicy, Source: "wikidata", License: "CC0", State: "live", Enabled: true, ObservedAt: time.Now().UTC(), Bindings: []TDBBinding{{ID: id, QID: "Q123", Method: "restored-synthetic-ID-claim", Score: 0.9}}}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_tdb_shadow_events WHERE shadow_id IN(SELECT id FROM kentity_tdb_shadows WHERE tdb_id=$1)`, id); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_tdb_shadows WHERE tdb_id=$1`, id); err != nil {
			t.Error(err)
		}
	})
	if r, err := s.ImportTDBBindings(ctx, "restored-fixture", b, false); err != nil || r.Created != 1 {
		t.Fatal(r, err)
	}
	if _, err := s.ImportTDBBindings(ctx, "restored-fixture", b, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&TDBShadowWorker{Store: s, Source: researchSource()}).ProcessOne(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	var state string
	var records int
	if err := pool.QueryRow(ctx, `SELECT state,jsonb_array_length(result->'names') FROM kentity_tdb_shadows WHERE tdb_id=$1`, id).Scan(&state, &records); err != nil || state != "review" || records < 1 {
		t.Fatal(state, records, err)
	}
	if shadowCount(t, s, "kentity_entities") != roots || shadowCount(t, s, "kentity_names") != names || shadowCount(t, s, "kentity_crosswalks") != links {
		t.Fatal("shadow changed existing masters")
	}
}
