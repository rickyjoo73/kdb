package kdb

// suggestion_fill — 소비자가 prepare 에 실어 보낸 제안 표기(직역·음역)를 **우리가 못 채운 칸**에 쓴다.
//
// ★계기 (2026-09-30, 오너).
//   "prepare 에 직역해서 넘어 온 것이 많은데 그것을 사용을 안 하는가? 우리가 해결 못 하는
//   것은 우선 그것을 활용해야 하지 않을까?"
//   "우리가 빈자리라면 gpt-6 sol 이 문맥에 맞게 직역한 거라 우리가 직역한 것보다 더 효율적
//   … 어차피 llm 번역이라면 제안에서 올라온 것이 더 좋을 거야. 우리가 찾다가 못 찾으면
//   우선 사용하는 것을 봐야지."
//
// ★그래서 순서는 이렇다.
//   근거 있는 출처(1~7: 운영자·매체·권위 API·위키·검색) → **소비자 제안(8)** →
//   우리 기계값(9: codex-fallback·gtranslate·kana-rule) → 우리 잠정(10: llm-provisional)
//   제안은 기사 원문을 보고 만든 값이고, 우리 기계값은 이름 하나만 보고 만든 값이다.
//
// ★종전(09-24)의 두 구멍.
//   ① 제안은 **요청 한 번 안에서**, 대상이 이미 active 이고 칸이 빌 때만 쓰였다. 답하지
//      못한 이름이 바로 그 조건 밖이다 — 원장에 없거나 candidate 라 entity_id NULL 로 적히기만
//      했고, 뒤에 등록돼도 다시 보는 경로가 없었다.
//   ② 등급이 잠정과 같은 최하위라 우리 LLM 이 먼저 칸을 차지하면 제안은 못 들어갔다.
//
// ★한 함수(ApplySuggestionsToEntity)를 네 자리가 같이 쓴다 — 사본을 두면 갈라진다.
//     prepare 즉시 채움 · enricher L4 직전 · orchestrator L4 직전 · 요청 대상 CJK 채움 직전
//   그리고 이 레인(suggestion-fill)이 30분마다 연결과 뒷정리를 한다.
//
// ★가드.
//   - 대상에 붙이는 것은 **같은 이름의 활성 대상이 하나뿐일 때만**(동명 함정 — 채영 TWICE/CLC).
//     소비자가 구체 유형을 보냈으면 대상 유형과 맞아야 한다(«좋은 날» 곡 제안 ≠ 동명 드라마).
//   - 인명·그룹명(person·group)의 «직역(literal)» 은 쓰지 않는다. 이름은 음역한다.
//   - 우리가 **일부러 지운 값**(dataqa_log old_value, 미복원)은 제안으로 되살리지 않는다.
//   - 문자셋(validLocaleValue) — 간체 칸의 번체, 번체 칸의 간체, 한글 잔존.
//   - 운영자 잠금·근거 있는 출처의 칸은 건드리지 않는다.
//   - 쓴 칸은 전부 dataqa_log(verdict='suggestion-backfill')에 **옛 값·옛 출처와 함께** 남는다
//     — 한 라벨로 전량 되돌릴 수 있다.
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

// suggestionFillLocaleCols — 정규화된 로케일 → 표기 칸.
var suggestionFillLocaleCols = map[string]string{
	"en": "canonical_en", "ja": "canonical_ja", "vi": "canonical_vi",
	"zh": "canonical_zh", "zh_hant": "canonical_zh_hant",
	"es": "canonical_es", "id": "canonical_id", "pt_br": "canonical_pt_br",
}

// suggestionFillLocaleOrder — 칸을 도는 순서를 고정한다(로그·시험이 흔들리지 않게).
var suggestionFillLocaleOrder = []string{"en", "ja", "vi", "zh", "zh_hant", "es", "id", "pt_br"}

// suggestionLocaleAliases — 소비자가 보내는 로케일 키의 변형 → 우리 칸.
var suggestionLocaleAliases = map[string]string{
	"zh_hans": "zh", "zh_cn": "zh", "zh_sg": "zh",
	"zh_tw": "zh_hant", "zh_hk": "zh_hant", "zh_mo": "zh_hant",
	"pt": "pt_br",
}

// NormalizeSuggestionLocale — 소비자가 보내는 로케일 키를 우리 칸 이름으로.
// zh-hant·zh_hant·zh-TW 는 번체, zh-Hans·zh-CN 은 간체, pt-BR 은 pt_br. 모르는 키는 "".
func NormalizeSuggestionLocale(loc string) string {
	l := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(loc)), "-", "_")
	if a, ok := suggestionLocaleAliases[l]; ok {
		l = a
	}
	if _, ok := suggestionFillLocaleCols[l]; ok {
		return l
	}
	return ""
}

// suggestionCellSQL — 제안 행 s 의 로케일에 해당하는 대상 e 의 칸(값 또는 출처) 식.
// NormalizeSuggestionLocale 과 같은 표로 만든다 — SQL 에 따로 적으면 갈라진다.
func suggestionCellSQL(source bool) string {
	keys := map[string][]string{}
	for _, loc := range suggestionFillLocaleOrder {
		keys[loc] = []string{loc}
	}
	for alias, loc := range suggestionLocaleAliases {
		keys[loc] = append(keys[loc], alias)
	}
	// ★검색형 CASE(`CASE WHEN x IN (…)`)로 쓴다. 단순형 `CASE x WHEN 'a','b'` 는 Postgres 가
	//   받지 않는다 — 처음 그렇게 썼다가 격리 DB 에서 문법 오류로 잡혔다(레인이 매 회차 0건).
	const key = "replace(lower(btrim(s.locale)),'-','_')"
	var b strings.Builder
	b.WriteString("(CASE")
	for _, loc := range suggestionFillLocaleOrder {
		ks := keys[loc]
		sort.Strings(ks)
		col := "e." + suggestionFillLocaleCols[loc]
		if source {
			col += "_source"
		}
		b.WriteString(" WHEN " + key + " IN ('" + strings.Join(ks, "','") + "')")
		b.WriteString(" THEN COALESCE(" + col + ",'')")
	}
	b.WriteString(" END)")
	return b.String()
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

// suggestionReplaceable — 이 칸을 제안으로 채워도 되는가: 비었거나, 제안보다 약한 우리 기계값.
func suggestionReplaceable(curVal, curSrc string) bool {
	if strings.TrimSpace(curVal) == "" {
		return true
	}
	return isWeakerThan(curSrc, SourceConsumerSuggestion)
}

type suggestionCand struct {
	id                     int64
	value, basis, producer string
	seen                   int
	removed                bool // 우리가 일부러 지운 적 있는 값
	unlinked               bool // 아직 entity_id 가 없다 — 쓰면 이 대상에 붙인다
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

// SuggestionApply — 한 대상에 제안을 적용한 결과.
type SuggestionApply struct {
	Written  map[string]string // 로케일 → 쓴 값 (dry 면 쓸 값)
	Replaced int               // 그중 우리 기계값을 덮은 칸
	Linked   int               // 이 대상에 새로 붙인 제안
	Skipped  map[string]int    // 안 쓴 칸의 사유
}

// ApplySuggestionsToEntity — 한 대상의 **비었거나 우리 기계값인 칸**을 소비자 제안으로 채운다.
//
// 근거 있는 출처를 다 찾은 뒤, 우리 LLM 을 부르기 **전에** 부른다. 채운 칸은 호출자가
// 다시 LLM 에 묻지 않는다.
func ApplySuggestionsToEntity(ctx context.Context, pool *pgxpool.Pool, entityID string, dry bool) SuggestionApply {
	return applySuggestionsToEntity(ctx, pool, entityID, nil, true, dry)
}

// applySuggestionsToEntity — extra: dry 실행에서 «연결될» 제안 id(아직 entity_id NULL)도 함께 본다.
// replace=false 면 빈 칸만 채운다(레인이 회차당 교체 상한에 닿았을 때).
func applySuggestionsToEntity(ctx context.Context, pool *pgxpool.Pool, entityID string, extra []int64, replace, dry bool) SuggestionApply {
	out := SuggestionApply{Written: map[string]string{}, Skipped: map[string]int{}}
	if pool == nil || entityID == "" {
		return out
	}
	// 대상의 칸 상태.
	var etype, ko string
	var vals [16]string
	if err := pool.QueryRow(ctx, `
SELECT entity_type::text, canonical_ko,
       COALESCE(canonical_en,''), COALESCE(canonical_en_source,''), COALESCE(canonical_ja,''), COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''), COALESCE(canonical_vi_source,''), COALESCE(canonical_zh,''), COALESCE(canonical_zh_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,''), COALESCE(canonical_es,''), COALESCE(canonical_es_source,''),
       COALESCE(canonical_id,''), COALESCE(canonical_id_source,''), COALESCE(canonical_pt_br,''), COALESCE(canonical_pt_br_source,'')
  FROM kwave_entities WHERE id = $1::uuid AND status = 'active' AND operator_locked = false`, entityID).Scan(&etype, &ko,
		&vals[0], &vals[1], &vals[2], &vals[3], &vals[4], &vals[5], &vals[6], &vals[7],
		&vals[8], &vals[9], &vals[10], &vals[11], &vals[12], &vals[13], &vals[14], &vals[15]); err != nil {
		return out // 없거나 활성 아님·잠금 — 할 일 없다
	}
	cur := map[string][2]string{}
	for i, loc := range suggestionFillLocaleOrder {
		cur[loc] = [2]string{vals[2*i], vals[2*i+1]}
	}

	// 이 대상의 제안: 붙어 있는 것 + (아직 안 붙었고) 이름·유형이 맞고 같은 이름 활성 대상이
	// 이것 하나뿐인 것. 유형을 모르는 옛 행(term_type='')은 레인의 연결 단계가 요청 기록과
	// 대조해 붙인다 — 여기서 이름만으로 붙이지 않는다.
	if extra == nil {
		extra = []int64{}
	}
	rows, err := pool.Query(ctx, `
SELECT s.id, s.locale, s.value, s.basis, s.producer, s.seen_count, s.entity_id IS NULL,
       EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
                WHERE d.entity_id = e.id AND d.reverted_at IS NULL AND d.verdict <> 'suggestion-backfill'
                  AND replace(lower(d.locale),'-','_') = replace(lower(s.locale),'-','_')
                  AND lower(btrim(d.old_value)) = lower(btrim(s.value)))
  FROM kwave_entities e
  JOIN kwave_kdb_suggested_names s ON s.superseded_at IS NULL AND (
         s.entity_id = e.id
      OR s.id = ANY($2::bigint[])
      OR (s.entity_id IS NULL AND s.term_type = e.entity_type::text
          AND (s.term_ko = e.canonical_ko OR s.term_ko = ANY(e.aliases_ko))
          AND NOT EXISTS (SELECT 1 FROM kwave_entities o
                           WHERE o.status = 'active' AND o.id <> e.id
                             AND (o.canonical_ko = s.term_ko OR o.aliases_ko @> ARRAY[s.term_ko]))))
 WHERE e.id = $1::uuid`, entityID, extra)
	if err != nil {
		log.Printf("kdb.suggestion: %s 제안 조회: %v", entityID, err)
		return out
	}
	cands := map[string][]suggestionCand{}
	var toLink []int64
	for rows.Next() {
		var c suggestionCand
		var loc string
		if rows.Scan(&c.id, &loc, &c.value, &c.basis, &c.producer, &c.seen, &c.unlinked, &c.removed) != nil {
			continue
		}
		if c.unlinked {
			toLink = append(toLink, c.id)
		}
		nl := NormalizeSuggestionLocale(loc)
		if nl == "" {
			out.Skipped["unknown-locale"]++
			continue
		}
		cands[nl] = append(cands[nl], c)
	}
	rows.Close()

	weaker := MachineFilledSourcesWeakerThan(SourceConsumerSuggestion)
	for _, loc := range suggestionFillLocaleOrder {
		cs := cands[loc]
		if len(cs) == 0 {
			continue
		}
		cv, csrc := cur[loc][0], cur[loc][1]
		if !suggestionReplaceable(cv, csrc) {
			continue // 근거 있는 값이 있다 — 제안은 기록으로만 남는다
		}
		if cv != "" && !replace {
			out.Skipped["replace-cap"]++
			continue
		}
		pick, why := pickSuggestion(loc, etype, cs)
		if pick.value == "" {
			out.Skipped[why]++
			continue
		}
		val := strings.TrimSpace(pick.value)
		if strings.EqualFold(val, strings.TrimSpace(cv)) {
			continue // 이미 같은 값 — 이름표만 바꾸는 쓰기는 하지 않는다
		}
		if dry {
			out.Written[loc] = val
			if cv != "" {
				out.Replaced++
				log.Printf("kdb.suggestion: [dry] 교체 %s %s %q(%s) → %q (%s, %s)", ko, loc, cv, csrc, val, pick.basis, pick.producer)
			}
			continue
		}
		col := suggestionFillLocaleCols[loc]
		var oldV, oldS string
		err := pool.QueryRow(ctx, `
WITH old AS (
  SELECT id, COALESCE(`+col+`,'') v, COALESCE(`+col+`_source,'') s FROM kwave_entities
   WHERE id = $1::uuid AND status = 'active' AND operator_locked = false
     AND (COALESCE(`+col+`,'') = '' OR COALESCE(`+col+`_source,'') = ANY($4::text[]))
   FOR UPDATE)
UPDATE kwave_entities e SET `+col+` = $2, `+col+`_source = $3, updated_at = now()
  FROM old WHERE e.id = old.id
RETURNING old.v, old.s`, entityID, val, string(SourceConsumerSuggestion), weaker).Scan(&oldV, &oldS)
		if err != nil {
			out.Skipped["raced"]++ // 그 사이 근거 있는 값이 들어왔거나 잠겼다
			continue
		}
		out.Written[loc] = val
		if oldV != "" {
			out.Replaced++
			// 교체는 눈으로 읽을 수 있게 한 줄씩 남긴다(레인은 회차당 상한이 있다).
			log.Printf("kdb.suggestion: 교체 %s %s %q(%s) → %q (%s, %s)", ko, loc, oldV, oldS, val, pick.basis, pick.producer)
		}
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
VALUES ($1::uuid, $2, $3, $4, 'suggestion-backfill', $5, $6)`,
			entityID, loc, oldV, oldS,
			truncRunes("value="+val+" basis="+pick.basis+" seen="+strconv.Itoa(pick.seen), 200),
			truncRunes("consumer:"+pick.producer, 80))
	}
	if !dry && len(toLink) > 0 {
		if tag, err := pool.Exec(ctx, `UPDATE kwave_kdb_suggested_names SET entity_id = $1::uuid
 WHERE id = ANY($2::bigint[]) AND entity_id IS NULL`, entityID, toLink); err == nil {
			out.Linked = int(tag.RowsAffected())
		}
	}
	return out
}

// SuggestionFillResult — 레인 한 회차의 결과.
type SuggestionFillResult struct {
	Linked    int            // 대상에 새로 붙인 제안
	Ambiguous int            // 같은 이름 활성 대상이 둘 이상이라 안 붙인 제안
	Entities  int            // 제안을 적용해 본 대상
	Cells     int            // 채운 칸
	Replaced  int            // 그중 우리 기계값을 덮은 칸
	ByLocale  map[string]int // 채운 칸의 로케일별 수
	Skipped   map[string]int // 안 쓴 칸의 사유
}

// SuggestionFillEnabled — 기본 켜짐. KDB_SUGGESTION_FILL_ENABLED=0 이면 끈다.
func SuggestionFillEnabled() bool {
	return strings.TrimSpace(os.Getenv("KDB_SUGGESTION_FILL_ENABLED")) != "0"
}

// suggestionReplaceBatch — 한 회차에 **이미 값이 있던 칸**(우리 기계값)을 제안으로 바꾸는 상한
// (기본 100). KDB_SUGGESTION_REPLACE_BATCH.
//
// ★왜 따로 두나 (2026-09-30). 빈 칸 채우기는 잃는 것이 없지만 교체는 있던 값을 바꾼다.
// 운영 DB 에 제안이 몇 건인지 재 보지 못한 채 켜므로, 첫 회차부터 수천 칸을 한꺼번에
// 바꾸지 않게 한다 — 교체마다 로그에 옛 값→새 값이 남고, dataqa_log 로 되돌릴 수 있다.
// 사람 승인 단계를 만들지 않는 대신(오너 원칙 6) 속도를 제한한다.
func suggestionReplaceBatch() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KDB_SUGGESTION_REPLACE_BATCH"))); err == nil && v >= 0 {
		return v
	}
	return 100
}

// suggestionFillBatch — 한 회차에 채울 칸 상한(기본 500). KDB_SUGGESTION_FILL_BATCH.
func suggestionFillBatch() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KDB_SUGGESTION_FILL_BATCH"))); err == nil && v > 0 {
		return v
	}
	return 500
}

// suggestionLinkSQL — 연결 후보. 같은 이름(정식명·별칭)의 활성 대상 수를 센다.
// 유형: 제안 행에 소비자 유형(term_type)이 있으면 그것과, 없으면(옛 행) 요청 기록의 유형과
// 맞아야 한다. 우리 유형 목록($1) 밖의 값(term·unknown·오타)은 «모름»으로 읽는다.
const suggestionLinkSQL = `
WITH s0 AS (
  SELECT id, term_ko, CASE WHEN term_type = ANY($1::text[]) THEN term_type ELSE '' END term_type
    FROM kwave_kdb_suggested_names
   WHERE entity_id IS NULL AND superseded_at IS NULL),
rt AS (
  SELECT term_ko, array_agg(DISTINCT term_type) types
    FROM kwave_kdb_request_terms
   WHERE term_ko IN (SELECT term_ko FROM s0 WHERE term_type = '') AND term_type = ANY($1::text[])
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
   WHERE CASE WHEN s0.term_type <> '' THEN hit.etype = s0.term_type
              ELSE rt.types IS NULL OR hit.etype = ANY(rt.types) END
   GROUP BY hit.sid)`

// DrainSuggestionFill — 제안 연결 + 대상별 적용. limit<=0 이면 기본 상한(칸 수).
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
	types := AssignableEntityTypes()

	// ① 연결 — dry 면 세기만 하고, 연결될 제안 id 를 대상별로 모아 ② 에 넘긴다.
	extra := map[string][]int64{}
	if dry {
		rows, err := pool.Query(ctx, suggestionLinkSQL+`
SELECT sid, eid, n FROM m`, types)
		if err != nil {
			log.Printf("kdb.suggestion-fill: 연결 집계: %v", err)
		} else {
			for rows.Next() {
				var sid int64
				var eid string
				var n int
				if rows.Scan(&sid, &eid, &n) != nil {
					continue
				}
				if n == 1 {
					res.Linked++
					extra[eid] = append(extra[eid], sid)
				} else {
					res.Ambiguous++
				}
			}
			rows.Close()
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

	// ② 적용할 대상 — 붙어 있는 제안 중 그 칸이 비었거나 우리 기계값인 것. 최근 요청 순.
	rows, err := pool.Query(ctx, `
SELECT s.entity_id::text
  FROM kwave_kdb_suggested_names s JOIN kwave_entities e ON e.id = s.entity_id
 WHERE s.superseded_at IS NULL AND e.status = 'active' AND e.operator_locked = false
   AND (`+suggestionCellSQL(false)+` = '' OR `+suggestionCellSQL(true)+` = ANY($1::text[]))
 GROUP BY s.entity_id
 ORDER BY max(s.last_seen_at) DESC`, MachineFilledSourcesWeakerThan(SourceConsumerSuggestion))
	if err != nil {
		log.Printf("kdb.suggestion-fill: 선정: %v", err)
		return res
	}
	var ids []string
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil && !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	rows.Close()
	extraIDs := make([]string, 0, len(extra))
	for eid := range extra {
		extraIDs = append(extraIDs, eid)
	}
	sort.Strings(extraIDs)
	for _, eid := range extraIDs { // dry: 아직 안 붙은 제안만 가진 대상
		if !seen[eid] {
			ids = append(ids, eid)
			seen[eid] = true
		}
	}
	run.Scan(len(ids))

	replaceCap := suggestionReplaceBatch()
	for _, id := range ids {
		if ctx.Err() != nil || res.Cells >= limit {
			break
		}
		a := applySuggestionsToEntity(ctx, pool, id, extra[id], res.Replaced < replaceCap, dry)
		res.Entities++
		res.Replaced += a.Replaced
		res.Linked += a.Linked
		for loc := range a.Written {
			res.Cells++
			res.ByLocale[loc]++
			run.Apply()
		}
		for why, n := range a.Skipped {
			res.Skipped[why] += n
			for i := 0; i < n; i++ {
				run.Skip(why)
			}
		}
		if len(a.Written) == 0 && len(a.Skipped) == 0 {
			run.Skip("nothing-to-write")
		}
	}
	if res.Cells > 0 || res.Linked > 0 || dry {
		log.Printf("kdb.suggestion-fill: 연결 %d · 동명보류 %d · 대상 %d · 채움 %d칸(기계값 교체 %d) %v · 건너뜀 %v (dry=%v)",
			res.Linked, res.Ambiguous, res.Entities, res.Cells, res.Replaced, res.ByLocale, res.Skipped, dry)
	}
	return res
}
