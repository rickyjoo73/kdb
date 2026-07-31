package kdb

// reverted_terminate_drain — 07-21 오염감사로 active→candidate 강등된 뒤 어느 레인에도
// 속하지 못해 무기한 정체된 항목을 **종결(rejected)** 시킨다.
//
// ★왜 승급이 아니라 종결인가(2026-07-31 실측):
// 강등 잔존 191건(person 182·character 9, 최장 68일)은 187건이 wikidata ref 만 보유한다.
// 위키데이터 라이브 조회 결과 K-엔터테인먼트 인물은 **0건**이었다 —
//
//	가람 Q69509682   → P31=Q3409032 "Korean unisex given name"  (인명요소 항목)
//	황윤서 Q135354657 → South Korean basketball player
//	이병욱 Q16090884  → South Korean politician
//	송지윤 Q123157177 → South Korean association football player
//	이혜원 Q12613186  → South Korean entrepreneur
//	김은정 Q6408614   → South Korean curler
//
// 즉 07-21 감사도, 이들을 막는 두 가드도 전부 옳았다. 가드는 이렇게 이중으로 걸려 있다.
//   - hasOfficialPromotionAnchor: person 은 wikidata ref 단독으로 앵커 불가(07-21 #12,
//     실측 오염 20%).
//   - DrainWikidataPersonCandidates: 이미 wikidata ref 가 있는 건 이름검색 승급에서 제외
//     (동명이인 오매칭 방어 — 이정후→야구선수, 권도형→Do Kwon).
//
// 두 가드 사이에 낀 이들은 **승급도 종결도 안 되는 사각지대**에 있었다. 서빙에 안 쓰이면서
// candidate 풀만 차지하고 드레인마다 반복 조회된다. 이 드레인이 그 사각지대를 닫는다.
//
// ★안전(over-reject 금칙): 기각은 **증거가 있을 때만**이다.
//   - P31 이 이름요소/동음이의 클래스 → 기각(실존 엔티티가 아니라 "이름" 항목).
//   - description 이 있고 K-엔터 allowlist 에 없음 → 기각(직업이 명시적으로 비-엔터).
//   - description 이 비어 있음 → **판정 보류**(빈칸>틀린값). 근거 없이 죽이지 않는다.
//   - wikidata 외 공식 앵커(tmdb/kofic/naver-people 등)를 하나라도 가지면 대상에서 제외.
//
// 전건 kwave_kdb_recheck_log(verdict='revert-terminate')로 기록해 revert 가능하다.
// 조회한 description 은 raw_payload 에 캐시한다 — 기존 캐시가 186/191(97%) 비어 있어
// 직업 판별이 불가능했던 것이 이 사각지대가 오래 방치된 직접 원인이었다.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// DrainTerminateRevertedCandidates — 강등 잔존 candidate 중 wikidata 근거상 비-K 인 것을
// rejected 로 종결한다. 반환=(종결 수, 조회 수).
func DrainTerminateRevertedCandidates(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int) (rejected, checked int) {
	if pool == nil || cl == nil {
		return 0, 0
	}
	if limit <= 0 {
		limit = 40
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, r.external_id
  FROM kwave_entities e
  JOIN kwave_entity_external_refs r ON r.entity_id = e.id AND r.provider = 'wikidata'
 WHERE e.status = 'candidate'
   AND e.operator_locked = false
   AND COALESCE(e.notes,'') LIKE '%audit-revert%'
   AND COALESCE(e.notes,'') NOT LIKE '%[revert-term:%'
   -- wikidata 외 공식 앵커가 하나라도 있으면 다른 승급 경로가 살아 있다 — 건드리지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r2
                    WHERE r2.entity_id = e.id AND r2.provider <> 'wikidata'
                      AND r2.provider IN (`+OfficialPromotionProviderSQLList()+`))
 ORDER BY e.created_at
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.revert-term: select: %v", err)
		return 0, 0
	}
	type row struct{ id, ko, qid string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.qid) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if strings.TrimSpace(it.qid) == "" {
			continue
		}
		checked++
		ent, ferr := cl.Fetch(ctx, it.qid)
		time.Sleep(350 * time.Millisecond) // Wikidata 예의
		if ferr != nil || ent == nil {
			continue
		}

		desc := strings.TrimSpace(ent.Descriptions["en"])
		if desc == "" {
			desc = strings.TrimSpace(ent.Descriptions["ko"])
		}
		// description 캐시 — 다음 판정이 라이브 조회 없이도 근거를 갖게 한다.
		if desc != "" {
			payload, _ := json.Marshal(map[string]string{"description": desc, "label": ent.Labels["ko"]})
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_external_refs
   SET raw_payload = raw_payload || $2::jsonb, fetched_at = now()
 WHERE entity_id = $1 AND provider = 'wikidata'`, it.id, string(payload))
		}

		var reason string
		if isName, cls := ent.IsNameElement(); isName {
			reason = fmt.Sprintf("wikidata %s 가 이름요소/동음이의 항목(P31=%s) — 실존 엔티티 근거 아님", it.qid, cls)
		} else if desc == "" {
			// 근거 없음 → 판정 보류. 재조회만 막고 다음 라운드로 넘긴다.
			continue
		} else if isKEntertainerDesc(desc) {
			// K-엔터로 확인 — 종결 대상 아님. 승급은 앵커 요건을 갖춘 다른 레인의 몫이다.
			_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[revert-term:keep] wikidata desc=' || $2,
       updated_at = now()
 WHERE id = $1 AND status = 'candidate'`, it.id, desc)
			continue
		} else {
			reason = fmt.Sprintf("wikidata %s 직업이 비-엔터: %q", it.qid, desc)
		}

		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status = 'rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[revert-term:reject] ' || $2,
       updated_at = now()
 WHERE id = $1 AND status = 'candidate' AND operator_locked = false`, it.id, reason)
		if uerr != nil || tag.RowsAffected() == 0 {
			continue
		}
		rejected++
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, models, agreed, evidence)
VALUES ($1, $2, 'revert-terminate', 'wikidata-p31-desc', true, $3)`, it.id, it.ko, reason)
	}
	if checked > 0 {
		log.Printf("kdb.revert-term: checked=%d rejected=%d", checked, rejected)
	}
	return rejected, checked
}
