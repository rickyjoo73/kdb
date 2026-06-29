package kdb

// claude_adjudicate — 2단계 판정의 최종단계. Gemma 거름망이 [scope:review]/[contam:review] 로
// 플래그한 의심군만 claude(Sonnet)로 최종 판정한다:
//   - 비-K/정크 확정(k_entity=false) → reject(오염DB). autoReject=false 면 권고만 마킹.
//   - 실제 K 확정(k_entity=true)     → 플래그 해제 + [*:ok] (Claude 구제 — Gemma 과플래그 복구).
//   - 판정실패/timeout              → 마커 안 건드리고 다음 회차 재시도.
// 모든 reject 는 notes 에 '[adjudicated:claude] <근거>' 감사기록(누가/왜 거부했는지) — 추후
// 검토/복원 가능. 저볼륨(플래그분만)·직렬(claudejudge 내부 mutex). claude 미설치 시 no-op.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/claudejudge"
)

// DrainClaudeAdjudicate — 플래그 의심군 n건 최종판정. 반환=(판정수, reject수, 구제수).
func DrainClaudeAdjudicate(ctx context.Context, pool *pgxpool.Pool, cl *claudejudge.Client, limit int, autoReject bool) (judged, rejected, rescued int) {
	if pool == nil || cl == nil || limit <= 0 {
		return 0, 0, 0
	}
	if !cl.Available() {
		log.Printf("kdb.claude-adjudicate: claude 바이너리 없음 — skip")
		return 0, 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text,
       COALESCE(canonical_en,''), COALESCE(canonical_ja,''), COALESCE(canonical_zh,''), COALESCE(canonical_vi,'')
  FROM kwave_entities
 WHERE status='active' AND operator_locked=false
   AND (notes LIKE '%[scope:review]%' OR notes LIKE '%[contam:review]%')
   AND notes NOT LIKE '%[adjudicated:%'
 ORDER BY updated_at ASC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.claude-adjudicate: select: %v", err)
		return 0, 0, 0
	}
	type row struct{ id, ko, t, en, ja, zh, vi string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.t, &r.en, &r.ja, &r.zh, &r.vi) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		sp := map[string]string{"en": it.en, "ja": it.ja, "zh": it.zh, "vi": it.vi}
		v, err := cl.JudgeKEntity(ctx, it.ko, it.t, sp)
		if err != nil || v == nil {
			log.Printf("kdb.claude-adjudicate: %q 판정실패(재시도): %v", it.ko, err)
			continue // 마커 유지, 다음 회차
		}
		judged++
		reason := strings.TrimSpace(v.Reason)
		if len(reason) > 200 {
			reason = reason[:200]
		}
		if !v.KEntity {
			// 비-K/정크 확정 — reject(오염DB) 또는 권고 마킹.
			if autoReject {
				tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence=0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[adjudicated:claude reject] ' || $2,
       updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false`, it.id, reason)
				if tag.RowsAffected() == 1 {
					rejected++
					log.Printf("kdb.claude-adjudicate: REJECT %q [%s] — %s", it.ko, it.t, reason)
				}
			} else {
				_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[adjudicated:claude reject-recommended] ' || $2, updated_at=now()
 WHERE id=$1 AND status='active'`, it.id, reason)
				rejected++ // 권고 카운트(실제 reject 아님)
				log.Printf("kdb.claude-adjudicate: REJECT-REC %q [%s] — %s", it.ko, it.t, reason)
			}
			continue
		}
		// 실제 K 확정 — review 마커 제거 후 ok 마킹(Claude 구제).
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = regexp_replace(COALESCE(notes,''), '\s*·?\s*\[(scope|contam):review\][^·]*', '', 'g')
       || ' · [adjudicated:claude ok] ' || $2,
       updated_at=now()
 WHERE id=$1 AND status='active'`, it.id, reason)
		rescued++
		log.Printf("kdb.claude-adjudicate: RESCUE %q [%s] — %s", it.ko, it.t, reason)
	}
	return judged, rejected, rescued
}
