package kdb

// suggestion_fill — 소비자가 prepare 에 실어 보낸 제안 표기(직역·음역)를 **우리가 못 채운 칸**에 쓴다.
//
// ★계기 (2026-09-30, 오너). "prepare 에 직역해서 넘어 온 것이 많은데 그것을 사용을 안
//   하는가? 우리가 해결 못 하는 것은 우선 그것을 활용해야 하지 않을까?"
//
//   09-24 에 «빈 칸은 제안으로 채운다»를 넣었지만(prepare_suggestions.go) 그 자리는
//   **요청 한 번 안에서** 대상이 이미 정해지고(active) 칸이 비어 있을 때만 돈다.
//   답하지 못한 이름이 바로 그 조건 밖이다 — 원장에 없거나 candidate 라서 제안은
//   entity_id 가 NULL 인 채 적히기만 하고, 나중에 review-judge 가 등록·활성화해도
//   그 제안을 다시 보는 경로가 없었다. 게다가 review-judge 는 en/ja/zh/zh_hant 만
//   채우므로 vi/es/id/pt_br 은 제안이 와 있어도 빈 채 남는다.
//
// ★이 레인이 하는 일 두 가지.
//   ① 연결: entity_id 가 NULL 인 제안을 **같은 이름의 활성 대상이 딱 하나**일 때만 그
//      대상에 붙인다. 둘 이상이면 동명 함정이라 붙이지 않는다(채영 TWICE/CLC).
//      소비자가 구체 유형을 보냈다면 그 유형과 대상 유형이 맞아야 한다.
//   ② 채움: 활성 대상의 **빈 칸만**, 이름표 consumer-suggestion(최하위, verified_only 제외)
//      으로 쓴다. 어떤 출처가 와도 밀리고, SupersedeSuggestions 가 교체를 기록한다.
//
// ★가드.
//   - 인명·그룹명(person·group)의 «직역(literal)» 은 쓰지 않는다. 이름은 번역하지 않고
//     음역한다 — 직역된 이름은 틀린 값이다.
//   - 우리가 **일부러 지운 값**(kwave_kdb_dataqa_log 의 old_value, 되돌리지 않은 것)은
//     제안으로 다시 들어오지 않는다. 앵커 오염으로 걷은 값이 소비자 캐시를 타고 돌아오는
//     순환을 막는다.
//   - 문자셋 검사(validLocaleValue) — 간체 칸의 번체, 번체 칸의 간체, 한글 잔존을 막는다.
//   - 운영자 잠금 행은 건드리지 않는다.
//   - 쓴 칸은 전부 dataqa_log(verdict='suggestion-backfill')에 남는다 — 한 라벨로 전량 회수된다.
//
// 수동: `kdb-app suggestion-fill [n] [go]` (기본 dry). 자동: adjudicate 레인, review-judge 뒤.

import (
	"context"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SuggestionFillResult — 한 회차의 결과.
type SuggestionFillResult struct {
	Linked    int            // 대상에 새로 붙인 제안
	Ambiguous int            // 같은 이름 활성 대상이 둘 이상이라 안 붙인 제안
	Cells     int            // 채운 칸
	ByLocale  map[string]int // 채운 칸의 로케일별 수
	Skipped   map[string]int // 안 쓴 후보의 사유
}

// suggestionFillLocaleCols — 정규화된 로케일 → 표기 칸.
var suggestionFillLocaleCols = map[string]string{
	"en": "canonical_en", "ja": "canonical_ja", "vi": "canonical_vi",
	"zh": "canonical_zh", "zh_hant": "canonical_zh_hant",
	"es": "canonical_es", "id": "canonical_id", "pt_br": "canonical_pt_br",
}

// NormalizeSuggestionLocale — 소비자가 보내는 로케일 키를 우리 칸 이름으로.
// zh-hant·zh_hant·zh-TW 는 번체, zh-Hans·zh-CN 은 간체, pt-BR 은 pt_br. 모르는 키는 "".
func NormalizeSuggestionLocale(loc string) string {
	l := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(loc)), "-", "_")
	switch l {
	case "zh_hans", "zh_cn", "zh_sg":
		l = "zh"
	case "zh_tw", "zh_hk", "zh_mo":
		l = "zh_hant"
	case "pt":
		l = "pt_br"
	}
	if _, ok := suggestionFillLocaleCols[l]; ok {
		return l
	}
	return ""
}

// nameTypesNoLiteral — 이름을 음역해야 하는 유형. 여기서는 직역 제안을 쓰지 않는다.
var nameTypesNoLiteral = map[string]bool{"person": true, "group": true}

// suggestionBasisRank — 값을 만든 방식의 순위. official > transliteration > 나머지.
func suggestionBasisRank(basis string) int {
	switch basis {
	case "official":
		return 3
	case "transliteration":
		return 2
	default: // literal · unspecified
		return 1
	}
}

type suggestionCand struct {
	value, basis, producer string
	seen                   int
	removed                bool // 우리가 일부러 지운 적 있는 값
}

// pickSuggestion — 한 칸의 후보 중 쓸 것 하나. 못 고르면 ("", 사유).
//
// 순위: 방식 → 같은 값을 낸 제작처 수(합의) → 본 횟수 → 값(고정 순서).
func pickSuggestion(loc, entityType string, cands []suggestionCand) (suggestionCand, string) {
	type scored struct {
		c     suggestionCand
		agree int
	}
	agree := map[string]map[string]bool{}
	for _, c := range cands {
		k := strings.ToLower(strings.TrimSpace(c.value))
		if agree[k] == nil {
			agree[k] = map[string]bool{}
		}
		agree[k][c.producer] = true
	}
	var ok []scored
	reason := ""
	for _, c := range cands {
		v := strings.TrimSpace(c.value)
		switch {
		case c.removed:
			reason = "removed-before"
		case nameTypesNoLiteral[entityType] && c.basis == "literal":
			reason = "literal-name"
		case !validLocaleValue(loc, v):
			reason = "bad-charset"
		default:
			ok = append(ok, scored{c, len(agree[strings.ToLower(v)])})
		}
	}
	if len(ok) == 0 {
		return suggestionCand{}, reason
	}
	sort.SliceStable(ok, func(i, j int) bool {
		a, b := ok[i], ok[j]
		if ra, rb := suggestionBasisRank(a.c.basis), suggestionBasisRank(b.c.basis); ra != rb {
			return ra > rb
		}
		if a.agree != b.agree {
			return a.agree > b.agree
		}
		if a.c.seen != b.c.seen {
			return a.c.seen > b.c.seen
		}
		return a.c.value < b.c.value
	})
	return ok[0].c, ""
}

// SuggestionFillEnabled — 기본 켜짐. KDB_SUGGESTION_FILL_ENABLED=0 이면 끈다.
func SuggestionFillEnabled() bool {
	return strings.TrimSpace(os.Getenv("KDB_SUGGESTION_FILL_ENABLED")) != "0"
}

// suggestionFillBatch — 한 회차에 채울 칸 상한(기본 500). KDB_SUGGESTION_FILL_BATCH.
func suggestionFillBatch() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KDB_SUGGESTION_FILL_BATCH"))); err == nil && v > 0 {
		return v
	}
	return 500
}

// suggestionLinkSQL — 연결 후보. 같은 이름(정식명·별칭)의 활성 대상 수를 센다.
// 소비자가 구체 유형을 보냈으면(우리 유형 목록 안의 값만) 대상 유형이 그중 하나여야 한다.
const suggestionLinkSQL = `
WITH s0 AS (
  SELECT id, term_ko FROM kwave_kdb_suggested_names
   WHERE entity_id IS NULL AND superseded_at IS NULL),
rt AS (
  SELECT term_ko, array_agg(DISTINCT term_type) types
    FROM kwave_kdb_request_terms
   WHERE term_ko IN (SELECT term_ko FROM s0) AND term_type = ANY($1::text[])
   GROUP BY term_ko),
hit AS (
  SELECT s0.id sid, e.id eid, e.entity_type::text etype
    FROM s0 JOIN kwave_entities e ON e.status = 'active' AND e.canonical_ko = s0.term_ko
  UNION
  SELECT s0.id, a.id, a.etype
    FROM s0 JOIN (SELECT id, entity_type::text etype, unnest(aliases_ko) alias
                    FROM kwave_entities WHERE status = 'active') a ON a.alias = s0.term_ko),
m AS (
  SELECT hit.sid, min(hit.eid::text) eid, count(DISTINCT hit.eid) n
    FROM hit JOIN s0 ON s0.id = hit.sid LEFT JOIN rt ON rt.term_ko = s0.term_ko
   WHERE rt.types IS NULL OR hit.etype = ANY(rt.types)
   GROUP BY hit.sid)`

// DrainSuggestionFill — 제안 연결 + 빈 칸 채움. limit<=0 이면 기본 상한.
func DrainSuggestionFill(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) SuggestionFillResult {
	res := SuggestionFillResult{ByLocale: map[string]int{}, Skipped: map[string]int{}}
	if pool == nil {
		return res
	}
	if limit <= 0 {
		limit = suggestionFillBatch()
	}
	run := NewLaneRun("suggestion-fill", dry)
	defer run.Record(ctx, pool)

	// ① 연결
	types := AssignableEntityTypes()
	if dry {
		if err := pool.QueryRow(ctx, suggestionLinkSQL+`
SELECT count(*) FILTER (WHERE n = 1), count(*) FILTER (WHERE n > 1) FROM m`, types).Scan(&res.Linked, &res.Ambiguous); err != nil {
			log.Printf("kdb.suggestion-fill: 연결 집계: %v", err)
		}
	} else {
		tag, err := pool.Exec(ctx, suggestionLinkSQL+`
UPDATE kwave_kdb_suggested_names s SET entity_id = m.eid::uuid
  FROM m WHERE s.id = m.sid AND m.n = 1 AND s.entity_id IS NULL`, types)
		if err != nil {
			log.Printf("kdb.suggestion-fill: 연결: %v", err)
		} else {
			res.Linked = int(tag.RowsAffected())
		}
		_ = pool.QueryRow(ctx, suggestionLinkSQL+`
SELECT count(*) FROM m WHERE n > 1`, types).Scan(&res.Ambiguous)
	}

	// ② 채움 — 연결된(또는 dry 에선 연결될) 제안 중 대상 칸이 빈 것.
	//   dry 에선 ① 을 쓰지 않았으므로, 연결될 제안도 같은 규칙으로 함께 본다.
	rows, err := pool.Query(ctx, suggestionLinkSQL+`,
live AS (
  SELECT s.entity_id eid, s.locale, s.value, s.basis, s.producer, s.seen_count
    FROM kwave_kdb_suggested_names s
   WHERE s.superseded_at IS NULL AND s.entity_id IS NOT NULL
  UNION ALL
  SELECT m.eid::uuid, s.locale, s.value, s.basis, s.producer, s.seen_count
    FROM kwave_kdb_suggested_names s JOIN m ON m.sid = s.id AND m.n = 1
   WHERE $2 AND s.entity_id IS NULL)
SELECT l.eid::text, e.entity_type::text, l.locale, l.value, l.basis, l.producer, l.seen_count,
       COALESCE(e.canonical_en,'') = '', COALESCE(e.canonical_ja,'') = '', COALESCE(e.canonical_vi,'') = '',
       COALESCE(e.canonical_zh,'') = '', COALESCE(e.canonical_zh_hant,'') = '', COALESCE(e.canonical_es,'') = '',
       COALESCE(e.canonical_id,'') = '', COALESCE(e.canonical_pt_br,'') = '',
       EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
                WHERE d.entity_id = l.eid AND d.reverted_at IS NULL
                  AND replace(lower(d.locale),'-','_') = replace(lower(l.locale),'-','_')
                  AND lower(btrim(d.old_value)) = lower(btrim(l.value)))
  FROM live l JOIN kwave_entities e ON e.id = l.eid
 WHERE e.status = 'active' AND e.operator_locked = false
 ORDER BY l.eid, l.locale`, types, dry)
	if err != nil {
		log.Printf("kdb.suggestion-fill: 선정: %v", err)
		return res
	}
	type cellKey struct{ eid, loc string }
	cands := map[cellKey][]suggestionCand{}
	etypes := map[string]string{}
	var order []cellKey
	for rows.Next() {
		var eid, etype, loc, val, basis, producer string
		var seen int
		var blank [8]bool
		var removed bool
		if rows.Scan(&eid, &etype, &loc, &val, &basis, &producer, &seen,
			&blank[0], &blank[1], &blank[2], &blank[3], &blank[4], &blank[5], &blank[6], &blank[7], &removed) != nil {
			continue
		}
		nl := NormalizeSuggestionLocale(loc)
		if nl == "" {
			res.Skipped["unknown-locale"]++
			continue
		}
		isBlank := map[string]bool{"en": blank[0], "ja": blank[1], "vi": blank[2], "zh": blank[3],
			"zh_hant": blank[4], "es": blank[5], "id": blank[6], "pt_br": blank[7]}[nl]
		if !isBlank {
			continue // 우리 값이 있다 — 제안은 기록으로만 남는다
		}
		k := cellKey{eid, nl}
		if _, seenKey := cands[k]; !seenKey {
			order = append(order, k)
		}
		cands[k] = append(cands[k], suggestionCand{value: val, basis: basis, producer: producer, seen: seen, removed: removed})
		etypes[eid] = etype
	}
	rows.Close()
	run.Scan(len(order))

	for _, k := range order {
		if ctx.Err() != nil || res.Cells >= limit {
			break
		}
		pick, why := pickSuggestion(k.loc, etypes[k.eid], cands[k])
		if pick.value == "" {
			res.Skipped[why]++
			run.Skip(why)
			continue
		}
		if dry {
			res.Cells++
			res.ByLocale[k.loc]++
			continue
		}
		col := suggestionFillLocaleCols[k.loc]
		val := strings.TrimSpace(pick.value)
		tag, err := pool.Exec(ctx, `UPDATE kwave_entities SET `+col+` = $2, `+col+`_source = $3, updated_at = now()
 WHERE id = $1::uuid AND status = 'active' AND operator_locked = false AND COALESCE(`+col+`,'') = ''`,
			k.eid, val, string(SourceConsumerSuggestion))
		if err != nil || tag.RowsAffected() == 0 {
			run.Skip("raced")
			continue
		}
		res.Cells++
		res.ByLocale[k.loc]++
		run.Apply()
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, $2, '', '', 'suggestion-backfill', $3, $4)`,
			k.eid, k.loc, truncRunes("value="+val+" basis="+pick.basis+" seen="+strconv.Itoa(pick.seen), 200),
			truncRunes("consumer:"+pick.producer, 80))
	}
	if res.Cells > 0 || res.Linked > 0 {
		log.Printf("kdb.suggestion-fill: 연결 %d · 동명보류 %d · 채움 %d칸 %v · 건너뜀 %v (dry=%v)",
			res.Linked, res.Ambiguous, res.Cells, res.ByLocale, res.Skipped, dry)
	}
	return res
}
