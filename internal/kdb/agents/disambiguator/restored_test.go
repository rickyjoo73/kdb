package disambiguator

import (
	"context"
	"hash/fnv"
	"strconv"
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
	// 자동 병합 게이트는 "같은 안정 식별자를 공유할 때만" 병합을 허용한다(merge.go).
	// 그런데 S04 는 서로 다른 UUID 가 같은 (provider,external_id) 를 예약할 수 없게 한다.
	// 즉 이 중복 상태는 **이제 새로 만들 수 없고**, 제약 이전에 쌓인 데이터에만 존재한다.
	// (그 모순 자체는 D-36 으로 기록했고 해소는 P5 의 판정 경로 전환이다.)
	// 여기서는 그 과거 상태를 명시적으로 재현한다 — 예약의 주인을 한 번 비우고 두 번째를 넣는다.
	qid := fixtureQID(l)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM kentity_id_reservations WHERE provider='wikidata' AND external_id=$1`, qid)
	})
	for _, id := range []uuid.UUID{l, w} {
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,entity_type,disambig) VALUES($1,'격리병합시험','person',$2)`, id, id.String()); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `UPDATE kentity_id_reservations SET entity_id=NULL WHERE provider='wikidata' AND external_id=$1`, qid); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata',$2)`, id, qid); err != nil {
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

// fixtureQID — UUID 에서 결정적으로 만드는 합성 QID. 하드코딩하면 같은 복원 DB 를 쓰는
// 다른 패키지의 시험과 예약을 두고 다툰다(실제로 readiness 시험과 Q123456 을 공유해 충돌했다).
func fixtureQID(id uuid.UUID) string {
	h := fnv.New64a()
	_, _ = h.Write(id[:])
	return "Q9" + strconv.FormatUint(h.Sum64()%1_000_000_000, 10)
}
