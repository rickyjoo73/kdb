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
	// proposed 48h 미응답 + proposed_value 있음 → 자동 적용(클라 미응답 = KDB 판단 우선).
	// codex 검증 수정안이므로 charset guard 이미 통과 상태.
	DrainProposed(ctx, pool)

	// proposed 7일 경과(자동적용 실패분) = 클라이언트 미응답 → 운영자 큐로 강등.
	tag2, err2 := pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='pending', resolution='클라이언트 7일 미응답 — 운영자 검토로 전환'
 WHERE status='proposed' AND created_at < now() - interval '7 days'`)
	if err2 == nil && tag2.RowsAffected() > 0 {
		log.Printf("kdb.corrections: %d stale proposed → pending (7d no client response)", tag2.RowsAffected())
	}

	// codex 검증 불확실 7일 경과 → 증거 없음으로 자동 기각.
	tag3, err3 := pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='rejected', resolved_at=now(),
       resolution='codex 검증 불확실 7일 경과 — 증거 없음으로 자동 기각'
 WHERE status='pending' AND resolution LIKE '%codex 검증 불확실%'
   AND created_at < now() - interval '7 days'`)
	if err3 == nil && tag3.RowsAffected() > 0 {
		log.Printf("kdb.corrections: %d uncertain → auto-rejected (7d no evidence)", tag3.RowsAffected())
	}
}

// DrainProposed — proposed 48h 미응답 교정을 자동 적용.
// codex 가 확인한 수정안(proposed_value)을 클라 확인 없이 반영.
// 워커 reapStale 내부에서 호출 + 외부(Hermes researchTicker)에서도 직접 호출 가능.
func DrainProposed(ctx context.Context, pool *pgxpool.Pool) (applied int) {
	rows, err := pool.Query(ctx, `
SELECT id, entity_id, locale, proposed_value
  FROM kwave_kdb_corrections
 WHERE status='proposed' AND COALESCE(proposed_value,'') <> ''
   AND created_at < now() - interval '48 hours'
 ORDER BY created_at ASC LIMIT 50`)
	if err != nil {
		return 0
	}
	type item struct {
		id     int64
		eid    uuid.UUID
		locale string
		value  string
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.eid, &it.locale, &it.value) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		col, ok := localeCol[normLocale(it.locale)]
		if !ok {
			continue
		}
		if !kdb.IsValidSpellingForLocale(normLocale(it.locale), it.value) {
			continue
		}
		// codex 검증 수정안 강제 적용 (source priority 무관).
		var old string
		q := fmt.Sprintf(`
WITH old AS (SELECT COALESCE(%[1]s,'') v FROM kwave_entities WHERE id=$1)
UPDATE kwave_entities
   SET %[1]s=$2, %[1]s_source='correction-verified', updated_at=now()
 WHERE id=$1
 RETURNING (SELECT v FROM old)`, col)
		if err := pool.QueryRow(ctx, q, it.eid, it.value).Scan(&old); err != nil {
			continue
		}
		_, _ = pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='auto_applied', resolved_at=now(),
       resolution='codex 수정안 48h 미응답 — 자동 적용'
 WHERE id=$1`, it.id)
		applied++
	}
	if applied > 0 {
		log.Printf("kdb.corrections: DrainProposed applied=%d (48h no client response)", applied)
	}
	return applied
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
 WHERE status='pending'
   AND (resolution LIKE '%Wikidata%' OR resolution LIKE '%보호됨%')
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
		// can_replace_canonical 가드 제거 — Wikidata 검증 교정은 source priority 무관하게 적용.
		var old string
		q := fmt.Sprintf(`
WITH old AS (SELECT COALESCE(%[1]s,'') v FROM kwave_entities WHERE id=$1)
UPDATE kwave_entities
   SET %[1]s=$2, %[1]s_source='correction-verified', updated_at=now()
 WHERE id=$1
 RETURNING (SELECT v FROM old)`, col)
		if err := pool.QueryRow(ctx, q, p.eid, p.value).Scan(&old); err != nil {
			continue
		}
		_ = old
		_, _ = pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='auto_applied', resolved_at=now(),
       resolution='Wikidata 교차검증 완료 — 강제 자동 반영(source priority 무관)'
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

// RetryStuckPending — 운영자 큐(pending)로 밀렸지만 심사자가 없어 정체된 검증 실패류를
// 1회 재검증하고 반드시 종결한다(감사 2026-07-25: 8건이 3주 정체 — '검증 실패/불가'·
// '검증 미완료'는 ReapStale 자동기각의 '검증 불확실' 문구 커버리지 밖이었다).
// 무인 운영 정책: 영구 대기보다 명시적 종결이 소비자 계약("접수하면 결과를 준다")에 맞다.
// 행 선점은 status='verifying' 전이로 원자화 — 겹치는 틱이 같은 행을 두 번 검증하지
// 않고, 프로세스가 중간에 죽으면 ReapStale(10분)이 pending 으로 되돌려 재시도된다.
func (s *Service) RetryStuckPending(ctx context.Context, limit int) (done int) {
	if s.Pool == nil {
		return 0
	}
	// 보호값 충돌은 재검증이 무의미(잠금/출처 우선 정책이 결론) — 즉시 종결.
	_, _ = s.Pool.Exec(ctx, `
UPDATE kwave_kdb_corrections
   SET status='rejected', resolved_at=now(),
       resolution='현재 값 보호(운영자 잠금/출처 우선) — 교정 기각 종결'
 WHERE status='pending' AND resolution LIKE '%현재 값이 보호됨%'
   AND created_at < now() - interval '24 hours'`)
	if s.Judge == nil {
		return 0
	}
	rows, err := s.Pool.Query(ctx, `
UPDATE kwave_kdb_corrections c
   SET status='verifying', resolution='[재검증] 무인 정체 자동 재검증 중'
  FROM kwave_entities e
 WHERE c.id IN (
         SELECT id FROM kwave_kdb_corrections
          WHERE status='pending'
            AND (resolution LIKE '%검증 실패/불가%' OR resolution LIKE '%검증 미완료%'
              OR resolution LIKE '%검증 중 오류%' OR resolution LIKE '%반영 중 오류%')
            AND created_at < now() - interval '24 hours'
          ORDER BY created_at LIMIT $1)
   AND e.id = c.entity_id
 RETURNING c.id, c.entity_id, c.locale, c.suggested_value, e.canonical_ko, e.entity_type::text`, limit)
	if err != nil {
		return 0
	}
	type item struct {
		id        int64
		eid       uuid.UUID
		loc, sug  string
		ko, etype string
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.eid, &it.loc, &it.sug, &it.ko, &it.etype) == nil {
			items = append(items, it)
		}
	}
	rows.Close()
	for _, it := range items {
		loc := normLocale(it.loc)
		col, ok := localeCol[loc]
		if !ok {
			_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
   SET status='rejected', resolved_at=now(), resolution='지원하지 않는 locale — 자동 종결' WHERE id=$1`, it.id)
			done++
			continue
		}
		cur := current(s, ctx, it.eid, col)
		v, vok := s.verify(ctx, it.eid, it.ko, it.etype, loc, cur, it.sug)
		switch {
		case !vok:
			_ = s.finalize(ctx, it.id, "rejected", "재검증 실패 — 증거 불충분, 자동 종결(무인 운영 정책)", "")
		case v.Verdict == "current" && v.Confidence >= 0.7:
			_ = s.finalize(ctx, it.id, "rejected", "재검증: 현재 값이 정확 — "+v.Reason, "")
		case v.Verdict == "suggested" && v.Confidence >= 0.8 && kdb.IsValidSpellingForLocale(loc, it.sug):
			s.finalizeApply(ctx, it.id, it.eid, col, it.sug, "재검증: 제안이 정확 — 반영. "+v.Reason)
		case v.Verdict == "other" && v.Confidence >= 0.8 &&
			strings.TrimSpace(v.CorrectValue) != "" && kdb.IsValidSpellingForLocale(loc, v.CorrectValue):
			// KDB 수정안 회신 — 클라 미응답이어도 DrainProposed(48h)가 자동 종결한다.
			_, _ = s.Pool.Exec(ctx, `UPDATE kwave_kdb_corrections
   SET status='proposed', proposed_value=$2,
       resolution='재검증: KDB 수정안(확인 필요): '||$3 WHERE id=$1`, it.id, v.CorrectValue, v.Reason)
		default:
			_ = s.finalize(ctx, it.id, "rejected", "재검증 불확실 — 증거 없음 자동 기각", "")
		}
		done++
	}
	if done > 0 {
		log.Printf("kdb.corrections: retried %d stuck pending → 종결", done)
	}
	return done
}
