package kdb

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ★기각 행이 요청을 닫으려면 **같은 유형이어야 한다** (2026-09-15).
//
//	종전엔 이름만 봤다. 기각된 `규림`(show)이 `규림`(character) 요청을 닫았고,
//	기각된 `승우`(person)가 `승우`(character) 요청을 닫았다. 이름이 같다는 것은
//	같은 대상이라는 증거가 아니다(I05: 동명이인은 분리한다).
//	실측: 이 사유로 닫힌 1,319건 중 276건이 소비자가 **다른 유형을 지목한** 건이었다.
func TestRestoredRejectedEntityClosesOnlyTheSameType(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	v := &IntakeAutoVerifier{Pool: pool}

	ko := "자동종결시험대상" + uuid.New().String()[:8]
	rejected := uuid.New()
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entity_research_queue WHERE entity_ko=$1`, ko); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entities WHERE id=$1`, rejected); err != nil {
			t.Error(err)
		}
	})
	// 기각된 show 하나.
	if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entities(id, canonical_ko, entity_type, status)
VALUES ($1, $2, 'show', 'rejected')`, rejected, ko); err != nil {
		t.Fatal(err)
	}
	// 같은 이름으로 들어온 요청 둘 — 하나는 character(다른 대상), 하나는 show(같은 대상).
	for _, typ := range []string{"character", "show"} {
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entity_research_queue
  (entity_ko, requested_entity_type, status, precheck_status, intake_normalized_key)
VALUES ($1, $2::kwave_entity_type, 'done', 'review', $3)`, ko, typ, ko+":"+typ); err != nil {
			t.Fatal(err)
		}
	}

	v.Run(ctx, 1)

	verdict := func(typ string) string {
		var s string
		if err := pool.QueryRow(ctx, `
SELECT precheck_status FROM kwave_entity_research_queue
 WHERE entity_ko=$1 AND requested_entity_type::text=$2`, ko, typ).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	if got := verdict("character"); got == "reject" {
		t.Fatalf("기각된 show 가 character 요청을 닫았다 — 동명은 같은 대상이라는 증거가 아니다 (%s)", got)
	}
	if got := verdict("show"); got != "reject" {
		t.Fatalf("같은 유형의 기각은 닫아야 한다 (%s)", got)
	}
}
