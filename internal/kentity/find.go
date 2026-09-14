package kentity

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

// 통합 찾기 — 한 상자로 **두 원장과 모든 언어 표기**를 본다. 없으면 **왜 없는지**를 말한다.
//
// 왜 필요한가(운영자 지적 2026-09-14). 지금은 공통 원장과 기존 원장이 각각 다른 화면이고,
// 검색은 `canonical_ko` 만 본다. 그리고 없을 때 화면이 주는 것은 "조건에 맞는 Entity가
// 없습니다 · 다른 필터로 찾아보세요"가 전부다 — **막다른 길이다.**
//
// ★"없다"는 네 가지로 갈린다. 가르지 않으면 **이미 있는 것을 또 만든다.**
//   진짜 없음 / 편집 범위 밖 / 이미 조사 중 / 표기만 다름

type FindHit struct {
	Ledger  string    `json:"ledger"` // common | legacy
	ID      uuid.UUID `json:"id"`
	KO      string    `json:"ko"`
	Type    string    `json:"type"`
	Subtype string    `json:"subtype"`
	Status  string    `json:"status"`
	Owner   string    `json:"owner"`
	MatchOn string    `json:"match_on"` // canonical_ko | name:<locale>
	Value   string    `json:"value"`    // 표기로 걸렸을 때 그 표기
}

type FindResult struct {
	Term string    `json:"term"`
	Hits []FindHit `json:"hits"`
	// 없을 때의 사유. 비어 있으면 결과가 있다는 뜻이다.
	Absence string `json:"absence"` // none | out_of_scope | researching | spelling_only
	// 사유를 뒷받침하는 것들 — 화면이 "왜"를 말할 수 있게.
	OutOfScope   []FindHit `json:"out_of_scope"`
	Researching  string    `json:"researching"` // 발굴 큐 상태
	SpellingHits []FindHit `json:"spelling_hits"`
}

// Find — 편집 범위 안에서 먼저 찾고, 없으면 범위 밖·조사 중·표기 차이를 차례로 확인한다.
func (s *Store) Find(ctx context.Context, term string) (FindResult, error) {
	r := FindResult{Term: strings.TrimSpace(term), Hits: []FindHit{}, OutOfScope: []FindHit{}, SpellingHits: []FindHit{}}
	if r.Term == "" || len([]rune(r.Term)) > 200 {
		return r, ErrInvalid
	}
	scan := func(q string, args ...any) ([]FindHit, error) {
		rows, err := s.Pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := []FindHit{}
		for rows.Next() {
			var h FindHit
			if err := rows.Scan(&h.Ledger, &h.ID, &h.KO, &h.Type, &h.Subtype, &h.Status, &h.Owner, &h.MatchOn, &h.Value); err != nil {
				return nil, err
			}
			out = append(out, h)
		}
		return out, rows.Err()
	}

	// ① 편집 범위 안 — 공통 원장(이름·모든 언어 표기) + 기존 원장.
	const inScope = `
SELECT 'common', e.id, e.canonical_ko, e.entity_type, COALESCE(e.subtype,''), e.status, e.write_owner,
       'canonical_ko', e.canonical_ko
  FROM kentity_entities e WHERE e.canonical_ko = $1 AND e.status <> 'rejected'
UNION ALL
SELECT 'common', e.id, e.canonical_ko, e.entity_type, COALESCE(e.subtype,''), e.status, e.write_owner,
       'name:' || n.locale, n.value
  FROM kentity_names n JOIN kentity_entities e ON e.id = n.entity_id
 WHERE n.value = $1 AND e.status <> 'rejected' AND e.canonical_ko <> $1
UNION ALL
SELECT 'legacy', k.id, k.canonical_ko, k.entity_type::text, '', k.status, 'kdb',
       'canonical_ko', k.canonical_ko
  FROM kwave_entities k WHERE k.canonical_ko = $1
LIMIT 50`
	var err error
	if r.Hits, err = scan(inScope, r.Term); err != nil {
		return r, err
	}
	if len(r.Hits) > 0 {
		return r, nil
	}

	// ② 편집 범위 밖 — **있는데 다루지 않기로 한 것**이다. "없음"과 전혀 다르다.
	if r.OutOfScope, err = scan(`
SELECT 'common', e.id, e.canonical_ko, e.entity_type, COALESCE(e.subtype,''), e.status, e.write_owner,
       'canonical_ko', e.canonical_ko
  FROM kentity_entities e WHERE e.canonical_ko = $1 AND e.status = 'rejected' LIMIT 20`, r.Term); err != nil {
		return r, err
	}
	if len(r.OutOfScope) > 0 {
		r.Absence = "out_of_scope"
		return r, nil
	}

	// ③ 이미 조사 중인가 — 같은 것을 또 요청하지 않게.
	if err = s.Pool.QueryRow(ctx, `
SELECT COALESCE(string_agg(DISTINCT status, ', '), '')
  FROM kwave_entity_research_queue WHERE entity_ko = $1`, r.Term).Scan(&r.Researching); err != nil {
		return r, err
	}
	if r.Researching != "" {
		r.Absence = "researching"
		return r, nil
	}

	// ④ 표기만 다른가 — 공백·대소문자를 지우고 맞춰 본다. 있으면 새로 만들면 안 된다.
	if r.SpellingHits, err = scan(`
SELECT 'common', e.id, e.canonical_ko, e.entity_type, COALESCE(e.subtype,''), e.status, e.write_owner,
       'canonical_ko', e.canonical_ko
  FROM kentity_entities e
 WHERE e.status <> 'rejected'
   AND lower(regexp_replace(e.canonical_ko, '\s', '', 'g')) = lower(regexp_replace($1, '\s', '', 'g'))
 LIMIT 20`, r.Term); err != nil {
		return r, err
	}
	if len(r.SpellingHits) > 0 {
		r.Absence = "spelling_only"
		return r, nil
	}

	r.Absence = "none"
	return r, nil
}
