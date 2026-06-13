package corrections

import (
	"context"
	"errors"
	"fmt"
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
	Suggested   string
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
	rows, err := pool.Query(ctx, `
SELECT c.id, c.entity_id, COALESCE(e.canonical_ko,''), c.locale,
       c.returned_value, c.suggested_value, c.evidence_url, c.reporter, c.reason, c.created_at
  FROM kwave_kdb_corrections c
  LEFT JOIN kwave_entities e ON e.id = c.entity_id
 WHERE c.status='pending'
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
			&p.Suggested, &p.EvidenceURL, &p.Reporter, &p.Reason, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CountPending — 심사 대기 건수.
func CountPending(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	var n int
	err := pool.QueryRow(ctx,
		`SELECT count(*) FROM kwave_kdb_corrections WHERE status='pending'`).Scan(&n)
	return n, err
}

// Approve — 운영자 승인. suggested 를 적용한다. 운영자 결정이므로 source='operator'
// 로 기록(이후 자동 파이프라인이 덮어쓰지 못하게 최고 우선순위)하고 원값을
// returned_value 에 스냅샷해 revert 가능하게 둔다. 문자셋 가드는 한 번 더 확인.
func Approve(ctx context.Context, pool *pgxpool.Pool, id int64, operator string) error {
	var eid uuid.UUID
	var locale, suggested string
	err := pool.QueryRow(ctx,
		`SELECT entity_id, locale, suggested_value FROM kwave_kdb_corrections
		  WHERE id=$1 AND status='pending'`, id).Scan(&eid, &locale, &suggested)
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
	if !kdb.IsValidSpellingForLocale(normLocale(locale), suggested) {
		return fmt.Errorf("suggested fails %s charset guard — refusing to apply", locale)
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
	if err := tx.QueryRow(ctx, q, eid, suggested).Scan(&old); err != nil {
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
 WHERE id=$1 AND status='pending'`, id, operator, strings.TrimSpace(why))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("correction %d not pending", id)
	}
	return nil
}
