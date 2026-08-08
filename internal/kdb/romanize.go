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
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// romanizeLatinLocales — 재속성 대상 Latin locale(canonical 컬럼 접미사).
var romanizeLatinLocales = []string{"vi", "es", "id", "pt_br"}

// DrainRomanizeLatin — active 엔티티(전 타입)의 빈칸/codex Latin locale 을 canonical_en 으로
// 재속성한다. 결정적·벌크안전(외부호출 0)이라 전량 일괄 처리. 반환=(채운 셀 수).
//   - canonical_en 이 검증(비-codex)·라틴 스크립트(latinOriginClass)·비어있지 않을 때만 복사
//     (codex en 전파 차단). ★2026-08-08: 종전엔 ASCII 전용이라 `It’s 2PM` 같은 곡선따옴표 en 이
//     라틴 4종으로 못 넘어갔다 — 승계 레인을 넓히고 여기를 안 넓히면 en 만 차고 4칸이 그대로 빈다.
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
   AND canonical_en <> '' AND canonical_en ~ $1` + latinPropagateSQLGuard + `
   AND COALESCE(canonical_en_source,'') NOT IN ('codex-fallback','')
   AND ( ` + col + ` = '' OR ` + col + ` IS NULL OR COALESCE(` + src + `,'')='codex-fallback' )
   AND COALESCE(` + col + `,'') <> canonical_en
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = '` + loc + `'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`
		tag, err := pool.Exec(ctx, q, latinOriginPattern)
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
// 가드: 라틴 스크립트(latinOriginClass — 2026-08-08 이전엔 ASCII 전용) + 알파벳 1자 이상
// (숫자·기호만인 잡음 제외), 2자 이상, operator_locked/unknown/term 제외, en 빈칸일 때만.
// source='romanization'(prio7 결정적 파생) — 공식 영문표기(TMDb/KMDb 등 prio4)가 나중에
// 들어오면 자동 업그레이드.
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
   AND canonical_ko ~ $1 AND canonical_ko ~ '[A-Za-z]'
   AND length(canonical_ko) >= 2`, latinOriginPattern)
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
// DrainLatinKoToEN 은 canonical_ko **전체**가 라틴 스크립트일 때만 승계한다. 그래서
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
	return acceptLatinName(found)
}

// prefixGlossRe — `LATIN(주석)(주석)…` 전체를 통으로 잡는다. 접두부 뒤로는 괄호 묶음만
// 오고 그 사이·뒤에 다른 글자가 없어야 매치된다.
var prefixGlossRe = regexp.MustCompile(`^([^(（]+?)\s*((?:[(（][^)）]*[)）])+)$`)

// PrefixLatinName — canonical_ko 가 `LATIN(한글/한자 주석)` 꼴일 때 앞의 라틴표기를 뽑는다.
//
//	1'ONLY(원앤온리)      → 1'ONLY
//	HAWWAH (夏渦)(하와)   → HAWWAH
//
// ParenLatinName 의 역패턴이다. 저 함수는 괄호 **안**을 보므로 라틴표기가 바깥에 있는
// 이 형태를 못 잡았다.
//
// ★위험한 것은 "제목 일부만 떼어내는" 경우다. `Where To Now? (Part.2) : NOWHERE(웨어…)`
// 에서 앞부분만 취하면 불완전한 제목이 canonical_en 이 된다. 그래서 두 겹으로 막는다:
//   - 괄호 묶음 뒤나 사이에 다른 글자가 있으면 통째로 거부(위 정규식이 `$` 로 강제).
//   - 괄호 안에 라틴 알파벳이 하나라도 있으면 거부 — 그건 주석이 아니라 제목의 일부이거나
//     `(Part.2)` 같은 마커다. 순수 주석(한글·한자)일 때만 접두부를 이름으로 인정한다.
//
// `OOH-AHH하게`(공식 영문제목은 "Like OOH-AHH")처럼 괄호가 없는 혼합 표기는 애초에
// 매치되지 않는다 — 접두부를 이름으로 볼 근거가 없기 때문이다.
func PrefixLatinName(ko string) string {
	m := prefixGlossRe.FindStringSubmatch(strings.TrimSpace(ko))
	if m == nil {
		return ""
	}
	for _, g := range parenGroupRe.FindAllStringSubmatch(m[2], -1) {
		if hasLatinLetter(g[1]) {
			return "" // 주석이 아니다 — 제목의 일부이거나 마커다.
		}
	}
	head := strings.TrimSpace(m[1])
	if len(head) < 2 || !isASCIIPrintable(head) || !hasLatinLetter(head) {
		return ""
	}
	return acceptLatinName(head)
}

// acceptLatinName — 마커 검사(첫·마지막 토큰)를 두 추출기가 공유한다. 규칙이 갈리면
// 같은 문자열이 어느 경로로 들어왔는지에 따라 다르게 판정된다.
func acceptLatinName(v string) string {
	toks := parenTokens(v)
	if len(toks) == 0 {
		return ""
	}
	if parenLatinMarkers[toks[0]] || parenLatinMarkers[toks[len(toks)-1]] {
		return ""
	}
	return v
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

// ─── 라틴 원제 판별 (닫힌 허용목록) ─────────────────────────────────────────
//
// ★2026-08-08 확장. 종전 가드는 `canonical_ko ~ '^[ -~]+$'`(ASCII 전용)였다. 실측하니
// **CJK 미판정 20건 중 18건(90%)이 이 조건 하나로만 제외**되고 있었고, 막힌 문자를 전부
// 뽑아보니 대부분 정상 표기였다:
//
//	’ U+2019 곡선 아포스트로피 10건 — `It’s 2PM` `Don’t Save Me` `Time’s Tickin’`
//	á Ã æ 라틴확장 3건        — `DáFF` `… IN SÃO PAULO` `SYNK : COMPLæXITY`
//	ẹ ề Ơ ư 베트남어 2건      — `Mẹ Ơi, Về Nhà`
//	～ ［］ ♥︎ 표기기호 4건      — `One Last Day ～Japan Special Edition～`
//
// 곡선따옴표는 K-pop 표제의 표준 표기다. 그 한 글자 때문에 1건당 8칸(ja/zh/zh_hant +
// en→라틴 4종)이 영구히 막혀 있었다. `[ -~]` 의 **상위집합**이라 이미 채운 500건에는
// 회귀가 없다 — 순수 확장이다.
//
// ★왜 블록리스트가 아니라 허용목록인가: 같은 20건에 `BE (Delلuxe Edition)` 이 있다 —
// Deluxe 의 `l` 자리에 아랍문자 ل(U+0644)가 박힌 오염 제목이다. 허용목록이면 이런 건
// 목록에 없어서 **자동으로** 기각된다. 0106 이 여는 목록으로 144칸을 망칠 뻔한 것과
// 같은 이유다(핸드오프 44차 §1).
//
// ★일부러 뺀 것 — 보이지 않는 문자:
//   - U+00A0 NBSP, U+202F narrow NBSP: 눈에 안 보이는 공백은 제목 데이터의 오염 신호다.
//   - U+2028/2029 줄바꿈: 제목에 들어갈 이유가 없다.
//   - U+202A–202E bidi 제어문자: 표시 순서를 뒤집어 사람 눈과 저장값을 어긋나게 한다.
//     ل 혼입과 같은 계열의 위험이고, 8개 locale 로 증폭되면 되짚기가 불가능해진다.
//
// Go 문자열 리터럴이 컴파일 시점에 실제 문자로 풀리므로, 이 한 상수를 Go regexp 와
// SQL(`~ $1`) **양쪽이 그대로 쓴다** — 두 벌로 나뉘면 규칙이 갈라진다.
const latinOriginClass = "[ -~" +
	"¡-ÿ" + // Latin-1 Supplement (á à ã æ ÷ …) — NBSP(U+00A0) 제외
	"Ā-ɏ" + // Latin Extended-A/B (Ơ ơ ư …)
	"Ḁ-ỿ" + // Latin Extended Additional (ẹ ề … 베트남어)
	"‐-‧" + // General Punctuation 앞부분 (– — ‘ ’ “ ” … •)
	"‰-⁞" + // 〃 뒷부분 (‰ ′ ″ ‹ ›) — 줄바꿈·bidi 제어(U+2028–202F) 제외
	"☀-➿" + // Misc Symbols + Dingbats (♥ ★ ✧ …) — 글자가 아닌 기호뿐
	"︀-️" + // Variation Selectors (♥︎ 의 U+FE0E)
	"！-～" + // Fullwidth ASCII variants (～ ［ ］) — 반각 가타카나(U+FF61–) 제외
	"]"

// latinOriginPattern — 위 허용목록의 전체일치 형태. SQL 파라미터로 그대로 넘긴다.
const latinOriginPattern = "^" + latinOriginClass + "+$"

// mcuneReischauerRe — 매큔-라이샤워(MR) 로마자의 표지 모음. 이 DB 의 표준은 **개정 로마자**다
// (이 파일 머리말: 송강호 = "Song Kang-ho"). MR 은 다른 체계라 같은 인물이 다른 이름이 된다.
//
// ★왜 별도 가드가 필요해졌나(2026-08-08): 종전 ASCII 전용 가드가 ŏ/ŭ 때문에 MR 값을
// **우연히** 격리하고 있었다. 라틴 스크립트로 넓히면서 그 우연한 보호가 사라졌고, 실제로
// 한 번의 전파로 5건이 4종 locale 에 퍼졌다. 그 결과가 이것이다:
//
//	양수경  en=Yang Su-kyŏng  vi/es=Yang Su-kyŏng  id/pt_br=Yang Soo Kyung(local-usage)
//	이수형  en=Yi Suhyŏng     id=Lee Su-hyung(local-usage)  vi=Lý Tú Huỳnh(wikidata)
//
// 더 신뢰되는 소스가 든 칸은 불가침이라 안 덮이고, 빈칸만 MR 로 찼다 — **한 인물이 locale
// 마다 다른 이름으로 서빙된다.** 어느 로마자가 옳으냐 이전에 이 불일치 자체가 결함이다.
// (`안영민 → Yŏng-min An` 은 성·이름 순서까지 뒤집혀 있다.)
//
// 우연한 보호를 의도적인 가드로 바꾼다. en 쪽 값을 고치는 건 별개 레인의 판단이라
// 여기서 건드리지 않는다(44차 §4 가 선행 레인 오염에 쓴 것과 같은 선).
var mcuneReischauerRe = regexp.MustCompile(`[ŏŭŎŬ]`)

// latinPropagateSQLGuard — 라틴 4종 전파에서 제외할 en. 위 regexp 와 같은 문자 집합이다.
const latinPropagateSQLGuard = ` AND canonical_en !~ '[ŏŭŎŬ]'`

var latinOriginRe = regexp.MustCompile(latinOriginPattern)

// IsLatinOrigin — canonical_ko 가 "그대로 승계해도 되는 라틴 원제"인지.
// 허용목록 전체일치 + 라틴 글자 1자 이상(연도·기호만인 잡음 제외) + 2자 이상.
// 길이는 **문자 수**로 센다 — SQL 쪽 `length()` 와 같은 기준이라야 두 판정이 갈라지지 않는다.
func IsLatinOrigin(ko string) bool {
	return len([]rune(ko)) >= 2 && latinOriginRe.MatchString(ko) && hasLatinLetter(ko)
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
   AND canonical_ko ~ '[A-Za-z]'
   AND canonical_ko ~ '[(（]'`)
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
		// 괄호 **안**을 먼저 본다(`넬(Nell)`). 없으면 괄호 **바깥**의 접두부를 본다
		// (`1'ONLY(원앤온리)`). 둘 다 안 되면 빈칸을 유지한다.
		v := ParenLatinName(it.ko)
		if v == "" {
			v = PrefixLatinName(it.ko)
		}
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
// 가드는 DrainLatinKoToEN 과 동일(latinOriginClass·알파벳 1자 이상·2자 이상·operator/unknown/term 제외)
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
   AND canonical_ko ~ $1 AND canonical_ko ~ '[A-Za-z]'
   AND length(canonical_ko) >= 2
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = '` + loc + `'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`
		tag, err := pool.Exec(ctx, q, latinOriginPattern)
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

// latinOriginRejectField — 위 레인의 기각 원장 field. cjkFillLaneSQL 에도 같은 이름이 있다.
const latinOriginRejectField = "latin-origin"

// MarkLatinOriginRejects — 한글 없는 원제인데 CJK 칸이 여전히 빈 건에 **기각 사유를 남긴다**.
//
// ★왜 필요한가(2026-08-08): DrainLatinKoToCJK 는 성공(UPDATE)만 기록하고 기각은 아무 흔적도
// 남기지 않았다. 그래서 이 레인이 "못 채운다"고 판단한 건이 backlog-watch 에는 영원히
// **"아직 어느 레인도 판정 안 함"** 으로 남는다. 44차 §3 이 지표 쪽에서 잡아낸 병
// (판정 끝난 것과 손댈 수 있는 것의 혼동)이 레인 안쪽에 그대로 있었던 셈이다.
//
// 기각 사유를 남기면 경보가 정직해진다 — 30건이 "배선 구멍"이 아니라 "판정된 불가"로
// 바뀌고, 남는 숫자가 진짜 손댈 것이 된다. 사유에 **문제 문자를 그대로 적는다**: 이번
// 세션의 진단이 가능했던 건 `ل=U+644` 을 눈으로 봤기 때문이다. 집계만 봤으면 못 찾는다.
//
// 값은 건드리지 않는다(UPDATE 0). 원장만 쓴다.
func MarkLatinOriginRejects(ctx context.Context, pool *pgxpool.Pool) (marked int) {
	if pool == nil {
		return 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, canonical_ko, entity_type FROM kwave_entities e
 WHERE status='active' AND operator_locked = false
   AND COALESCE(canonical_ko,'') <> '' AND canonical_ko !~ '[가-힣]'
   AND (COALESCE(canonical_ja,'')='' OR COALESCE(canonical_zh,'')='' OR COALESCE(canonical_zh_hant,'')='')`)
	if err != nil {
		log.Printf("kdb.romanize: latin-origin 기각 조회: %v", err)
		return 0
	}
	type reject struct{ id, verdict, reason string }
	var todo []reject
	for rows.Next() {
		var id, ko, typ string
		if err := rows.Scan(&id, &ko, &typ); err != nil {
			continue
		}
		switch {
		case typ == "unknown" || typ == "term":
			todo = append(todo, reject{id, "type-excluded", "entity_type=" + typ + " 는 승계 대상 아님"})
		case !IsLatinOrigin(ko):
			todo = append(todo, reject{id, "foreign-script", "허용목록 밖 문자: " + nonLatinChars(ko)})
		}
		// 그 밖(가드 통과인데 빈칸)은 dataqa 오염표시나 방금 채워진 건이다 — 마킹하지 않는다.
		// 여기서 마킹하면 다음 회차에 정상 승계될 건에 낙인을 찍는다.
	}
	rows.Close()
	for _, r := range todo {
		MarkFillAttempt(ctx, pool, r.id, latinOriginRejectField, r.verdict, r.reason)
		marked++
	}
	log.Printf("kdb.romanize: MarkLatinOriginRejects marked=%d (라틴 원제 승계 불가 판정)", marked)
	return marked
}

// nonLatinChars — 허용목록 밖 문자를 `ل=U+644` 형태로 나열한다(중복 제거, 최대 6개).
func nonLatinChars(s string) string {
	var out []string
	seen := map[rune]bool{}
	for _, r := range s {
		if seen[r] || latinOriginRe.MatchString(string(r)) {
			continue
		}
		seen[r] = true
		if len(out) == 6 {
			out = append(out, "…")
			break
		}
		out = append(out, fmt.Sprintf("%c=U+%04X", r, r))
	}
	if len(out) == 0 {
		// 문자는 전부 허용목록인데 전체일치가 실패 = 라틴 글자 없음 또는 1자.
		return "라틴 글자 없음 또는 2자 미만"
	}
	return strings.Join(out, " ")
}

// DrainReattributeRomanization — Latin locale 의 codex-fallback 셀 중 값이 이미
// canonical_en(검증된 로마자)과 바이트단위 동일한 것을 source='romanization'으로 재라벨한다.
// ★값 변경 0: codex 가 이미 올바른 로마자를 생성했으나 소스 라벨만 'codex-fallback'로 잘못
// 붙은 케이스(DrainRomanizeLatin 의 no-churn 가드 `col<>canonical_en`가 영구히 건너뛰던 셀).
// 대상은 DrainRomanizeLatin 과 동일 범위(전 타입, unknown/term 제외 — 2026-07-29 확장).
// reattributeTMDb(오너 승인 패턴)와 동일하게 라벨 정직성을 회복할 뿐 새 값을 만들지 않는다.
// 가드: en 검증(비-codex)·라틴 스크립트, 대상 src=codex 한정, dataqa contaminated(미revert) locale 제외.
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
   AND canonical_en <> '' AND canonical_en ~ $1` + latinPropagateSQLGuard + `
   AND COALESCE(canonical_en_source,'') NOT IN ('codex-fallback','')
   AND COALESCE(` + src + `,'')='codex-fallback'
   AND ` + col + ` = canonical_en
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = e.id AND d.locale = '` + loc + `'
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`
		tag, err := pool.Exec(ctx, q, latinOriginPattern)
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
