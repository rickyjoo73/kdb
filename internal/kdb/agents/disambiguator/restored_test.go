package disambiguator

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestMergeAgainstRestoredSchemaAndTriggers(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	l, w := uuid.New(), uuid.New()
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_kdb_evidence_refs WHERE entity_id=ANY($1)`, []uuid.UUID{l, w}); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entities WHERE id=ANY($1)`, []uuid.UUID{l, w}); err != nil {
			t.Error(err)
		}
	})
	for _, id := range []uuid.UUID{l, w} {
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,entity_type,disambig) VALUES($1,'격리병합시험','person',$2)`, id, id.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123456')`, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE kwave_entities SET canonical_en='Fixture Name',canonical_en_source='wikidata-label' WHERE id=$1`, l); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_person_details(entity_id,primary_role,birth_year) VALUES($1,'actor',1990)`, l); err != nil {
		t.Fatal(err)
	}
	ms, err := readMembers(ctx, pool, []uuid.UUID{l, w})
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uuid.UUID]member{}
	for _, m := range withWellFormed(ms) {
		byID[m.id] = m
	}
	if got := mergeResult(ctx, pool, byID[l], byID[w]); got.Action != agents.ActionMerged {
		t.Fatal(got)
	}
	var state, value string
	var n int
	if err = pool.QueryRow(ctx, `SELECT l.status,w.canonical_en,(SELECT count(*) FROM kwave_entity_person_details WHERE entity_id=w.id) FROM kwave_entities l,kwave_entities w WHERE l.id=$1 AND w.id=$2`, l, w).Scan(&state, &value, &n); err != nil || state != "rejected" || value != "Fixture Name" || n != 1 {
		t.Fatal(state, value, n, err)
	}
}
