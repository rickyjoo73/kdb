package kdb

// anchor_verdict_enforce — **이미 판정해 저장해 둔** 앵커 어긋남을 집행한다.
//
// 왜 따로 필요한가. `DrainWithdrawWrongAnchors` 는 `AuditPersonAnchors` 를 불러
// **새로 조회할 대상**을 고른다. 그런데 그 감사는 30일 신선도를 지켜 이미 본 것을
// 다시 보지 않는다(`AnchorAuditFreshness`). 2026-09-15 에 전량 감사를 돌렸으므로
// 그 뒤로 감사기는 **아무것도 돌려주지 않는다.**
//
// 그래서 2026-09-21 실측이 이렇게 갈렸다:
//
//	backlog-watch  anchor-contradicts-type 위반=495   ← 판정은 저장돼 있다
//	anchor-withdraw 조회 40 · 철회 0                  ← 집행기는 저장된 판정을 안 읽는다
//
// 495건 **전부 `verification_tier='authoritative'`** 로 서빙 중이었다. 가장 믿을 만한
// 등급으로 나가는데 근거가 틀렸다고 이미 판정돼 있던 것이다. 실물:
//
//	태권 (movie)          Q69509618 "Korean male given name"  → ja テコン
//	춘향 (show)           "Korean female given name"          → ja 春香 · zh 春香
//	윤정 (channel_outlet) "Korean unisex given name"          → zh 尹贞
//
// 판정하는 자리와 집행하는 자리가 다르면, 판정은 쌓이고 아무 일도 일어나지 않는다.
// 이 저장소가 반복해 밟는 모양이다 — 레인은 고쳤는데 그 레인이 남긴 표시를 아무도 안 걷는다.
//
// ★정책은 새로 만들지 않는다. 무엇을 자동으로 떼고 무엇을 검수로 보내는지는
// `DrainWithdrawWrongAnchors` 가 2026-09-15 dry-run 으로 정한 그대로다 —
// `name-element` 만 자동, 나머지는 검수. 쓰기도 같은 `withdrawOneAnchor` 를 쓴다.
// 여기서 바뀌는 것은 **대상을 어디서 고르느냐** 하나뿐이다.

import (
	"context"
	"log"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StoredAnchorVerdicts — 저장된 어긋남 판정 중 **아직 서빙 중인** 것.
//
// 조건이 `backlog-watch` 의 `anchor-contradicts-type` 과 **글자 그대로 같아야** 한다.
// 다르면 경보가 세는 수와 집행이 보는 수가 갈리고, 지금 이 결함이 그대로 재발한다.
func StoredAnchorVerdicts(ctx context.Context, pool *pgxpool.Pool, limit int) ([]PersonAnchorMismatch, error) {
	if pool == nil || limit <= 0 {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text, a.external_id,
       a.verdict, COALESCE(a.class,''), COALESCE(a.description,''),
       COALESCE(e.verification_tier,''), COALESCE(e.canonical_ja,''),
       COALESCE(e.canonical_ja_source,''), COALESCE(a.label_en,'')
  FROM kwave_entities e
  JOIN kwave_kdb_anchor_audit a
    ON a.entity_id = e.id AND a.verdict <> '' AND a.entity_type = e.entity_type::text
 WHERE e.status = 'active'
   -- 이미 뗀 것은 다시 고를 것이 없다. 감사 행은 기록으로 남지만 위반은 아니다.
   AND EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                WHERE x.entity_id = e.id AND x.provider = 'wikidata'
                  AND x.external_id = a.external_id)
 ORDER BY (a.verdict = $2) DESC, e.updated_at DESC
 LIMIT $1`, limit, AnchorNameElement)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PersonAnchorMismatch
	for rows.Next() {
		var m PersonAnchorMismatch
		if err := rows.Scan(&m.ID, &m.KO, &m.EntityType, &m.QID, &m.Verdict, &m.Class,
			&m.Desc, &m.Tier, &m.JA, &m.JASource, &m.LabelEN); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// EnforceStoredAnchorVerdicts — 저장된 판정을 집행한다. dry=true 면 무엇이 바뀔지 찍기만 한다.
//
// 자동 철회는 `name-element` 하나뿐이고, 그것도 운영자 잠금이 아니고 wikidata ref 가
// **정확히 하나**일 때만이다. ref 가 여럿이면 어느 것이 그 표기를 만든 출처인지 가릴 수
// 없으므로 근거 없이 지우지 않는다(D-37).
func EnforceStoredAnchorVerdicts(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) AnchorWithdrawResult {
	var r AnchorWithdrawResult
	stored, err := StoredAnchorVerdicts(ctx, pool, limit)
	if err != nil {
		log.Printf("kdb.anchor-enforce: 선정 실패: %v", err)
		return r
	}
	r.Checked = len(stored)

	for _, m := range stored {
		if m.Verdict != AnchorNameElement {
			// 앵커와 유형 중 어느 쪽이 틀렸는지 근거만으로 못 가른다 — 사람에게 보낸다.
			r.Review = append(r.Review, m)
			r.Skipped++
			continue
		}
		var locked bool
		var refCount int
		if err := pool.QueryRow(ctx, `
SELECT e.operator_locked,
       (SELECT count(*) FROM kwave_entity_external_refs x
         WHERE x.entity_id = e.id AND x.provider = 'wikidata')
  FROM kwave_entities e WHERE e.id = $1`, m.ID).Scan(&locked, &refCount); err != nil {
			log.Printf("kdb.anchor-enforce: %s: %v", m.KO, err)
			continue
		}
		if locked || refCount != 1 {
			r.Skipped++
			why := "wikidata ref 가 1개가 아님(" + strconv.Itoa(refCount) + "개) — 어느 것이 표기 출처인지 못 가린다"
			if locked {
				why = "운영자 잠금"
			}
			log.Printf("  건너뜀 %-14s (%s) — %s", m.KO, m.QID, why)
			continue
		}
		cells, err := anchorSourcedCells(ctx, pool, m.ID)
		if err != nil {
			log.Printf("kdb.anchor-enforce: %s: %v", m.KO, err)
			continue
		}
		log.Printf("  %-14s %-10s %-12s 표기 %d칸 비움 %v  (%s)",
			m.KO, m.EntityType, m.QID, len(cells), cells, m.Desc)
		if dry {
			r.Withdrawn++
			r.CellsCleared += len(cells)
			continue
		}
		down, err := withdrawOneAnchor(ctx, pool, m, cells)
		if err != nil {
			log.Printf("kdb.anchor-enforce: %s 실패: %v", m.KO, err)
			continue
		}
		r.Withdrawn++
		r.CellsCleared += len(cells)
		if down {
			r.Downgraded++
		}
	}
	return r
}
