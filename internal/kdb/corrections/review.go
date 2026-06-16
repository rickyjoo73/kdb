package corrections

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// Pending — 심사 대기 정정 1건(운영자 리스트/CLI 용).
type Pending struct {
	ID          int64
	EntityID    uuid.UUID
	Ko          string
	Locale      string
	Returned    string
	Suggested   string // 클라이언트 원 제안
	Proposed    string // KDB(codex) 검증 수정안 — 있으면 이걸 적용해야 함
	EvidenceURL string
	Reporter    string
	Reason      string
	CreatedAt   time.Time
}

// ListPending — 심사 대기 큐를 오래된 순으로 limit 만큼.
func ListPending(ctx context.Context, pool *pgxpool.Pool, limit int) ([]Pending, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	// pending(미해결) + proposed(클라가 confirm 안 한 KDB 수정안 — 운영자가 대신
	// 적용 가능) 모두 노출. 운영자 사각지대 제거.
	rows, err := pool.Query(ctx, `
SELECT c.id, c.entity_id, COALESCE(e.canonical_ko,''), c.locale,
       c.returned_value, c.suggested_value, COALESCE(c.proposed_value,''),
       c.evidence_url, c.reporter, c.reason, c.created_at
  FROM kwave_kdb_corrections c
  LEFT JOIN kwave_entities e ON e.id = c.entity_id
 WHERE c.status IN ('pending','proposed')
 ORDER BY c.created_at ASC
 LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Pending
	for rows.Next() {
		var p Pending
		if err := rows.Scan(&p.ID, &p.EntityID, &p.Ko, &p.Locale, &p.Returned,
			&p.Suggested, &p.Proposed, &p.EvidenceURL, &p.Reporter, &p.Reason, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPending — 심사 대기 건수(pending+proposed).
func CountPending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM kwave_kdb_corrections WHERE status IN ('pending','proposed')`).Scan(&n)
	return n, err
}

// ReapStale — fire-and-forget verifyAsync goroutine 이 배포/재시작/크래시로 죽어
// 'verifying' 에 영구 갇힌 행을 pending(운영자 큐)으로 복구한다. 워커 틱에서 호출.
// 클라이언트 7일 미응답 proposed 도 함께 pending 으로 강등한다.
func ReapStale(ctx context.Context, pool *pgxpool.Pool) {
	tag, err := pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='pending', resolution='검증 미완료(프로세스 재시작) — 운영자 심사'
 WHERE status='verifying' AND created_at < now() - interval '10 minutes'`)
	if err == nil && tag.RowsAffected() > 0 {
		log.Printf("kdb.corrections: reaped %d stale verifying → pending", tag.RowsAffected())
	}
	// proposed 7일 경과 = 클라이언트 미응답 → 운영자 큐로 강등.
	tag2, err2 := pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='pending', resolution='클라이언트 7일 미응답 — 운영자 검토로 전환'
 WHERE status='proposed' AND created_at < now() - interval '7 days'`)
	if err2 == nil && tag2.RowsAffected() > 0 {
		log.Printf("kdb.corrections: %d stale proposed → pending (7d no client response)", tag2.RowsAffected())
	}
}

// DrainWikidataVerified — 이전 source priority 설계로 막혔던 Wikidata 교차검증 완료
// pending 적체를 correction-verified(priority 4)로 재적용해 해소한다.
// 워커 researchTicker 에서 주기 호출.
func DrainWikidataVerified(ctx context.Context, pool *pgxpool.Pool, limit int) (applied int) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := pool.Query(ctx, `
SELECT id, entity_id, locale, COALESCE(NULLIF(proposed_value,''), suggested_value)
  FROM kwave_kdb_corrections
 WHERE status='pending' AND resolution LIKE '%Wikidata%'
 ORDER BY created_at ASC LIMIT $1`, limit)
	if err != nil {
		return 0
	}
	type pending struct {
		id     int64
		eid    uuid.UUID
		locale string
		value  string
	}
	var ps []pending
	for rows.Next() {
		var p pending
		if rows.Scan(&p.id, &p.eid, &p.locale, &p.value) == nil {
			ps = append(ps, p)
		}
	}
	rows.Close()

	for _, p := range ps {
		col, ok := localeCol[normLocale(p.locale)]
		if !ok {
			continue
		}
		if !kdb.IsValidSpellingForLocale(normLocale(p.locale), p.value) {
			continue
		}
		tag, err := pool.Exec(ctx, fmt.Sprintf(`
UPDATE kwave_entities
   SET %[1]s=$2, %[1]s_source=$3, updated_at=now()
 WHERE id=$1
   AND (%[1]s IS NULL OR %[1]s=''
        OR can_replace_canonical(operator_locked, COALESCE(%[1]s_source,''), $3))`, col),
			p.eid, p.value, string(kdb.SourceCorrectionVerified))
		if err != nil || tag.RowsAffected() == 0 {
			continue
		}
		_, _ = pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='auto_applied', resolved_at=now(),
       resolution='Wikidata 교차검증 완료 적체 재처리(correction-verified 적용)'
 WHERE id=$1`, p.id)
		applied++
	}
	if applied > 0 {
		log.Printf("kdb.corrections: drain applied=%d Wikidata-verified pending", applied)
	}
	return applied
}

// Approve — 운영자 승인. KDB(codex) 검증 수정안(proposed_value)이 있으면 그것을,
// 없으면 클라이언트 제안(suggested)을 적용한다 — verdict=other 로 codex 가 제3의
// 올바른 값을 회신한 경우 클라이언트의 (codex 가 기각한) 원 제안을 쓰면 안 되기
// 때문. 운영자 결정이므로 source='operator'(최고 신뢰)로 기록 + 원값 스냅샷(revert).
func Approve(ctx context.Context, pool *pgxpool.Pool, id int64, operator string) error {
	var eid uuid.UUID
	var locale, applyVal string
	err := pool.QueryRow(ctx,
		`SELECT entity_id, locale, COALESCE(NULLIF(proposed_value,''), suggested_value)
		   FROM kwave_kdb_corrections WHERE id=$1 AND status IN ('pending','proposed')`, id).Scan(&eid, &locale, &applyVal)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("correction %d not pending", id)
	}
	if err != nil {
		return err
	}
	col, ok := localeCol[normLocale(locale)]
	if !ok {
		return fmt.Errorf("unsupported locale: %q", locale)
	}
	if !kdb.IsValidSpellingForLocale(normLocale(locale), applyVal) {
		return fmt.Errorf("apply value fails %s charset guard — refusing to apply", locale)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	// 운영자 승인은 operator 우선순위로 적용(operator_locked 자체는 별도이나, 값은
	// 최고 신뢰로 기록). 원값 스냅샷.
	var old string
	q := fmt.Sprintf(`
WITH old AS (SELECT COALESCE(%[1]s,'') v FROM kwave_entities WHERE id=$1)
UPDATE kwave_entities
   SET %[1]s=$2, %[1]s_source='operator', updated_at=now()
 WHERE id=$1
 RETURNING (SELECT v FROM old)`, col)
	if err := tx.QueryRow(ctx, q, eid, applyVal).Scan(&old); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='approved', returned_value=$2,
       resolution = 'operator '||$3||' 승인', resolved_at=now()
 WHERE id=$1`, id, old, operator); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Reject — 운영자 거부(반영 안 함).
func Reject(ctx context.Context, pool *pgxpool.Pool, id int64, operator, why string) error {
	tag, err := pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='rejected', resolved_at=now(),
       resolution = 'operator '||$2||' 거부'||CASE WHEN $3<>'' THEN ': '||$3 ELSE '' END
 WHERE id=$1 AND status IN ('pending','proposed')`, id, operator, strings.TrimSpace(why))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("correction %d not pending", id)
	}
	return nil
}
