package kdb

// tmdb_drain — 갇힌 K-작품 candidate(movie/drama/show)를 TMDb 로 재심해 승급한다.
// TMDb 는 공신력 승급 provider(source_policy.officialPromotionProviders)이고 movie/tv 를
// 모두 커버하므로, KOFIC(movie만)·KMDb(movie만)가 못 닿는 drama/show 버킷의 최대 커버리지
// 레버다. ★homonym 방어(2026-07-21 오염 사고 교훈): tmdb.SearchExactID 로 정규화 제목이
// 정확히 유일하게 일치할 때만 승급(동명작 2건+이면 보류). media 타입 필터(movie vs tv)가
// 1차 분리, 유일성 가드가 2차 방어. 30d 쿨다운. en 채움(빈칸일 때만, source=tmdb) — #4
// readiness 로 en 채워지면 즉시 ready 서빙, 나머지 locale 은 active 후 enrich 가 보강.

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
)

func tmdbMediaPath(entityType string) string {
	if entityType == "drama" || entityType == "show" {
		return "tv"
	}
	return "movie"
}

// DrainTMDbCandidates — movie/drama/show candidate 를 TMDb 정확제목 유일매치로 승급.
// dry=true 면 검색·판정만 하고 DB 쓰기(쿨다운·ref·채움·승급) 없음(검증용).
// 반환=(승급, 채움, 시도).
func DrainTMDbCandidates(ctx context.Context, pool *pgxpool.Pool, cl *tmdb.Client, token string, limit int, dry bool) (promoted, filled, checked int) {
	if pool == nil || cl == nil || strings.TrimSpace(token) == "" || limit <= 0 {
		return 0, 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type::text
  FROM kwave_entities
 WHERE status='candidate' AND entity_type IN ('movie','drama','show')
   AND operator_locked=false
   AND (
     EXISTS(SELECT 1 FROM kwave_entity_research_queue q
            WHERE q.intake_normalized_key=lower(regexp_replace(btrim(kwave_entities.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))
              AND q.precheck_status IN ('pass','approved'))
     OR EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts g
               WHERE g.entity_id=kwave_entities.id AND g.field='candidate-gate' AND g.last_source='kept')
   )
   AND canonical_ko ~ '[가-힣]'
   AND char_length(canonical_ko) BETWEEN 2 AND 60
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=kwave_entities.id AND r.provider='tmdb')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=kwave_entities.id
                  AND a.field='tmdb' AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0, 0
	}
	type row struct{ id, ko, etype string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.etype) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		checked++
		mid, ambiguous, serr := cl.SearchExactID(ctx, token, it.ko, it.etype)
		time.Sleep(300 * time.Millisecond) // TMDb 예의
		if !dry {
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'tmdb',1,now(),'tmdb')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, it.id)
		}
		if serr != nil {
			continue
		}
		if mid == 0 || ambiguous {
			if dry && ambiguous {
				log.Printf("kdb.tmdb-drain[dry]: %s(%s) — 동명작 다수, 보류", it.ko, it.etype)
			}
			continue // 무매칭 또는 동명작 모호 → candidate 유지
		}
		titles, _, terr := cl.Enrich(ctx, token, it.ko, it.etype)
		if terr != nil {
			continue
		}
		en := ""
		if v := titles["en"]; len(v) > 0 {
			en = strings.TrimSpace(v[0])
		}
		if dry {
			log.Printf("kdb.tmdb-drain[dry]: 승급후보 %s(%s) → tmdb#%d en=%q", it.ko, it.etype, mid, en)
			promoted++
			continue
		}
		// 권위 참조 저장.
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'tmdb',$2,$3,0.8,'{}',now())
ON CONFLICT DO NOTHING`, it.id, fmt.Sprintf("%d", mid),
			fmt.Sprintf("https://www.themoviedb.org/%s/%d", tmdbMediaPath(it.etype), mid))
		// en 채움(빈칸일 때만 — 기존 값·상위소스 존중).
		if en != "" {
			tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities SET canonical_en=$2, canonical_en_source='tmdb', updated_at=now()
 WHERE id=$1 AND COALESCE(canonical_en,'')=''`, it.id, en)
			if tag.RowsAffected() > 0 {
				filled++
			}
		}
		// candidate → active 승급.
		tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.8),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'tmdb 정확제목 유일매치 확정 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, it.id)
		if tag.RowsAffected() > 0 {
			promoted++
		}
	}
	return promoted, filled, checked
}
