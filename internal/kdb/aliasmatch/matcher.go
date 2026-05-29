// Package aliasmatch — 신규 candidate ko_hint 가 기존 active entity 의
// 별칭 / 약자 / 슬랭 / 오타인지 판별. 매칭 시 새 entity 만들지 않고
// 기존 entity 의 aliases_ko 로 merge.
//
// 운영자 정공법:
//   - exact = aliases_ko 에 이미 등록 (확실).
//   - typo  = pg_trgm similarity ≥ 0.6 (운영자 confirm 권장).
//   - abbr  = 한글 약자 패턴 (운영자 confirm 권장).
package aliasmatch

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MatchKind — alias 매칭 유형.
type MatchKind string

const (
	KindAlias        MatchKind = "alias"        // aliases_ko 정확 일치 — 자동 merge 가능
	KindAbbreviation MatchKind = "abbreviation" // 한글 약자 — 운영자 confirm 권장
	KindTypo         MatchKind = "typo"         // pg_trgm similarity ≥ 0.6 — 운영자 confirm
)

// Match — 후보 매칭 1건.
type Match struct {
	EntityID   uuid.UUID
	EntityKo   string
	EntityType string
	Kind       MatchKind
	Score      float64 // 1.0 = exact, 0.0~1.0 = similarity
}

// Find — koHint 에 대한 매칭 후보 0~N 건 반환 (Score 내림차순).
// 우선순위: alias (1.0) > abbreviation > typo. 같은 entity 가 여러 종류로
// 매칭되면 가장 높은 종류만 남김.
func Find(ctx context.Context, pool *pgxpool.Pool, koHint string) ([]Match, error) {
	koHint = strings.TrimSpace(koHint)
	if koHint == "" {
		return nil, nil
	}
	out := []Match{}
	seen := map[uuid.UUID]bool{}

	// 1) aliases_ko 정확 매칭 — score=1.0
	if rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text
FROM kwave_entities
WHERE status='active' AND $1 = ANY(aliases_ko)
LIMIT 5`, koHint); err == nil {
		defer rows.Close()
		for rows.Next() {
			var m Match
			if err := rows.Scan(&m.EntityID, &m.EntityKo, &m.EntityType); err == nil {
				m.Kind = KindAlias
				m.Score = 1.0
				if !seen[m.EntityID] {
					out = append(out, m)
					seen[m.EntityID] = true
				}
			}
		}
	}

	// 2) 한글 약자 패턴 — koHint 가 active 의 음절 일부 조합
	// 예: "유퀴즈" → "유 퀴즈 온 더 블럭", "모자무싸" → "모두가 자신의 무가치함과 싸우고 있다"
	if abbrs := abbreviationCandidates(ctx, pool, koHint); len(abbrs) > 0 {
		for _, m := range abbrs {
			if !seen[m.EntityID] {
				out = append(out, m)
				seen[m.EntityID] = true
			}
		}
	}

	// 3) pg_trgm similarity (typo) — 0.6 ~ 0.99
	if rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text, similarity(canonical_ko, $1)::float
FROM kwave_entities
WHERE status='active'
  AND canonical_ko <> $1
  AND char_length(canonical_ko) >= 2 AND char_length($1) >= 2
  AND canonical_ko %% $1
  AND similarity(canonical_ko, $1) >= 0.6
ORDER BY similarity(canonical_ko, $1) DESC
LIMIT 5`, koHint); err == nil {
		defer rows.Close()
		for rows.Next() {
			var m Match
			if err := rows.Scan(&m.EntityID, &m.EntityKo, &m.EntityType, &m.Score); err == nil {
				m.Kind = KindTypo
				if !seen[m.EntityID] {
					out = append(out, m)
					seen[m.EntityID] = true
				}
			}
		}
	}
	return out, nil
}

// abbreviationCandidates — koHint 가 active entity 의 음절 조합 약자인지.
// 단순 휴리스틱: koHint 의 각 글자가 active 의 단어들 시작 글자거나 음절 순서대로 등장.
// 비용 절감 위해 koHint 첫 글자 일치하는 active 만 검사.
func abbreviationCandidates(ctx context.Context, pool *pgxpool.Pool, koHint string) []Match {
	runes := []rune(koHint)
	if len(runes) < 2 || len(runes) > 6 {
		return nil
	}
	firstChar := string(runes[0])
	// 첫 글자 매칭 + 길이 ≥ koHint*2 인 active entity 검사 (약자는 보통 풀이름의 절반 이하).
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, entity_type::text
FROM kwave_entities
WHERE status='active'
  AND canonical_ko LIKE $1 || '%'
  AND char_length(canonical_ko) >= $2
LIMIT 100`, firstChar, len(runes)*2)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []Match{}
	for rows.Next() {
		var m Match
		if err := rows.Scan(&m.EntityID, &m.EntityKo, &m.EntityType); err != nil {
			continue
		}
		if isAbbreviation(koHint, m.EntityKo) {
			m.Kind = KindAbbreviation
			m.Score = 0.85
			out = append(out, m)
		}
	}
	return out
}

// isAbbreviation — koHint 의 음절들이 full 의 단어/음절에서 순서대로 첫 음절 위치에 등장.
//
// 매칭 1: 단어 첫 음절 조합 — "유퀴즈" vs "유 퀴즈 온 더 블럭" → 단어 첫음절 "유"/"퀴"/"온"/... 중 "유","퀴"  → match
// 매칭 2: 연속 음절 prefix — "어거스트 디" 와 "어거스트 디 투어"
// 매칭 3: 첫음절 + 마지막음절 — "모자무싸" vs "모두가 자신의 무가치함과 싸우고 있다" — 너무 복잡, skip
func isAbbreviation(koHint, full string) bool {
	if !strings.ContainsRune(full, ' ') {
		// 공백 없는 full 이면 prefix 매칭만 시도.
		return strings.HasPrefix(full, koHint) && len([]rune(full)) > len([]rune(koHint))
	}
	// 공백 분리 단어들의 첫 음절을 모음.
	words := strings.Fields(full)
	if len(words) < 2 {
		return false
	}
	initials := make([]rune, 0, len(words))
	for _, w := range words {
		r := []rune(w)
		if len(r) > 0 {
			initials = append(initials, r[0])
		}
	}
	koRunes := []rune(koHint)
	// case A: koHint 가 단어 첫 음절 prefix 와 일치.
	if len(initials) >= len(koRunes) {
		match := true
		for i, r := range koRunes {
			if initials[i] != r {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	// case B: koHint 공백 제거 가 full 공백 제거 의 prefix.
	stripFull := strings.ReplaceAll(full, " ", "")
	stripHint := strings.ReplaceAll(koHint, " ", "")
	if strings.HasPrefix(stripFull, stripHint) && len([]rune(stripFull)) > len([]rune(stripHint)) {
		return true
	}
	return false
}
