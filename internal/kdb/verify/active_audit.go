package verify

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/naver"
)

// TriageFn — 내용 판별기(고유명사인가) 주입 시그니처. cmd 가 kdb.TriageKeywordConfirmed
// (gemma 이중판정 — 변호인 동의 시만 garbage)를 배선한다. verify→kdb import 순환 회피.
type TriageFn func(ctx context.Context, ko, typeHint string) (garbage bool, reason string)

// ActiveAuditPass — active 고유명사 전수 오염 감사(오너 지시 2026-07-17: "오염 DB 는 번역을
// 채울 수 없다 — 찾아서 제거"). 권위앵커(외부 ref) 보유 엔티티는 실존 증명이 있어 제외 —
// 대상은 무ref evidenced/unverified(~2.4k). last_dataqa_at 커서로 전수 1회전 보장(30d 재감사).
//
// ★이중 게이트(오거부=최상위 금칙 — 한 축 판정만으로 절대 제거하지 않는다):
//
//	news real          → 실존 확정: unverified 면 evidenced 승격. 커서만.
//	news contaminated  → 내용판정(triage 이중판정)도 garbage 동의 시에만 reject(스냅샷).
//	                     불동의 → [audit:review] 플래그만(서빙 유지, 운영자 몫).
//	news unclear/무신호 → 내용판정 garbage 동의 시 reject(뉴스근거 없음+비고유명사 양축 실패).
//	                     아니면 커서만(실존 추정 유지 — 무명은 죄가 아님).
//
// reject = status='rejected' tombstone + dataqa_log(verdict='audit-contam-reject') 스냅샷.
// 복원: `verify-entities revert-contam`(retrace 계열과 동일 경로). 반환: (기각, 플래그, 승격, processed).
func ActiveAuditPass(ctx context.Context, pool *pgxpool.Pool, n int, triage TriageFn) (rejected, flagged, upgraded, processed int, err error) {
	nv, err := naver.NewFromSettings(ctx, pool)
	if err != nil {
		return 0, 0, 0, 0, fmt.Errorf("active audit: %w", err)
	}
	judge := newVerifyJudge()

	// 우선순위: unverified → en 빈칸(번역 못 채우는 오염 의심 — 오너 통찰) → 미감사 오래된 순.
	rows, qerr := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'::text[]),
       COALESCE(e.verification_tier,'unverified')
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.status='active' AND e.operator_locked=false
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id)
   AND COALESCE(e.notes,'') NOT LIKE '%[audit:review]%'
   AND (e.last_dataqa_at IS NULL OR e.last_dataqa_at < now()-interval '30 days')
 ORDER BY (COALESCE(e.verification_tier,'unverified')='unverified') DESC,
          (COALESCE(e.canonical_en,'')='') DESC,
          e.last_dataqa_at ASC NULLS FIRST, e.created_at ASC
 LIMIT $1`, n)
	if qerr != nil {
		return 0, 0, 0, 0, fmt.Errorf("active audit query: %w", qerr)
	}
	type auditEnt struct {
		e    evEntity
		tier string
	}
	var ents []auditEnt
	for rows.Next() {
		var a auditEnt
		if serr := rows.Scan(&a.e.id, &a.e.ko, &a.e.etype, &a.e.role, &a.e.works, &a.tier); serr != nil {
			rows.Close()
			return 0, 0, 0, 0, serr
		}
		ents = append(ents, a)
	}
	rows.Close()

	log.Printf("kdb.verify.audit: start (무ref active=%d)", len(ents))
	for _, a := range ents {
		if ctx.Err() != nil {
			break
		}
		e := a.e
		processed++
		hits := gatherEvidence(ctx, nv, e)
		verdict := "unclear"
		identity, reason := "", ""
		if len(hits) >= 2 {
			v, jerr := judgeVerify(ctx, judge, e, hits)
			if jerr != nil {
				log.Printf("  [err] %s (%s): %v", e.ko, e.etype, jerr)
				markAuditCursor(ctx, pool, e.id)
				continue
			}
			verdict, identity, reason = v.Verdict, strings.TrimSpace(v.Identity), strings.TrimSpace(v.Reason)
		}
		switch verdict {
		case "real":
			if a.tier == "unverified" {
				ev := "search+gemma"
				if identity != "" {
					ev = "search+gemma: " + truncateRunes(identity, 70)
				}
				_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET verification_tier='evidenced', verification_evidence=$2, verified_tier_at=now(),
       last_dataqa_at=now()
 WHERE id=$1::uuid AND verification_tier='unverified'`, e.id, ev)
				upgraded++
			} else {
				markAuditCursor(ctx, pool, e.id)
			}
		case "contaminated":
			if garbage, treason := triage(ctx, e.ko, e.etype); garbage {
				if auditReject(ctx, pool, e.id, a.tier, "news="+identity+" / "+reason+" · triage="+treason) {
					rejected++
					log.Printf("  [audit→reject] %s (%s) — 기사=%q · 내용판정 동의", e.ko, e.etype, identity)
				}
				continue
			}
			note := "[audit:review] 의심=" + truncateRunes(strings.TrimSpace(identity+" / "+reason), 80)
			tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
       last_dataqa_at=now(), updated_at=now()
 WHERE id=$1::uuid AND status='active' AND COALESCE(notes,'') NOT LIKE '%[audit:review]%'`, e.id, note)
			if uerr == nil && tag.RowsAffected() > 0 {
				flagged++
				log.Printf("  [audit→review] %s (%s) — 기사=%q (내용판정 불동의)", e.ko, e.etype, identity)
			}
		default: // unclear/무신호 — 내용판정 garbage 동의 시에만 기각(양축 실패).
			if garbage, treason := triage(ctx, e.ko, e.etype); garbage {
				if auditReject(ctx, pool, e.id, a.tier, "뉴스근거 무신호 · triage="+treason) {
					rejected++
					log.Printf("  [audit→reject] %s (%s) — 무근거+내용판정 garbage", e.ko, e.etype)
				}
				continue
			}
			markAuditCursor(ctx, pool, e.id)
		}
	}
	log.Printf("kdb.verify.audit: done rejected=%d review=%d upgraded=%d /%d", rejected, flagged, upgraded, processed)
	return rejected, flagged, upgraded, processed, nil
}

// auditReject — 이중 게이트 통과 오염을 tombstone 기각한다(복원 스냅샷 필수 규약).
func auditReject(ctx context.Context, pool *pgxpool.Pool, id, tier, detail string) bool {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'status', 'active', $2, 'audit-contam-reject', $3, 'gemma')`,
		id, tier, truncateRunes(detail, 200))
	note := "[audit:contam-reject] " + truncateRunes(detail, 80)
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected',
       notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
       last_dataqa_at=now(), updated_at=now()
 WHERE id=$1::uuid AND status='active'`, id, note)
	return err == nil && tag.RowsAffected() > 0
}

func markAuditCursor(ctx context.Context, pool *pgxpool.Pool, entityID string) {
	_, _ = pool.Exec(ctx,
		`UPDATE kwave_entities SET last_dataqa_at=now() WHERE id=$1::uuid`, entityID)
}
