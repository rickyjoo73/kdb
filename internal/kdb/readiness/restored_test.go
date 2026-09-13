package readiness

import (
	"context"
	"hash/fnv"
	"strconv"
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
	// QID 는 실행마다 고유해야 한다. 하드코딩하면 같은 복원 DB 를 쓰는 다른 패키지의
	// 시험과 예약을 두고 다투고, S04(한 외부 키의 주인은 하나) 때문에 나중 쪽이 거부된다.
	qid := fixtureQID(id)
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata',$2)`, id, qid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM kentity_id_reservations WHERE provider='wikidata' AND external_id=$1`, qid)
	})
	in := input("ja", "zh")
	in.Terms[0].EntityID = id.String()
	p, err := s.Create(ctx, owner, "schema-test", in)
	if err != nil {
		t.Fatal(err)
	}
	// 합성 원천이 돌려주는 QID 를 저장된 anchor 와 맞춘다. 다르면 "다른 정체성"으로 막힌다.
	w := &Worker{Store: s, Source: sourceWithQID(qid)}
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

// fixtureQID — UUID 에서 결정적으로 만드는 합성 QID. 형식은 Q + 숫자여야 한다.
func fixtureQID(id uuid.UUID) string {
	h := fnv.New64a()
	_, _ = h.Write(id[:])
	return "Q9" + strconv.FormatUint(h.Sum64()%1_000_000_000, 10)
}
