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
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_source_policies WHERE provider='fixture' AND version='p1-test'`); err != nil {
			t.Error(err)
		}
	})
	// 이 시험은 준비 기제를 보는 것이지 권리를 보는 것이 아니다. 새 계약(S03)은 ready 에
	// 승인된 원천 정책을 요구하므로, 합성 provider 의 정책도 fixture 로 함께 만든다.
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_source_policies(provider,version,license_code,status,reviewed_by,reviewed_at,storage_allowed,verification_allowed,name_export_allowed,conditions)
 VALUES('fixture','p1-test','CC0-1.0','approved','test',now(),true,true,true,'격리 시험 전용 합성 정책')
 ON CONFLICT(provider,version) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
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
