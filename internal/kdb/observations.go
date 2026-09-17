// Package kdb — observations 누적 + 매체 합의 promote.
//
// Phase 2 합의 강화 (Agent NLP 권고 2026-05-25):
//  1. parent_org 기반 독립 source 카운트 (wire-cascade 차단)
//  2. 가중 합의: SUM(media_trust × confidence) ≥ ConsensusWeightThreshold
//  3. spelling_normalized 기준 grouping (raw 변종 통합)
//  4. promote 직전 character-set sanity (locale ↔ script 매칭)
//  5. 48h cooldown (같은 entity × locale promote 후 후속 합의 차단)
package kdb

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// 합의 임계 (운영자 정공법).
const (
	// 독립 매체 (parent_org) 수 — wire-family 1개 = 합의 1로 계산.
	MediaConsensusThreshold = 2

	// 가중 합의 점수 — SUM(media_trust × confidence) 이 이상 충족 시 promote.
	// 기본 trust=1.0, confidence≈0.95 → 2 매체면 ~1.9, 임계 1.5 → 통과.
	ConsensusWeightThreshold = 1.5

	// 같은 entity×locale 의 후속 promote 차단 시간 (drift 누적 차단).
	PromoteCooldown = 48 * time.Hour
)

// ObservationStore — observation INSERT + 합의 평가.
type ObservationStore struct {
	Pool *pgxpool.Pool
}

func NewObservationStore(pool *pgxpool.Pool) *ObservationStore {
	return &ObservationStore{Pool: pool}
}

// Save — Codex 추출 spelling 을 observation 으로 누적 (raw + normalized 동시 저장).
func (s *ObservationStore) Save(ctx context.Context, entityID uuid.UUID, sp ExtractedSpelling, sourceDomain, sourceURL string) error {
	return s.SaveWithCycle(ctx, entityID, sp, sourceDomain, sourceURL, 0)
}

func (s *ObservationStore) SaveWithCycle(ctx context.Context, entityID uuid.UUID, sp ExtractedSpelling, sourceDomain, sourceURL string, cycleID int64) error {
	normalized := NormalizeSpelling(sp.Spelling)
	if normalized == "" {
		return nil
	}
	var cid interface{}
	if cycleID > 0 {
		cid = cycleID
	}
	_, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_media_observations
  (entity_id, locale, spelling, spelling_normalized,
   source_domain, source_url, confidence, cycle_id, observed_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())`,
		entityID, sp.Locale, sp.Spelling, normalized,
		sourceDomain, nullIfEmpty(sourceURL), sp.Confidence, cid)
	return err
}

// EvaluateConsensus — 한 entity × locale 의 매체 합의 평가 → canonical_X UPDATE.
//
// Agent NLP 권고: parent_org 독립 + 가중 + sanity + cooldown.
func (s *ObservationStore) EvaluateConsensus(ctx context.Context, entityID uuid.UUID, locale string) (string, bool, error) {
	col := canonicalCol(locale)
	if col == "" {
		return "", false, fmt.Errorf("unknown locale: %s", locale)
	}

	// 48h cooldown: 같은 entity×locale 의 직전 promote 이후 PromoteCooldown 미만이면 skip.
	var lastPromoted *time.Time
	_ = s.Pool.QueryRow(ctx, `
SELECT MAX(attempted_at) FROM kwave_entity_resolution_attempts
WHERE entity_id = $1
  AND provider = 'kdb:media-consensus'
  AND status = 'promoted-' || $2`, entityID, locale).Scan(&lastPromoted)
	if lastPromoted != nil && time.Since(*lastPromoted) < PromoteCooldown {
		return "", false, nil
	}

	// 가중 합의: 같은 normalized spelling 의 distinct parent_org 수 + sum(trust × confidence).
	// parent_org NULL = domain 자체로 fallback (독립 매체 취급).
	//
	// ★**페이지도 센다** (2026-09-15). 매체 수만 세던 때, 같은 URL 하나가 서로 다른
	//   source_domain 으로 여러 번 적히면 매체 여럿이 각각 말한 것으로 셌다.
	//   검색이 `site:` 를 안 지켜 밖의 페이지를 주는데 우리가 요청한 도메인을 출처로
	//   적은 탓이다(최근 30일 3,012건 전부). 그래서:
	//
	//     구잘      pt-br "GuzalTV"  ← youtube.com 한 페이지가 브라질 매체 5곳으로
	//     쉿(Shhh)  ja    "Shhh!"    ← store.steampowered.com 한 페이지
	//     벡터      ja    "vector"   ← 수학 벡터를 다룬 네이버 블로그 한 페이지
	//
	//   합의란 **서로 다른 곳이 같은 말을 했다**는 뜻이다. 한 페이지는 아무리
	//   여러 이름으로 적혀도 한 곳이다. URL 이 없는 옛 관측은 도메인당 한 페이지로
	//   쳐서 종전 판정을 바꾸지 않는다.
	row := s.Pool.QueryRow(ctx, `
WITH agg AS (
  SELECT
    o.spelling_normalized,
    COUNT(DISTINCT COALESCE(w.parent_org, o.source_domain)) AS n_parents,
    COUNT(DISTINCT COALESCE(NULLIF(o.source_url, ''), 'domain:' || o.source_domain)) AS n_pages,
    SUM(COALESCE(w.media_trust, 1.0) * COALESCE(o.confidence, 0.85))::float8 AS weight_sum,
    -- raw 다수결 (정공법: canonical = 매체가 실제 쓴 표기)
    MODE() WITHIN GROUP (ORDER BY o.spelling) AS spelling_majority
  FROM kwave_media_observations o
  LEFT JOIN kwave_news_whitelist w
    ON w.domain = o.source_domain AND w.locale = $2
  WHERE o.entity_id = $1 AND o.locale = $2
    AND o.observed_at > now() - interval '90 days'
    AND o.confidence >= 0.7
  GROUP BY o.spelling_normalized
)
SELECT spelling_majority, n_parents, weight_sum
FROM agg
WHERE n_parents >= $3 AND n_pages >= $3 AND weight_sum >= $4
ORDER BY weight_sum DESC, n_parents DESC
LIMIT 1`, entityID, locale, MediaConsensusThreshold, ConsensusWeightThreshold)

	var spelling string
	var nParents int
	var weight float64
	if err := row.Scan(&spelling, &nParents, &weight); err != nil {
		return "", false, nil // no consensus
	}

	// Sanity check (Agent NLP #3): locale 별 character-set 매칭.
	if !isValidSpellingForLocale(locale, spelling) {
		_ = s.auditSanityReject(ctx, entityID, locale, spelling, nParents, weight)
		return "", false, nil
	}

	// UPDATE with priority guard (cascade.go::can_replace_canonical SQL function).
	q := fmt.Sprintf(`
UPDATE kwave_entities
   SET %s = $2,
       %s_source = 'media-consensus',
       updated_at = now()
 WHERE id = $1
   AND (%s IS NULL
        OR can_replace_canonical(operator_locked, %s_source, 'media-consensus'))`,
		col, col, col, col)
	tag, err := s.Pool.Exec(ctx, q, entityID, spelling)
	if err != nil {
		return "", false, fmt.Errorf("update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		_ = s.auditDrift(ctx, entityID, locale, spelling, nParents)
		return "", false, nil
	}

	// 성공 audit (cooldown 기준점 + 운영자 추적)
	_, _ = s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_resolution_attempts
  (entity_id, provider, status, error_text, attempted_at)
VALUES ($1, 'kdb:media-consensus', 'promoted-' || $2, $3, now())`,
		entityID, locale,
		fmt.Sprintf("spelling=%q parents=%d weight=%.2f", spelling, nParents, weight))

	log.Printf("kdb.consensus: entity=%s locale=%s spelling=%q (parents=%d weight=%.2f) → promoted",
		entityID, locale, spelling, nParents, weight)
	return spelling, true, nil
}

// auditDrift — operator-locked / priority 가드로 보존된 경우 audit.
func (s *ObservationStore) auditDrift(ctx context.Context, entityID uuid.UUID, locale, attempted string, nParents int) error {
	_, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_resolution_attempts (entity_id, provider, status, error_text, attempted_at)
VALUES ($1, 'kdb:media-consensus', 'drift-locked', $2, now())`,
		entityID,
		fmt.Sprintf("locale=%s consensus=%q from %d parents (preserved by lock/priority)",
			locale, attempted, nParents))
	return err
}

// auditSanityReject — character-set 미스매치 등 sanity 위반 audit.
func (s *ObservationStore) auditSanityReject(ctx context.Context, entityID uuid.UUID, locale, attempted string, nParents int, weight float64) error {
	_, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_entity_resolution_attempts (entity_id, provider, status, error_text, attempted_at)
VALUES ($1, 'kdb:media-consensus', 'sanity-reject', $2, now())`,
		entityID,
		fmt.Sprintf("locale=%s spelling=%q parents=%d weight=%.2f (character-set mismatch)",
			locale, attempted, nParents, weight))
	return err
}

// SweepEvaluation — 최근 since 시간 안 새 observation 가진 모든 (entity, locale) 에 대해
// consensus 평가 호출.
func (s *ObservationStore) SweepEvaluation(ctx context.Context, since time.Duration) (int, error) {
	rows, err := s.Pool.Query(ctx, `
SELECT DISTINCT entity_id, locale
FROM kwave_media_observations
WHERE observed_at > now() - $1::interval
  AND entity_id IS NOT NULL`,
		fmt.Sprintf("%d minutes", int(since.Minutes())))
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type pair struct {
		id     uuid.UUID
		locale string
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.locale); err != nil {
			continue
		}
		pairs = append(pairs, p)
	}

	var promoted int
	for _, p := range pairs {
		_, ok, err := s.EvaluateConsensus(ctx, p.id, p.locale)
		if err != nil {
			log.Printf("kdb.consensus: %s/%s err=%v", p.id, p.locale, err)
			continue
		}
		if ok {
			promoted++
		}
	}
	if len(pairs) > 0 {
		log.Printf("kdb.SweepEvaluation: pairs=%d promoted=%d", len(pairs), promoted)
	}
	return promoted, nil
}

// ─── locale 별 character-set sanity (Agent NLP #3) ─────────────────

var (
	// 한자 (CJK Unified Ideographs + Extension A) — zh, zh-hant 전용
	cjkRE = regexp.MustCompile(`\p{Han}`)
	// 가나 (히라가나 + 카타카나 + 반각 카타카나) — ja 전용
	kanaRE = regexp.MustCompile(`[\p{Hiragana}\p{Katakana}]`)
	// 한글 — ko 전용 (vi/es/id/pt-br/en 에 등장하면 의심)
	hangulRE = regexp.MustCompile(`\p{Hangul}`)
)

// IsValidSpellingForLocale — locale 문자셋 검증의 공개 wrapper. enrich 쓰기 경로
// (MusicBrainz/Wikidata/TMDb 등 외부 소스)가 canonical_<loc> 에 값을 넣기 전에
// 호출해, 영문 칸에 한글이 들어가는 류의 오염을 차단한다. locale 키의 underscore
// 변종(pt_br/zh_hant)도 허용하도록 정규화한다.
func IsValidSpellingForLocale(locale, spelling string) bool {
	return isValidSpellingForLocale(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"), spelling)
}

// zeroWidthChars — 이름에 들어갈 수 없는 폭 0 서식문자. **코드포인트로 쓴다** — 소스에
// 리터럴로 넣으면 보이지 않아서 나중에 읽을 수 없고, 실제로 조회 정규식에 일반 공백이
// 섞여 수천 건 오탐을 낸 적이 있다(2026-08-09).
var zeroWidthChars = []rune{
	'\u200B', '\u200C', '\u200D', // ZWSP ZWNJ ZWJ
	'\u200E', '\u200F', // LRM RLM
	'\u202A', '\u202B', '\u202C', '\u202D', '\u202E', // bidi 제어
	'\u202F', '\u2060', '\uFEFF', // narrow NBSP, word joiner, BOM
}

// hasZeroWidth — 폭 0 서식문자가 하나라도 있는가.
func hasZeroWidth(s string) bool {
	for _, r := range s {
		for _, z := range zeroWidthChars {
			if r == z {
				return true
			}
		}
	}
	return false
}

// isValidSpellingForLocale — 매체 합의 spelling 의 character-set 이 locale 과 일관?
func isValidSpellingForLocale(locale, spelling string) bool {
	if strings.TrimSpace(spelling) == "" {
		return false
	}
	// ★폭 0 서식문자 거부(2026-08-09). locale 과 무관하게 먼저 본다.
	//
	// 실측 13칸이 이 상태였다 — `빌리` en=`Billlie`+U+200E · ja 8칸에 U+FEFF(wikidata 6·
	// tmdb 2) · `수윤` zh_hant 에 U+202C. **소스를 가리지 않는다**(외부 스크래핑 잔재).
	//
	// 왜 값을 살리지 않고 거부하나: 보이지 않아서 눈으로는 영영 못 찾는데 실해는 조용하다 —
	// 같은 이름이 문자열 비교에서 안 맞아 중복 탐지·별칭 매칭·소비자 검색이 전부 빗나간다.
	// 그리고 `수윤` 처럼 **파생 레인이 오염째로 복사**해 증폭된다(zh_hant→zh, opencc).
	// 서식문자가 섞여 들어왔다는 건 추출이 깨졌다는 신호이므로, 오너 원칙 "빈칸 > 틀린값"
	// 대로 빈칸으로 두고 다른 소스가 다시 시도하게 한다.
	if hasZeroWidth(spelling) {
		return false
	}
	switch locale {
	case "ko":
		// 한글 칸 오염 차단(2026-06-20): 일본어(가나) 또는 순수 한자(한글 없음)가
		// canonical_ko 에 들어가는 손상 거부 — 예: '常田大希', 'ホジュン～伝説の心医～',
		// '100日の郎君様'(canonical_ko=canonical_ja 동일이 손상 시그니처였음). 한글이
		// 하나라도 있거나(혼용 '아이브(IVE)' 허용) 라틴(예: 'IVE','BTS')이면 통과.
		if kanaRE.MatchString(spelling) {
			return false // 가나 포함 = 일본어 → 한국어 정본 아님
		}
		if cjkRE.MatchString(spelling) && !hangulRE.MatchString(spelling) {
			return false // 한자만(한글 전무) = 한국어 정본 아님
		}
		return true
	case "zh", "zh-hant":
		// 한글 혼입 거부(부분음역 "俊한" 류 차단 — ja 분기와 대칭).
		if hangulRE.MatchString(spelling) {
			return false
		}
		// ★간체 칸과 번체 칸을 구분한다 (2026-09-17 실측).
		//
		//   종전엔 `case "zh", "zh-hant":` 하나로 **똑같이** 취급했다. 한글·가나만
		//   보고 자체(字體)는 보지 않으니 양방향 오염이 통과했다:
		//
		//     간체 칸에 번체  87건 (KBS→韓國放送公社, 김종국→金鍾國 …)
		//     번체 칸에 간체  26건 (고래별→鲸鱼星, 넥슨코리아→乐线韩国 …)
		//
		//   중국과 대만을 동시에 서비스하면 이건 그대로 잘못된 표기로 나간다.
		//
		//   ★«전용 글자»만 본다. 두 자체에 **공통인 한자가 대부분**이므로(金·李·山…)
		//     "번체 전용 글자가 간체 칸에 있으면 거부" 만 판정한다. 공통 글자로만 된
		//     표기는 어느 쪽에서도 옳으므로 통과시킨다 — 오거부를 만들지 않는다.
		if locale == "zh" && ContainsTradOnly(spelling) {
			return false // 간체 칸에 번체 전용 글자
		}
		if locale == "zh-hant" && ContainsHansOnly(spelling) {
			return false // 번체 칸에 간체 전용 글자
		}
		// 가나 거부 — 중국어 칸에 일본어. 종전에는 한자 필수 조건이 이걸 부수적으로
		// 막고 있었는데, 아래에서 라틴을 허용하면서 명시 조건이 필요해졌다.
		// "ホジュン Legend" 처럼 가나+라틴 혼합이 라틴 조건만으로 통과하면 안 된다.
		if kanaRE.MatchString(spelling) {
			return false
		}
		// ★한자 필수 → 한자 **또는** 라틴 (2026-08-15). ja 분기와 대칭이 됐다.
		//
		// 종전 조건은 `KARD`·`Nine Muses`·`Wavve` 같은 라틴 zh 를 거부했다. 그런데
		// **DB 에는 이미 라틴 zh 가 1,404건 들어 있다** — romanization 618 ·
		// codex-fallback 296 · **wikidata-label 217** · itunes 108 ·
		// **operator-locked 64** · **wikipedia-sitelink 61** · discogs 19 · tmdb 4.
		// 이 게이트를 호출하지 않는 경로(DrainLatinKoToCJK·DrainZhWikiTitle)로 들어온
		// 값들이다. 즉 게이트가 **자기가 지키는 데이터와 불일치**였고, 오너가 직접 잠근
		// 값 64건까지 거부하는 규칙이었다.
		//
		// 라틴이 정답이라는 건 우리 추측이 아니라 권위 출처의 답이다(08-15 실측):
		// 한글 ko + 라틴 en 인 group 223건 중 **권위 zh 가 라틴인 것이 159건(71%)**.
		// `나인뮤지스→Nine Muses`(wikipedia-sitelink) `카드→KARD`(wikidata-label)
		// `레드벨벳→Red Velvet` `키스오브라이프→KISS OF LIFE`.
		//
		// 반대쪽(한자가 정답)은 ko 가 한국어 의미를 가진 경우다 — `신화→神话`
		// `우주소녀→宇宙少女` `동방신기→東方神起` `봄여름가을겨울→春夏秋冬`. 그 구분은
		// **값의 문자셋이 아니라 소스가 할 판단**이다. 이 함수는 문자셋 sanity 만 본다 —
		// 여기서 한자를 강제하면 소스가 맞게 가져온 라틴 답을 문자셋 규칙이 되돌린다.
		//
		// opencc 도 이 변경의 수혜다(opencc_convert.go:200): 라틴 zh 는 간→번 변환이
		// **항등**이 정답인데 종전에는 결과가 거부돼 zh_hant 가 영구 빈칸이었다.
		return cjkRE.MatchString(spelling) || containsLatin(spelling)
	case "ja":
		// 한자 또는 가나 — 한국어/라틴 only 는 의심
		if hangulRE.MatchString(spelling) {
			return false
		}
		return cjkRE.MatchString(spelling) || kanaRE.MatchString(spelling) ||
			containsLatin(spelling) // K-pop 그룹명 (BTS 등) 라틴 OK
	case "en", "vi", "es", "id", "pt-br":
		// 한글/한자/가나 없음
		return !hangulRE.MatchString(spelling) && !cjkRE.MatchString(spelling) && !kanaRE.MatchString(spelling)
	}
	return true
}

func containsLatin(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// ─── locale → canonical_X column 매핑 ──────────────────────────────
func canonicalCol(locale string) string {
	switch strings.TrimSpace(locale) {
	case "en":
		return "canonical_en"
	case "ja":
		return "canonical_ja"
	case "vi":
		return "canonical_vi"
	case "es":
		return "canonical_es"
	case "id":
		return "canonical_id"
	case "pt-br":
		return "canonical_pt_br"
	case "zh":
		return "canonical_zh"
	case "zh-hant":
		return "canonical_zh_hant"
	}
	return ""
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

// ★자체 판정은 ContainsTradOnly / ContainsHansOnly 로 옮겼다 (2026-09-17 저녁).
//
//	여기에는 손으로 고른 82자 정규식이 있었다. 그 목록으로 오염을 쓸고 «168 → 0»
//	이라 보고했는데, OpenCC 사전 전체로 다시 재니 **430건이 남아 있었다**:
//
//	    전보람  canonical_zh = 全寶藍   (寶·藍 이 목록에 없었다)
//	    정선철  canonical_zh = 鄭先哲   (鄭 이 목록에 없었다)
//	    황동혁  canonical_zh = 黄東赫   (黄 은 간체, 東 은 번체 — 한 칸에 섞였다)
//
//	사람이 고른 목록은 «본 적 있는 글자»만 담는다. 사전에서 굽는다
//	(scripts/gen_zh_charsets.py → zh_charsets_gen.go).
