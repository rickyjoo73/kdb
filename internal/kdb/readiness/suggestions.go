package readiness

// suggestions.go — 소비자가 만든 표기를 **재료로** 받아 두고, 우리가 입증하면 물러나게 한다.
//
// ★두 가지를 엄격히 가른다.
//     승격: 제안이 자라서 검증값이 된다        — **금지.** 우리가 보낸 값이 되돌아오는 순환이다.
//     교체: 근거 있는 값이 들어오면 제안이 물러난다 — **당연.** 여기서 강제한다.
//
// 제안은 kwave_entities 의 표기 칸에 **절대** 안 들어간다. 이 파일에도 그 경로가 없다.
// 우리 값이 생기면 SupersedeSuggestions 가 제안에 물러난 시각을 적고, 그때부터 서빙에서 빠진다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5"
)

// suggestionBasis — 어떻게 만든 값인지. 받는 쪽이 성격을 알고 쓰게 하는 것이 목적이라
// 모르는 값은 버리지 않고 'unspecified' 로 적는다(관측한 것을 그대로 적는다, D-37).
func suggestionBasis(b string) string {
	switch strings.ToLower(strings.TrimSpace(b)) {
	case "literal", "translation", "translated":
		return "literal"
	case "transliteration", "translit", "romanization":
		return "transliteration"
	case "official":
		return "official"
	case "":
		return "unspecified"
	default:
		return "unspecified"
	}
}

// SaveSuggestions — 준비 요청에 실려 온 제안을 적어 둔다.
//
// ★이름·로케일·제작처당 하나만 남는다. 호출마다 달라지는 직역을 매번 쌓으면 후보가
//   여러 개가 되고, "먼저 정한 것을 모두가 다시 쓴다"는 목적 자체가 깨진다.
//   먼저 온 것이 남고 같은 값이 다시 오면 seen_count 만 오른다.
//   ★단, **이미 물러난 제안은 되살리지 않는다** — 우리 값이 있는데 제안이 다시 올라오면
//     교체가 무효가 된다.
func SaveSuggestions(ctx context.Context, tx pgx.Tx, prepID any, in Input) int {
	meta := in.SuggestionMeta
	producer := strings.TrimSpace(meta.Producer)
	if producer == "" || len(producer) > 64 {
		return 0 // 누가 만들었는지 모르는 값은 안 받는다. 출처 없는 제안은 재료도 아니다.
	}
	saved := 0
	for _, t := range in.Terms {
		ko := strings.TrimSpace(t.KO)
		if ko == "" || len(t.Suggestions) == 0 {
			continue
		}
		for loc, sg := range t.Suggestions {
			loc, val := strings.TrimSpace(loc), strings.TrimSpace(sg.Value)
			if loc == "" || val == "" || len([]rune(val)) > 400 || len(loc) > 16 {
				continue
			}
			var entityID any
			if t.EntityID != "" {
				entityID = t.EntityID
			}
			tag, err := tx.Exec(ctx, `
INSERT INTO kwave_kdb_suggested_names
  (entity_id, term_ko, locale, value, basis, producer, model, reasoning, preparation_id, source_url)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT (term_ko, locale, producer) DO UPDATE
   SET seen_count = kwave_kdb_suggested_names.seen_count + 1,
       last_seen_at = now(),
       entity_id = COALESCE(kwave_kdb_suggested_names.entity_id, EXCLUDED.entity_id)
 WHERE kwave_kdb_suggested_names.superseded_at IS NULL`,
				entityID, ko, loc, val, suggestionBasis(sg.Basis), producer,
				trunc(meta.Model, 80), trunc(meta.Reasoning, 32), prepID, trunc(in.SourceURL, 2048))
			if err != nil {
				log.Printf("kdb.suggestion: %s/%s: %v", ko, loc, err)
				continue
			}
			if tag.RowsAffected() > 0 {
				saved++
			}
		}
	}
	return saved
}

func trunc(s string, n int) string {
	s = strings.TrimSpace(s)
	if len([]rune(s)) > n {
		return string([]rune(s)[:n])
	}
	return s
}

// suggestionLocaleCols — 로케일 → (값 컬럼, 출처 컬럼). kwave 의 표기 칸과 같아야 한다.
var suggestionLocaleCols = map[string][2]string{
	"en": {"canonical_en", "canonical_en_source"}, "ja": {"canonical_ja", "canonical_ja_source"},
	"vi": {"canonical_vi", "canonical_vi_source"}, "zh": {"canonical_zh", "canonical_zh_source"},
	"zh-hant": {"canonical_zh_hant", "canonical_zh_hant_source"},
	"es":      {"canonical_es", "canonical_es_source"}, "id": {"canonical_id", "canonical_id_source"},
	"pt-br": {"canonical_pt_br", "canonical_pt_br_source"},
}

// SupersedeSuggestions — **우리가 입증한 값이 생기면 제안은 물러난다.**
//
// 승격이 아니라 교체다. 제안을 표기 칸으로 올리는 것이 아니라, 표기 칸에 근거 있는 값이
// 들어온 순간 제안을 서빙에서 빼는 것이다. 기록은 지우지 않는다 — 우리 값과 제안이
// 얼마나 맞았는지(matched_ours)가 제안 품질을 재는 유일한 방법이다.
//
// 대상: 제안이 붙은 대상의 그 로케일 칸이 **비어 있지 않고 출처가 자격 있는 것**일 때.
// (qualifiedSource — 운영자·tmdb·musicbrainz·kofic 등. gtranslate·codex-fallback 은 아니다:
//  기계값으로 기계값을 밀어내는 것은 교체가 아니다.)
func SupersedeSuggestions(ctx context.Context, tx pgx.Tx) (int, error) {
	total := 0
	for loc, c := range suggestionLocaleCols {
		tag, err := tx.Exec(ctx, `
UPDATE kwave_kdb_suggested_names s
   SET superseded_at = now(),
       superseded_by = e.`+c[0]+`,
       superseded_src = COALESCE(e.`+c[1]+`,''),
       matched_ours = (lower(btrim(s.value)) = lower(btrim(e.`+c[0]+`)))
  FROM kwave_entities e
 WHERE s.superseded_at IS NULL
   AND s.locale = $1
   AND e.id = s.entity_id
   AND COALESCE(e.`+c[0]+`,'') <> ''
   AND COALESCE(e.`+c[1]+`,'') = ANY($2::text[])`, loc, qualifiedSourceList())
		if err != nil {
			return total, err
		}
		total += int(tag.RowsAffected())
	}
	return total, nil
}

// qualifiedSourceList — qualifiedSource 와 **같은 목록**이어야 한다. 사본을 두면 갈라진다
// (2026-09-15 오판 29 가 정확히 그 계열이었다 — 두 곳의 목록이 달라 한쪽이 죽은 가지였다).
func qualifiedSourceList() []string {
	out := []string{}
	for _, s := range []string{
		"operator", "operator-locked", "wikidata-label", "tmdb", "musicbrainz", "kofic", "kmdb",
		"naver-people", "correction-verified", "netflix", "disney", "itunes", "discogs",
		"media-consensus", "local-usage",
	} {
		if qualifiedSource(s) {
			out = append(out, s)
		}
	}
	return out
}
