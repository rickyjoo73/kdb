package verify

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	kdbroot "github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/googlenews"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

// EvidencePass — unverified 상위 n 개를 검색근거(네이버 news + SearXNG 폴백) + gemma 판정으로
// evidenced 로 업그레이드 시도한다(handoff 27차 §6 증분2 tier2). 권위앵커가 없는 롱테일 전용.
//
// ★설계 근거(실측): encyc 단독 역할토큰 매칭은 노이지(아이유→"국제단위" 등) → encyc 대신
// news(+역할어)를 "증거"로 모아 gemma 가 실재성/동일성을 판정한다(판정 주체=gemma, 네이버=조연).
// 쿼터(1,000/일) 캡 = n(엔티티당 news 1콜). 결정론 스윕이 강등하지 못하게 evidence='search+gemma%'.
//
// 반환: (real 승급, contam 의심 플래그, processed, err).
func EvidencePass(ctx context.Context, pool *pgxpool.Pool, n int) (int, int, int, error) {
	nv, err := naver.NewFromSettings(ctx, pool)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("verify evidence: %w", err)
	}
	judge := newVerifyJudge()

	// ★2026-08-15: 대상에 **evidenced 인데 되짚을 근거가 없는 건**을 더한다.
	//
	// 왜: 그 상태가 실측 3,419건이고(전량 verified_tier_at 08-10 주), 종전 선정은
	// unverified 만 봐서 **한 번 잘못 붙은 evidenced 는 영원히 재검토되지 않았다**.
	// 지표(invariant `evidenced-unretrievable`)는 그걸 세고 있었지만 고치는 경로가 없었다.
	//
	// 일괄 강등하지 않는 이유: 그중 상당수는 **실재가 맞고 근거만 안 남긴 것**이다
	// (`사랑의 리퀘스트`=KBS 1TV 예능, `Kill This Love`). 전부 unverified 로 내리면 참인
	// 주장까지 버린다. 다시 조사해서 근거를 남길 수 있으면 남기고(주장 유지), 못 남기면
	// 그때 내린다(주장 철회) — 어느 쪽이든 위반은 사라지고 값은 정직해진다.
	//
	// reexamine=true 인 건은 아래에서 "근거 못 남김 → unverified 강등" 경로를 탄다.
	//
	// ★배치를 두 풀에서 나눠 뽑는다. 한 쿼리에 `ORDER BY unverified DESC` 로 두면
	// **재조사분에 영원히 못 닿는다** — unverified 가 565건이고 재조사분이 3,419건이라,
	// 앞쪽이 매 회차 배치를 다 먹고 강등·승급으로 다시 채워진다. 이 저장소가 이미 겪은
	// 기아 패턴이다(tmdb-locale 이 `updated_at DESC` 로 백로그에 못 닿던 것, 12c5060).
	//
	// ★단 **예약이 아니라 배분**이다(2026-08-25). 종전엔 n/2 를 재조사 풀에 무조건
	// 예약했는데, 08-16 의 retrievable 가드로 그 풀이 0 이 된 뒤로는 배치의 절반이 매
	// 회차 빈 쿼리로 갔다(실측: 야간 레인이 batch 300 을 선언하고 대상 150 을 집었다).
	// 그래서 재조사 풀을 **먼저** 뽑고, 그 몫이 남으면 unverified 에 돌려준다.
	nRe := n / 2
	if nRe < 1 {
		nRe = 1
	}
	re, err := queryEvPool(ctx, pool, `
		SELECT e.id::text, e.canonical_ko, e.entity_type::text,
		       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'::text[]), true
		  FROM kwave_entities e
		  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
		 WHERE e.status='active' AND e.canonical_ko <> ''
		   AND COALESCE(e.notes,'') NOT LIKE '%[retrace:review]%'
		   AND e.verification_tier='evidenced'
		   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id)
		   AND COALESCE(array_length(e.source_urls,1),0)=0
		   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_evidence_refs v WHERE v.entity_id=e.id)
		 -- 오래 거짓 주장을 달고 있던 것부터
		 ORDER BY e.verified_tier_at ASC NULLS FIRST
		 LIMIT $1`, nRe)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("verify evidence query(reexamine): %w", err)
	}

	// ★시도 원장을 선정에 끼운다(2026-08-25). **이 레인만 빠져 있었다.**
	//
	// 증상: 야간 드레인이 54라운드를 도는데 승급이 밤당 2~4건이고 잔량이 안 줄었다.
	// 원인은 소스도 수율도 아니라 선정이다 — 실패 경로가 엔티티에 아무것도 쓰지 않으므로
	// (tier·confidence·updated_at 불변) 다음 라운드가 같은 ORDER BY 로 **같은 150건**을
	// 다시 집었다. 실측: 선정 상위 150 중 149건이 3일 이상 미변경, 밤새 8,100회 조회의
	// 실제 대상이 150건. 그 아래 1,665건(song_album 330·person 287·show 224…)은 한 번도
	// 안 불렸다. head 가 승급된 만큼만 밀리는데 승급이 2건이라 벽이 됐다.
	//
	// 앵커 레인 넷(kowiki·itunes·tmdb·mbgroup)은 전부 이 술어를 갖고 있어서 정상 소진한다.
	// 규칙을 새로 쓰지 않고 같은 헬퍼를 쓴다 — 사본을 만들면 규칙이 갈라진다.
	// 지문에 canonical_ko·notable_works·external_refs 가 들어 있어(0100) 질의 입력이
	// 바뀌거나 앵커가 붙으면 90일을 기다리지 않고 자동으로 다시 집힌다.
	nUn := n - len(re)
	if nUn < 0 {
		nUn = 0
	}
	un, err := queryEvPool(ctx, pool, `
		SELECT e.id::text, e.canonical_ko, e.entity_type::text,
		       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'::text[]), false
		  FROM kwave_entities e
		  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
		 WHERE e.status='active' AND e.canonical_ko <> ''
		   AND COALESCE(e.notes,'') NOT LIKE '%[retrace:review]%'
		   AND e.verification_tier='unverified'
		   AND `+kdbroot.FillRetryPredicate("e", "$2")+`
		 -- 임계 0.75 에 근접할수록 실재 가능성↑
		 ORDER BY e.confidence DESC, e.updated_at DESC
		 LIMIT $1`, nUn, evidenceSweepField)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("verify evidence query: %w", err)
	}
	ents := append(un, re...)

	reexam := 0
	for _, e := range ents {
		if e.reexamine {
			reexam++
		}
	}
	log.Printf("kdb.verify.evidence: start (대상=%d — unverified %d · 근거없는 evidenced 재조사 %d)",
		len(ents), len(ents)-reexam, reexam)
	upgraded, flagged, processed, demoted := 0, 0, 0, 0
	searchFail := 0

	// demote — 재조사 대상인데 이번에도 근거를 못 남긴 건의 **주장을 철회**한다.
	// unverified 로 내리면 다음 회차에 다시 대상이 되므로(자기치유) 영구 낙인이 아니다.
	// 이미 evidenced 인 동안만 동작(멱등). unverified 신규건은 손대지 않는다.
	demote := func(e evEntity, why string) {
		if !e.reexamine {
			return
		}
		tag, uerr := pool.Exec(ctx, `
			UPDATE kwave_entities
			   SET verification_tier='unverified',
			       verification_evidence=$2,
			       verified_tier_at=now()
			 WHERE id=$1 AND verification_tier='evidenced'`,
			e.id, truncateRunes("근거 되짚기 실패("+why+") — evidenced 철회 2026-08-15", 200))
		if uerr == nil && tag.RowsAffected() > 0 {
			demoted++
			log.Printf("  [강등] %s (%s) → unverified — %s", e.ko, e.etype, why)
		}
	}

	for _, e := range ents {
		processed++
		// ★2026-08-15: URL 을 들고 다닌다 + 적합성 게이트를 건다. 종전에는 둘 다 없어서
		// (a) 무관 검색결과로 승급하고 (b) 근거 URL 을 버린 채 evidenced 를 주었다.
		evHits, searchOK := gatherRelevantHitsStatus(ctx, nv, e)
		hits := evHitLines(evHits)
		// ★네이버가 답하지 못했으면 판정이 아니다 — 강등 금지(2026-08-15).
		//
		// 쿼터(1,000/일)가 떨어지면 nv.Search 가 실패하고 SearXNG 폴백이 무관 결과를
		// 내며, 적합성 게이트가 그걸 떨궈 `근거 2건 미만`이 된다. 그 상태는 "근거가
		// 없다"와 구별되지 않고 재조사 경로에서 곧바로 강등이 된다 — **쿼터가 떨어지는
		// 순간부터 멀쩡한 엔티티가 줄줄이 unverified 로 내려간다.** 배치를 키울수록
		// 위험이 커지는 구조라, 배치를 올리기 전에 이걸 먼저 막아야 한다.
		if !searchOK {
			searchFail++
			// 연속 실패는 쿼터 소진이다. 남은 건을 계속 돌아봐야 같은 실패이므로 중단한다
			// (시간 낭비 방지 + 실패를 로그에 드러냄).
			if searchFail >= searchFailStop {
				log.Printf("kdb.verify.evidence: 뉴스소스 연속 무응답 %d회 — 중단"+
					" (처리 %d/%d, 강등 없음)", searchFail, processed, len(ents))
				break
			}
			continue
		}
		searchFail = 0
		if len(hits) < 2 {
			demote(e, "적합한 근거 2건 미만")
			// ★판정을 원장에 남긴다. 뉴스 두 소스가 **답은 했는데** 적합한 기사가 없는
			// 상태이므로 전송실패가 아니라 판정이다 — 내일 다시 물어도 같은 답이다.
			// (안 남기면 이 건이 head 에 눌러앉아 다음 라운드마다 다시 뽑힌다.)
			kdbroot.MarkFillAttempt(ctx, pool, e.id, evidenceSweepField,
				"no-evidence", "적합한 근거 2건 미만")
			continue // 근거 부족 — 판정 보류(다음 라운드/운영자 검토)
		}
		v, err := judgeVerify(ctx, judge, e, hits)
		if err != nil {
			// 전송실패는 판정이 아니다 — 강등하지 않는다(MarkFillAttempt 와 같은 원칙).
			log.Printf("  [err] %s (%s): %v", e.ko, e.etype, err)
			continue
		}
		reason := strings.TrimSpace(v.Reason)
		identity := strings.TrimSpace(v.Identity)
		switch v.Verdict {
		case "real":
			// ★근거 대장 적재가 승급의 선행조건(2026-08-15). 0건이면 보류 — 다음 회차에
			// 다시 본다. 오거부 위험 0 이고, 되짚을 수 없는 evidenced 는 생기지 않는다.
			if !requireEvidenceForTier(ctx, pool, e, evidenceSweepField, evHits) {
				demote(e, "근거 URL 적재 0건")
				kdbroot.MarkFillAttempt(ctx, pool, e.id, evidenceSweepField,
					"no-evidence", "근거 URL 적재 0건")
				continue
			}
			// 기사 맥락으로 실존 확정 → evidenced 승급 + 특정된 정체를 근거에 기록.
			ev := "search+gemma"
			if identity != "" {
				ev = "search+gemma: " + truncateRunes(identity, 70)
			} else if reason != "" {
				ev = "search+gemma: " + truncateRunes(reason, 70)
			}
			// unverified 이거나 재조사 중인 evidenced 인 동안만 쓴다. authoritative 로 올라간
			// 건은 건드리지 않는다(그새 권위앵커가 붙었으면 결정론 스윕이 이미 올렸다).
			// 재조사 건은 티어가 그대로 evidenced 지만, 위에서 근거 대장을 채웠으므로 이제
			// **주장이 되짚어진다** — 위반이 값 변경 없이 해소된다.
			tag, uerr := pool.Exec(ctx, `
				UPDATE kwave_entities
				   SET verification_tier='evidenced', verification_evidence=$2, verified_tier_at=now()
				 WHERE id=$1 AND verification_tier IN ('unverified','evidenced')`, e.id, ev)
			if uerr == nil && tag.RowsAffected() > 0 {
				upgraded++
				log.Printf("  [real] %s (%s) → %s", e.ko, e.etype, ev)
			}
		case "contaminated":
			// ★기본은 자동 tombstone 금지(2026-07-05 실측): 뉴스검색 근거는 오탐이 잦다 — 일반어명
			// (있지=ITZY·웨이브=Wavve)이나 비뉴스성 실존체(대한가수협회·니쥬=NiziU)가 무근거로
			// contaminated 판정돼 실재 K 를 오기각한다(실측 FP ~33%). 실재 K 오거부는 오너 최상위
			// 금칙이라 기본은 '리뷰 플래그'만(서빙 유지). 하드 기각은 opt-in — KDB_VERIFY_AUTO_REJECT=1
			// (완전 삭제 아님: rejected tombstone + dataqa_log revert 스냅샷 → 복원 가능).
			if os.Getenv("KDB_VERIFY_AUTO_REJECT") == "1" {
				if rejectContaminated(ctx, pool, e.id, identity, reason) {
					flagged++
					log.Printf("  [contam→reject] %s (%s) — 기사는 %q: %s", e.ko, e.etype, identity, truncateRunes(reason, 55))
				}
			} else if flagContamReview(ctx, pool, e.id, identity, reason) {
				kdbroot.MarkFillAttempt(ctx, pool, e.id, evidenceSweepField,
					"contaminated", truncateRunes(identity+" / "+reason, 200))
				flagged++
				log.Printf("  [contam→review] %s (%s) — 기사는 %q: %s", e.ko, e.etype, identity, truncateRunes(reason, 55))
			}
		default: // unclear
			// 유지 — 근거 부족. 운영자 검토 대상.
			// 단 재조사 건은 "확증됨"이라고 주장 중이므로 그 주장은 철회한다.
			demote(e, "재조사 판정 unclear")
			// gemma 가 기사를 읽고 내린 판정이다(전송실패 아님) — 원장에 남긴다.
			kdbroot.MarkFillAttempt(ctx, pool, e.id, evidenceSweepField,
				"unclear", truncateRunes(reason, 200))
		}
	}
	log.Printf("kdb.verify.evidence: done real=%d rejected=%d 강등=%d /%d",
		upgraded, flagged, demoted, processed)
	return upgraded, flagged, processed, nil
}

// rejectContaminated — 기사 맥락이 명확한 오염(다른 정체/대표작 모순/K-아님)으로 판정한 엔티티를
// 자동 기각한다(오너 지시). ★완전 삭제(DELETE) 아님 — status='rejected' tombstone 으로 서빙에서만
// 제거하고, kwave_kdb_dataqa_log 에 이전 상태를 스냅샷(locale='status', verdict='retrace-contam-reject')
// 해 gemma 오판 시 복원 가능. active 인 동안만(멱등). revert: 그 로그로 status='active' 되돌림.
func rejectContaminated(ctx context.Context, pool *pgxpool.Pool, id, identity, reason string) bool {
	var curStatus, curTier string
	if err := pool.QueryRow(ctx,
		`SELECT status, COALESCE(verification_tier,'') FROM kwave_entities WHERE id=$1`, id).
		Scan(&curStatus, &curTier); err != nil || curStatus != "active" {
		return false
	}
	detail := strings.TrimSpace(identity + " / " + reason)
	// 1) revert 스냅샷.
	_, _ = pool.Exec(ctx, `
		INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
		VALUES ($1, 'status', $2, $3, 'retrace-contam-reject', $4, 'gemma')`,
		id, curStatus, curTier, truncateRunes(detail, 200))
	// 2) 기각 + notes 사유(멱등).
	note := "[retrace:contam-reject] 기사=" + truncateRunes(detail, 80)
	tag, err := pool.Exec(ctx, `
		UPDATE kwave_entities
		   SET status='rejected',
		       notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
		       updated_at=now()
		 WHERE id=$1 AND status='active'`, id, note)
	return err == nil && tag.RowsAffected() > 0
}

// flagContamReview — contaminated 의심을 자동 tombstone 하지 않고 운영자 검토용으로 표시만 한다
// (서빙 유지). 자동기각의 근거검색 오탐(실재 K 오거부)을 피하는 기본 경로. active·미표시인 동안만
// (멱등) notes 에 [retrace:review] 태그 + 의심 정체를 남겨 admin 검증뷰/엔티티 노트에 노출한다.
// status·verification_tier 는 불변(unverified 유지) — EvidencePass SELECT 가 이 태그를 제외하므로
// 재처리(쿼터 소모)도 안 한다. 운영자가 확인 후 태그를 지우거나 수동 기각/승급.
func flagContamReview(ctx context.Context, pool *pgxpool.Pool, id, identity, reason string) bool {
	detail := strings.TrimSpace(identity + " / " + reason)
	note := "[retrace:review] 의심=" + truncateRunes(detail, 80)
	tag, err := pool.Exec(ctx, `
		UPDATE kwave_entities
		   SET notes = CASE WHEN COALESCE(notes,'')='' THEN $2 ELSE notes || ' ' || $2 END,
		       updated_at=now()
		 WHERE id=$1 AND status='active' AND COALESCE(notes,'') NOT LIKE '%[retrace:review]%'`, id, note)
	return err == nil && tag.RowsAffected() > 0
}

// RevertContamRejects — 역추적 자동기각을 되돌린다(gemma 오판 복구). dataqa_log 의
// retrace-contam-reject(미revert) 항목을 찾아 엔티티 status 를 이전값(active)으로 복원하고
// 로그를 revert 처리. 반환=복원 수.
func RevertContamRejects(ctx context.Context, pool *pgxpool.Pool, n int) (int, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, entity_id::text, old_value FROM kwave_kdb_dataqa_log
		 WHERE verdict IN ('retrace-contam-reject','audit-contam-reject') AND reverted_at IS NULL
		 ORDER BY created_at DESC LIMIT $1`, n)
	if err != nil {
		return 0, err
	}
	type logRow struct {
		logID    int64
		entityID string
		oldVal   string
	}
	var logs []logRow
	for rows.Next() {
		var lr logRow
		if err := rows.Scan(&lr.logID, &lr.entityID, &lr.oldVal); err != nil {
			rows.Close()
			return 0, err
		}
		logs = append(logs, lr)
	}
	rows.Close()
	restored := 0
	for _, lr := range logs {
		tag, err := pool.Exec(ctx, `
			UPDATE kwave_entities SET status=$2, updated_at=now()
			 WHERE id=$1 AND status='rejected'`, lr.entityID, lr.oldVal)
		if err == nil && tag.RowsAffected() > 0 {
			restored++
		}
		_, _ = pool.Exec(ctx, `UPDATE kwave_kdb_dataqa_log SET reverted_at=now() WHERE id=$1`, lr.logID)
	}
	log.Printf("kdb.verify.revert: restored=%d/%d", restored, len(logs))
	return restored, nil
}

type evEntity struct {
	id, ko, etype, role string
	works               []string
	// reexamine — 이미 evidenced 인데 **되짚을 근거가 없어** 다시 보는 건인가.
	// true 면 이번 회차에 근거를 못 남길 때 등급을 unverified 로 내린다(주장 철회).
	reexamine bool
}

// queryEvPool — 선정 쿼리 하나를 evEntity 목록으로 읽는다. 두 풀이 컬럼 순서를 공유하므로
// 스캔을 한 곳에만 둔다(종전 UNION ALL 을 두 쿼리로 나누면서 스캔이 두 벌 될 뻔했다).
func queryEvPool(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]evEntity, error) {
	rows, err := pool.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []evEntity
	for rows.Next() {
		var e evEntity
		if err := rows.Scan(&e.id, &e.ko, &e.etype, &e.role, &e.works, &e.reexamine); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// gatherEvidence — 네이버 news(+역할어) 검색으로 증거 코퍼스(제목 — 스니펫 라인)를 모은다.
// 결과가 빈약하면 SearXNG 로 보강. 각 엔티티당 네이버 1콜(쿼터 절약).
func gatherEvidence(ctx context.Context, nv *naver.Client, e evEntity) []string {
	query := e.ko
	if w := roleWord(e.etype); w != "" {
		query = e.ko + " " + w
	}
	return gatherEvidenceQuery(ctx, nv, query)
}

// evHit — 증거 1건. Line 은 판정 프롬프트에 들어가는 텍스트, URL/Title/Provider 는
// 사후검증용 출처다. 2026-07-31 이전에는 Line 만 만들고 URL 을 이 자리에서 버려서
// 승급 근거를 되짚을 수 없었다(migrations/0098 주석 참조).
type evHit struct {
	Line     string
	URL      string
	Title    string
	Provider string
}

// evidenceSweepField — 시도 원장(kwave_kdb_enrich_attempts.field)에 쓰는 이름.
// 근거대장(kwave_kdb_evidence_refs.lane)이 쓰는 라벨과 **같은 문자열**이다 — 한 레인이
// 두 이름을 들면 나중에 원장과 대장을 대조할 수 없다.
const evidenceSweepField = "evidence-sweep"

// searchFailStop — 뉴스 소스가 연속 이 횟수만큼 응답하지 않으면 회차를 끝낸다.
// 5 인 이유: 개별 질의의 일시 오류(타임아웃 1~2회)는 넘기고, 쿼터 소진처럼 **모든 후속
// 호출이 실패하는 상태**만 잡는다.
const searchFailStop = 5

// gatherRelevantHits — 근거 수집 + 적합성 게이트를 한 번에. 세 레인이 **같은 입력**을
// 써야 판정이 재현되고, 게이트가 한 곳에서만 적용되는 사태를 막는다.
func gatherRelevantHits(ctx context.Context, nv *naver.Client, e evEntity) []evHit {
	hits, _ := gatherRelevantHitsStatus(ctx, nv, e)
	return hits
}

// gatherRelevantHitsStatus — 위와 같되 네이버 응답 여부를 함께 돌려준다.
// searchOK=false 는 "근거가 없다"가 아니라 "물어보지 못했다"이다 — 강등 금지.
func gatherRelevantHitsStatus(ctx context.Context, nv *naver.Client, e evEntity) ([]evHit, bool) {
	// ★질의를 2단으로 나눈다(2026-08-25). 종전엔 항상 `이름 + 역할어` 하나였다.
	//
	// 왜: 역할어는 네이버 news 를 겨냥해 붙인 것인데(07-31), 08-16 에 Google News RSS 가
	// 1순위가 되면서 **같은 질의가 다른 엔진에 들어가게 됐다.** 구글 뉴스는 추가 낱말을
	// 또 하나의 매칭 대상으로 삼아 표류한다 — 실측: `빅크 브랜드` → "삼성 브랜드 가치…
	// 빅테크"·"빅링크BIZ", `코엑스 메가박스 브랜드` → 쿠팡 팝업 기사. 이름만 물으면
	// 둘 다 정답이 첫 줄에 온다(`빅크, 스티비 어워즈 골드…`).
	//
	// ★★그렇다고 역할어를 없애면 안 된다 — 25건 A/B 로 쟀다(적합근거 2건 이상 확보:
	// 이름만 21 · 역할어 16). 숫자는 이름만이 낫지만 **이득의 상당수가 쓰레기**다
	// (`Knife`→영문 살인사건 기사, `MARS`→게임 Surviving Mars, `Blu`→시계 브랜드,
	// `차세계`→**3차 세계대전**). 반대로 `Team H` 는 이름만 0 · 역할어 3 이었다.
	// 짧거나 라틴인 이름은 이름만으로 안 걸리고, 긴 한글 고유명사는 역할어가 방해만 한다.
	//
	// 그래서 고르지 않고 **순서를 준다**: 이름만으로 먼저 묻고, 적합 근거가 모자랄 때만
	// 역할어를 붙여 한 번 더 묻는다. 성공하는 다수는 호출 1회로 끝나고(쿼터·시간 절약),
	// 짧은 이름만 2회를 쓴다.
	//
	// ⚠ 이 게이트는 부분문자열 일치라 `차세계` ⊂ `3차 세계대전` 을 못 막는다(노트 21 이
	// kowiki 이름사전에서 푼 낱말경계 규칙이 여기엔 없다). 그 계층은 gemma 판정 몫이다.
	hits, searchOK := gatherEvidenceHitsStatus(ctx, nv, e.ko)
	kept, dropped, total := applyRelevanceGate(hits, e.ko)

	if len(kept) < 2 {
		if w := roleWord(e.etype); w != "" {
			more, ok2 := gatherEvidenceHitsStatus(ctx, nv, e.ko+" "+w)
			searchOK = searchOK || ok2
			mk, md, mt := applyRelevanceGate(more, e.ko)
			dropped, total = dropped+md, total+mt
			kept = mergeHitsByURL(kept, mk)
		}
	}
	if dropped > 0 {
		log.Printf("  [적합성] %s (%s): 무관 근거 %d/%d 제외", e.ko, e.etype, dropped, total)
	}
	return kept, searchOK
}

// applyRelevanceGate — filterRelevantHits 를 게이트 on/off 와 함께 감싼다. (남은 것, 버린 수, 전체).
func applyRelevanceGate(hits []evHit, ko string) ([]evHit, int, int) {
	if !relevanceGateEnabled() {
		return hits, 0, len(hits)
	}
	kept, dropped := filterRelevantHits(hits, ko)
	return kept, dropped, len(hits)
}

// mergeHitsByURL — 두 질의의 결과를 합치되 같은 기사를 두 번 세지 않는다. 근거 2건 하한이
// **서로 다른 기사 2건**을 뜻해야 하므로 중복 제거는 선택이 아니라 요건이다.
func mergeHitsByURL(a, b []evHit) []evHit {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]evHit, 0, len(a)+len(b))
	for _, h := range append(append([]evHit{}, a...), b...) {
		u := strings.TrimSpace(h.URL)
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, h)
	}
	return out
}

// requireEvidenceForTier — evidenced 승급의 공통 선행조건. 판정에 쓴 근거의 URL 을 대장에
// 적재하고, 0건이면 false 를 돌려준다(호출측은 승급하지 말고 **보류**할 것 — 기각 아님).
//
// ★왜 함수로 뽑았나(2026-08-15): 이 규칙이 세 레인에 필요한데 손으로 쓴 곳이 하나뿐이었다
// (candidate_evidence.go, 08-02 기제2). 나머지 둘 — ActiveAuditPass·EvidenceSweep — 이
// 빠져서 **되짚을 수 없는 evidenced 3,419건**이 생산됐다(전량 verified_tier_at 08-10 주).
// 이 저장소가 반복해서 밟은 계열이다: 같은 규칙을 두 곳 이상에 손으로 쓰면 한쪽이 빠진다
// (MarkLatinOriginRejects 가 FillRetryPredicate 를 빠뜨린 것과 같은 구조, 08-08).
//
// tier='evidenced' 의 명제는 "독립 확증됨"이고, 확증은 되짚을 수 있어야 성립한다.
// 대장을 **먼저** 쓰는 순서가 핵심이다 — 승급 뒤 best-effort 로 적재하면 적재가 0건이어도
// evidenced 가 이미 나가 있다.
func requireEvidenceForTier(ctx context.Context, pool *pgxpool.Pool, e evEntity, lane string, hits []evHit) bool {
	if n := recordEvidenceRefs(ctx, pool, e.id, lane, hits); n > 0 {
		return true
	}
	log.Printf("  [근거미기록] %s (%s): 대장 적재 0건 — 승급 보류(evidenced 미부여)", e.ko, e.etype)
	return false
}

// evHitLines — 판정 프롬프트 입력(종전 []string 과 동일한 내용·순서).
func evHitLines(hits []evHit) []string {
	lines := make([]string, 0, len(hits))
	for _, h := range hits {
		lines = append(lines, h.Line)
	}
	return lines
}

// --- 적합성 게이트 (2026-07-31) -------------------------------------------
//
// ★왜 필요한가(실측): 근거 URL 을 보존하기 시작하자마자, 이 레인이 **엔티티와 무관한
// 검색결과로 승급**시키고 있다는 게 드러났다. 표본 10건 중 근거가 엔티티를 실제로
// 언급한 건 1건뿐이었다.
//
//	권욱     ← 이세돌·알파고 바둑기사 + 영국 가스회사 + LinkedIn "Sonny Parmar"
//	셔더로드 ← Google 홈페이지 6건          은체   ← "How to Get Help in Windows 11" 6건
//	정중식   ← 필리핀 BancNet 결제사이트    HYBE LABELS ← pinkbike 산악자전거
//
// 원인 2층: ①naver-news 가 무결과면 SearXNG **전역** 검색으로 폴백하는데 한국 고유명사에
// 일반 웹페이지를 반환한다 ②판정 프롬프트 규칙 3("무관 결과뿐이면 unclear, 억지 추론
// 금지")이 있는데도 gemma 가 근거 대신 사전지식으로 real 을 낸다.
//
// LLM 에게 "무관함을 알아채라"고 맡기는 대신 **결정적으로 걸러서 아예 안 보여준다.**
// 오거부 위험은 없다 — 걸러서 근거가 부족해지면 기각이 아니라 보류(쿨다운)다.
// ★한계: 동명이인은 못 잡는다(임미라=대전시 공무원 기사에 이름이 실제로 나온다).
// 이건 판정기 몫으로 남는다.

// normalizeForMatch — 공백·구두점·대소문자를 지운 비교용 형태.
func normalizeForMatch(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case ' ', '\t', ' ', '·', '‧', '.', ',', '\'', '"', '’', '“', '”',
			'-', '–', '—', '~', '/', '\\', '(', ')', '[', ']', '{', '}', '!', '?', ':', ';', '_':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// nameVariants — canonical_ko 에서 뽑은 비교용 이름 후보. 괄호 병기("셋 더 템포(SET THE
// TEMPO)")는 안팎을 각각 하나의 변형으로 본다 — 기사는 둘 중 하나만 쓰기 때문이다.
// 2글자 미만 변형은 버린다(아무 데나 걸린다).
func nameVariants(ko string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		n := normalizeForMatch(s)
		if len([]rune(n)) < 2 || seen[n] {
			return
		}
		seen[n] = true
		out = append(out, n)
	}
	add(ko)
	if i := strings.IndexAny(ko, "(（"); i >= 0 {
		add(ko[:i])
		if j := strings.LastIndexAny(ko, ")）"); j > i {
			add(ko[i+1 : j])
		}
	}
	return out
}

// filterRelevantHits — 엔티티명을 언급하지 않는 근거를 버린다. 반환 (남은 것, 버린 수).
// 변형을 하나도 못 만들면(1글자 이름 등) 거르지 않는다 — 판단 근거가 없을 땐 종전 동작.
func filterRelevantHits(hits []evHit, ko string) ([]evHit, int) {
	vars := nameVariants(ko)
	if len(vars) == 0 {
		return hits, 0
	}
	kept := make([]evHit, 0, len(hits))
	for _, h := range hits {
		n := normalizeForMatch(h.Line)
		match := false
		for _, v := range vars {
			if strings.Contains(n, v) {
				match = true
				break
			}
		}
		if match {
			kept = append(kept, h)
		}
	}
	return kept, len(hits) - len(kept)
}

// gatherEvidenceQuery — 완성된 쿼리 문자열로 동일한 news+SearXNG 수집을 수행한다.
// (2026-07-23 P2: cand-evidence 문맥증강 쿼리가 재사용할 수 있게 분리.)
func gatherEvidenceQuery(ctx context.Context, nv *naver.Client, query string) []string {
	return evHitLines(gatherEvidenceHits(ctx, nv, query))
}

// gatherEvidenceHits — gatherEvidenceQuery 와 동일 수집이되 출처 URL 을 함께 보존한다.
// 라인 생성 규칙·컷오프(8)를 종전과 한 글자도 다르게 두지 않는다 — 판정 입력이 바뀌면
// 승급 결과가 바뀌고, 이 변경의 목적은 추적성 확보지 판정 변경이 아니다.
// ★SearXNG 를 앞에 두는 모드를 만들었다가 **되돌렸다**(2026-08-15).
//
// 동기는 타당했다 — 네이버 news 는 하루 1,000콜 한도이고 그게 처리 속도의 천장이라,
// 재조사 3,365건에 34일이 걸린다. 쿼터 없는 자체 SearXNG 를 앞에 놓으면 천장이 사라진다.
//
// 실측이 막았다. 40건 배치에서 **강등 19 / 승급 1** 이 나왔고(네이버 우선일 때는 8/8),
// 강등 목록에 `니쥬` `BTS 진` `정택운(레오)` `우리 이혼했어요2` `멜론티켓` 같은 명백한
// 실재 엔티티가 무더기로 있었다. 원인을 직접 조회해 확인했다 — SearXNG 는 `니쥬` 질의에
// **헝가리어 구글 크롬 도움말 페이지**를 돌려준다. 07-31 에 기록된 실패 모드 그대로다
// (`셔더로드`←Google 홈페이지 6건, `은체`←"How to Get Help in Windows 11").
//
// **한국 고유명사에 대해 SearXNG 는 네이버의 대체재가 아니다.** 속도를 위해 정확도를
// 버리는 거래였고, 실재 K 오거부는 오너 최상위 금칙이다. 네이버 쿼터는 외부 제약이므로
// 처리량은 그 안에서 올린다(배치 크기·쿼터 배분).

func gatherEvidenceHits(ctx context.Context, nv *naver.Client, query string) []evHit {
	hits, _ := gatherEvidenceHitsStatus(ctx, nv, query)
	return hits
}

// gatherEvidenceHitsStatus — 근거 수집 + **네이버가 실제로 답했는지**를 함께 돌려준다.
//
// ★왜 이 신호가 필요한가(2026-08-15): 네이버가 **쿼터 소진·장애로 실패**하면 SearXNG
// 폴백이 무관 결과를 내고, 적합성 게이트가 그걸 떨궈 `근거 2건 미만`이 된다. 그 상태는
// "이 엔티티는 근거가 없다"와 **로그에서 구별되지 않는다** — 그리고 재조사 경로에서는
// 곧바로 **강등**으로 이어진다. 즉 쿼터가 떨어지는 순간부터 멀쩡한 엔티티가 줄줄이
// unverified 로 내려간다. 배치를 키울수록 위험이 커지는 구조다.
//
// 이 저장소가 네 번 고친 계열과 같다 — **전송실패는 판정이 아니다**(MarkFillAttempt 주석).
// searchOK=false 면 호출측은 강등하지 말고 다음 회차로 넘겨야 한다.
// gnClient — Google News RSS(키·쿼터 없음). 패키지 단위로 하나 — 내장 Limiter 가
// 호출 간격을 지키므로 인스턴스를 나누면 그 간격이 무의미해진다.
var gnClient = googlenews.New()

func gatherEvidenceHitsStatus(ctx context.Context, nv *naver.Client, query string) (hits []evHit, searchOK bool) {
	// ① Google News RSS 를 먼저 — **키도 쿼터도 없다**(2026-08-16, 오너 질문
	// "굳이 네이버 API 가 필요한가?" 에 대한 실측 답). 네이버 1,000콜/일이 시스템
	// 전체 처리 속도의 천장이었는데, 이 소스는 한국어 기사 제목을 질의당 100건까지
	// 준다. 네이버가 못 찾던 `니쥬` `BTS 진` `멜론티켓` `부산MBC` 가 여기서 다 나온다.
	//
	// 웹검색(SearXNG)이 대체재가 못 된 이유와 대비된다 — 문제는 품질이 아니라 **모양**
	// 이었다. 웹검색은 홈페이지를, 뉴스검색은 기사 제목을 준다. 적합성 게이트가
	// "엔티티명이 본문에 나오는가"를 보므로 기사 제목 형태여야 통과한다.
	if res, err := gnClient.Search(ctx, query, 5); err == nil {
		searchOK = true
		for _, it := range res {
			line := strings.TrimSpace(it.Title)
			if it.Source != "" {
				line += " — " + it.Source
			}
			if line == "" || it.Link == "" {
				continue
			}
			hits = append(hits, evHit{
				Line: line, URL: it.Link, Title: it.Title, Provider: "google-news",
			})
		}
	}
	// ② 네이버 news 로 보강(쿼터 1,000/일 — 이제 폴백이라 소비가 크게 준다).
	if len(hits) < 2 && nv != nil {
		if res, err := nv.Search(ctx, "news", query, 5); err == nil {
			searchOK = true
			for _, it := range res.Items {
				title := stripTagsLocal(it.Title)
				line := strings.TrimSpace(title + " — " + stripTagsLocal(it.Description))
				if line != "" && line != "—" {
					hits = append(hits, evHit{
						Line: line, URL: strings.TrimSpace(it.Link),
						Title: strings.TrimSpace(title), Provider: "naver-news",
					})
				}
			}
		}
	}
	// ③ 마지막으로 SearXNG. **searchOK 를 올리지 않는다** — 웹검색은 한국 고유명사에서
	// 뉴스검색의 대체재가 아니고(08-16 실측: `니쥬` 에 헝가리어 크롬 도움말), 여기만
	// 성공한 상태를 "물어봤다"로 세면 뉴스 두 소스가 모두 죽었을 때 그 사실이 가려진다.
	if len(hits) < 2 {
		if res, _, err := websearch.Default().Search(ctx, query, "ko", 6); err == nil {
			for _, r := range res {
				line := strings.TrimSpace(r.Title + " — " + r.Snippet)
				if line != "" && line != "—" {
					hits = append(hits, evHit{
						Line: line, URL: strings.TrimSpace(r.URL),
						Title: strings.TrimSpace(r.Title), Provider: "searxng",
					})
				}
			}
		}
	}
	if len(hits) > 8 {
		hits = hits[:8]
	}
	return hits, searchOK
}

// roleWord — entity_type 별 news 쿼리에 덧붙일 역할/유형어(동명이의 잡음 억제). person 은
// 이름만으로 news 가 잘 나와 생략(직업이 다양). 나머지는 유형어로 특정.
func roleWord(etype string) string {
	switch etype {
	case "drama":
		return "드라마"
	case "movie":
		return "영화"
	case "show":
		return "예능"
	case "song_album":
		return "노래"
	case "group":
		return "그룹"
	case "agency":
		return "엔터테인먼트"
	case "channel_outlet":
		return "방송"
	case "event_tour":
		return "공연"
	case "brand_place":
		return "브랜드"
	}
	return ""
}

func stripTagsLocal(s string) string {
	s = strings.ReplaceAll(s, "<b>", "")
	s = strings.ReplaceAll(s, "</b>", "")
	return strings.TrimSpace(s)
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ── gemma 정체성 판정기 ────────────────────────────────────────────────────

// verifyResult — gemma 의 기사맥락 판별. verdict 3분기:
//
//	real         : 뉴스가 이 이름을 위 역할/대표작의 실존 K-엔티티로 뒷받침 → evidenced 승급.
//	contaminated : 뉴스가 다른 정체를 명확히 보이거나 저장된 대표작/역할과 모순 → 오염 의심 플래그.
//	unclear      : 근거 부족·무관뿐 → 유지(다음 라운드/운영자).
//
// identity = 특정된 정체(누구/무엇). 오너 방향: 단어가 아니라 기사 맥락으로 정체를 특정한다.
type verifyResult struct {
	Verdict  string `json:"verdict"`
	Identity string `json:"identity"`
	Reason   string `json:"reason"`
}

var verifyJudgeSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "verdict": {"type": "string", "enum": ["real", "contaminated", "unclear"]},
    "identity": {"type": "string"},
    "reason": {"type": "string"}
  },
  "required": ["verdict"]
}`)

type verifyInput struct {
	e    evEntity
	hits []string
}

func newVerifyJudge() *agents.Base {
	r := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("VERIFY", "gemma")).
		WithEffort(codexcli.RoleEffort("VERIFY", "low"))
	return agents.NewBase(r, agents.LLMRole{
		Role:   agents.Role("Verify"),
		Schema: verifyJudgeSchema,
		BuildPrompt: func(in any) (string, error) {
			vi, ok := in.(verifyInput)
			if !ok {
				return "", fmt.Errorf("verify: bad input")
			}
			return buildVerifyPrompt(vi), nil
		},
	})
}

func judgeVerify(ctx context.Context, judge *agents.Base, e evEntity, hits []string) (verifyResult, error) {
	var res verifyResult
	err := judge.CallJSON(ctx, verifyInput{e: e, hits: hits}, &res)
	return res, err
}

func buildVerifyPrompt(vi verifyInput) string {
	e := vi.e
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사의 '기사맥락 판별기'입니다.\n")
	b.WriteString("아래 한국 엔티티가 검색된 뉴스 기사에서 어떻게 확인되는지 맥락으로 판별하세요.\n\n")
	b.WriteString("한국어 정식명: " + e.ko + "\n")
	b.WriteString("종류: " + e.etype + "\n")
	if e.role != "" {
		b.WriteString("우리 DB의 역할/직업: " + e.role + "\n")
	}
	if len(e.works) > 0 {
		b.WriteString("우리 DB의 대표작/소속: " + strings.Join(e.works, ", ") + "\n")
	}
	b.WriteString("\n검색된 뉴스(제목 — 스니펫):\n")
	for i, h := range vi.hits {
		if i >= 8 {
			break
		}
		b.WriteString("- " + h + "\n")
	}
	b.WriteString("\n판별 규칙(기사 맥락 기준, 엄격):\n")
	b.WriteString("★핵심: '실존하는 한국 대중문화(K-콘텐츠) 엔티티인가'만 본다. 세부 역할/대표작이 우리 DB와 달라도 실존 K-엔티티면 real 이다(메타데이터는 나중에 보강). 기각(contaminated)은 아예 K-엔티티가 아닐 때만.\n")
	b.WriteString("1. verdict=real: 뉴스가 이 이름을 실존하는 한국 대중문화 " + e.etype + "(가수·배우·아이돌·그룹·작품 등)로 뒷받침. 우리 DB의 역할/작품과 세부가 달라도 실존 K-" + e.etype + "면 real. identity=기사로 특정한 정체(예: SF9 멤버, OO의 노래).\n")
	b.WriteString("2. verdict=contaminated: 이 이름이 '한국 대중문화 엔티티가 아예 아님' — 해외 인물, 일반 단어/명사, 의약품·스포츠·정치 등 무관 분야, 또는 저장된 종류(" + e.etype + ")가 근본적으로 틀림(예: 넷플릭스 다큐멘터리인데 노래·앨범으로 저장). identity=기사가 말하는 실제 정체.\n")
	b.WriteString("3. verdict=unclear: 근거 부족·무관 결과뿐·동명이인 뒤섞여 특정 불가. 억지 추론 금지 — 확실할 때만 real/contaminated.\n")
	b.WriteString("4. reason = 판별 근거 한국어 한 줄(어느 스니펫이 근거인지).\n")
	b.WriteString("JSON 한 개만: {\"verdict\":\"real|contaminated|unclear\",\"identity\":\"...\",\"reason\":\"...\"}\n")
	return b.String()
}
