package readiness

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestCommonFillAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool, CommonEnabled: true, CommonFillEnabled: true}
	id, anchor := uuid.New(), uuid.New()
	qid := "Q9999999990121"
	var preparation uuid.UUID
	t.Cleanup(func() {
		if preparation != uuid.Nil {
			if _, err := pool.Exec(ctx, `DELETE FROM kentity_preparations WHERE id=$1`, preparation); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_id_reservations WHERE native_owner=$1`, id); err != nil {
			t.Error(err)
		}
		for _, table := range []string{"kentity_locale_fill_jobs", "kentity_evidence_dependencies", "kentity_names", "kentity_external_ids", "kentity_evidence", "kentity_audit_events"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE entity_id=$1", id); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_entities WHERE id=$1 AND origin_system='native'`, id); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,status) VALUES($1,'person','공통동명','native','candidate')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,license_code,export_allowed,status,verified_by,verified_at) VALUES($1,$2,'wikidata',$3,$4,'identity','CC0-1.0',true,'verified','restored-synthetic-review',now())`, anchor, id, qid, "https://www.wikidata.org/wiki/"+qid); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_external_ids(entity_id,provider,external_id,status,evidence_id) VALUES($1,'wikidata',$2,'verified',$3)`, id, qid, anchor); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kentity_id_reservations(provider,external_id,native_owner) VALUES('wikidata',$1,$2)`, qid, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE kentity_entities SET status='active' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	p, err := s.Create(ctx, "restored-common-fill", id.String(), commonInput(id, "ja"))
	if err != nil {
		t.Fatal(err)
	}
	preparation = p.ID
	src := commonFillSource()
	src.Ent.QID = qid
	if ok, err := (&Worker{Store: s, Source: src}).ProcessCommonOne(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	p, err = s.Get(ctx, "restored-common-fill", p.ID)
	if err != nil || localeOf(t, p, "ja").State != "ready" {
		t.Fatal(p, err)
	}
	if _, err = pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE id=$1`, anchor); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, "restored-common-fill", p.ID)
	if err != nil || localeOf(t, p, "ja").State != "policy_blocked" || localeOf(t, p, "ja").Value != "" {
		t.Fatal("withdrawal not respected", p, err)
	}
}
