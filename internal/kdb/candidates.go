// Package kdb — 신규 entity 자연 등록 (kwave_entities.status 흡수, 2026-05-25).
//
// 운영자 비판 + Agent (Architect) 권고: kwave_entity_candidates 별 테이블 폐기.
// kwave_entities.status='candidate' + source_domains[] 컬럼 1곳 통합 (§5
// single source of truth).
//
// 흐름:
//  1. RSS cheap-gate fail item + K-content category → Codex 추출 → ko_hint 발견
//  2. cand.Observe(ko_hint, source_domain):
//     - kwave_entities 존재 X → INSERT status='candidate', confidence=0.4
//     - 존재 + status='candidate' → source_domains 누적
//     - 존재 + status='active' → no-op (이미 정식 entity)
//  3. SweepPromote: status='candidate' AND COUNT(source_domains) ≥ 2
//     → status='active', confidence=0.5 자동 promote
//
// 운영자 admin 페이지 (/admin/entities/?status=candidate) 에서 확인.
package kdb

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
)

// CandidateThreshold — 자동 promote 임계 (운영자 확정 2 매체).
const CandidateThreshold = 2

// CandidateStore — kwave_entities (status='candidate') 관리.
type CandidateStore struct {
	Pool *pgxpool.Pool
}

// NewCandidateStore — 기본 생성자.
func NewCandidateStore(pool *pgxpool.Pool) *CandidateStore {
	return &CandidateStore{Pool: pool}
}

// Observe — 신규 ko_hint 발견. kwave_entities UPSERT (status=candidate).
//
// 운영자 directive 2026-05-25: RSS 매체가 사용 중인 locale 표기 (spelling)
// 도 함께 저장. ko 만 저장하던 옛 흐름이 candidates 화면에 locale 칸 비어있게
// 만든 누락의 원인. spelling="" / locale 미지원이면 ko 만 저장.
func (s *CandidateStore) Observe(ctx context.Context, koHint, locale, spelling, sourceDomain string) error {
	koHint = strings.TrimSpace(koHint)
	if koHint == "" {
		return nil
	}
	// LLM 이 발견했다고 주장하는 ko_hint 는 신규 candidate 로 INSERT 되어 2-매체
	// 합의 시 자동 promote 된다. 따라서 gatekeeper 와 동일한 결정론적 pre-gate 를
	// 통과해야 한다: 자소 깨짐("임ㅇ원희"), control/PUA, 과길이(설명/헤드라인 구),
	// 4+ 단어 구문, 조사/어미 꼬리(문장 조각) 는 신규 entity 생성 거부.
	// (gatekeeper.PreGate 는 IsCleanKorean + lone-jamo 검사를 포함한다.)
	if gatekeeper.PreGate(koHint).Verdict == gatekeeper.PreReject {
		return nil
	}
	// canonical_ko 문자셋 가드(2026-06-20): 일본어(가나)·순수 한자가 canonical_ko 로
	// 신규 INSERT 되는 손상 차단(예: 常田大希·ホジュン~ — canonical_ko=canonical_ja 손상
	// 시그니처). PreGate 는 한글 필수를 보지 않아 이 류가 통과했다.
	if !IsValidSpellingForLocale("ko", koHint) {
		return nil
	}
	locale = strings.TrimSpace(locale)
	spelling = strings.TrimSpace(spelling)

	// 동명이인(homonym) 대응 (2026-05-29): canonical_ko UNIQUE 제거 후
	// ON CONFLICT (canonical_ko) upsert 는 더 이상 유효하지 않다.
	// 대신 explicit lookup → insert/누적 으로 분기한다 (homonym-safe).
	//
	//  - 같은 canonical_ko entity 가 0개  → 신규 candidate INSERT.
	//  - 정확히 1개          → source_domains 누적 (기존 동작).
	//  - 2개 이상 (homonym)  → 어느 사람인지 RSS ko_hint 만으로 알 수 없으므로
	//    blind 누적 금지. needs_disambig 신호만 켜고 누적은 보류 (운영자 리뷰).
	var existing int
	if err := s.Pool.QueryRow(ctx,
		`SELECT count(*) FROM kwave_entities WHERE canonical_ko = $1`, koHint).Scan(&existing); err != nil {
		return err
	}
	switch {
	case existing == 0:
		if _, err := s.Pool.Exec(ctx, `
INSERT INTO kwave_entities (canonical_ko, entity_type, confidence, status, source_domains, notes)
VALUES ($1, 'unknown', 0.400, 'candidate', ARRAY[$2::text],
        'KDB candidate — RSS 발견 (cheap-gate 0 hit + K-content 매체)')`, koHint, sourceDomain); err != nil {
			return err
		}
	case existing == 1:
		if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET source_domains = (
     SELECT ARRAY(SELECT DISTINCT d FROM unnest(source_domains || ARRAY[$2::text]) AS d WHERE d != '')
   ),
       updated_at = now()
 WHERE canonical_ko = $1`, koHint, sourceDomain); err != nil {
			return err
		}
	default:
		// homonym 다수 — 운영자가 어느 사람인지 결정해야 한다.
		if _, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities SET needs_disambig = true, updated_at = now()
 WHERE canonical_ko = $1`, koHint); err != nil {
			return err
		}
		return nil
	}

	// spelling 이 있으면 locale 칸도 채움. active row 는 잠금 (operator-curated 보호).
	canonCol, aliasCol, srcCol := localeColumns(locale)
	if canonCol == "" || spelling == "" {
		return nil
	}
	// ★문자셋 가드 — 관측 spelling 이 locale 에 부적합(ja 칸에 한글, zh 칸에 라틴/한글
	// 혼입 등)이면 canonical 에 쓰지 않는다. 유일하게 남아있던 오염 신규유입 경로
	// (닥터X canonical_ja='닥터X' 류) 차단. alias 도 같은 가드 적용.
	if !IsValidSpellingForLocale(locale, spelling) {
		return nil
	}
	// canonical_<locale> 비어있으면 set, 같은 값이면 no-op, 다른 값이면 aliases 에 append.
	// source 컬럼은 wikidata-label 기본값에서 rss-observation:<domain> 으로 갱신.
	q := fmt.Sprintf(`
UPDATE kwave_entities
   SET %[1]s = COALESCE(NULLIF(%[1]s,''), $2),
       %[2]s = CASE
                  WHEN %[1]s IS NULL OR %[1]s = '' OR %[1]s = $2 THEN %[2]s
                  WHEN $2 = ANY(%[2]s) THEN %[2]s
                  ELSE array_append(%[2]s, $2)
              END,
       %[3]s = CASE
                  WHEN %[1]s IS NULL OR %[1]s = '' THEN 'rss-observation:' || $3
                  ELSE %[3]s
              END,
       updated_at = now()
 WHERE canonical_ko = $1
   AND status = 'candidate'`, canonCol, aliasCol, srcCol)
	if _, err := s.Pool.Exec(ctx, q, koHint, spelling, sourceDomain); err != nil {
		return fmt.Errorf("locale fill (%s): %w", locale, err)
	}
	return nil
}

// localeColumns — Codex locale 코드 → kwave_entities 컬럼명 매핑 (whitelist).
// 알 수 없는 locale 은 empty 반환 → ko 만 저장.
func localeColumns(locale string) (canonical, aliases, source string) {
	switch strings.ToLower(locale) {
	case "en":
		return "canonical_en", "aliases_en", "canonical_en_source"
	case "ja":
		return "canonical_ja", "aliases_ja", "canonical_ja_source"
	case "vi":
		return "canonical_vi", "aliases_vi", "canonical_vi_source"
	case "zh":
		return "canonical_zh", "aliases_zh", "canonical_zh_source"
	case "zh-hant", "zh_hant":
		return "canonical_zh_hant", "aliases_zh_hant", "canonical_zh_hant_source"
	case "es":
		return "canonical_es", "aliases_es", "canonical_es_source"
	case "id":
		return "canonical_id", "aliases_id", "canonical_id_source"
	case "pt-br", "pt_br":
		return "canonical_pt_br", "aliases_pt_br", "canonical_pt_br_source"
	}
	return "", "", ""
}

// SweepPromote — status='candidate' AND ≥ 2 매체 등장 → status='active' 자동 promote.
// 다음 cycle 부터 정상 cheap-gate hit → cascade 정상 발동.
func (s *CandidateStore) SweepPromote(ctx context.Context) (int, error) {
	tag, err := s.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='active',
       confidence=GREATEST(confidence, 0.500),
       -- candidate 단계의 needs_disambig(게이트키퍼 격리 등)를 active 로 끌고
       -- 오지 않는다 — 동명이인 충돌은 active 끼리의 markHomonymsIfConflict 가
       -- 다시 판단한다(가짜 충돌 누수 차단).
       needs_disambig=false, disambig_reviewed_at=NULL,
       notes = COALESCE(notes,'') || ' [auto-promoted ' || array_length(source_domains, 1) || ' 매체]',
       updated_at = now()
 WHERE status='candidate'
   AND array_length(source_domains, 1) >= $1`, CandidateThreshold)
	if err != nil {
		return 0, fmt.Errorf("promote: %w", err)
	}
	n := int(tag.RowsAffected())
	if n > 0 {
		log.Printf("kdb.candidates: promoted=%d (≥%d 매체 합의)", n, CandidateThreshold)
	}
	return n, nil
}
