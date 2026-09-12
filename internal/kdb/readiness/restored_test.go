package readiness

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestReadinessAgainstRestoredSchemaAndTriggers(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool}
	id := uuid.New()
	owner := "fixture:" + uuid.NewString()
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM kentity_preparations WHERE owner_key=$1`, `DELETE FROM kentity_locale_fill_jobs WHERE entity_id=$1`, `DELETE FROM kwave_kdb_evidence_refs WHERE entity_id=$1`, `DELETE FROM kwave_entities WHERE id=$1`} {
			var arg any = id
			if q == `DELETE FROM kentity_preparations WHERE owner_key=$1` {
				arg = owner
			}
			if _, err := pool.Exec(ctx, q, arg); err != nil {
				t.Error(err)
			}
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,entity_type,canonical_en,canonical_en_source) VALUES($1,'시험인물','person','Test Person','operator')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123456')`, id); err != nil {
		t.Fatal(err)
	}
	in := input("ja", "zh")
	in.Terms[0].EntityID = id.String()
	p, err := s.Create(ctx, owner, "schema-test", in)
	if err != nil {
		t.Fatal(err)
	}
	w := &Worker{Store: s, Source: source()}
	for i := 0; i < 2; i++ {
		if ok, err := w.ProcessOne(ctx); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if err = s.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, owner, p.ID)
	if err != nil || p.Status != "ready" {
		t.Fatal(p, err)
	}
	var fingerprint string
	if err = pool.QueryRow(ctx, `SELECT fill_input_hash FROM kwave_entities WHERE id=$1`, id).Scan(&fingerprint); err != nil || fingerprint == "" {
		t.Fatal("identity trigger did not execute", err)
	}
}
