package kdb

// opencc_convert — 보유한 검증 zh(간체)↔zh_hant(번체)를 OpenCC 로 결정적 변환해 빈/codex 인
// 반대 변종을 채운다(외부호출 0·벌크안전). 한자 스크립트 변환은 결정적이라 원본의 신뢰를 승계.
//   - zh(검증) → zh_hant(빈/codex): s2t
//   - zh_hant(검증) → zh(빈/codex): t2s
// source='opencc'(prio 7) → media-consensus/권위/wiki 가 이후 업그레이드. verified tier 제외.
// ★보수 변환: s2t/t2s 만(s2twp 대만관용어 변환 금지 — 고유명사 과변환 방지, 리서치 권고).

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/longbridgeapp/opencc"
)

// hasOtherScript — 한글/가나/라틴이 섞였으면 true(순수 한자 변환 대상이 아님 → 스킵).
func hasOtherScript(s string) bool {
	for _, r := range s {
		if (r >= 0xAC00 && r <= 0xD7A3) || // 한글
			(r >= 0x3040 && r <= 0x30FF) { // 가나
			return true
		}
	}
	return false
}

// DrainZhVariants — 검증 zh↔zh_hant 를 OpenCC 로 상호 변환해 빈/codex 변종을 채운다.
// 반환=(채운 셀 수).
func DrainZhVariants(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	s2t, err := opencc.New("s2t")
	if err != nil {
		log.Printf("kdb.opencc: s2t init: %v", err)
		return 0
	}
	t2s, err := opencc.New("t2s")
	if err != nil {
		log.Printf("kdb.opencc: t2s init: %v", err)
		return 0
	}

	type dir struct {
		srcCol, srcSrc, dstCol, dstSrc string
		conv                           *opencc.OpenCC
	}
	dirs := []dir{
		{"canonical_zh", "canonical_zh_source", "canonical_zh_hant", "canonical_zh_hant_source", s2t},
		{"canonical_zh_hant", "canonical_zh_hant_source", "canonical_zh", "canonical_zh_source", t2s},
	}

	for _, d := range dirs {
		// 검증(비-codex) 원본 보유 + 대상 빈칸/codex 인 행.
		q := `SELECT id, ` + d.srcCol + ` FROM kwave_entities
		       WHERE status='active'
		         AND ` + d.srcCol + ` <> '' AND ` + d.srcCol + ` ~ '[一-鿿]'
		         AND COALESCE(` + d.srcSrc + `,'') NOT IN ('codex-fallback','','opencc')
		         AND ( ` + d.dstCol + ` = '' OR ` + d.dstCol + ` IS NULL OR COALESCE(` + d.dstSrc + `,'')='codex-fallback' )`
		rows, err := pool.Query(ctx, q)
		if err != nil {
			log.Printf("kdb.opencc: select %s: %v", d.srcCol, err)
			continue
		}
		type item struct {
			id  string
			val string
		}
		var items []item
		for rows.Next() {
			var it item
			if rows.Scan(&it.id, &it.val) == nil {
				items = append(items, it)
			}
		}
		rows.Close()

		n := 0
		for _, it := range items {
			if hasOtherScript(it.val) {
				continue // 한글/가나 혼입 — 순수 한자 변환 대상 아님
			}
			out, cerr := d.conv.Convert(it.val)
			if cerr != nil {
				continue
			}
			out = strings.TrimSpace(out)
			if out == "" || !IsValidSpellingForLocale("zh", out) {
				continue
			}
			var applied bool
			err := pool.QueryRow(ctx, `
UPDATE kwave_entities SET `+d.dstCol+`=$2, `+d.dstSrc+`='opencc', updated_at=now()
 WHERE id=$1 AND ( `+d.dstCol+`='' OR `+d.dstCol+` IS NULL
        OR can_replace_canonical(operator_locked, COALESCE(`+d.dstSrc+`,''),'opencc'))
 RETURNING true`, it.id, out).Scan(&applied)
			if err == nil && applied {
				n++
			}
		}
		filled += n
		log.Printf("kdb.opencc: %s→%s 변환 %d건", d.srcCol, d.dstCol, n)
	}
	log.Printf("kdb.opencc: DrainZhVariants filled=%d cells", filled)
	return filled
}
