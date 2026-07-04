// Package verify — 엔티티-레벨 "정체성 검증 tier"(handoff 27차 §6 증분2).
//
// 기존 verified_only 게이트(kdbapi)는 per-locale VALUE 신뢰를 다룬다("이 일본어 표기가
// 믿을 만한가"). 이 패키지는 엔티티 IDENTITY 를 다룬다("이 항목이 실재하는 올바른
// K-엔티티인가 — 오염/동명이인 mislink 가 아닌가"). 둘은 상호보완.
//
// ★핵심 설계 = "1회 검증 → 캐시 → 빠른 서빙"(속도+정확도가 기본):
//   - 서빙 핫패스는 kwave_entities.verification_tier 캐시 컬럼만 읽어 즉답. 요청마다 재검사 ✕.
//   - tier 사다리(싼→비싼):
//       authoritative  결정론·무료·즉시: Wikidata QID/TMDb/KOFIC/KMDb/MusicBrainz 권위앵커.
//       evidenced      권위앵커 없지만 독립 확증: wikipedia langlink·강한 source·conf≥0.75,
//                      또는 검색근거+gemma 판정(verify_evidence.go, evidence 'search+gemma%').
//       unverified     독립 확증 없음 = 리스크 표면(evidence 업그레이드 or 운영자 검토 대상).
//
// SweepDeterministic 은 결정론 backbone(전 active 를 set-based UPDATE 로 즉시 분류, LLM/쿼터 ✕).
// EvidencePass(verify_evidence.go)는 unverified 소수를 네이버news+gemma 로 업그레이드(쿼터 캡).
package verify

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Tier 상수 — verification_tier 컬럼 값.
const (
	TierAuthoritative = "authoritative"
	TierEvidenced     = "evidenced"
	TierUnverified    = "unverified"
)

// Counts — tier 별 엔티티 수(스윕/조회 결과).
type Counts struct {
	Authoritative int
	Evidenced     int
	Unverified    int
}

func (c Counts) Total() int { return c.Authoritative + c.Evidenced + c.Unverified }

// authProviders — 권위앵커로 인정하는 external_ref provider(결정론 tier1).
// Wikidata QID/TMDb 는 enrich 단계 QID mislink·ko-label·동명이인 가드를 이미 통과해 저장된
// 것이므로 정체성 앵커로 신뢰. KOFIC/KMDb/MusicBrainz 도 공신력 매핑.
const authProviderList = "'wikidata','tmdb','kofic','kmdb','musicbrainz'"

// strongSources — 권위앵커가 없어도 독립 확증으로 인정하는 canonical_<loc>_source(tier2).
// 사람잠금·현지사용확정·미디어합의·공신력 매핑 소스. wikidata-label/wikipedia-*/romanization/
// opencc/local-search/codex-fallback 는 제외(약한/파생 신호 → conf·wiki_ref 로만 판정).
const strongSourceList = "'operator-locked','operator','local-usage','media-consensus'," +
	"'correction-verified','naver-people','tmdb','kofic','kmdb','musicbrainz'"

// sigCTE — 엔티티별 결정론 신호(권위 provider·wiki ref·강한 source). {{scope}} 자리에
// active 전체 또는 단일 id 필터를 끼운다. tierCASE/evidenceCASE 가 이 신호로 tier 를 정한다.
const sigCTE = `
WITH sig AS (
  SELECT e.id, e.confidence,
    (SELECT string_agg(DISTINCT r.provider, '+' ORDER BY r.provider)
       FROM kwave_entity_external_refs r
      WHERE r.entity_id = e.id AND r.provider IN (` + authProviderList + `)) AS auth_providers,
    EXISTS(SELECT 1 FROM kwave_entity_external_refs r
             WHERE r.entity_id = e.id AND r.provider LIKE 'wikipedia-%') AS wiki_ref,
    (ARRAY[e.canonical_en_source, e.canonical_ja_source, e.canonical_zh_source,
           e.canonical_zh_hant_source, e.canonical_vi_source, e.canonical_es_source,
           e.canonical_id_source, e.canonical_pt_br_source]
       && ARRAY[` + strongSourceList + `]) AS strong_src
  FROM kwave_entities e
  WHERE e.status = 'active'{{scope}}
)`

// tierCASE / evidenceCASE — 신호 → tier/근거 매핑. evidence 패스가 올린 값
// (evidence 'search+gemma%')은 결정론이 unverified 로 강등하지 않고 보존한다.
const tierCASE = `CASE
    WHEN sig.auth_providers IS NOT NULL THEN 'authoritative'
    WHEN sig.wiki_ref OR sig.confidence >= 0.75 OR sig.strong_src THEN 'evidenced'
    WHEN e.verification_evidence LIKE 'search+gemma%' THEN 'evidenced'
    ELSE 'unverified' END`

const evidenceCASE = `CASE
    WHEN sig.auth_providers IS NOT NULL THEN sig.auth_providers
    WHEN sig.wiki_ref THEN 'wikipedia-langlink'
    WHEN sig.strong_src THEN 'strong-source'
    WHEN sig.confidence >= 0.75 THEN 'confidence ' || round(sig.confidence, 2)::text
    WHEN e.verification_evidence LIKE 'search+gemma%' THEN e.verification_evidence
    ELSE 'no independent anchor' END`

// updateStmt — {{scope}} 자리에 CTE WHERE 추가절(코드 상수만)을 끼운 UPDATE 문.
func updateStmt(scope string) string {
	return strings.Replace(sigCTE, "{{scope}}", scope, 1) + `
UPDATE kwave_entities e SET
  verification_tier     = ` + tierCASE + `,
  verification_evidence = ` + evidenceCASE + `,
  verified_tier_at      = now()
FROM sig WHERE e.id = sig.id`
}

// SweepDeterministic — 전 active 엔티티를 결정론으로 재분류하고 tier 별 카운트를 반환한다.
// set-based 단일 UPDATE(수천 행도 1초 미만). LLM/네이버 쿼터 소모 ✕. 항상 최신으로 다시 돌려도 됨.
func SweepDeterministic(ctx context.Context, pool *pgxpool.Pool) (Counts, error) {
	if _, err := pool.Exec(ctx, updateStmt("")); err != nil {
		return Counts{}, fmt.Errorf("verify sweep: %w", err)
	}
	return Tally(ctx, pool)
}

// Tally — 현재 캐시된 tier 분포를 집계한다.
func Tally(ctx context.Context, pool *pgxpool.Pool) (Counts, error) {
	rows, err := pool.Query(ctx, `
		SELECT COALESCE(verification_tier,'(none)'), count(*)
		  FROM kwave_entities WHERE status='active' GROUP BY 1`)
	if err != nil {
		return Counts{}, err
	}
	defer rows.Close()
	var c Counts
	for rows.Next() {
		var tier string
		var n int
		if err := rows.Scan(&tier, &n); err != nil {
			return Counts{}, err
		}
		switch tier {
		case TierAuthoritative:
			c.Authoritative = n
		case TierEvidenced:
			c.Evidenced = n
		case TierUnverified:
			c.Unverified = n
		}
	}
	return c, rows.Err()
}

// ClassifyOne — 단일 엔티티를 결정론 재분류(enrich 직후 on-demand 갱신용). CTE 를 그 id
// 로만 스코프해 전체 스윕을 돌리지 않는다. evidence 패스가 올린 값은 보존. 반환=새 tier.
func ClassifyOne(ctx context.Context, pool *pgxpool.Pool, entityID string) (string, error) {
	stmt := updateStmt(" AND e.id = $1") + ` RETURNING e.verification_tier`
	var tier string
	if err := pool.QueryRow(ctx, stmt, entityID).Scan(&tier); err != nil {
		return "", fmt.Errorf("verify classify %s: %w", entityID, err)
	}
	return tier, nil
}
