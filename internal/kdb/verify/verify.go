// Package verify — 엔티티-레벨 "정체성 검증 tier"(handoff 27차 §6 증분2).
//
// 기존 verified_only 게이트(kdbapi)는 per-locale VALUE 신뢰를 다룬다("이 일본어 표기가
// 믿을 만한가"). 이 패키지는 엔티티 IDENTITY 를 다룬다("이 항목이 실재하는 올바른
// K-엔티티인가 — 오염/동명이인 mislink 가 아닌가"). 둘은 상호보완.
//
// ★핵심 설계 = "1회 검증 → 캐시 → 빠른 서빙"(속도+정확도가 기본):
//   - 서빙 핫패스는 kwave_entities.verification_tier 캐시 컬럼만 읽어 즉답. 요청마다 재검사 ✕.
//   - tier 사다리(싼→비싼):
//     authoritative  결정론·무료·즉시: Wikidata QID/TMDb/KOFIC/KMDb/MusicBrainz 권위앵커.
//     evidenced      권위앵커 없지만 독립 확증: wikipedia langlink·강한 source·conf≥0.75,
//     또는 검색근거+gemma 판정(verify_evidence.go, evidence 'search+gemma%').
//     unverified     독립 확증 없음 = 리스크 표면(evidence 업그레이드 or 운영자 검토 대상).
//
// SweepDeterministic 은 결정론 backbone(전 active 를 set-based UPDATE 로 즉시 분류, LLM/쿼터 ✕).
// EvidencePass(verify_evidence.go)는 unverified 소수를 네이버news+gemma 로 업그레이드(쿼터 캡).
package verify

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	kdbroot "github.com/rickyjoo73/kdb/internal/kdb"
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

// sigCTE — 엔티티별 결정론 신호(권위 provider·wiki ref·강한 source). {{scope}} 자리에
// active 전체 또는 단일 id 필터를 끼운다. tierCASE/evidenceCASE 가 이 신호로 tier 를 정한다.
func sigCTE() string {
	return `
WITH sig AS (
  SELECT e.id, e.confidence,
    (SELECT string_agg(DISTINCT r.provider, '+' ORDER BY r.provider)
       FROM kwave_entity_external_refs r
      WHERE r.entity_id = e.id AND r.provider IN (` + kdbroot.AuthoritativeIdentityProviderSQLList() + `)) AS auth_providers,
    EXISTS(SELECT 1 FROM kwave_entity_external_refs r
             WHERE r.entity_id = e.id AND r.provider LIKE 'wikipedia-%') AS wiki_ref,
    -- ★강한 소스는 "라벨"이 아니라 "그 라벨이 붙은 값"이 있어야 성립한다(2026-08-02).
    -- canonical_*_source 8 개 컬럼의 DB 기본값이 'wikidata-label' 이고 그게 강한 목록에
    -- 들어 있어서, 종전 배열겹침 검사는 값이 빈 슬롯에 남은 기본값만으로 evidenced 를
    -- 내주고 있었다(실측: 앵커 없는 strong-source 2,331 중 1,443 이 값 없이 라벨뿐).
    EXISTS(SELECT 1 FROM (VALUES
             (e.canonical_en_source, e.canonical_en),
             (e.canonical_ja_source, e.canonical_ja),
             (e.canonical_zh_source, e.canonical_zh),
             (e.canonical_zh_hant_source, e.canonical_zh_hant),
             (e.canonical_vi_source, e.canonical_vi),
             (e.canonical_es_source, e.canonical_es),
             (e.canonical_id_source, e.canonical_id),
             (e.canonical_pt_br_source, e.canonical_pt_br)) v(src, val)
            WHERE v.src IN (` + kdbroot.StrongEvidenceSourceSQLList() + `)
              AND COALESCE(v.val, '') <> '') AS strong_src,
    -- ★뉴스근거 승급 이력은 verification_evidence 가 아니라 append-only 대장에서 읽는다.
    -- 아래 UPDATE 가 바로 그 컬럼을 덮어쓰므로, 컬럼을 근거로 삼은 보존 조항은 다른
    -- 가지가 한 번이라도 이기는 순간 영구히 무력화된다(실측 974건이 이미 그 상태였다).
    (SELECT l.reason FROM kwave_kdb_dataqa_log l
      WHERE l.entity_id = e.id AND l.verdict = 'candidate-evidence-promote'
      ORDER BY l.id DESC LIMIT 1) AS gemma_reason
  FROM kwave_entities e
  WHERE e.status = 'active'{{scope}}
)`
}

// tierCASE / evidenceCASE — 신호 → tier/근거 매핑. evidence 패스가 올린 건 결정론이
// unverified 로 강등하지 않는다. 그 판정은 verification_evidence 컬럼이 아니라
// sig.gemma_reason(append-only 대장)으로 한다 — 컬럼은 이 UPDATE 가 덮어쓰는 대상이라
// 자기가 지켜야 할 신호를 자기가 지우고 있었다.
const tierCASE = `CASE
    WHEN sig.auth_providers IS NOT NULL THEN 'authoritative'
    WHEN sig.wiki_ref OR sig.confidence >= 0.75 OR sig.strong_src THEN 'evidenced'
    WHEN e.verification_evidence LIKE 'search+gemma%' OR sig.gemma_reason IS NOT NULL THEN 'evidenced'
    ELSE 'unverified' END`

// evidenceCASE — 근거 문자열. gemma 가지를 둘로 나눈 이유: 컬럼이 살아 있으면 원문을
// 그대로 쓰고, 이미 덮여 사라졌으면 대장에서 복원한다(종전엔 복원 경로가 없어 소실이 곧
// 강등이었다). strong-source 가 gemma 보다 앞인 건 유지 — 실제 값을 가진 강한 소스가
// 더 구체적인 근거이고, gemma 이력은 이제 대장에 영속되어 언제든 되짚을 수 있다.
const evidenceCASE = `CASE
    WHEN sig.auth_providers IS NOT NULL THEN sig.auth_providers
    WHEN sig.wiki_ref THEN 'wikipedia-langlink'
    WHEN sig.strong_src THEN 'strong-source'
    WHEN sig.confidence >= 0.75 THEN 'confidence ' || round(sig.confidence, 2)::text
    WHEN e.verification_evidence LIKE 'search+gemma%' THEN e.verification_evidence
    WHEN sig.gemma_reason IS NOT NULL THEN 'search+gemma: ' || left(sig.gemma_reason, 70)
    ELSE 'no independent anchor' END`

// updateStmt — {{scope}} 자리에 CTE WHERE 추가절(코드 상수만)을 끼운 UPDATE 문.
func updateStmt(scope string) string {
	return strings.Replace(sigCTE(), "{{scope}}", scope, 1) + `
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
