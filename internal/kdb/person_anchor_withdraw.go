package kdb

// person_anchor_withdraw — 감사(person_anchor_audit.go)가 **근거로** 어긋났다고 판정한
// wikidata 앵커를 뗀다.
//
// ★무엇을 떼고 무엇을 남기는가.
//   가비는 실존 무용가다. 틀린 것은 Q5515395(영화 `GABI/ガビ-国境の愛-`)이지 사람이 아니다.
//   그러니 **대상은 건드리지 않는다.** status 도, entity_type 도, ID 도 그대로다(I01/I02).
//   떼는 것은 셋뿐이다:
//     ① 틀린 ref 행                         — 앵커가 아니었으므로
//     ② 그 항목에서 긁어온 표기 칸           — 근거가 사라지면 값도 남으면 안 된다(D-37)
//     ③ verification_tier                    — 권위 ref 가 없어졌는데 authoritative 로 두면 거짓말
//
// ★②를 두고 한 번 망설였다. `남준` 의 zh 는 `金南俊` 으로 **맞는 값**인데 출처가
//   주어진 이름 항목(Q69511277)이다. 맞는 값을 지우는 셈이다.
//   그래도 지운다. 우리는 그것이 맞는지 **말할 수 없기** 때문이다 — 근거가 틀린 항목
//   하나뿐이고, 맞는 것과 틀린 것을 가를 방법이 없다. `가비` 의 ja 가 영화 제목인 것이
//   같은 칸에서 나왔다. 빈칸은 다시 채울 수 있고(레인이 재시도한다), 틀린 값은
//   authoritative 로 나가서 소비자가 저장한다. 되돌릴 수 있는 쪽을 고른다.
//   운영자 결정 "디비에 있으면 제공하고 없으면 제공 안 한다"와도 같은 방향이다.
//
// ★기본이 dry-run 이다. opencc 간체 교정에서 겪었다 — 제안의 절반이 틀렸는데 세어만
//   보고 돌릴 뻔했다. 무엇이 바뀌는지 먼저 눈으로 본다.
//
// 전건 kwave_kdb_recheck_log(verdict='anchor-withdraw') 로 남아 되돌릴 수 있다.

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// anchorLocaleCols — 앵커에서 끌어온 표기가 앉는 칸. (값 컬럼, 출처 컬럼).
var anchorLocaleCols = [][2]string{
	{"canonical_en", "canonical_en_source"},
	{"canonical_ja", "canonical_ja_source"},
	{"canonical_vi", "canonical_vi_source"},
	{"canonical_zh", "canonical_zh_source"},
	{"canonical_zh_hant", "canonical_zh_hant_source"},
	{"canonical_es", "canonical_es_source"},
	{"canonical_id", "canonical_id_source"},
	{"canonical_pt_br", "canonical_pt_br_source"},
}

type AnchorWithdrawResult struct {
	Checked, Withdrawn, CellsCleared, Downgraded, Skipped int
}

// DrainWithdrawWrongAnchors — dry=true 면 무엇이 바뀔지 찍기만 한다.
func DrainWithdrawWrongAnchors(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, limit int, dry bool) AnchorWithdrawResult {
	var r AnchorWithdrawResult
	if pool == nil || cl == nil {
		return r
	}
	bad, checked := AuditPersonAnchors(ctx, pool, cl, limit)
	r.Checked = checked

	for _, m := range bad {
		// 배역 판정(fictional)은 여기서 처리하지 않는다. 그건 유형을 옮기는 일이고
		// 유형 변경은 앵커를 떼는 것과 다른 결정이다(P4.13).
		if m.Verdict == AnchorFictional || m.Verdict == AnchorHumanOnChar {
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
			log.Printf("kdb.anchor-withdraw: %s: %v", m.KO, err)
			continue
		}
		// 운영자 잠금은 건드리지 않는다. wikidata ref 가 둘 이상이면 어느 것이 표기의
		// 출처였는지 가릴 수 없다 — 근거 없이 지우지 않는다(D-37).
		if locked || refCount != 1 {
			r.Skipped++
			why := fmt.Sprintf("wikidata ref %d개", refCount)
			if locked {
				why = "운영자 잠금"
			}
			log.Printf("  건너뜀 %-14s (%s) — %s", m.KO, m.QID, why)
			continue
		}

		cells, err := anchorSourcedCells(ctx, pool, m.ID)
		if err != nil {
			log.Printf("kdb.anchor-withdraw: %s: %v", m.KO, err)
			continue
		}
		log.Printf("  %-14s %-12s %-14s 표기 %d칸 비움 %v  (%s)",
			m.KO, m.QID, m.Verdict, len(cells), cells, m.Desc)
		if dry {
			r.Withdrawn++
			r.CellsCleared += len(cells)
			continue
		}
		down, err := withdrawOneAnchor(ctx, pool, m, cells)
		if err != nil {
			log.Printf("kdb.anchor-withdraw: %s 실패: %v", m.KO, err)
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

// anchorSourcedCells — 이 대상에서 출처가 wikidata-label 인 표기 칸 이름.
func anchorSourcedCells(ctx context.Context, pool *pgxpool.Pool, id string) ([]string, error) {
	var sel []string
	for _, c := range anchorLocaleCols {
		sel = append(sel, fmt.Sprintf("(COALESCE(%s,'') <> '' AND COALESCE(%s,'') = 'wikidata-label')", c[0], c[1]))
	}
	row := pool.QueryRow(ctx, `SELECT `+strings.Join(sel, ",")+` FROM kwave_entities WHERE id = $1`, id)
	flags := make([]bool, len(anchorLocaleCols))
	dest := make([]any, len(flags))
	for i := range flags {
		dest[i] = &flags[i]
	}
	if err := row.Scan(dest...); err != nil {
		return nil, err
	}
	var out []string
	for i, on := range flags {
		if on {
			out = append(out, anchorLocaleCols[i][0])
		}
	}
	return out, nil
}

func withdrawOneAnchor(ctx context.Context, pool *pgxpool.Pool, m PersonAnchorMismatch, cells []string) (downgraded bool, err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err = tx.Exec(ctx, `
DELETE FROM kwave_entity_external_refs
 WHERE entity_id = $1 AND provider = 'wikidata' AND external_id = $2`, m.ID, m.QID); err != nil {
		return false, err
	}

	if len(cells) > 0 {
		var sets []string
		for _, c := range cells {
			sets = append(sets, c+" = ''", c+"_source = ''")
		}
		if _, err = tx.Exec(ctx, `UPDATE kwave_entities SET `+strings.Join(sets, ",")+
			`, updated_at = now() WHERE id = $1`, m.ID); err != nil {
			return false, err
		}
	}

	// 권위 ref 가 하나도 안 남으면 최상위 등급을 유지할 근거가 없다.
	tag, err := tx.Exec(ctx, `
UPDATE kwave_entities e
   SET verification_tier = 'unverified', verified_tier_at = now(), updated_at = now()
 WHERE e.id = $1 AND e.verification_tier = 'authoritative'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs x
                    WHERE x.entity_id = e.id
                      AND x.provider IN (`+AuthoritativeIdentityProviderSQLList()+`))`, m.ID)
	if err != nil {
		return false, err
	}
	downgraded = tag.RowsAffected() > 0

	if _, err = tx.Exec(ctx, `
INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, models, agreed, evidence)
VALUES ($1, $2, 'anchor-withdraw', 'wikidata-p31', true, $3)`,
		m.ID, m.KO, fmt.Sprintf("%s %s (%s) — 표기 %d칸 비움 %v · 등급강등 %v",
			m.QID, m.Verdict, m.Desc, len(cells), cells, downgraded)); err != nil {
		return false, err
	}
	return downgraded, tx.Commit(ctx)
}
