package kdb

// romanize — 한국 인물/그룹의 Latin locale(vi/es/id/pt_br) 표기를 canonical_en(검증된 로마자)
// 에서 결정적 재속성한다. 외부 호출 0·벌크안전. 한국 인명은 라틴문자권에서 사실상 영문 로마자와
// 동일 표기이므로(송강호 vi/es/id/pt_br = "Song Kang-ho" = en), codex 합성·빈칸을 실제 로마자로 채운다.
//
// ★source-레벨 가드(필드단위 잠금): 대상은 빈칸 또는 codex-fallback 인 locale 만 — operator·media·
// wikidata 등 더 신뢰되는 값은 절대 건드리지 않는다. canonical_en 은 비-codex(검증)·Latin 일 때만 복사
// (codex en 전파 차단). source='romanization'(prio 7) → media-consensus/권위/wiki 가 이후 업그레이드.
//
// ★음역 허용선(빈칸>틀린값): Latin locale 의 한국 인명은 로마자=현지통용이라 고신뢰. ja/zh 같은
// 비-라틴 스크립트엔 적용 안 함(거긴 권위소스/hanja만). verified_only 소비자엔 노출 안 함(api.go).

import (
	"context"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// romanizeLatinLocales — 재속성 대상 Latin locale(canonical 컬럼 접미사).
var romanizeLatinLocales = []string{"vi", "es", "id", "pt_br"}

// DrainRomanizePersons — person/group 의 빈칸/codex Latin locale 을 canonical_en 으로 재속성한다.
// 결정적·벌크안전(외부호출 0)이라 전량 일괄 처리. 반환=(채운 셀 수).
//   - canonical_en 이 검증(비-codex)·Latin(ASCII)·비어있지 않을 때만 복사(codex en 전파 차단).
//   - 대상 locale 이 빈칸 또는 codex-fallback 일 때만(operator/media/wikidata 값은 불가침).
//   - 기존값과 동일하면 건너뜀(불필요한 updated_at 갱신 회피).
func DrainRomanizePersons(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	for _, loc := range romanizeLatinLocales {
		col := "canonical_" + loc
		src := col + "_source"
		q := `
UPDATE kwave_entities
   SET ` + col + ` = canonical_en, ` + src + ` = 'romanization', updated_at = now()
 WHERE status='active' AND entity_type IN ('person','group')
   AND canonical_en <> '' AND canonical_en ~ '^[ -~]+$'
   AND COALESCE(canonical_en_source,'') NOT IN ('codex-fallback','')
   AND ( ` + col + ` = '' OR ` + col + ` IS NULL OR COALESCE(` + src + `,'')='codex-fallback' )
   AND COALESCE(` + col + `,'') <> canonical_en`
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			log.Printf("kdb.romanize: %s: %v", loc, err)
			continue
		}
		c := int(tag.RowsAffected())
		filled += c
		log.Printf("kdb.romanize: %s <- canonical_en 재속성 %d건", loc, c)
	}
	log.Printf("kdb.romanize: DrainRomanizePersons filled=%d cells", filled)
	return filled
}
