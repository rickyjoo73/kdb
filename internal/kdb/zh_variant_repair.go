package kdb

// zh_variant_repair — 간체 칸에 번체가, 번체 칸에 간체가 들어간 행을 제자리로 돌린다.
//
// ★왜 필요한가 (2026-09-17 실측). 오너가 기본 언어를 en·ja·zh(간체·본토)로 정했는데
// 간체 칸에 번체가 168건, 번체 칸에 간체가 46건 들어 있었다:
//
//	KBS        canonical_zh = 韓國放送公社   (본토 표기는 韩国放送公社)
//	국립국악원  canonical_zh = 韓國國立國樂院
//	무쇠소녀단  canonical_zh_hant = 钢铁少女团 (tmdb 가 간체를 준 사례)
//
// 공급원은 둘이었다. 위키데이터의 `zh` 레이블이 간체라는 보장이 없는데 로케일 드레인이
// 그대로 간체 칸에 썼고(SimplifiedZh 로 고침), 검증이 두 칸을 구분하지 않아 반대 방향도
// 통과했다(hansOnlyRE/hantOnlyRE 로 고침). 이 파일은 **이미 들어간 것**을 치운다.
//
// ★버리지 않고 옮긴다. 간체 칸의 번체 값은 **틀린 값이 아니라 칸을 잘못 찾아간 값**이다
// (출처가 wikidata-label·tmdb 인 진짜 표기다). 그래서:
//
//	① 그 값을 번체 칸으로 옮긴다 — 번체 칸이 비었거나 파생값(opencc/codex)일 때만.
//	   번체 칸에 이미 독립 근거가 있으면 건드리지 않는다.
//	② 간체 칸은 t2s 변환으로 채운다. 한자 자체 변환은 결정적이라 환각 여지가 없다.
//
// 그냥 비우면 57건이 빈칸으로 남는다(번체 칸마저 그 오염에서 파생된 경우). 옮기면
// 두 칸이 다 산다.
//
// ★operator_locked 는 건드리지 않는다. 오너가 직접 넣은 값이고(삼성전자 三星電子,
// 농심 農心 …) 번체로 쓸지 간체로 쓸지는 오너 판단이다. 목록만 보고한다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/longbridgeapp/opencc"
)

// ZhRepairResult — 한 회차 결과.
type ZhRepairResult struct {
	Checked      int
	Repaired     int // 간체 칸을 제대로 고친 수
	MovedToHant  int // 번체 칸으로 옮긴 수
	Skipped      int // 변환 실패·혼합 문자 등
	OperatorHeld int // 오너 잠금이라 손대지 않은 수
}

// RepairZhVariants — 자체가 뒤바뀐 칸을 고친다. dry=true 면 쓰지 않는다.
func RepairZhVariants(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) ZhRepairResult {
	var res ZhRepairResult
	if pool == nil {
		return res
	}
	if limit <= 0 {
		limit = 500
	}
	t2s, err := opencc.New("t2s")
	if err != nil {
		log.Printf("kdb.zh-repair: t2s init: %v", err)
		return res
	}
	s2t, err := opencc.New("s2t")
	if err != nil {
		log.Printf("kdb.zh-repair: s2t init: %v", err)
		return res
	}

	_ = pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities
 WHERE status='active' AND operator_locked
   AND (canonical_zh ~ $1 OR canonical_zh_hant ~ $2)`,
		hantOnlyRE.String(), hansOnlyRE.String()).Scan(&res.OperatorHeld)

	type dir struct {
		badCol, badSrc  string // 잘못된 자체가 들어 있는 칸
		goodCol, goodSrc string // 그 값이 원래 있어야 할 칸
		pattern         string
		fix             *opencc.OpenCC // badCol 을 제 자체로 되돌리는 변환
		label           string
	}
	dirs := []dir{
		{"canonical_zh", "canonical_zh_source", "canonical_zh_hant", "canonical_zh_hant_source",
			hantOnlyRE.String(), t2s, "간체 칸의 번체"},
		{"canonical_zh_hant", "canonical_zh_hant_source", "canonical_zh", "canonical_zh_source",
			hansOnlyRE.String(), s2t, "번체 칸의 간체"},
	}

	for _, d := range dirs {
		rows, qerr := pool.Query(ctx, `
SELECT id::text, canonical_ko, `+d.badCol+`, COALESCE(`+d.badSrc+`,''),
       COALESCE(`+d.goodCol+`,''), COALESCE(`+d.goodSrc+`,'')
  FROM kwave_entities
 WHERE status='active' AND operator_locked = false
   AND `+d.badCol+` ~ $1
 ORDER BY canonical_ko
 LIMIT $2`, d.pattern, limit)
		if qerr != nil {
			log.Printf("kdb.zh-repair: select %s: %v", d.badCol, qerr)
			continue
		}
		type item struct{ id, ko, bad, badSrc, good, goodSrc string }
		var items []item
		for rows.Next() {
			var it item
			if rows.Scan(&it.id, &it.ko, &it.bad, &it.badSrc, &it.good, &it.goodSrc) == nil {
				items = append(items, it)
			}
		}
		rows.Close()

		for _, it := range items {
			res.Checked++
			if hasOtherScript(it.bad) {
				res.Skipped++ // 한글·가나 혼입 — 순수 한자 변환 대상 아님
				continue
			}
			fixed, cerr := d.fix.Convert(it.bad)
			// 고유명사 과변환 되돌리기(朴·姜). 변환 직후에 건다 — 이걸 빼면 수리가
			// 오히려 오염을 만든다(실측: 박씨 108건이 樸 로 바뀌어 있었다).
			fixed = keepProperNouns(it.bad, fixed)
			fixed = strings.TrimSpace(fixed)
			if cerr != nil || fixed == "" || fixed == it.bad {
				// 변환이 아무것도 못 바꿨다면 애초에 전용 글자가 아니었다는 뜻이다.
				res.Skipped++
				continue
			}

			// ① 원래 자체 값을 제 칸으로 옮길 수 있나 — 비었거나 파생값일 때만.
			moveOK := it.good == "" || it.goodSrc == "opencc" || it.goodSrc == "codex-fallback" || it.goodSrc == ""
			if dry {
				log.Printf("  [%s] %-16s %s → %s%s", d.label, it.ko, it.bad, fixed,
					map[bool]string{true: "  (원본은 " + d.goodCol + " 으로 이동)", false: ""}[moveOK])
				res.Repaired++
				if moveOK {
					res.MovedToHant++
				}
				continue
			}

			tx, terr := pool.Begin(ctx)
			if terr != nil {
				res.Skipped++
				continue
			}
			// 감사 원장 먼저 — 되돌릴 수 있게.
			_, _ = tx.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, $2, $3, $4, 'zh-variant-repaired', $5, 'opencc')`,
				it.id, strings.TrimPrefix(d.badCol, "canonical_"), it.bad, it.badSrc,
				d.label+" — 제 자체로 변환("+it.bad+" → "+fixed+"). 원본 이동="+
					map[bool]string{true: "예", false: "아니오"}[moveOK])

			if moveOK {
				_, _ = tx.Exec(ctx, `UPDATE kwave_entities
   SET `+d.goodCol+`=$2, `+d.goodSrc+`=$3, updated_at=now() WHERE id=$1::uuid`,
					it.id, it.bad, it.badSrc)
				res.MovedToHant++
			}
			ct, uerr := tx.Exec(ctx, `UPDATE kwave_entities
   SET `+d.badCol+`=$2, `+d.badSrc+`='opencc', updated_at=now()
 WHERE id=$1::uuid AND operator_locked=false`, it.id, fixed)
			if uerr != nil || ct.RowsAffected() == 0 {
				_ = tx.Rollback(ctx)
				res.Skipped++
				continue
			}
			if cerr := tx.Commit(ctx); cerr != nil {
				res.Skipped++
				continue
			}
			res.Repaired++
		}
	}
	// ── 이미 만들어진 과변환도 되돌린다 ──────────────────────────
	//
	//   가드(keepProperNouns)는 **앞으로** 만들어질 것을 막는다. 이미 DB 에 들어간
	//   것은 그대로다 — 실측 108건이 우리 opencc 레인이 만든 樸/薑 였다.
	//
	//   ★출처가 'opencc' 인 것만 고친다. wikidata-label·tmdb 가 樸 를 준 23건은
	//     외부 권위의 판단이라 우리가 뒤집지 않는다 — 목록으로 보고한다.
	for _, p := range zhProperNounKeep {
		rows, qerr := pool.Query(ctx, `
SELECT id::text, canonical_ko, canonical_zh_hant, COALESCE(canonical_zh,'')
  FROM kwave_entities
 WHERE status='active' AND operator_locked = false
   AND canonical_zh_hant_source = 'opencc'
   AND canonical_zh_hant LIKE '%' || $1 || '%'
   AND COALESCE(canonical_zh,'') LIKE '%' || $2 || '%'
 ORDER BY canonical_ko LIMIT $3`, string(p.wrong), string(p.src), limit)
		if qerr != nil {
			continue
		}
		type it struct{ id, ko, hant, zh string }
		var items []it
		for rows.Next() {
			var x it
			if rows.Scan(&x.id, &x.ko, &x.hant, &x.zh) == nil {
				items = append(items, x)
			}
		}
		rows.Close()
		for _, x := range items {
			res.Checked++
			fixed := strings.ReplaceAll(x.hant, string(p.wrong), string(p.src))
			if fixed == x.hant {
				res.Skipped++
				continue
			}
			if dry {
				log.Printf("  [고유명사 되돌림] %-16s %s → %s", x.ko, x.hant, fixed)
				res.Repaired++
				continue
			}
			tx, terr := pool.Begin(ctx)
			if terr != nil {
				res.Skipped++
				continue
			}
			_, _ = tx.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, 'zh_hant', $2, 'opencc', 'zh-propernoun-restored', $3, 'opencc')`,
				x.id, x.hant, "OpenCC 과변환 되돌림 "+string(p.wrong)+"→"+string(p.src)+
					" (한국 성씨는 간체·번체가 같다)")
			ct, uerr := tx.Exec(ctx, `UPDATE kwave_entities
   SET canonical_zh_hant=$2, updated_at=now()
 WHERE id=$1::uuid AND operator_locked=false AND canonical_zh_hant_source='opencc'`, x.id, fixed)
			if uerr != nil || ct.RowsAffected() == 0 {
				_ = tx.Rollback(ctx)
				res.Skipped++
				continue
			}
			if cerr := tx.Commit(ctx); cerr != nil {
				res.Skipped++
				continue
			}
			res.Repaired++
		}
	}

	log.Printf("kdb.zh-repair: 조회 %d · 고침 %d · 원본이동 %d · 건너뜀 %d · 오너잠금 %d (dry=%v)",
		res.Checked, res.Repaired, res.MovedToHant, res.Skipped, res.OperatorHeld, dry)
	return res
}
