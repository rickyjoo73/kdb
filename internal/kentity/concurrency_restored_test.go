package kentity

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// P1.06 동시성 — 두 세션이 실제로 겹칠 때 무슨 일이 벌어지는가.
//
// 지금까지 P1.06 시험 5건은 전부 단일 세션의 제약 검사였다(멱등키·기대 revision·
// 승인 없는 apply·감사 모양). 그런데 병합에서 실제로 위험한 것은 **두 세션이 같은 대상을
// 동시에 건드릴 때**다. 그건 SQL 한 문장으로는 못 만든다.

// concurrentFixture — 합성 legacy 엔티티 두 개. 정리는 넣기 전에 건다.
func concurrentFixture(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := testdb.Restored(t)
	a, b := uuid.New(), uuid.New()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM kwave_entity_external_refs WHERE entity_id = ANY($1)`, []uuid.UUID{a, b})
		_, _ = pool.Exec(bg, `DELETE FROM kentity_id_reservations WHERE external_id LIKE 'Q9E%'`)
		_, _ = pool.Exec(bg, `DELETE FROM kwave_entities WHERE id = ANY($1)`, []uuid.UUID{a, b})
	})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,entity_type,canonical_ko,status) VALUES($1,'person','동시성시험 가',$2),($3,'person','동시성시험 나',$2)`, a, "candidate", b); err != nil {
		t.Fatal(err)
	}
	return pool, a, b
}

// TestRestoredConcurrentExternalIDReservation — 같은 외부 ID 를 두 대상이 동시에 가져갈 수 없다.
//
// 식별 계약 §4: "검증된 한 외부 키의 현재 소유자는 하나다."
func TestRestoredConcurrentExternalIDReservation(t *testing.T) {
	pool, a, b := concurrentFixture(t)
	ctx := context.Background()
	const qid = "Q9E00001"

	var wg sync.WaitGroup
	errs := make([]error, 2)
	start := make(chan struct{})
	for i, id := range []uuid.UUID{a, b} {
		wg.Add(1)
		go func(i int, id uuid.UUID) {
			defer wg.Done()
			<-start
			_, errs[i] = pool.Exec(ctx,
				`INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata',$2)`, id, qid)
		}(i, id)
	}
	close(start)
	wg.Wait()

	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("같은 외부 ID 를 %d개 대상이 가져갔다 (1이어야 한다): %v", ok, errs)
	}
	// 예약이 실제로 주인을 기록해야 한다. NULL 이면 다음 요청자도 막히지 않는다.
	var owner *uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT entity_id FROM kentity_id_reservations WHERE provider='wikidata' AND external_id=$1`, qid).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if owner == nil {
		t.Fatal("예약에 주인이 기록되지 않았다 — 다음 요청자를 막을 근거가 없다")
	}
}

// TestRestoredMergePairLockOrderAvoidsDeadlock — 양방향 병합 요청이 교착하지 않는다.
//
// 두 세션이 같은 두 행을 서로 반대 순서로 잠그면 교착한다. 그래서 잠금은 방향과 무관하게
// **UUID 순서**로 건다. 여기서는 반대 방향 요청을 실제로 동시에 던져 교착이 없음을 본다.
func TestRestoredMergePairLockOrderAvoidsDeadlock(t *testing.T) {
	pool, a, b := concurrentFixture(t)
	lockPair := func(first, second uuid.UUID) error {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='5s'`); err != nil {
			return err
		}
		// merge.go 의 lockMergePair 와 같은 형태 — 요청 방향과 무관하게 ORDER BY id.
		rows, err := tx.Query(ctx, `SELECT id FROM kwave_entities WHERE id=ANY($1) ORDER BY id FOR UPDATE`,
			[]uuid.UUID{first, second})
		if err != nil {
			return err
		}
		for rows.Next() {
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return err
		}
		time.Sleep(150 * time.Millisecond) // 겹치는 구간을 실제로 만든다
		return tx.Commit(ctx)
	}

	var wg sync.WaitGroup
	errs := make([]error, 2)
	pairs := [][2]uuid.UUID{{a, b}, {b, a}}
	for i := range pairs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = lockPair(pairs[i][0], pairs[i][1]) }(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil && strings.Contains(strings.ToLower(err.Error()), "deadlock") {
			t.Fatalf("방향 %d 에서 교착 — UUID 순서 잠금이 깨졌다: %v", i, err)
		}
	}
}

// TestRestoredSerializationConflictIsSurfaced — 직렬화 충돌을 조용히 삼키지 않는다.
//
// merge.go 주석의 계약: "serialization/deadlock failures are surfaced, never silently retried."
// 조용히 재시도하면 두 번째 세션은 자기가 본 적 없는 상태 위에 결정을 쓰게 된다.
func TestRestoredSerializationConflictIsSurfaced(t *testing.T) {
	pool, a, _ := concurrentFixture(t)
	ctx := context.Background()

	tx1, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(context.Background())
	tx2, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		t.Fatal(err)
	}
	defer tx2.Rollback(context.Background())

	// 두 세션이 같은 행을 읽고 각자 쓴다.
	var n int
	if err = tx1.QueryRow(ctx, `SELECT count(*) FROM kwave_entities WHERE id=$1`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if err = tx2.QueryRow(ctx, `SELECT count(*) FROM kwave_entities WHERE id=$1`, a).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if _, err = tx1.Exec(ctx, `UPDATE kwave_entities SET notes='세션1' WHERE id=$1`, a); err != nil {
		t.Fatal(err)
	}
	if err = tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, err2 := tx2.Exec(ctx, `UPDATE kwave_entities SET notes='세션2' WHERE id=$1`, a)
	if err2 == nil {
		err2 = tx2.Commit(ctx)
	}
	if err2 == nil {
		t.Fatal("나중 세션이 앞 세션의 쓰기를 모른 채 통과했다")
	}
	if !strings.Contains(err2.Error(), "40001") && !strings.Contains(strings.ToLower(err2.Error()), "serial") {
		t.Fatalf("직렬화 충돌이 아닌 다른 이유로 실패: %v", err2)
	}
	t.Logf("직렬화 충돌이 호출자에게 그대로 올라왔다: %v", err2)
}

// TestRestoredIdentityOperationRollsBackWhole — 부분 성공을 완료로 보고하지 않는다(I10).
func TestRestoredIdentityOperationRollsBackWhole(t *testing.T) {
	pool, a, b := concurrentFixture(t)
	ctx := context.Background()
	opID := uuid.New()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO kentity_identity_operations
 (id,owner_key,request_key,payload_hash,operation,source_id,target_id,status,plan_hash,actor,reason)
 VALUES($1,'op','req-rollback',repeat('a',64),'merge',$2,$3,'planned',repeat('b',64),'operator','부분 rollback 시험')`,
		opID, a, b)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	// 같은 manifest 안의 두 번째 작업이 실패한다(대상이 없는 redirect).
	_, err = tx.Exec(ctx, `INSERT INTO kentity_redirects(from_id,to_id,operation_id)
 VALUES($1,'00000000-0000-4000-8000-0000000000ff',$2)`, a, opID)
	if err == nil {
		tx.Rollback(ctx)
		t.Fatal("없는 대상으로의 redirect 가 통과했다")
	}
	tx.Rollback(ctx)

	var left int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM kentity_identity_operations WHERE id=$1`, opID).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatal("앞 문장이 남았다 — 부분 성공이 기록됐다:", left)
	}
}
