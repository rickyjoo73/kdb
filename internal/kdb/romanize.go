package kdb

// romanize — 한국 고유명사의 Latin locale(vi/es/id/pt_br) 표기를 canonical_en(검증된 로마자/
// 영문표기)에서 결정적 재속성한다. 외부 호출 0·벌크안전. 한국 인명은 라틴문자권에서 사실상 영문
// 로마자와 동일 표기이므로(송강호 vi/es/id/pt_br = "Song Kang-ho" = en), codex 합성·빈칸을 실제
// 로마자로 채운다.
//
// ★2026-07-29 전 타입 확장(오너 승인): 대상이 person/group 이던 탓에 작품·브랜드류는 en 을 이미
// 보유하고도 Latin locale 이 영구 빈칸이었다(실측 es 3,005·vi 3,110·id 3,094·pt_br 3,254 = 12,463셀
// — 소비자 preparing 의 최대 원인). K-콘텐츠는 라틴문자권에서 영문 제목이 국제 통용 표기이고
// (song_album 은 원제가 영문인 경우도 다수), 공식 현지제목(TMDb/Netflix prio4)이 나중에 들어오면
// 아래 source 가드가 자동 업그레이드하므로 "빈칸>틀린값" 원칙과 충돌하지 않는다.
//
// ★source-레벨 가드(필드단위 잠금): 대상은 빈칸 또는 codex-fallback 인 locale 만 — operator·media·
// wikidata 등 더 신뢰되는 값은 절대 건드리지 않는다. canonical_en 은 비-codex(검증)·Latin 일 때만 복사
// (codex en 전파 차단). source='romanization'(prio 7) → media-consensus/권위/wiki 가 이후 업그레이드.
// ★단 en 자체가 기계번역(gtranslate prio8)이면 복사본도 같은 prio 로 승계한다 — 파생값이 원본보다
// 높은 신뢰로 표기되는 provenance 부풀림 방지.
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

// DrainRomanizeLatin — active 엔티티(전 타입)의 빈칸/codex Latin locale 을 canonical_en 으로
// 재속성한다. 결정적·벌크안전(외부호출 0)이라 전량 일괄 처리. 반환=(채운 셀 수).
//   - canonical_en 이 검증(비-codex)·Latin(ASCII)·비어있지 않을 때만 복사(codex en 전파 차단).
//   - 대상 locale 이 빈칸 또는 codex-fallback 일 때만(operator/media/wikidata 값은 불가침).
//   - source 는 en 의 신뢰도를 승계 — en 이 기계번역(prio8)이면 그 라벨 그대로, 그 외는 'romanization'.
//   - dataqa 가 오염(contaminated·미revert)으로 표시한 locale 셀은 제외(재오염 방지).
//   - 기존값과 동일하면 건너뜀(불필요한 updated_at 갱신 회피).
//
// entity_type 은 unknown/term 만 제외한다: unknown 은 정체 미확정, term 은 일반어(고유명사 아님)라
// 라틴 표기 전파 대상이 아니다.
func DrainRomanizeLatin(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	for _, loc := range romanizeLatinLocales {
		col := "canonical_" + loc
		src := col + "_source"
		q := `
UPDATE kwave_entities e
   SET ` + col + ` = canonical_en,
       ` + src + ` = CASE WHEN kdb_source_priority(COALESCE(canonical_en_source,'')) >= 8
                          THEN COALESCE(canonical_en_source,'romanization')
                          ELSE 'romanization' END,
       updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND entity_type NOT IN ('unknown','term')
   AND canonical_en <> '' AND canonical_en ~ '^[ -~]+$'
   AND COALESCE(canonical_en_source,'') NOT IN ('codex-fallback','')
   AND ( ` + col + ` = '' OR ` + col + ` IS NULL OR COALESCE(` + src + `,'')='codex-fallback' )
   AND COALESCE(` + col + `,'') <> canonical_en
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = '` + loc + `'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			log.Printf("kdb.romanize: %s: %v", loc, err)
			continue
		}
		c := int(tag.RowsAffected())
		filled += c
		log.Printf("kdb.romanize: %s <- canonical_en 재속성 %d건", loc, c)
	}
	log.Printf("kdb.romanize: DrainRomanizeLatin filled=%d cells", filled)
	return filled
}

// DrainLatinKoToEN — canonical_ko 자체가 이미 라틴 표기인 엔티티("Thank U", "SM CLASSICS LIVE",
// "2026 ONEUS FANCON…")의 canonical_en 빈칸을 ko 그대로 채운다. 번역이 아니라 원제 승계다.
//
// 배경(2026-07-29 실측): en 빈칸 445건 중 346건이 이 케이스인데, 유일한 en 채움 경로인
// translate-fill 이 `canonical_ko ~ '[가-힣]'` 를 요구해 영구 제외했다. en 이 비면 Latin locale
// 4종도 함께 비므로 1건당 5셀이 막힌다. 결정적·외부호출 0.
//
// 가드: ASCII 출력가능 문자만으로 구성 + 알파벳 1자 이상(숫자·기호만인 잡음 제외), 2자 이상,
// operator_locked/unknown/term 제외, en 빈칸일 때만. source='romanization'(prio7 결정적 파생) —
// 공식 영문표기(TMDb/KMDb 등 prio4)가 나중에 들어오면 자동 업그레이드.
func DrainLatinKoToEN(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_en = canonical_ko, canonical_en_source = 'romanization', updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND entity_type NOT IN ('unknown','term')
   AND COALESCE(canonical_en,'') = ''
   AND canonical_ko ~ '^[ -~]+$' AND canonical_ko ~ '[A-Za-z]'
   AND length(canonical_ko) >= 2`)
	if err != nil {
		log.Printf("kdb.romanize: latin-ko→en: %v", err)
		return 0
	}
	filled = int(tag.RowsAffected())
	log.Printf("kdb.romanize: DrainLatinKoToEN filled=%d cells (ko 원제가 라틴표기)", filled)
	return filled
}

// cjkLatinOriginLocales — 라틴 원제를 그대로 승계할 CJK locale.
var cjkLatinOriginLocales = []string{"zh", "zh_hant", "ja"}

// DrainLatinKoToCJK — canonical_ko 자체가 라틴 원제인 엔티티("VOYAGER", "To Be Continued",
// "IDOL RADIO UNIVERSE")의 zh/zh_hant/ja 빈칸을 원제 그대로 채운다. 번역이 아니라 원문 승계다.
//
// ★근거(2026-07-29 Wikidata 실측): 중국어·일본어권도 이 계층의 K-엔티티는 라틴 원문을 그대로
// 쓴다 — Q484082(핑클) zh 라벨="FIN.K.L", Q16168934 zh/ja="KBS Joy", Q137883875 zh/ja=
// "DAILY:DIRECTION". 즉 라틴 원제 승계는 추측이 아니라 권위 소스가 확인해 주는 표기다.
//
// 대상 규모(실측): zh 525·ja 458 (+zh_hant 는 zh 승계분과 동일). MT 경로가 이 계층을 못 채운
// 이유는 구글번역이 라틴 원제를 뜻으로 풀거나 그대로 반환(untranslatable)해 게이트가 버렸기 때문.
//
// 가드는 DrainLatinKoToEN 과 동일(ASCII·알파벳 1자 이상·2자 이상·operator/unknown/term 제외)
// + 대상 locale 이 빈칸일 때만 + dataqa contaminated 제외. source='romanization'(prio7 결정적
// 파생) → 공식 현지제목(TMDb/Netflix prio4)이 들어오면 자동 업그레이드.
func DrainLatinKoToCJK(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	for _, loc := range cjkLatinOriginLocales {
		col := "canonical_" + loc
		src := col + "_source"
		q := `
UPDATE kwave_entities e
   SET ` + col + ` = canonical_ko, ` + src + ` = 'romanization', updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND entity_type NOT IN ('unknown','term')
   AND COALESCE(` + col + `,'') = ''
   AND canonical_ko ~ '^[ -~]+$' AND canonical_ko ~ '[A-Za-z]'
   AND length(canonical_ko) >= 2
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = '` + loc + `'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			log.Printf("kdb.romanize: latin-ko→%s: %v", loc, err)
			continue
		}
		c := int(tag.RowsAffected())
		filled += c
		log.Printf("kdb.romanize: %s <- 라틴 원제 승계 %d건", loc, c)
	}
	log.Printf("kdb.romanize: DrainLatinKoToCJK filled=%d cells", filled)
	return filled
}

// DrainReattributeRomanization — Latin locale 의 codex-fallback 셀 중 값이 이미
// canonical_en(검증된 로마자)과 바이트단위 동일한 것을 source='romanization'으로 재라벨한다.
// ★값 변경 0: codex 가 이미 올바른 로마자를 생성했으나 소스 라벨만 'codex-fallback'로 잘못
// 붙은 케이스(DrainRomanizeLatin 의 no-churn 가드 `col<>canonical_en`가 영구히 건너뛰던 셀).
// 대상은 DrainRomanizeLatin 과 동일 범위(전 타입, unknown/term 제외 — 2026-07-29 확장).
// reattributeTMDb(오너 승인 패턴)와 동일하게 라벨 정직성을 회복할 뿐 새 값을 만들지 않는다.
// 가드: en 검증(비-codex)·ASCII, 대상 src=codex 한정, dataqa contaminated(미revert) locale 제외.
// verified_only 소비자에게도 올바르게 서빙됨(llm-only→romanization 권위). 반환=(재라벨 셀 수).
func DrainReattributeRomanization(ctx context.Context, pool *pgxpool.Pool) (relabeled int) {
	if pool == nil {
		return 0
	}
	for _, loc := range romanizeLatinLocales {
		col := "canonical_" + loc
		src := col + "_source"
		q := `
UPDATE kwave_entities e
   SET ` + src + ` = 'romanization', updated_at = now()
 WHERE status='active' AND entity_type NOT IN ('unknown','term')
   AND canonical_en <> '' AND canonical_en ~ '^[ -~]+$'
   AND COALESCE(canonical_en_source,'') NOT IN ('codex-fallback','')
   AND COALESCE(` + src + `,'')='codex-fallback'
   AND ` + col + ` = canonical_en
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = '` + loc + `'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`
		tag, err := pool.Exec(ctx, q)
		if err != nil {
			log.Printf("kdb.romanize: reattribute %s: %v", loc, err)
			continue
		}
		c := int(tag.RowsAffected())
		relabeled += c
		log.Printf("kdb.romanize: %s codex→romanization 재라벨 %d건(값불변)", loc, c)
	}
	log.Printf("kdb.romanize: DrainReattributeRomanization relabeled=%d cells", relabeled)
	return relabeled
}
