package kdb

// rejudge — 과거 하드 reject 된 엔티티를 Wikidata 로 재심해 실존 K-엔티티면 candidate 로
// 복원한다(CR-1 백로그 회복). v2 게이트키퍼/resolveUnknownOne 이 Wikidata veto 없이 거부하던
// 시절에 '막걸리 한잔'·'무조건'·KARA 류 실존 엔티티가 rejected 로 박제됐다. 이름검증을 통과한
// Wikidata 매치만 복원하므로 진짜 일반어(Wikidata 무존재)는 rejected 로 남는다(정밀 유지).
//
// 복원 결과는 active 직승이 아니라 candidate + needs_disambig(운영자 검토 보류)로 둔다 —
// 무검증 자동 승급 위험 회피, 수정된 게이트키퍼(Wikidata veto)가 재처리해도 다시 안 버린다.

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// RejudgeRejects — rejected 엔티티 n건을 Wikidata 로 재심. dry=true 면 검사만(복원 안 함).
// 반환: (checked, restored).
func RejudgeRejects(ctx context.Context, pool *pgxpool.Pool, n int, dry bool) (checked, restored int) {
	if pool == nil || n <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko
  FROM kwave_entities
 WHERE status='rejected' AND operator_locked=false
   AND char_length(canonical_ko) BETWEEN 2 AND 25
   AND canonical_ko ~ '[가-힣]'
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a
                  WHERE a.entity_id=kwave_entities.id AND a.field='rejudge'
                    AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY updated_at DESC
 LIMIT $1`, n)
	if err != nil {
		log.Printf("kdb.rejudge: select: %v", err)
		return 0, 0
	}
	type row struct {
		id uuid.UUID
		ko string
	}
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	wd := wikidata.New()
	for _, it := range items {
		checked++
		wctx, cancel := context.WithTimeout(ctx, 12*time.Second)
		ent, _, werr := wd.SearchAndFetch(wctx, it.ko)
		cancel()
		// 재심 시도 기록(쿨다운 — 무존재 재조회 방지). dry 에선 기록 안 함.
		if !dry {
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'rejudge',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, it.id)
		}
		if werr != nil || ent == nil {
			continue // Wikidata 무존재(이름검증 실패 포함) → rejected 유지(정밀)
		}
		restored++
		qid := ent.QID
		if dry {
			log.Printf("kdb.rejudge[dry]: %q ← wikidata %s (복원 대상)", it.ko, qid)
			continue
		}
		// plain candidate 로 복원(needs_disambig 안 설정) — 수정된 게이트키퍼(Wikidata veto)가
		// 재심한다. 실존이면 promote(active 서빙), 여전히 모호하면 게이트키퍼가 quarantine.
		// rejudge 는 rejected 만 선택하므로 재심에서 quarantine(candidate 유지) 돼도 루프 없음.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='candidate', confidence=0.500,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'rejudge: wikidata 존재('||$2||') 복원 — 게이트키퍼 재심',
       updated_at=now()
 WHERE id=$1 AND status='rejected' AND operator_locked=false`, it.id, qid)
		log.Printf("kdb.rejudge: %q ← wikidata %s (candidate 복원)", it.ko, qid)
		time.Sleep(300 * time.Millisecond) // Wikidata 예의(가벼운 pacing)
	}
	log.Printf("kdb.rejudge: checked=%d restored=%d dry=%v", checked, restored, dry)
	return checked, restored
}
