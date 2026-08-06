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
	"regexp"
	"strings"

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

// ─── 괄호 병기 라틴표기 회수 ────────────────────────────────────────────────
//
// DrainLatinKoToEN 은 canonical_ko **전체**가 ASCII 일 때만 승계한다(`^[ -~]+$`). 그래서
// `넬(Nell)` · `엑소(EXO)` · `김준수(XIA)` 처럼 한글과 공식 라틴표기가 **함께** 적힌 건
// 한글 한 글자 때문에 통째로 제외됐다. 정답을 이미 들고 있으면서 en 을 비워둔 셈이다.
//
// ★왜 급한가: en 이 비면 Latin locale 4종(vi/es/id/pt_br)도 함께 빈다 — DrainRomanizeLatin
// 이 canonical_en 을 복사원본으로 쓰기 때문이다. 1건당 5셀이 막힌다(DrainLatinKoToEN 주석의
// 같은 계산). 그리고 유일한 en 채움 경로인 translate-fill 은 이 건들에 대해 구글이 번역을
// 내놓지 않아(2026-08-06 실측: 80건 전부 no-translation) 영원히 채우지 못한다.
//
// 번역도 음역도 아니다 — 입력 문자열 안에 이미 있는 값을 **꺼내는 것**이라 환각이 없다.
// source='romanization'(prio 7, 결정적 파생) — 공식 영문표기(TMDb/KMDb prio4)가 나중에
// 들어오면 자동 업그레이드된다.

// parenLatinMarkers — 괄호 안이 "이름"이 아니라 "수식어/크레딧"인 표시. 2026-08-06 DB 실측
// 에서 실제로 걸린 것: `Mono (Feat. skaiwater)` · `Where To Now? (Part.1 : Yellow Light)` ·
// `자유롭게 날아 (Feat. 우기(YUQI))` · `(Reprism Ver.)`. 이걸 안 막으면 canonical_en 이
// "Feat. skaiwater" 가 된다.
//
// ★방송사·플랫폼(MBC/tvN/TVING…)은 일부러 넣지 않았다. 실측에서 이 패턴에 걸리는 유일한
// 건이 `티빙(TVING)` 인데 그건 **정답**이다(플랫폼의 한글명↔영문명). 막으면 득보다 실이다.
var parenLatinMarkers = map[string]bool{
	"inst": true, "instrumental": true, "acoustic": true, "live": true,
	"remix": true, "remaster": true, "remastered": true, "ver": true, "version": true,
	"edit": true, "mix": true, "feat": true, "featuring": true, "ft": true,
	"with": true, "prod": true, "narr": true, "original": true, "extended": true,
	"demo": true, "cover": true, "mr": true, "ost": true, "single": true,
	"full": true, "short": true, "part": true, "pt": true, "disc": true,
	"cd": true, "vol": true, "intro": true, "outro": true, "interlude": true,
	"bonus": true, "deluxe": true, "repackage": true, "solo": true, "duet": true,
}

var parenGroupRe = regexp.MustCompile(`[(（]([^)）]*)[)）]`)

// ParenLatinName — canonical_ko 의 괄호에서 공식 라틴표기를 뽑는다. 못 뽑으면 "".
//
// 규칙(전부 통과해야 채운다 — 애매하면 빈칸):
//   - ASCII 인쇄가능 문자만. `찬(灿/Lucid)` 처럼 한자가 섞이면 버린다 — 어디까지가 이름인지
//     정할 방법이 없다.
//   - 알파벳 1자 이상(연도 `(2024)` 같은 숫자만 제외), 공백 제거 후 2자 이상.
//   - 괄호 문자를 다시 포함하면 버린다 — `(Feat. 우기(YUQI)` 처럼 짝이 깨진 데이터다(실측 4건).
//   - 조건을 만족하는 괄호가 **정확히 하나**일 때만. 둘 이상이면 어느 게 이름인지 모른다.
//   - 첫 토큰이 마커면 버린다(`Feat. …` · `Part.1 : …`). 마지막 토큰이 마커여도 버린다
//     (`Reprism Ver.`). 가운데는 보지 않는다 — `Boy With Luv` 를 살리기 위해서다.
func ParenLatinName(ko string) string {
	var found string
	n := 0
	for _, m := range parenGroupRe.FindAllStringSubmatch(ko, -1) {
		v := strings.TrimSpace(m[1])
		if len(v) < 2 || !isASCIIPrintable(v) || !hasLatinLetter(v) || strings.ContainsAny(v, "(（") {
			continue
		}
		n++
		found = v
	}
	if n != 1 {
		return ""
	}
	toks := parenTokens(found)
	if len(toks) == 0 {
		return ""
	}
	if parenLatinMarkers[toks[0]] || parenLatinMarkers[toks[len(toks)-1]] {
		return ""
	}
	return found
}

// parenTokens — 영숫자 덩어리만 소문자로 끊어낸다. "Feat. skaiwater" → [feat skaiwater],
// "Part.2" → [part 2], "DAY6" → [day6].
func parenTokens(s string) []string {
	var out []string
	cur := make([]rune, 0, len(s))
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cur = append(cur, r)
			continue
		}
		flush()
	}
	flush()
	return out
}

func isASCIIPrintable(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return s != ""
}

func hasLatinLetter(s string) bool {
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return true
		}
	}
	return false
}

// DrainParenLatinToEN — 한글과 라틴표기가 병기된 엔티티의 canonical_en 빈칸을 괄호 안
// 표기로 채운다. 외부호출 0·결정적. 채우지 못한 후보는 이유와 함께 로그에 남긴다 —
// 조용히 버리면 "다 처리했다"로 읽힌다.
func DrainParenLatinToEN(ctx context.Context, pool *pgxpool.Pool) (filled int) {
	if pool == nil {
		return 0
	}
	// canonical_ko 에 한글이 있는 건만 — 전체가 ASCII 인 건은 DrainLatinKoToEN 담당이다.
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko FROM kwave_entities
 WHERE status='active' AND operator_locked = false
   AND entity_type NOT IN ('unknown','term')
   AND COALESCE(canonical_en,'') = ''
   AND canonical_ko ~ '[가-힣]'
   AND canonical_ko ~ '[(（][^)）]*[A-Za-z][^)）]*[)）]'`)
	if err != nil {
		log.Printf("kdb.romanize: paren-latin select: %v", err)
		return 0
	}
	type cand struct{ id, ko string }
	var items []cand
	for rows.Next() {
		var c cand
		if rows.Scan(&c.id, &c.ko) == nil {
			items = append(items, c)
		}
	}
	rows.Close()

	skipped := 0
	for _, it := range items {
		v := ParenLatinName(it.ko)
		if v == "" {
			skipped++
			log.Printf("kdb.romanize: paren-latin 보류 %q — 규칙 미통과(빈칸 유지)", it.ko)
			continue
		}
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET canonical_en = $2, canonical_en_source = 'romanization', updated_at = now()
 WHERE id = $1 AND status='active' AND operator_locked = false
   AND COALESCE(canonical_en,'') = ''`, it.id, v)
		if uerr != nil {
			log.Printf("kdb.romanize: paren-latin update %q: %v", it.ko, uerr)
			continue
		}
		if tag.RowsAffected() > 0 {
			filled++
			log.Printf("kdb.romanize: paren-latin %q → en=%q", it.ko, v)
		}
	}
	if len(items) > 0 {
		log.Printf("kdb.romanize: DrainParenLatinToEN filled=%d skipped=%d /%d (괄호 병기 라틴표기)",
			filled, skipped, len(items))
	}
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
