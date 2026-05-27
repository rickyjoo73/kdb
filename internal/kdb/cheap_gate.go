// Package kdb — cheap-gate (외부 호출 0 entity 매칭).
//
// 정공법 (CLAUDE.md §8.6): LLM 호출 앞에 항상 non-LLM cheap gate.
// RSS item title+description 에서 우리가 알고 있는 K-content entity 가
// 단 1개라도 substring 매칭되면 Gemma 추출 큐로. 0개면 LLM 호출 안 함
// (신규 entity 후보 큐로 별도 분기).
//
// In-memory index 1회/poll cycle 로딩 (entity 600~ × aliases ~5 = ~3000 string).
// substring 매칭은 corasick / aho 같은 multi-pattern 매칭이 이상적이지만
// 600개 정도면 단순 loop 도 충분 (item 당 ~1ms).
package kdb

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EntityHint — cheap-gate 매칭 결과 1건.
type EntityHint struct {
	EntityID    uuid.UUID
	CanonicalKo string
	Matched     string // 실제 매칭된 spelling (canonical_ko 또는 aliases_ko 중 하나)
}

// EntityIndex — 한국어 spelling → entity 매핑 in-memory.
type EntityIndex struct {
	// spelling 은 lowercase 가 아닌 원본 그대로 (한글). substring 매칭이라 lowercase 무관.
	spellings map[string][]uuid.UUID // spelling → entity id 들 (동명이인 가능)
	byID      map[uuid.UUID]string   // entity id → canonical_ko
}

// LoadEntityIndex — confidence ≥ 0.5 entity 의 canonical_ko + aliases_ko 모두 로드.
//
// 정공법: 1글자 spelling 은 false positive 폭증 — skip (예: "비" → 모든 기사).
// 2글자 이상만 인덱스에 등록.
func LoadEntityIndex(ctx context.Context, pool *pgxpool.Pool) (*EntityIndex, error) {
	idx := &EntityIndex{
		spellings: make(map[string][]uuid.UUID, 4096),
		byID:      make(map[uuid.UUID]string, 1024),
	}
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, aliases_ko
FROM kwave_entities
WHERE confidence >= 0.5`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var ko string
		var aliases []string
		if err := rows.Scan(&id, &ko, &aliases); err != nil {
			continue
		}
		idx.byID[id] = ko
		addSpelling := func(s string) {
			s = strings.TrimSpace(s)
			if len([]rune(s)) < 2 {
				return // 1글자 skip (false positive)
			}
			idx.spellings[s] = append(idx.spellings[s], id)
		}
		addSpelling(ko)
		for _, a := range aliases {
			addSpelling(a)
		}
	}
	return idx, nil
}

// Size — index 의 spelling 개수.
func (e *EntityIndex) Size() int {
	if e == nil {
		return 0
	}
	return len(e.spellings)
}

// EntityCount — 인덱싱된 entity 수.
func (e *EntityIndex) EntityCount() int {
	if e == nil {
		return 0
	}
	return len(e.byID)
}

// MatchText — text 에서 spelling 매칭 시도. 매칭된 entity 들 반환.
// 한 text 안 같은 entity 가 여러 번 등장하면 1번만 (entity_id dedup).
//
// 운영자 정공법 (Agent NLP 권고 2026-05-25): false positive -60% 위해
// 한글 word boundary 체크 — spelling 의 양쪽 rune 이 한글이면 reject
// ("지민" 이 "구지민" / "유지민감" 같은 substring 에 매칭되는 케이스).
func (e *EntityIndex) MatchText(text string) []EntityHint {
	if e == nil || text == "" {
		return nil
	}
	seen := make(map[uuid.UUID]EntityHint)
	for spelling, ids := range e.spellings {
		if !containsWithBoundary(text, spelling) {
			continue
		}
		for _, id := range ids {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = EntityHint{
				EntityID:    id,
				CanonicalKo: e.byID[id],
				Matched:     spelling,
			}
		}
	}
	if len(seen) == 0 {
		return nil
	}
	out := make([]EntityHint, 0, len(seen))
	for _, h := range seen {
		out = append(out, h)
	}
	return out
}

// containsWithBoundary — substring 매칭 + 한글 word boundary 검사.
// spelling 의 양쪽 (직전/직후) 가 한글이면 reject (단어 가운데 substring).
func containsWithBoundary(text, spelling string) bool {
	if spelling == "" {
		return false
	}
	idx := 0
	for {
		hit := strings.Index(text[idx:], spelling)
		if hit < 0 {
			return false
		}
		pos := idx + hit
		// before / after rune 검사
		if !isHangulNeighbor(text, pos-1) && !isHangulNeighbor(text, pos+len(spelling)) {
			return true
		}
		idx = pos + 1
	}
}

func isHangulNeighbor(text string, byteIdx int) bool {
	if byteIdx < 0 || byteIdx >= len(text) {
		return false
	}
	// 가장 가까운 rune 시작점 찾기 (UTF-8)
	for byteIdx > 0 && (text[byteIdx]&0xC0) == 0x80 {
		byteIdx--
	}
	r, _ := utf8.DecodeRuneInString(text[byteIdx:])
	// 한글 음절 (가-힣) + 한글 자모 + 호환 자모
	return (r >= 0xAC00 && r <= 0xD7A3) || (r >= 0x1100 && r <= 0x11FF) || (r >= 0x3130 && r <= 0x318F)
}
