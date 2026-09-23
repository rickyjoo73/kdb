package kdbapi

// prepare_suggestions — `/v1/prepare` 로 들어온 제안 표기를 적어 둔다.
//
// ★왜 여기에도 있나. 제안 저장은 원래 readiness.SaveSuggestions(= `/v1/preparations`)에만
//   달았는데, 소비자가 실제로 쓰는 문은 `/v1/prepare` 다 — 실측으로 하루 110회 대
//   누적 7회다. 쓰는 문에 안 달면 기능이 닿지 않는다.
//
// ★제안은 재료다 — 다만 **우리 칸이 비어 있으면** 그 칸을 채운다 (2026-09-24, 오너 지시
//   "어차피 없다면 넣어야지"). 이름표는 consumer-suggestion(등급 9, 최하위)이라 무엇이
//   오든 밀리고, 소비자는 이 이름표로 자기 제안을 골라 거를 수 있다. 값이 있는 칸은
//   건드리지 않는다 — 그 경우 제안은 지금처럼 기록으로만 남는다.
//   저장 실패는 준비 자체를 막지 않는다 — 제안은 부가물이지 요청의 일부가 아니다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
)

// suggestionFillCols — 제안의 locale 키 → 표기 칸. 소비자는 zh-hant·zh_hant 둘 다 보낸다.
var suggestionFillCols = map[string]string{
	"en": "canonical_en", "ja": "canonical_ja", "vi": "canonical_vi", "zh": "canonical_zh",
	"zh-hant": "canonical_zh_hant", "zh_hant": "canonical_zh_hant",
	"es": "canonical_es", "id": "canonical_id", "pt-br": "canonical_pt_br", "pt_br": "canonical_pt_br",
}

// fillBlankFromSuggestion — 대상이 정해졌고 그 칸이 비어 있을 때만 제안으로 채운다.
func fillBlankFromSuggestion(ctx context.Context, pool *pgxpool.Pool, entityID, loc, val string) bool {
	col, ok := suggestionFillCols[strings.ToLower(loc)]
	if !ok || entityID == "" {
		return false
	}
	if !kdb.IsValidSpellingForLocale(strings.TrimPrefix(col, "canonical_"), val) {
		return false
	}
	tag, err := pool.Exec(ctx, `UPDATE kwave_entities SET `+col+` = $2, `+col+`_source = $3, updated_at = now()
 WHERE id = $1 AND status = 'active' AND COALESCE(`+col+`,'') = ''`, entityID, val, string(kdb.SourceConsumerSuggestion))
	return err == nil && tag.RowsAffected() > 0
}

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
			if id, _ := entityID.(string); id != "" && fillBlankFromSuggestion(ctx, pool, id, loc, val) {
				log.Printf("kdbapi.suggestion: %s/%s 빈칸을 제안으로 채움(%s)", ko, loc, producer)
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
