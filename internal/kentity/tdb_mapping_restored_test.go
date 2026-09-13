package kentity

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
	"time"
)

func TestTDBMappingAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	s := &Store{Pool: pool}
	ctx := context.Background()
	sourceID := uuid.New()
	qid := "Q900000000000000001"
	var targetID uuid.UUID
	var existing bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kentity_external_ids WHERE provider='wikidata' AND external_id=$1) OR EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE provider='wikidata' AND external_id=$1)`, qid).Scan(&existing); err != nil || existing {
		t.Fatal("synthetic ID collision", existing, err)
	}
	t.Cleanup(func() {
		for _, q := range []string{`DELETE FROM kentity_tdb_shadow_events WHERE shadow_id IN(SELECT id FROM kentity_tdb_shadows WHERE tdb_id=$1)`, `DELETE FROM kentity_crosswalks WHERE source_system='tdb' AND source_id=$1::uuid::text`, `DELETE FROM kentity_tdb_shadows WHERE tdb_id=$1`} {
			if _, err := pool.Exec(ctx, q, sourceID); err != nil {
				t.Error(err)
			}
		}
		if targetID != uuid.Nil {
			for _, table := range []string{"kentity_candidate_requests", "kentity_audit_events", "kentity_entity_domains", "kentity_names", "kentity_external_ids", "kentity_evidence"} {
				if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE entity_id=$1", targetID); err != nil {
					t.Error(err)
				}
			}
			if _, err := pool.Exec(ctx, `DELETE FROM kentity_id_reservations WHERE entity_id=$1`, targetID); err != nil {
				t.Error(err)
			}
			if _, err := pool.Exec(ctx, `DELETE FROM kentity_entities WHERE id=$1`, targetID); err != nil {
				t.Error(err)
			}
		}
	})
	batch := TDBBindingBatch{Policy: TDBShadowPolicy, Source: "wikidata", License: "CC0", State: "live", Enabled: true, ObservedAt: time.Now(), Bindings: []TDBBinding{{ID: sourceID, QID: qid, Type: "person", Method: "isolated-restored-fixture", Score: 0.9}}}
	if _, err := s.ImportTDBBindings(ctx, "restored-fixture", batch, true); err != nil {
		t.Fatal(err)
	}
	src := fakeResearch{Entities: map[string]*wikidata.Entity{qid: {QID: qid, SourceLabels: map[string]string{"ko": "복원 격리 합성 대상", "en": "Restored Synthetic Identity"}, InstanceOf: []string{"Q5"}}}}
	if ok, err := (&TDBShadowWorker{Store: s, Source: src}).ProcessOne(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	var shadowID uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM kentity_tdb_shadows WHERE tdb_id=$1`, sourceID).Scan(&shadowID); err != nil {
		t.Fatal(err)
	}
	m, err := s.TDBMapping(ctx, shadowID)
	if err != nil {
		t.Fatal(err)
	}
	targetID, err = s.RegisterTDBCandidate(ctx, "restored-fixture", tdbRegistration(m))
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.TDBMapping(ctx, shadowID)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.DecideTDBMapping(ctx, "restored-fixture", tdbDecision(m, targetID)); err != nil {
		t.Fatal(err)
	}
	m, err = s.TDBMapping(ctx, shadowID)
	if err != nil || !m.Current {
		t.Fatal(m, err)
	}
	batch.ObservedAt = time.Now()
	batch.Bindings[0].Locked = true
	if _, err = s.ImportTDBBindings(ctx, "restored-fixture", batch, true); err != nil {
		t.Fatal(err)
	}
	m, err = s.TDBMapping(ctx, shadowID)
	if err != nil || m.Current || m.Status != "conflict" {
		t.Fatal(m, err)
	}
	input := observationBatch(batch, m)
	input.Records[0].Available = false
	input.Records[0].Reason = "place_inactive"
	if report, err := s.ObserveTDBBindings(ctx, input, true); err != nil || report.Unavailable != 1 || report.Changed != 1 {
		t.Fatal(report, err)
	}
	m, err = s.TDBMapping(ctx, shadowID)
	if err != nil || m.Current || m.Shadow.State != "blocked" || m.Shadow.Proposal != nil {
		t.Fatal(m, err)
	}
	var ready int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM kentity_names WHERE entity_id=$1 AND status='verified'`, targetID).Scan(&ready); err != nil || ready != 0 {
		t.Fatal("internal mapping approved serving names", ready, err)
	}
}
