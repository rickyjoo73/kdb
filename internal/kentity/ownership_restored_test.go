package kentity

import (
	"context"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
	"testing"
)

func TestOwnershipAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	id := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO kwave_entities(id,entity_type,canonical_ko,status,notes) VALUES($1,'agency','격리 범위 전환 합성 기관','rejected','합성 사회 기관: 연예 범위 밖이라는 기각')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_ownership_decisions(entity_id,expected_revision,source_fingerprint,selected_type,domains,actor,reason,source_url) SELECT e.id,c.revision,md5(to_jsonb(e)::text),'organization',ARRAY['society'],'restored-fixture','실제 복원 스키마에서 원래 UUID를 유지하는 합성 전환 검증','https://example.test/scope' FROM kwave_entities e JOIN kentity_entities c ON c.id=e.id WHERE e.id=$1`, id); err != nil {
		t.Fatal(err)
	}
	var owner, origin, status, legacy string
	var names, domains int
	if err = tx.QueryRow(ctx, `SELECT c.write_owner,c.origin_system,c.status,e.status,(SELECT count(*) FROM kentity_name_catalog WHERE entity_id=c.id),(SELECT count(*) FROM kentity_entity_domains WHERE entity_id=c.id AND domain='entertainment') FROM kentity_entities c JOIN kwave_entities e ON e.id=c.id WHERE c.id=$1`, id).Scan(&owner, &origin, &status, &legacy, &names, &domains); err != nil {
		t.Fatal(err)
	}
	if owner != "native" || origin != "kdb" || status != "candidate" || legacy != "rejected" || names != 1 || domains != 0 {
		t.Fatal(owner, origin, status, legacy, names, domains)
	}
	// This entire synthetic transaction is rolled back; no real row is changed.
}
