package kdbapi

// prepare_suggestions — `/v1/prepare` 로 들어온 제안 표기를 적어 둔다.
//
// ★왜 여기에도 있나. 제안 저장은 원래 readiness.SaveSuggestions(= `/v1/preparations`)에만
//   달았는데, 소비자가 실제로 쓰는 문은 `/v1/prepare` 다 — 실측으로 하루 110회 대
//   누적 7회다. 쓰는 문에 안 달면 기능이 닿지 않는다.
//
// ★제안은 값이 아니라 재료다. 여기서도 kwave_entities 표기 칸으로 가는 경로는 없다.
//   저장 실패는 준비 자체를 막지 않는다 — 제안은 부가물이지 요청의 일부가 아니다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
)

// savePrepareSuggestions — terms[].suggestions 를 kwave_kdb_suggested_names 에 적는다.
// entityID 는 이미 대상이 정해진 term 에만 채운다(이름에 붙이면 동명 함정이다).
func savePrepareSuggestions(ctx context.Context, pool *pgxpool.Pool, req PrepareRequest, terms []PrepareTerm, resolved map[string]string) int {
	if pool == nil {
		return 0
	}
	producer := strings.TrimSpace(req.SuggestionMeta.Producer)
	if producer == "" || len(producer) > 64 {
		return 0 // 누가 만들었는지 모르는 값은 안 받는다.
	}
	saved := 0
	for _, t := range terms {
		ko := strings.TrimSpace(t.Ko)
		if ko == "" || len(t.Suggestions) == 0 {
			continue
		}
		for loc, sg := range t.Suggestions {
			loc, val := strings.TrimSpace(loc), strings.TrimSpace(sg.Value)
			if loc == "" || val == "" || len([]rune(val)) > 400 || len(loc) > 16 {
				continue
			}
			var entityID any
			if id := resolved[ko]; id != "" {
				entityID = id
			}
			tag, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_suggested_names
  (entity_id, term_ko, locale, value, basis, producer, model, reasoning, source_url)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
ON CONFLICT (term_ko, locale, producer) DO UPDATE
   SET seen_count = kwave_kdb_suggested_names.seen_count + 1,
       last_seen_at = now(),
       entity_id = COALESCE(kwave_kdb_suggested_names.entity_id, EXCLUDED.entity_id)
 WHERE kwave_kdb_suggested_names.superseded_at IS NULL`,
				entityID, ko, loc, val, readiness.NormalizeSuggestionBasis(sg.Basis), producer,
				cut(req.SuggestionMeta.Model, 80), cut(req.SuggestionMeta.Reasoning, 32), cut(req.SourceURL, 2048))
			if err != nil {
				log.Printf("kdbapi.suggestion: %s/%s: %v", ko, loc, err)
				continue
			}
			if tag.RowsAffected() > 0 {
				saved++
			}
		}
	}
	return saved
}

func cut(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > n {
		return string([]rune(s)[:n])
	}
	return s
}
