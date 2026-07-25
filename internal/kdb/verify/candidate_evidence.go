package verify

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
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

// candEvidenceSelect — 스위프(CandidateEvidencePass)와 단건 패스트레인(CandidateEvidenceOne)
// 이 공유하는 선별 SELECT 몸통. WHERE 이하만 호출부가 붙인다.
const candEvidenceSelect = `
SELECT e.id::text AS id, e.canonical_ko AS ko, e.entity_type::text AS etype,
       COALESCE(d.primary_role::text,'') AS role, COALESCE(d.notable_works,'{}'::text[]) AS works,
       COALESCE((SELECT q.context_hint FROM kwave_entity_research_queue q
                  WHERE (q.entity_ko=e.canonical_ko OR q.entity_ko=ANY(e.aliases_ko))
                    AND COALESCE(q.context_hint,'')<>'' ORDER BY q.created_at DESC LIMIT 1),'') AS hint,
       EXISTS (SELECT 1 FROM kwave_entity_research_queue q
                WHERE q.precheck_status='review'
                  AND (q.entity_ko=e.canonical_ko OR q.entity_ko=ANY(e.aliases_ko))) AS waiting,
       e.confidence AS conf, e.created_at AS created
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id`

type candEvidenceRow struct {
	e       evEntity
	hint    string
	waiting bool
}

// CandidateEvidenceOne — 특정 candidate 1건 즉시 판정(★2026-07-25 전략1 프레시 패스트레인).
// research worker 가 소비자 origin 발굴로 candidate 를 만들거나 유지한 직후 호출 — 20분
// 스위프를 기다리지 않고 승급 기회를 즉시 준다(실측 p50 15h 의 주범이 스위프 대기였다).
// 게이트·오염 방어·소진 기록은 스위프와 완전히 동일 코드라 품질 리스크 증가 없음.
// 부적격(이미 active·쿨다운·플래그)이면 조용히 no-op.
func CandidateEvidenceOne(ctx context.Context, pool *pgxpool.Pool, entityID string) (bool, error) {
	nv, err := naver.NewFromSettings(ctx, pool)
	if err != nil {
		return false, err
	}
	var r candEvidenceRow
	err = pool.QueryRow(ctx, `
SELECT id, ko, etype, role, works, hint, waiting FROM (`+candEvidenceSelect+`
 WHERE e.id=$1::uuid
   AND e.status='candidate' AND e.operator_locked=false
   AND e.entity_type::text NOT IN ('unknown','term')
   AND COALESCE(e.notes,'') NOT LIKE '%[cand-evidence:review]%'
   AND COALESCE(e.notes,'') NOT LIKE '%[cand-evidence:exhausted]%'
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts g
        WHERE g.entity_id=e.id AND g.field='cand-evidence'
          AND g.last_attempt_at > now()-interval '1 hour')
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
        WHERE a.status='active' AND a.canonical_ko=e.canonical_ko)
) t`, entityID).
		Scan(&r.e.id, &r.e.ko, &r.e.etype, &r.e.role, &r.e.works, &r.hint, &r.waiting)
	if err != nil {
		return false, nil // 부적격/미존재 — 스위프 몫으로 남김
	}
	promoted, _ := processCandEvidenceRow(ctx, pool, nv, newVerifyJudge(), r)
	if promoted {
		log.Printf("kdb.verify.cand-evidence: 패스트레인 즉시승급 %s (%s)", r.e.ko, r.e.etype)
	}
	return promoted, nil
}

func CandidateEvidencePass(ctx context.Context, pool *pgxpool.Pool, n int) (promoted, flagged, processed int, err error) {
	nv, err := naver.NewFromSettings(ctx, pool)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("cand evidence: %w", err)
	}
	judge := newVerifyJudge()

	// 2026-07-23 P2(오너 승인 개선안): review 큐 행 요건 제거 — 전 candidate 대상
	// (정체 풀의 ~75% 가 아예 선별 밖이던 사각 해소). 요청대기(waiting) 우선 정렬만 유지.
	// 문맥증강: 소비자 기사 context_hint 를 함께 끌어와 ①공기 아티스트 스코프 쿼리
	// ②판정 증거로 활용(백카탈로그 뉴스빈약 보완).
	//
	// ★2026-07-23 기아 수정: 쿨다운을 last_enriched_at(on-demand 공식앵커축·lookup enrich 와
	// 공유)에서 전용 키(enrich_attempts field='cand-evidence')로 분리. 공유 컬럼은 다른 축이
	// 하루 150~325건씩 스탬프를 찍어 1,055 풀 전체가 상시 7일 쿨다운 → 이 레인이 매 사이클
	// "대상=0" 공회전(실측). 역방향(이 레인이 last_enriched_at 을 찍어 on-demand 재시도를
	// 늦추는 것)은 이중작업 방지로 유지한다.
	// ★2026-07-25 전략2·3(요청→서빙 p50 15h 단축): 소비자 대기(waiting) 건은 쿨다운을
	// 7d→1h 로 분리(첫 시도 근거부족이 하루 뒤 재시도되던 꼬리 제거)하고, waiting 그룹
	// 안에서는 최신 유입 우선(기사가 지금 나오는 키워드가 근거 확보 확률도 최고).
	// 비대기(백로그)는 종전 그대로(7d 쿨다운·오래된 것 우선).
	rows, qerr := pool.Query(ctx, `
SELECT id, ko, etype, role, works, hint, waiting FROM (`+candEvidenceSelect+`
 WHERE e.status='candidate' AND e.operator_locked=false
   AND e.entity_type::text NOT IN ('unknown','term')
   AND COALESCE(e.notes,'') NOT LIKE '%[cand-evidence:review]%'
   AND COALESCE(e.notes,'') NOT LIKE '%[cand-evidence:exhausted]%'
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts g
        WHERE g.entity_id=e.id AND g.field='cand-evidence'
          AND g.last_attempt_at > now() - CASE WHEN EXISTS (
                SELECT 1 FROM kwave_entity_research_queue q
                 WHERE q.precheck_status='review'
                   AND (q.entity_ko=e.canonical_ko OR q.entity_ko=ANY(e.aliases_ko)))
              THEN interval '1 hour' ELSE interval '7 days' END)
   AND NOT EXISTS (SELECT 1 FROM kwave_entities a
        WHERE a.status='active' AND a.canonical_ko=e.canonical_ko)
) t
 ORDER BY t.waiting DESC,
          CASE WHEN t.waiting THEN t.created END DESC NULLS LAST,
          t.conf DESC, t.created ASC
 LIMIT $1`, n)
	if qerr != nil {
		return 0, 0, 0, fmt.Errorf("cand evidence query: %w", qerr)
	}
	var ents []candEvidenceRow
	for rows.Next() {
		var r candEvidenceRow
		if serr := rows.Scan(&r.e.id, &r.e.ko, &r.e.etype, &r.e.role, &r.e.works, &r.hint, &r.waiting); serr != nil {
			rows.Close()
			return 0, 0, 0, serr
		}
		ents = append(ents, r)
	}
	rows.Close()

	log.Printf("kdb.verify.cand-evidence: start (대상 candidate=%d)", len(ents))
	for _, row := range ents {
		if ctx.Err() != nil {
			break
		}
		processed++
		p, f := processCandEvidenceRow(ctx, pool, nv, judge, row)
		if p {
			promoted++
		}
		if f {
			flagged++
		}
	}
	log.Printf("kdb.verify.cand-evidence: done promoted=%d contam?=%d /%d", promoted, flagged, processed)
	return promoted, flagged, processed, nil
}

// processCandEvidenceRow — candidate 1건의 근거수집→판정→반영. 스위프와 패스트레인이
// 공유(게이트·오염 방어·소진 기록 단일화). 반환 (승급, 오염플래그).
func processCandEvidenceRow(ctx context.Context, pool *pgxpool.Pool, nv *naver.Client, judge *agents.Base, row candEvidenceRow) (promoted, flagged bool) {
	e := row.e
	// 문맥증강 쿼리: 힌트에 공기(co-mention)하는 active 인물/그룹을 스코프로 결합
	// (예: "SEXY LOVE" + "티아라"). 없으면 종전 역할어 쿼리.
	query := e.ko
	if artist := coMentionArtist(ctx, pool, row.hint, e.ko); artist != "" {
		query = e.ko + " " + artist
	} else if w := roleWord(e.etype); w != "" {
		query = e.ko + " " + w
	}
	hits := gatherEvidenceQuery(ctx, nv, query)
	searchHits := len(hits)
	if row.hint != "" {
		// 소비자 기사 문맥도 실증거다(요청을 만든 기사 원문 스니펫). 단 검색 실증거
		// 최소 1건은 요구 — 힌트 단독 승급 금지.
		hits = append([]string{"[소비자 기사문맥] " + truncateRunes(row.hint, 220)}, hits...)
	}
	if searchHits < 1 || len(hits) < 2 {
		markCandEvidenceInsufficient(ctx, pool, e.id)
		return false, false
	}
	v, jerr := judgeVerify(ctx, judge, e, hits)
	if jerr != nil {
		log.Printf("  [err] %s (%s): %v", e.ko, e.etype, jerr)
		return false, false
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
			promoted = true
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
			flagged = true
			log.Printf("  [contam→review] %s (%s) — 기사는 %q: %s", e.ko, e.etype, identity, truncateRunes(reason, 55))
		}
	default: // unclear — 판정 이력 태그(1회) + 소진 카운트. 태그가 있으면 ResolveOnDemand 의
		// 3회 무검증 기각 보류(오거부 가드)가 풀린다 — 두 축 모두 실패한 것만 기각되게.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN COALESCE(notes,'')='' THEN '[cand-evidence:unclear]'
                    WHEN notes NOT LIKE '%[cand-evidence:unclear]%' THEN notes || ' [cand-evidence:unclear]'
                    ELSE notes END
 WHERE id=$1::uuid AND status='candidate'`, e.id)
		markCandEvidenceInsufficient(ctx, pool, e.id)
	}
	return promoted, flagged
}

// coMentionArtist — 소비자 기사 힌트에 함께 등장하는 active 인물/그룹명을 찾아 검색
// 스코프로 쓴다(흔한단어 제목의 동명이의 잡음 억제 + 백카탈로그 특정). 자기 이름과
// 같으면 제외. 최장 일치 1건.
func coMentionArtist(ctx context.Context, pool *pgxpool.Pool, hint, selfKo string) string {
	h := strings.TrimSpace(hint)
	if h == "" {
		return ""
	}
	var artist string
	err := pool.QueryRow(ctx, `
SELECT canonical_ko FROM kwave_entities
 WHERE status='active' AND entity_type::text IN ('person','group')
   AND char_length(canonical_ko) BETWEEN 2 AND 20
   AND canonical_ko <> $2
   AND position(canonical_ko IN $1) > 0
 ORDER BY char_length(canonical_ko) DESC
 LIMIT 1`, h, selfKo).Scan(&artist)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(artist)
}

// markCandEvidenceInsufficient — 근거부족/unclear 소진 관리: 쿨다운(7d) + 시도 카운트.
// 2회 소진 시 [cand-evidence:exhausted] — 무한 재시도 루프를 끊고 claude 종결자(P3) 큐로
// 넘긴다(2026-07-23 P2: 종전엔 7일 쿨다운 무한 반복이 정체의 한 축이었다).
func markCandEvidenceInsufficient(ctx context.Context, pool *pgxpool.Pool, entityID string) {
	var attempts int
	err := pool.QueryRow(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'cand-evidence',1,now(),'insufficient')
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now(),
    last_source=EXCLUDED.last_source
RETURNING attempts`, entityID).Scan(&attempts)
	if err == nil && attempts >= 2 {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN COALESCE(notes,'')='' THEN '[cand-evidence:exhausted]'
                    WHEN notes NOT LIKE '%[cand-evidence:exhausted]%' THEN notes || ' [cand-evidence:exhausted]'
                    ELSE notes END,
       last_enriched_at=now()
 WHERE id=$1::uuid AND status='candidate'`, entityID)
		return
	}
	_, _ = pool.Exec(ctx,
		`UPDATE kwave_entities SET last_enriched_at=now() WHERE id=$1::uuid`, entityID)
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

