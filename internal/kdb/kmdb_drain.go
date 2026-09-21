package kdb

// kmdb_drain — KMDb(한국영상자료원) 승급·채움 드레인 (오너 키 제공 2026-07-17, 등록 5659).
//
// 대상: movie 타입 ① candidate → 정확 제목 매칭이면 external_ref(kmdb) + active 승급
// ② active 인데 canonical_en 빈칸 → titleEng 로 채움(source='kmdb', 권위 API prio).
// 오매칭 가드: kmdb.ExactMatch — 정확 제목/별칭 일치만, 동명작 상이 영문이면 채택 안 함
// (KOFIC "아몬드" 교훈과 동일 원칙). 30d 쿨다운(enrich_attempts field='kmdb').
// ★쿼터 일 100건 — 레인 시간당 4건(96/일) + 수동 드레인 시 주의.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/kmdb"
)

// DrainKMDb — movie candidate 승급 + active en 빈칸 채움. 반환=(승급, 채움, 시도).
func DrainKMDb(ctx context.Context, pool *pgxpool.Pool, cl *kmdb.Client, key string, limit int) (promoted, filled, checked int) {
	// changedRows — 원장이 실제로 바뀐 **행** 수(채움과 승급을 합쳐 한 번만 센다).
	changedRows := 0
	if pool == nil || cl == nil || strings.TrimSpace(key) == "" || limit <= 0 {
		return 0, 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, status, COALESCE(canonical_en,'')
  FROM kwave_entities
 WHERE entity_type='movie' AND operator_locked=false
   AND (status='candidate' OR (status='active' AND COALESCE(canonical_en,'')=''))
   AND canonical_ko ~ '[가-힣]'
   AND char_length(canonical_ko) BETWEEN 2 AND 40
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=kwave_entities.id AND r.provider='kmdb')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=kwave_entities.id
                  AND a.field='kmdb' AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY (status='active') DESC, updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0, 0
	}
	type row struct{ id, ko, status, en string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.status, &r.en) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		checked++
		results, serr := cl.Search(ctx, key, it.ko)
		time.Sleep(700 * time.Millisecond) // KMDb 예의 + 쿼터 보호
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'kmdb',1,now(),'kmdb')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, it.id)
		if serr != nil {
			continue // 일시 장애/쿼터 — 쿨다운 후 재시도
		}
		m := kmdb.ExactMatch(results, it.ko)
		if m == nil {
			continue // 정확 매칭 없음 — 상태 유지
		}
		// 권위 참조 저장.
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'kmdb',$2,$3,0.75,$4,now())
ON CONFLICT DO NOTHING`, it.id, m.DocID,
			"https://www.kmdb.or.kr/db/kor/detail/movie/"+m.DocID,
			fmt.Sprintf(`{"titleEng":%q,"prodYear":%q}`, m.TitleEng, m.ProdYear))
		// en 빈칸 채움(빈칸일 때만 — 기존 값 존중, 상위/동급 소스는 안 건드림).
		changed := false
		if m.TitleEng != "" && it.en == "" {
			tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities SET canonical_en=$2, canonical_en_source='kmdb', updated_at=now()
 WHERE id=$1 AND COALESCE(canonical_en,'')=''`, it.id, m.TitleEng)
			if tag.RowsAffected() > 0 {
				filled++
				changed = true
			}
		}
		// candidate 승급.
		if it.status == "candidate" {
			tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.75),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'kmdb 정확제목 확정 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, it.id)
			if tag.RowsAffected() > 0 {
				promoted++
				changed = true
			}
		}
		if changed {
			changedRows++
		}
	}
	// 레인 성과 원장(0151). scanned=검사한 행 수, applied=**원장이 바뀐 행 수**.
	//
	// ★2026-09-21 39회차에 고쳤다. 배선할 때 applied 에 `filled` 만 넣었는데, 이 레인은
	//	 원장을 두 갈래로 바꾼다 — canonical_en 채움과 **candidate→active 승급**이다.
	//	 승급만 한 회차는 applied=0 으로 적혔고, 그 건이 사유에서 「채우지 못함」으로까지
	//	 세어져 **이중으로 거짓말**을 했다. 그래서 4회차 연속 조용한0건 신호가 떴는데
	//	 실제로는 그 사이 3건(더 스튜디오·이타미 준의 바다·동갑내기 과외하기)을 승급시키고
	//	 kmdb 참조 3건을 만들고 있었다. **계측기가 자기 레인을 병으로 신고한 것이다.**
	//
	// ★한 행이 채움과 승급을 동시에 해도 **한 번만** 센다. 0151 의 계약이 「원장이 바뀐
	//	 행 수」이고, 둘을 더하면 applied > scanned 가 되어 DB 제약(applied<=scanned)에
	//	 걸린다 — 거기서 scanned 를 올려 맞추면 그 보정이 또 다른 거짓말이 된다.
	RecordCounts(ctx, pool, "kmdb", false, checked, changedRows, map[string]int{
		"채우지 못함": checked - changedRows,
	})
	return promoted, filled, checked
}
