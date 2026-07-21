package kdb

// wikidata_person_drain — 갇힌 person candidate 를 Wikidata 로 재심해, description 에 K-Wave
// 단서(South Korean/한국/K-pop 등)가 있는 실존 인물만 external_ref(wikidata) 저장 + active
// 승급한다. wikidata.Search(filterKWave=true) 내장 IsKWaveDescription 이 비-K(외국·역사·비연예)
// 를 자동 배제한다 — 실측(2026-07-08): Hani·Yuju 통과, Jackson Pollock(미국화가)·조선 문신·
// economist 정확 배제. person 은 country 내장필터가 없어 이 description 게이트가 오염 방어의 핵심.
// person 은 최대 candidate 버킷(≈494)이라 확보 잠재량이 가장 크다. 30d 쿨다운(field=wdperson).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// DrainWikidataPersonCandidates — candidate(person)를 Wikidata K-Wave 필터로 재심해 승급.
// 반환=(승급 수, 시도 수).
func DrainWikidataPersonCandidates(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int) (promoted, checked int) {
	if pool == nil || cl == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko
  FROM kwave_entities
 WHERE status='candidate' AND entity_type='person'
   AND operator_locked=false
   AND (
     EXISTS(SELECT 1 FROM kwave_entity_research_queue q
            WHERE q.intake_normalized_key=lower(regexp_replace(btrim(kwave_entities.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))
              AND q.precheck_status IN ('pass','approved'))
     OR EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts g
               WHERE g.entity_id=kwave_entities.id AND g.field='candidate-gate' AND g.last_source='kept')
   )
   AND char_length(canonical_ko) BETWEEN 2 AND 20
   -- ★이 제외조항은 유지한다(2026-07-21 재확인). 한때 "orphan 재평가"를 위해 제거를
   -- 시도했으나, 이 드레인은 이름검색→isKEntertainerDesc 로만 승급해 "그 이름의 연예인이
   -- 존재"만 확인할 뿐 "우리 엔티티가 그 사람인지"(동명이인)는 검증 못 한다. 실측: 이정후
   -- (유명=야구선수)가 동명 배우 Q12612604 에, 권도형(유명=Do Kwon)이 동명 배우 Q123157349
   -- 에 오매칭 승급됨 → 롤백. 이미 wikidata ref 있는 candidate 는 별도 disambiguation
   -- 경로에서 처리한다(이름검색 승급 대상에서 제외).
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=kwave_entities.id AND r.provider='wikidata')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=kwave_entities.id
                  AND a.field='wdperson' AND a.last_attempt_at > now() - interval '30 days')
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
		// filterKWave=true → description 에 K-Wave 단서 있는 후보만 반환(오염 방어).
		cands, serr := cl.Search(ctx, it.ko, "ko", 3, true)
		time.Sleep(350 * time.Millisecond) // Wikidata 예의
		// 쿨다운 기록(무매칭 재조회 방지).
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'wdperson',1,now(),'wikidata')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, it.id)
		if serr != nil || len(cands) == 0 {
			continue
		}
		// filterKWave 게이트("south korean" 등)는 정치인·운동선수·학자 등 비연예 인물도
		// 통과시킨다(실측: "South Korean politician" 오승급) → 연예직업 allowlist 2차 필터.
		// 후보를 순회해 연예직업 description 을 가진 첫 후보만 채택(동명이인 방어).
		var best wikidata.Candidate
		found := false
		for _, cand := range cands {
			if strings.TrimSpace(cand.QID) == "" {
				continue
			}
			if isKEntertainerDesc(cand.Description) {
				best = cand
				found = true
				break
			}
		}
		if !found {
			continue
		}
		// 권위 참조 저장 + candidate→active 승급.
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'wikidata',$2,$3,0.75,$4,now())
ON CONFLICT DO NOTHING`, it.id, best.QID,
			"https://www.wikidata.org/wiki/"+best.QID,
			fmt.Sprintf(`{"label":%q,"description":%q}`, best.Label, best.Description))
		tag, _ := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.75),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'wikidata K-Wave 인물 확정('||$2||') 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND operator_locked=false`, it.id, best.QID)
		if tag.RowsAffected() > 0 {
			promoted++
		}
	}
	return promoted, checked
}

// isKEntertainerDesc — Wikidata description 이 K-엔터테인먼트 직업을 가리키는지(allowlist).
// filterKWave 를 통과한 "South Korean X" 중 연예직업만 남긴다 — 정치인/운동선수/학자 배제.
// 부분매칭 오탐을 피하려 모호한 어근(host/band/dj/group 단독)은 제외하고 명확한 것만 담는다.
func isKEntertainerDesc(desc string) bool {
	low := strings.ToLower(desc)
	for _, kw := range kEntertainerKeywords {
		if strings.Contains(low, kw) {
			return true
		}
	}
	return false
}

var kEntertainerKeywords = []string{
	"singer", "actor", "actress", "rapper", "idol", "musician", "composer",
	"songwriter", "dancer", "comedian", "entertainer", "k-pop", "k-drama",
	"girl group", "boy band", "voice actor", "television host", "tv host",
	"youtuber", "media personality", "record producer", "film director",
	"television personality", "singer-songwriter", "performer",
	"가수", "배우", "래퍼", "아이돌", "뮤지션", "작곡가", "댄서",
	"개그맨", "코미디언", "방송인", "성우", "연예인",
}
