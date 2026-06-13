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
	row := s.Pool.QueryRow(ctx, `
WITH agg AS (
  SELECT
    o.spelling_normalized,
    COUNT(DISTINCT COALESCE(w.parent_org, o.source_domain)) AS n_parents,
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
WHERE n_parents >= $3 AND weight_sum >= $4
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

// isValidSpellingForLocale — 매체 합의 spelling 의 character-set 이 locale 과 일관?
func isValidSpellingForLocale(locale, spelling string) bool {
	if strings.TrimSpace(spelling) == "" {
		return false
	}
	switch locale {
	case "zh", "zh-hant":
		// 한자 필수 + 한글 혼입 거부(부분음역 "俊한" 류 차단 — ja 분기와 대칭).
		return cjkRE.MatchString(spelling) && !hangulRE.MatchString(spelling)
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
