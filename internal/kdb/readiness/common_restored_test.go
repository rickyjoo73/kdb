package readiness

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestCommonReadinessAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool, CommonEnabled: true}
	id, evidence := uuid.New(), uuid.New()
	var preparationID uuid.UUID
	t.Cleanup(func() {
		if preparationID != uuid.Nil {
			if _, err := pool.Exec(ctx, `DELETE FROM kentity_preparations WHERE id=$1`, preparationID); err != nil {
				t.Error(err)
			}
		}
		for _, table := range []string{"kentity_names", "kentity_external_ids", "kentity_evidence", "kentity_entity_domains", "kentity_audit_events"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE entity_id=$1", id); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_entities WHERE id=$1 AND origin_system='native'`, id); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,status) VALUES($1,'organization','격리 검수 기관','native','candidate')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_url,license_code,export_allowed,status,verified_by,verified_at) VALUES($1,$2,'fixture','https://example.test/restored','CC0-1.0',true,'verified','restored-fixture',now())`, evidence, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,'en','Restored Organization','canonical','recorded','verified',$2,'wikidata-label')`, id, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE kentity_entities SET status='active',revision=revision+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	p, err := s.Create(ctx, "restored-common-fixture", id.String(), Input{Catalog: "common", Terms: []Term{{KO: "격리 검수 기관", Type: "organization", EntityID: id.String()}}, Locales: []string{"en", "zh-Hans"}})
	if err != nil {
		t.Fatal(err)
	}
	preparationID = p.ID
	if localeOf(t, p, "en").State != "ready" || localeOf(t, p, "zh-Hans").State != "no_evidence" {
		t.Fatal(p)
	}
	if _, err = pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE id=$1`, evidence); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, "restored-common-fixture", p.ID)
	if err != nil || localeOf(t, p, "en").State != "policy_blocked" || localeOf(t, p, "en").Value != "" {
		t.Fatal(p, err)
	}
}
