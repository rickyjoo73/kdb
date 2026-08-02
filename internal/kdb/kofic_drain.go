package kdb

// kofic_drain — 갇힌 K-영화 candidate(movie)를 KOFIC(영화진흥위원회) 로 재심해 한국어 제목이
// 정확히 일치하는 실존 극장영화면 external_ref(kofic) 저장 + active 승급한다. KOFIC 은 국내
// 극장영화 DB 이므로 movie 타입만 대상(드라마/TV 는 KMDB 소관 — 키 미발급으로 별도). 30d 쿨다운.
// kofic.Enrich 는 movieNm 정확일치만 채택(list[0] 폴백 금지)하므로 "바람" 류 부분일치/스팸 오매칭
// 을 이미 걸러낸다. 오너 지시(2026-07-08): 공식소스 미연결 해소 — MusicBrainz 에 이어 KOFIC 편입.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/kofic"
)

// DrainKoficCandidates — candidate(movie)를 KOFIC 로 재심해 정확제목 매칭이면 승급.
// 반환=(승급 수, 시도 수).
func DrainKoficCandidates(ctx context.Context, pool *pgxpool.Pool, cl *kofic.Client, key string, limit int) (promoted, checked int) {
	if pool == nil || cl == nil || strings.TrimSpace(key) == "" || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko
  FROM kwave_entities
 WHERE status='candidate' AND entity_type='movie'
   AND operator_locked=false
   AND (
     EXISTS(SELECT 1 FROM kwave_entity_research_queue q
            WHERE q.intake_normalized_key=lower(regexp_replace(btrim(kwave_entities.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))
              AND q.precheck_status IN ('pass','approved'))
     OR EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts g
               WHERE g.entity_id=kwave_entities.id AND g.field='candidate-gate' AND g.last_source='kept')
   )
   AND char_length(canonical_ko) BETWEEN 2 AND 40
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=kwave_entities.id AND r.provider='kofic')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=kwave_entities.id
                  AND a.field='kofic' AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0
	}
	type row struct{ id, ko string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if strings.TrimSpace(it.ko) == "" {
			continue
		}
		checked++
		res, movieCd, serr := cl.Enrich(ctx, key, it.ko)
		time.Sleep(500 * time.Millisecond) // KOFIC 예의
		// 쿨다운 기록(무매칭 재조회 방지).
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'kofic',1,now(),'kofic')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, it.id)
		if serr != nil {
			continue
		}
		en := ""
		if v := res["en"]; len(v) > 0 {
			en = strings.TrimSpace(v[0])
		}
		if en == "" {
			continue // 정확 제목 매칭 없음 → candidate 유지.
		}
		// 권위 참조 저장 + candidate→active 승급.
		//
		// ★external_id 는 movieCd 다(2026-08-03 정정). 종전엔 **영어 제목**을 식별자
		// 자리에 넣고 url 은 검색 첫 페이지를 넣었다 — 실측 30건 전부 "Star Wars"·
		// "Aladdin" 같은 값이라 어느 영화인지 되짚을 수 없었다. 행이 있으니
		// api-source-no-ref 불변식은 통과했고, 그래서 아무도 못 봤다.
		// 영어 제목은 식별자가 아니라 **속성**이므로 raw_payload 에 남긴다.
		//
		// 승급 조건은 손대지 않았다(en 있을 때만). movieCd 만으로도 실존 확인은
		// 되지만 그건 게이트 확장이라 별건 — 여기선 기록 품질만 고친다.
		if RecordAPIRef(ctx, pool, it.id, "kofic", movieCd, kofic.MovieURL(movieCd), 0.750) {
			// 영어 제목은 식별자가 아니라 **속성**이므로 raw_payload 로 간다.
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_external_refs SET raw_payload=$2
 WHERE entity_id=$1::uuid AND provider='kofic'`, it.id, fmt.Sprintf(`{"movieNmEn":%q}`, en))
		}
		tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.75),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'kofic 정확제목 확정 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, it.id)
		if tag.RowsAffected() > 0 {
			promoted++
		}
	}
	return promoted, checked
}
