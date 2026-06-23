// Package kdb — LocalFill: on-demand 빈 locale 현지표기 검색보강(KDB 자체 수행).
//
// 설계 docs/KDB_LOCAL_USAGE_DESIGN.md §C 를 server22 워커 없이 KDB 안에서 수행한다:
// 빈 locale 을 가진 엔티티를 골라, 이름+영문+역할+대표작 쿼리로 websearch(SearXNG 메타,
// locale 현지엔진 포함) → 결과 제목/스니펫을 gemma 다회투표(N=3)로 추출(native_form/
// latin_form, 동음이의 차단, grounding) → /v1/qa/result 로 POST(applyQAFills 재사용,
// 2단계: 만장일치+grounded=local-usage 승급, 그외=local-search 빈칸).
//
// 안전: on-demand·소량(websearch 전역 throttle)·dry-run 지원. 쓰기는 전부 기존
// /v1/qa/result 가드(charset·suppress·backstop·operator 보호·revert) 통과. codex 미사용
// (오너 방침: gemma 기본). CLI `kdb-app localfill [n] [--dry]` 로 수동 가동 후 관찰.
package kdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

const localFillVotes = 3 // gemma 다회투표 수(만장일치+grounded → local-usage 승급)

var localFillLocales = []string{"en", "ja", "vi", "id", "es", "pt_br", "zh", "zh_hant"}

// localFillEntity — 한 엔티티의 메타 + 처리할 locale 목록(targets).
type localFillEntity struct {
	id, ko, etype, role, en string
	works                   []string
	targets                 []string // 빈 locale(+reground 시 codex-fallback locale)
}

// LocalFillRun — limit 개 엔티티의 빈 locale 을 perEntity 개까지 검색보강한다. dry=true 면
// 검색·추출만 하고 쓰기(POST)는 생략(검증용). reground=true 면 빈칸뿐 아니라
// codex-fallback(LLM 합성·신뢰축 최하위) locale 도 재그라운딩 대상에 넣는다(강한 증거만
// 교체 — applyQAFills/promoteLocalUsage 가드). 반환=쓰기 적용 수(dry 면 추출 성공 수).
func LocalFillRun(ctx context.Context, pool *pgxpool.Pool, limit, perEntity int, dry, reground bool) (int, error) {
	if limit <= 0 {
		limit = 5
	}
	if perEntity <= 0 {
		perEntity = 2
	}
	ents, err := selectLocalFillEntities(ctx, pool, limit, reground)
	if err != nil {
		return 0, err
	}
	field := "localfill"
	if reground {
		field = "localfill:rg" // 재그라운딩 전용 쿨다운(빈칸-채움 쿨다운과 독립)
	}
	ex := newLocalFillExtractor()
	applied := 0
	for _, e := range ents {
		var fills []localFill
		done := 0
		for _, loc := range e.targets {
			if done >= perEntity {
				break
			}
			f, ok := localFillOne(ctx, ex, e, loc)
			if !ok {
				continue
			}
			done++
			fills = append(fills, f...)
			log.Printf("kdb.localfill: %s[%s] → native=%q latin=%q agree=%d/%d grounded=%v",
				e.ko, loc, firstNative(f), firstLatin(f), f[0].Agree, f[0].Total, f[0].Grounded)
		}
		if dry {
			applied += len(fills)
			continue
		}
		// 처리한 엔티티는 fills 유무와 무관하게 7일 쿨다운 기록 — 채울 수 없는 locale
		// (charset 거부 등)을 매 라운드 재검색하는 낭비 방지(수렴 가드).
		recordLocalFillAttempt(ctx, pool, e.id, field)
		if len(fills) == 0 {
			continue
		}
		n, err := postQAResult(ctx, e.id, e.ko, fills)
		if err != nil {
			log.Printf("kdb.localfill: post %s err=%v", e.ko, err)
			continue
		}
		applied += n
	}
	return applied, nil
}

// selectLocalFillEntities — 검색보강 대상 active 엔티티(소비자요청 우선, 오래된 순).
// reground=false: 빈 외국어 locale 을 가진 엔티티(쿨다운 field='localfill').
// reground=true:  빈칸 OR codex-fallback(LLM 합성) locale 을 가진 엔티티 — QID 없는(=
//                 FillVerifier 사각지대) 엔티티를 우선 선택, 쿨다운 field='localfill:rg'.
func selectLocalFillEntities(ctx context.Context, pool *pgxpool.Pool, limit int, reground bool) ([]localFillEntity, error) {
	field, where, order := "localfill", "", "(CASE WHEN rq.entity_ko IS NOT NULL THEN 0 ELSE 1 END), e.updated_at ASC"
	emptyPred := `(e.canonical_ja='' OR e.canonical_vi='' OR e.canonical_id='' OR e.canonical_es=''
       OR e.canonical_pt_br='' OR e.canonical_zh='' OR e.canonical_zh_hant='' OR e.canonical_en='')`
	if reground {
		field = "localfill:rg"
		// 빈칸 또는 codex-fallback 출처 locale 이 하나라도 있으면 대상.
		cfPred := `('codex-fallback' = ANY(ARRAY[e.canonical_en_source,e.canonical_ja_source,e.canonical_vi_source,
       e.canonical_id_source,e.canonical_es_source,e.canonical_pt_br_source,e.canonical_zh_source,e.canonical_zh_hant_source]))`
		where = "(" + emptyPred + " OR " + cfPred + ")"
		// QID 없는(FillVerifier 가 검증 못 하는) 엔티티 우선 → 소비자요청 → 오래된 순.
		order = `(CASE WHEN rq.entity_ko IS NOT NULL THEN 0 ELSE 1 END),
       (CASE WHEN EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                           WHERE r.entity_id=e.id AND r.provider ILIKE '%wikidata%' AND r.external_id LIKE 'Q%')
             THEN 1 ELSE 0 END),
       e.updated_at ASC`
	} else {
		where = emptyPred
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'), COALESCE(e.canonical_en,''),
       COALESCE(e.canonical_en,''), COALESCE(e.canonical_ja,''), COALESCE(e.canonical_vi,''),
       COALESCE(e.canonical_id,''), COALESCE(e.canonical_es,''), COALESCE(e.canonical_pt_br,''),
       COALESCE(e.canonical_zh,''), COALESCE(e.canonical_zh_hant,''),
       COALESCE(e.canonical_en_source,''), COALESCE(e.canonical_ja_source,''), COALESCE(e.canonical_vi_source,''),
       COALESCE(e.canonical_id_source,''), COALESCE(e.canonical_es_source,''), COALESCE(e.canonical_pt_br_source,''),
       COALESCE(e.canonical_zh_source,''), COALESCE(e.canonical_zh_hant_source,'')
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
LEFT JOIN (SELECT DISTINCT entity_ko FROM kwave_entity_research_queue) rq ON rq.entity_ko = e.canonical_ko
WHERE e.status='active' AND e.operator_locked = false
  AND `+where+`
  AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                   WHERE a.entity_id = e.id AND a.field = '`+field+`'
                     AND a.last_attempt_at > now() - interval '7 days')
ORDER BY `+order+`
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []localFillEntity
	for rows.Next() {
		var e localFillEntity
		var en, ja, vi, id, es, pt, zh, zhh string
		var enS, jaS, viS, idS, esS, ptS, zhS, zhhS string
		if err := rows.Scan(&e.id, &e.ko, &e.etype, &e.role, &e.works, &e.en,
			&en, &ja, &vi, &id, &es, &pt, &zh, &zhh,
			&enS, &jaS, &viS, &idS, &esS, &ptS, &zhS, &zhhS); err != nil {
			return nil, err
		}
		vals := map[string]string{"en": en, "ja": ja, "vi": vi, "id": id, "es": es, "pt_br": pt, "zh": zh, "zh_hant": zhh}
		srcs := map[string]string{"en": enS, "ja": jaS, "vi": viS, "id": idS, "es": esS, "pt_br": ptS, "zh": zhS, "zh_hant": zhhS}
		for _, loc := range localFillLocales {
			empty := strings.TrimSpace(vals[loc]) == ""
			if empty || (reground && srcs[loc] == "codex-fallback") {
				e.targets = append(e.targets, loc)
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// localFill — POST 용 fill(qa.go QAFill 와 동일 형태).
type localFill struct {
	Locale   string `json:"locale"`
	Value    string `json:"value"`
	Kind     string `json:"kind"` // native|latin
	Agree    int    `json:"agree"`
	Total    int    `json:"total"`
	Grounded bool   `json:"grounded"`
}

// localFillOne — 한 (엔티티,locale) 검색+다회투표 추출. native(+latin) fill 반환.
func localFillOne(ctx context.Context, ex *agents.Base, e localFillEntity, loc string) ([]localFill, bool) {
	query := buildLocalFillQuery(e)
	res, _, err := websearch.Default().Search(ctx, query, loc, 8)
	if err != nil || len(res) == 0 {
		return nil, false
	}
	// 검색 텍스트(제목+스니펫) — grounding 확인용.
	var corpus strings.Builder
	hits := make([]string, 0, len(res))
	for _, r := range res {
		line := strings.TrimSpace(r.Title + " — " + r.Snippet)
		hits = append(hits, line)
		corpus.WriteString(line)
		corpus.WriteString("\n")
	}
	corpusLow := strings.ToLower(corpus.String())

	// gemma 다회투표.
	votes := map[string]int{} // native_form → 표수
	latinOf := map[string]string{}
	total := 0
	for i := 0; i < localFillVotes; i++ {
		v, err := localFillVote(ctx, ex, e, loc, hits)
		if err != nil {
			continue
		}
		total++
		if !v.Found {
			continue
		}
		nv := strings.TrimSpace(v.Native)
		if nv == "" {
			continue
		}
		votes[nv]++
		if lf := strings.TrimSpace(v.Latin); lf != "" && lf != nv {
			latinOf[nv] = lf
		}
	}
	if total == 0 {
		return nil, false
	}
	// 최다 득표 native.
	best, agree := "", 0
	for nv, c := range votes {
		if c > agree {
			best, agree = nv, c
		}
	}
	if best == "" || agree < 2 { // 과반 미달 → 신뢰 부족, 스킵.
		return nil, false
	}
	grounded := strings.Contains(corpusLow, strings.ToLower(best))
	fills := []localFill{{Locale: loc, Value: best, Kind: "native", Agree: agree, Total: total, Grounded: grounded}}
	if lf := latinOf[best]; lf != "" {
		fills = append(fills, localFill{Locale: loc, Value: lf, Kind: "latin", Agree: agree, Total: total,
			Grounded: strings.Contains(corpusLow, strings.ToLower(lf))})
	}
	return fills, true
}

// buildLocalFillQuery — 이름+영문+역할+대표작(동음이의 차단). 설계 §C.
func buildLocalFillQuery(e localFillEntity) string {
	parts := []string{e.ko}
	if e.en != "" {
		parts = append(parts, e.en)
	}
	if e.role != "" {
		parts = append(parts, e.role)
	} else if e.etype != "" && e.etype != "unknown" {
		parts = append(parts, e.etype)
	}
	if len(e.works) > 0 && strings.TrimSpace(e.works[0]) != "" {
		parts = append(parts, e.works[0])
	}
	return strings.Join(parts, " ")
}

// --- gemma 추출 -------------------------------------------------------------

type localFillInput struct {
	ko, etype, role, loc string
	works                []string
	hits                 []string
}

type localFillResult struct {
	Found  bool   `json:"found"`
	Native string `json:"native_form"`
	Latin  string `json:"latin_form"`
}

func newLocalFillExtractor() *agents.Base {
	r := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("LOCALFILL", "gemma")).
		WithEffort(codexcli.RoleEffort("LOCALFILL", "low"))
	return agents.NewBase(r, agents.LLMRole{
		Role:   agents.RoleLocalFill,
		Schema: localFillSchema,
		BuildPrompt: func(in any) (string, error) {
			li, ok := in.(localFillInput)
			if !ok {
				return "", fmt.Errorf("localfill: bad input")
			}
			return buildLocalFillPrompt(li), nil
		},
	})
}

func localFillVote(ctx context.Context, ex *agents.Base, e localFillEntity, loc string, hits []string) (localFillResult, error) {
	var res localFillResult
	err := ex.CallJSON(ctx, localFillInput{
		ko: e.ko, etype: e.etype, role: e.role, loc: loc, works: e.works, hits: hits,
	}, &res)
	return res, err
}

var localFillSchema = []byte(`{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "found": {"type": "boolean"},
    "native_form": {"type": "string"},
    "latin_form": {"type": "string"}
  },
  "required": ["found"]
}`)

func buildLocalFillPrompt(li localFillInput) string {
	var b strings.Builder
	b.WriteString("당신은 한국 대중문화(K-콘텐츠) 고유명사의 현지 통용표기 추출기입니다.\n")
	b.WriteString("아래 한국 엔티티가 '" + li.loc + "' 언어권에서 실제로 쓰이는 현지 표기를 검색결과에서 찾으세요.\n\n")
	b.WriteString("한국어 정식명: " + li.ko + "\n")
	b.WriteString("종류: " + li.etype + "\n")
	if li.role != "" {
		b.WriteString("역할/직업: " + li.role + "\n")
	}
	if len(li.works) > 0 {
		b.WriteString("대표작/소속: " + strings.Join(li.works, ", ") + "\n")
	}
	b.WriteString("목표 locale: " + li.loc + "\n\n")
	b.WriteString("검색결과(제목 — 스니펫):\n")
	for i, h := range li.hits {
		if i >= 8 {
			break
		}
		b.WriteString("- " + h + "\n")
	}
	b.WriteString("\n규칙(엄격):\n")
	b.WriteString("1. 동음이의 차단: 검색결과가 진짜 이 한국 " + li.etype + "(위 대표작/역할)에 관한 것인지 먼저 판단. 무관/동음이의면 found=false.\n")
	b.WriteString("2. grounding: 검색결과에 '글자 그대로' 등장하는 표기만. 지어내기·번역·음역 금지. 없으면 found=false.\n")
	b.WriteString("3. native_form = 그 언어권에서 가장 자주 등장한 현지 문자 표기(ja=가나/한자, zh/zh_hant=한자, vi/id/es/pt_br/en=라틴). 한국어(한글)는 금지.\n")
	b.WriteString("4. latin_form = 라틴 표기가 native 와 다르면 그 값(같으면 빈 문자열).\n")
	b.WriteString("JSON 한 개만 출력: {\"found\":true|false,\"native_form\":\"...\",\"latin_form\":\"...\"}\n")
	return b.String()
}

// --- /v1/qa/result POST (applyQAFills 재사용) -------------------------------

var localFillClient = &http.Client{Timeout: 20 * time.Second}

// postQAResult — fills 를 자기 자신 /v1/qa/result 로 POST(write key). 반환=적용 수.
func postQAResult(ctx context.Context, entityID, ko string, fills []localFill) (int, error) {
	key := firstAPIKey()
	if key == "" {
		return 0, fmt.Errorf("no write API key (KDB_API_KEYS)")
	}
	port := os.Getenv("KDB_API_PORT")
	if port == "" {
		port = "9100"
	}
	body, _ := json.Marshal(map[string]any{
		"entity_id": entityID, "ko": ko, "ko_verdict": "real_k", "fills": fills,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://127.0.0.1:"+port+"/v1/qa/result", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-KDB-Key", key)
	resp, err := localFillClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("qa/result status %d", resp.StatusCode)
	}
	var out struct {
		Action string `json:"action"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	// action="kept+filled(N)" → N, "kept" → 0.
	if i := strings.Index(out.Action, "filled("); i >= 0 {
		var n int
		_, _ = fmt.Sscanf(out.Action[i:], "filled(%d)", &n)
		return n, nil
	}
	return 0, nil
}

// recordLocalFillAttempt — (entity, field) 쿨다운 기록(7일). 채움 실패도 기록.
// field='localfill'(빈칸-채움) 또는 'localfill:rg'(codex-fallback 재그라운딩).
func recordLocalFillAttempt(ctx context.Context, pool *pgxpool.Pool, id, field string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1::uuid, $2, 1, now(), 'localfill')
ON CONFLICT (entity_id, field)
DO UPDATE SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now()`, id, field)
}

func firstAPIKey() string {
	for _, k := range strings.Split(os.Getenv("KDB_API_KEYS"), ",") {
		if k = strings.TrimSpace(k); k != "" {
			return k
		}
	}
	return ""
}

func firstNative(f []localFill) string {
	for _, x := range f {
		if x.Kind == "native" {
			return x.Value
		}
	}
	return ""
}

func firstLatin(f []localFill) string {
	for _, x := range f {
		if x.Kind == "latin" {
			return x.Value
		}
	}
	return ""
}
