package verify

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/naver"
)

// CandidateEvidencePass — 소비자 요청(review 보류)이 기다리는데 승급이 정체된 candidate 를
// 뉴스근거(네이버 news + SearXNG 폴백) + gemma 기사맥락 판정으로 active(evidenced) 승급한다.
// EvidencePass(active 전용)의 candidate 확장.
//
// ★설계 근거(2026-07-17 실측): 미해결 보류의 두 번째 버킷 484건 = candidate 는 이미 있는데
// 승급 경로가 공식앵커(ResolveOnDemand)뿐이라 공식소스에 없는 실존 롱테일이 conf 0.40 에
// 영구 정체 → 3회 무검증 자동기각까지 감. 뉴스근거 축이 이 갭을 메운다.
//
// 3분기: real → 승급(dataqa_log 스냅샷·revert 가능) + 대기 중이던 review 큐 종결.
// contaminated → 검토 플래그만([cand-evidence:review]) — 자동기각 금지(뉴스검색 FP ~33% 실측,
// 오거부=최상위 금칙). unclear/근거부족 → 쿨다운만(last_enriched_at, ResolveOnDemand 와 공유해
// 이중 작업 방지 — '무검증' 기각 카운트에는 포함 안 됨).
//
// 동명 active 존재 시 스킵(중복 생성 방지 — 그쪽은 동명이인 라우팅 몫). 엔티티당 네이버 1콜.
// 반환: (승급, 오염플래그, processed, err).
func CandidateEvidencePass(ctx context.Context, pool *pgxpool.Pool, n int) (promoted, flagged, processed int, err error) {
	nv, err := naver.NewFromSettings(ctx, pool)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("cand evidence: %w", err)
	}
	judge := newVerifyJudge()

	rows, qerr := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'::text[])
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.status='candidate' AND e.operator_locked=false
   AND e.entity_type::text NOT IN ('unknown','term')
   AND COALESCE(e.notes,'') NOT LIKE '%[cand-evidence:review]%'
   AND (e.last_enriched_at IS NULL OR e.last_enriched_at < now()-interval '7 days')
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
        WHERE a.status='active' AND a.canonical_ko=e.canonical_ko)
   AND EXISTS (SELECT 1 FROM kwave_entity_research_queue q
        WHERE q.precheck_status='review'
          AND (q.entity_ko=e.canonical_ko OR q.entity_ko=ANY(e.aliases_ko)))
 ORDER BY e.confidence DESC, e.created_at ASC
 LIMIT $1`, n)
	if qerr != nil {
		return 0, 0, 0, fmt.Errorf("cand evidence query: %w", qerr)
	}
	var ents []evEntity
	for rows.Next() {
		var e evEntity
		if serr := rows.Scan(&e.id, &e.ko, &e.etype, &e.role, &e.works); serr != nil {
			rows.Close()
			return 0, 0, 0, serr
		}
		ents = append(ents, e)
	}
	rows.Close()

	log.Printf("kdb.verify.cand-evidence: start (요청대기 candidate=%d)", len(ents))
	for _, e := range ents {
		if ctx.Err() != nil {
			break
		}
		processed++
		hits := gatherEvidence(ctx, nv, e)
		if len(hits) < 2 {
			markCandEvidenceCooldown(ctx, pool, e.id)
			continue
		}
		v, jerr := judgeVerify(ctx, judge, e, hits)
		if jerr != nil {
			log.Printf("  [err] %s (%s): %v", e.ko, e.etype, jerr)
			continue
		}
		identity := strings.TrimSpace(v.Identity)
		reason := strings.TrimSpace(v.Reason)
		switch v.Verdict {
		case "real":
			ev := "search+gemma"
			if identity != "" {
				ev = "search+gemma: " + truncateRunes(identity, 70)
			} else if reason != "" {
				ev = "search+gemma: " + truncateRunes(reason, 70)
			}
			_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'status', 'candidate', '', 'candidate-evidence-promote', $2, 'gemma')`,
				e.id, truncateRunes(strings.TrimSpace(identity+" / "+reason), 200))
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence, 0.700),
       verification_tier='evidenced', verification_evidence=$2, verified_tier_at=now(),
       notes = CASE WHEN COALESCE(notes,'')='' THEN $3 ELSE notes || ' ' || $3 END,
       updated_at=now()
 WHERE id=$1 AND status='candidate'`, e.id, ev, "[cand-evidence] 뉴스근거 승급")
			if uerr == nil && tag.RowsAffected() > 0 {
				promoted++
				closeWaitingReviewRows(ctx, pool, e.id)
				log.Printf("  [real→active] %s (%s) → %s", e.ko, e.etype, ev)
			}
		case "contaminated":
			note := "[cand-evidence:review] 의심=" + truncateRunes(strings.TrimSpace(identity+" / "+reason), 80)
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
       last_enriched_at=now(), updated_at=now()
 WHERE id=$1 AND status='candidate' AND COALESCE(notes,'') NOT LIKE '%[cand-evidence:review]%'`,
				e.id, note)
			if uerr == nil && tag.RowsAffected() > 0 {
				flagged++
				log.Printf("  [contam→review] %s (%s) — 기사는 %q: %s", e.ko, e.etype, identity, truncateRunes(reason, 55))
			}
		default: // unclear — 판정 이력 태그(1회) + 쿨다운. 태그가 있으면 ResolveOnDemand 의
			// 3회 무검증 기각 보류(오거부 가드)가 풀린다 — 두 축 모두 실패한 것만 기각되게.
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN COALESCE(notes,'')='' THEN '[cand-evidence:unclear]'
                    WHEN notes NOT LIKE '%[cand-evidence:unclear]%' THEN notes || ' [cand-evidence:unclear]'
                    ELSE notes END,
       last_enriched_at=now()
 WHERE id=$1::uuid AND status='candidate'`, e.id)
		}
	}
	log.Printf("kdb.verify.cand-evidence: done promoted=%d contam?=%d /%d", promoted, flagged, processed)
	return promoted, flagged, processed, nil
}

// closeWaitingReviewRows — 승급된 엔티티를 기다리던 review 보류를 종결한다
// (intake_autoverify.closeAsExisting 과 동일 필드 — 소비자 즉시 서빙 가능).
func closeWaitingReviewRows(ctx context.Context, pool *pgxpool.Pool, entityID string) {
	_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_research_queue q
   SET status='done', finished_at=now(), picked_at=NULL, next_attempt_at=NULL,
       resolution_status='active', locale_status='complete', last_outcome='existing_entity',
       precheck_status='pass', precheck_reason='existing_entity',
       precheck_flags=array_append(precheck_flags,'auto_evidence'), last_error=NULL
 WHERE q.precheck_status='review' AND q.status <> 'in_progress'
   AND EXISTS (SELECT 1 FROM kwave_entities e WHERE e.id=$1::uuid
        AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))`, entityID)
}

// markCandEvidenceCooldown — 근거 부족/unclear: 쿨다운만(7d 재선택 방지). 노트 오염 없음.
func markCandEvidenceCooldown(ctx context.Context, pool *pgxpool.Pool, entityID string) {
	_, _ = pool.Exec(ctx,
		`UPDATE kwave_entities SET last_enriched_at=now() WHERE id=$1::uuid`, entityID)
}
