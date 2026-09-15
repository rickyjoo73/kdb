package kdbapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ★소비자가 보낸 유형을 발굴 큐까지 넘긴다 (2026-09-15).
//
//	종전엔 lookup miss 를 이름만 들고 넘겼다. 그래서 그 요청은 전부 유형 미상이 되고,
//	게이트가 `missing_or_unsupported_type` 으로 review 에 쌓았다 — 그 사유로 쌓인
//	190건 중 **끝내 채워진 것은 0건**이다.
//	정작 소비자들은 유형을 100% 붙여 보내고 있었다. 받아 놓고 우리가 버렸다.
func TestRestoredLookupMissKeepsTheConsumerType(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	h := &handler{store: &Store{Pool: pool}}

	ko := "유형보존시험" + uuid.New().String()[:8]
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entity_research_queue WHERE entity_ko=$1`, ko); err != nil {
			t.Error(err)
		}
	})

	h.enqueueDiscovery(ko, "character")

	// enqueueDiscovery 는 async 다. 행이 생길 때까지 짧게 기다린다.
	var got string
	for i := 0; i < 50; i++ {
		if err := pool.QueryRow(ctx, `
SELECT COALESCE(requested_entity_type::text,'') FROM kwave_entity_research_queue
 WHERE entity_ko=$1`, ko).Scan(&got); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if got != "character" {
		t.Fatalf("발굴 큐의 유형 = %q, want character — 소비자가 보낸 유형을 버렸다", got)
	}
}

// 유효하지 않은 유형은 지어내지 않고 비운다 — 잘못된 유형은 미상보다 나쁘다.
func TestLookupDiscoveryDropsInvalidType(t *testing.T) {
	if validEntityType("헛소리") {
		t.Fatal("validEntityType 가 아무 문자열이나 받는다")
	}
}
