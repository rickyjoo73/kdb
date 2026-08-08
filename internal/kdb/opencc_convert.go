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

// DrainZhLatinIdentity — zh 에 한자가 하나도 없을 때(라틴 브랜드명) zh_hant 를 zh 그대로
// 채운다. 변환이 아니라 **항등**이다.
//
// ★왜 필요한가(2026-08-08): DrainZhVariants 의 선정 조건이 원본에 한자를 요구한다
// (`~ '[一-鿿]'`). 간체↔번체 변환은 한자에만 의미가 있으니 그 조건 자체는 옳다. 그런데
// 그 결과 **zh 가 라틴 문자열인 건은 zh_hant 가 영영 빈칸으로 남는다** — 변환할 게 없으니
// 레인이 건너뛰고, 건너뛴 흔적도 안 남는다.
//
// 실측 16건이 그 상태였고 CJK 경보의 바닥에 깔려 있었다:
//
//	웨이브        zh=Wavve                    zh_hant=∅
//	하이브 레이블즈 zh=HYBE Labels              zh_hant=∅
//	뮤직팜엔터테인먼트 zh=Music Farm Entertainment zh_hant=∅
//
// 간체와 번체는 **한자에서만** 갈린다. `Wavve` 는 번체 표기에서도 `Wavve` 다 — 추측이
// 아니라 항등이라 환각 여지가 없다. 같은 날 고친 라틴 원제 승계(romanize.go)·타입제외
// 판정(mt_translit_fill.go)과 함께 **레인이 요구하는 스크립트를 데이터가 안 가졌을 때
// 생기는 구멍**의 세 번째 사례다.
//
// 가드: zh 가 검증(비-codex)·한자 0자·한글/가나 무혼입, **zh_hant 가 빈칸일 때만**.
// source='opencc' — DrainZhVariants 와 같은 파생 계층이라 라벨을 나누지 않는다(권위
// 현지표기가 들어오면 같은 규칙으로 자동 업그레이드).
//
// ★codex-fallback 덮어쓰기는 일부러 뺐다. DrainZhVariants 와 같은 조건으로 열면 대상이
// 16건에서 112건이 되는데, 그 96건은 "빈칸을 채우는 것"이 아니라 **기존 값을 바꾸는 것**
// 이라 전후 대조를 따로 읽어야 하는 별개 판단이다. 게다가 드라이런에서 선행 오염이
// 보였다 — `엔시티`(NCT)의 zh 가 `NCT Dream`(다른 그룹)이다. 오염된 zh 를 zh_hant 로
// 옮기면 한 실수가 두 칸이 된다(44차 §1: "내가 만든 것이 무엇을 먹이는가").
func DrainZhLatinIdentity(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_zh FROM kwave_entities
 WHERE status='active' AND operator_locked = false
   AND canonical_zh <> '' AND canonical_zh !~ '[一-鿿]'
   AND COALESCE(canonical_zh_source,'') NOT IN ('codex-fallback','','opencc')
   AND ( canonical_zh_hant = '' OR canonical_zh_hant IS NULL )`)
	if err != nil {
		log.Printf("kdb.opencc: latin-identity 조회: %v", err)
		return 0
	}
	type item struct{ id, val string }
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.val) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		// 한글·가나가 섞인 zh 는 애초에 오염이다 — 번체 칸으로 옮겨 증폭시키지 않는다.
		if hasOtherScript(it.val) {
			continue
		}
		var applied bool
		err := pool.QueryRow(ctx, `
UPDATE kwave_entities SET canonical_zh_hant=$2, canonical_zh_hant_source='opencc', updated_at=now()
 WHERE id=$1 AND ( canonical_zh_hant='' OR canonical_zh_hant IS NULL )
 RETURNING true`, it.id, it.val).Scan(&applied)
		if err == nil && applied {
			filled++
		}
	}
	log.Printf("kdb.opencc: DrainZhLatinIdentity filled=%d cells (zh 라틴 원문 → zh_hant 항등)", filled)
	return filled
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
