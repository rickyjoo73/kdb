package kentity

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 운영자 규칙(I01·I05)이 가드에 실제로 구현됐는지 **실데이터로** 확인한다.
//
//	I01  한 사람/한 대상 = 하나의 ID. 겸업으로 쪼개지 않는다.
//	     ID 가 갈리는 유일한 이유는 **다른 대상**이라는 것이다.
//	I05  동명이인은 분리한다.
//
// 종전 가드는 이름만 보고 무조건 막았다 — 답을 알아도 영원히 막혔고, 그것이 I05 위반이다.
func TestRestoredHomonymGuardReadsEvidence(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	count := func(where string) int {
		var n int
		q := `SELECT count(*) FROM kentity_entities e
		       WHERE e.write_owner='native' AND e.status='candidate' AND NOT e.operator_locked
		         AND EXISTS (SELECT 1 FROM kentity_entities k
		                      WHERE k.canonical_ko=e.canonical_ko AND k.write_owner='kdb' AND k.status='active')
		         AND ` + where
		if err := pool.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatalf("count(%s): %v", where, err)
		}
		return n
	}

	// 동명인데 가드를 통과하는 것 = 다른 대상임이 증명된 것.
	passed := count(guardNoHomonymTrap)
	// 그중 유형이 대응하지 않는 것은 확실히 다른 대상이다.
	typeMismatch := count(`NOT ` + homonymTypeCompatible + ` AND ` + guardNoHomonymTrap)

	if passed == 0 {
		t.Fatalf("동명 대상 중 가드를 통과하는 것이 하나도 없다 — 근거를 안 읽고 있다")
	}
	if typeMismatch == 0 {
		t.Errorf("유형이 다른데도 통과하는 것이 없다 — 유형 대응표가 안 걸린다")
	}
	t.Logf("동명이지만 다른 대상으로 통과: %d (그중 유형 불일치 %d)", passed, typeMismatch)

	// ★같은 위키데이터 항목이면 **같은 대상**이다. 따로 공급하면 I01 위반이므로 막혀야 한다.
	var sameQIDPassed int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kentity_entities e
	   WHERE e.write_owner='native' AND e.status='candidate' AND NOT e.operator_locked
	     AND EXISTS (SELECT 1 FROM kentity_entities k
	                  WHERE k.canonical_ko=e.canonical_ko AND k.write_owner='kdb' AND k.status='active'
	                    AND EXISTS (SELECT 1 FROM kentity_external_ids nx, kwave_entity_external_refs kx
	                                 WHERE nx.entity_id=e.id AND nx.provider='wikidata' AND nx.status='verified'
	                                   AND kx.entity_id=k.id AND kx.provider='wikidata'
	                                   AND nx.external_id = kx.external_id))
	     AND `+guardNoHomonymTrap).Scan(&sameQIDPassed); err != nil {
		t.Fatalf("same-qid count: %v", err)
	}
	if sameQIDPassed != 0 {
		t.Errorf("같은 위키데이터 항목인데 가드를 통과한 것이 %d건 — I01 위반(한 대상 = 하나의 ID)", sameQIDPassed)
	}
}
