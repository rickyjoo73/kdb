package kdb

// kopis_drain — event_tour candidate 를 KOPIS(공연예술통합전산망) 공식 카탈로그에
// 대조해 승급한다 (2026-07-23 Phase1, 오너 승인 개선안 — event_tour 첫 결정적 앵커).
//
// 매칭 규칙(라이브 실측 기반): KOPIS 공연명은 장식을 두른다 —
//   "박지현 콘서트: 쇼맨쉽 시즌2 SHOWMANSHIP SEASON 2 [서울 (앵콜)]"
// 그래서 정규화 포함(containment) 게이트를 쓰되 오염을 막는 3중 조건:
//   ①정규화 후보명 5자+ : 후보명⊂공연명 이면 매칭
//   ②정규화 후보명 2~4자: 후보명⊂공연명 AND 힌트 co-mention 아티스트도 공연명에 존재
//   ③힌트 아티스트가 있으면 항상 우선 corroborate(있는데 공연명에 없으면 보류)
// 영화제·시상식 등 KOPIS 밖 이벤트는 no_match → 뉴스축(cand-evidence)이 담당.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/kopis"
)

// DrainKopisEvents — event_tour candidate n건을 KOPIS 대조 승급. 반환 (promoted, checked).
func DrainKopisEvents(ctx context.Context, pool *pgxpool.Pool, cl *kopis.Client, serviceKey string, limit int) (promoted, checked int) {
	if pool == nil || cl == nil || strings.TrimSpace(serviceKey) == "" || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko,
       COALESCE((SELECT q.context_hint FROM kwave_entity_research_queue q
                  WHERE (q.entity_ko=e.canonical_ko OR q.entity_ko=ANY(e.aliases_ko))
                    AND COALESCE(q.context_hint,'')<>'' ORDER BY q.created_at DESC LIMIT 1),'')
  FROM kwave_entities e
 WHERE e.status='candidate' AND e.entity_type='event_tour'
   AND e.operator_locked=false
   AND char_length(e.canonical_ko) BETWEEN 2 AND 80
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=e.id AND r.provider='kopis')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a
                  WHERE a.entity_id=e.id AND a.field='kopis'
                    AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0
	}
	type row struct{ id, ko, hint string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.hint) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	mark := func(id, outcome string) {
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'kopis',1,now(),$2)
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now(), last_source=EXCLUDED.last_source`, id, outcome)
	}

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		checked++
		// 검색어: 장식 제거 — 연도 접두("2026 ") 및 대괄호/괄호 꼬리 제거한 핵심명.
		q := kopisQueryName(it.ko)
		events, serr := cl.Search(ctx, serviceKey, q, 20)
		if serr != nil {
			continue // transient — 쿨다운 없이 다음 회차
		}
		artist := coMentionActiveArtist(ctx, pool, it.hint, it.ko)
		hit := pickKopisMatch(it.ko, artist, events)
		if hit == nil {
			mark(it.id, "no_match")
			continue
		}
		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			continue
		}
		_, insErr := tx.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'kopis',$2,$3,0.8,$4,now())
ON CONFLICT DO NOTHING`, it.id, hit.ID, kopis.DetailURL(hit.ID),
			fmt.Sprintf(`{"prfnm":%q,"genre":%q,"venue":%q,"from":%q,"to":%q}`,
				hit.Name, hit.Genre, hit.Venue, hit.From, hit.To))
		if insErr != nil {
			_ = tx.Rollback(ctx)
			continue
		}
		tag, upErr := tx.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.78),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') ||
               'kopis 공연 확정('||$2||' · '||$3||') 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND entity_type='event_tour'
   AND operator_locked=false`, it.id, hit.Genre, hit.ID)
		if upErr != nil || tag.RowsAffected() != 1 {
			_ = tx.Rollback(ctx)
			continue
		}
		if tx.Commit(ctx) != nil {
			continue
		}
		promoted++
		mark(it.id, "applied")
	}
	return promoted, checked
}

// kopisQueryName — KOPIS 검색어용 핵심명: 선행 연도(“2026 ”류)·대괄호 꼬리 제거.
func kopisQueryName(s string) string {
	s = strings.TrimSpace(s)
	for _, pre := range []string{"2024 ", "2025 ", "2026 ", "2027 "} {
		s = strings.TrimPrefix(s, pre)
	}
	if i := strings.IndexAny(s, "[("); i > 3 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// pickKopisMatch — containment 게이트(패키지 주석 §매칭 규칙).
//
// ★2026-07-23 강화(첫 실행 FP 실측: "그리스"(뮤지컬 후보, 3자)가 "이창용 도슨트의
// 그리스 로마신화, 클래식을 만나다"에 오앵커): 정규화 6자 미만 짧은 이름은
// 문자열 포함만으로 절대 승급하지 않는다 —
//   ①6자+ : containment 허용(장식 두른 긴 공연명 흡수)
//   ②6자 미만: 구분자(쉼표·콜론·괄호)로 쪼갠 세그먼트 정확일치 필요
//     (예: "정주행 프로젝트, 헤다 가블러" 세그먼트 "헤다 가블러" == 후보 ✓
//          "…도슨트의 그리스 로마신화…" 어느 세그먼트도 "그리스" ≠ ✗)
//   ③또는 힌트 아티스트(3자+)가 원문 공연명에 그대로 존재(corroborate)
func pickKopisMatch(candKo, artist string, events []kopis.Event) *kopis.Event {
	nc := kopisNorm(kopisQueryName(candKo))
	runes := len([]rune(nc))
	if runes < 2 {
		return nil
	}
	rawArtist := strings.TrimSpace(artist)
	artistOK := len([]rune(rawArtist)) >= 3
	for i := range events {
		ev := &events[i]
		ne := kopisNorm(ev.Name)
		if ne == "" || !strings.Contains(ne, nc) {
			continue
		}
		if runes >= 6 {
			return ev
		}
		if segmentExact(ev.Name, nc) {
			return ev
		}
		if artistOK && strings.Contains(ev.Name, rawArtist) {
			return ev
		}
	}
	return nil
}

// segmentExact — 공연명을 구분자(쉼표·콜론·대괄호·소괄호·가운뎃점)로 쪼갠 세그먼트 중
// 정규화 정확일치가 있는지.
func segmentExact(rawName, nc string) bool {
	segs := strings.FieldsFunc(rawName, func(r rune) bool {
		switch r {
		case ',', ':', ';', '[', ']', '(', ')', '·', '|', '—', '-':
			return true
		}
		return false
	})
	for _, s := range segs {
		if kopisNorm(s) == nc {
			return true
		}
	}
	return false
}

// kopisNorm — 공연명 비교 정규화: 소문자 + 공백/구두점/중점 제거.
func kopisNorm(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '-', '.', '_', '\'', '"', ',', '(', ')', '[', ']', '!', '?', ':', ';', '·', '・', '’', '‘', '“', '”', '＆', '&', '＋', '+', '~', '～':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
