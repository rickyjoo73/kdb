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
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/websearch"
)

const localFillVotes = 3 // gemma 다회투표 수(만장일치+grounded → local-usage 승급)

var localFillLocales = []string{"en", "ja", "vi", "id", "es", "pt_br", "zh", "zh_hant"}

// localFillTarget — 처리할 (locale, 기존값 교체 여부). replace=true 면 codex-fallback 등
// 하위신뢰 값을 교체하는 고위험 케이스 → gemma 불확실 시 gpt-5.5 교정 에스컬레이트 대상.
type localFillTarget struct {
	loc     string
	replace bool
}

// localFillEntity — 한 엔티티의 메타 + 처리할 locale 목록(targets).
type localFillEntity struct {
	id, ko, etype, role, en string
	works                   []string
	targets                 []localFillTarget // 빈 locale(replace=false)+reground 시 codex-fallback(replace=true)
}

// LocalFillRun — limit 개 엔티티의 빈 locale 을 perEntity 개까지 검색보강한다. dry=true 면
// 검색·추출만 하고 쓰기(POST)는 생략(검증용). reground=true 면 빈칸뿐 아니라
// codex-fallback(LLM 합성·신뢰축 최하위) locale 도 재그라운딩 대상에 넣는다(강한 증거만
// 교체 — applyQAFills/promoteLocalUsage 가드). 반환=쓰기 적용 수(dry 면 추출 성공 수).
func LocalFillRun(ctx context.Context, pool *pgxpool.Pool, limit, perEntity int, dry, reground bool) (int, error) {
	applied, _, err := LocalFillRunWithStats(ctx, pool, limit, perEntity, dry, reground)
	return applied, err
}

// LocalFillRunWithStats is LocalFillRun with an explicit selected count for
// scheduler yield accounting. Historically Hermes recorded items_in=0, hiding
// thousands of zero-yield searches behind a healthy "ok" status.
func LocalFillRunWithStats(ctx context.Context, pool *pgxpool.Pool, limit, perEntity int, dry, reground bool) (applied, selected int, err error) {
	if limit <= 0 {
		limit = 5
	}
	if perEntity <= 0 {
		perEntity = 2
	}
	ents, err := selectLocalFillEntities(ctx, pool, limit, reground)
	if err != nil {
		return 0, 0, err
	}
	return runLocalFillEntities(ctx, pool, ents, perEntity, dry, reground), len(ents), nil
}

// LocalFillRunForName runs LocalFill against one explicit proper noun. It is
// intended for operator probes, so it does not use the autonomous 7-day
// selection cooldown; the same write guards still apply when dry=false.
func LocalFillRunForName(ctx context.Context, pool *pgxpool.Pool, ko string, perEntity int, dry, reground bool) (int, error) {
	if perEntity <= 0 {
		perEntity = 2
	}
	ents, err := selectLocalFillEntityByName(ctx, pool, ko, reground)
	if err != nil {
		return 0, err
	}
	return runLocalFillEntities(ctx, pool, ents, perEntity, dry, reground), nil
}

func runLocalFillEntities(ctx context.Context, pool *pgxpool.Pool, ents []localFillEntity, perEntity int, dry, reground bool) int {
	field := "localfill"
	if reground {
		field = "localfill:rg" // 재그라운딩 전용 쿨다운(빈칸-채움 쿨다운과 독립)
	}
	ex := newLocalFillExtractor()
	// gemma 1차 불확실 시 교정 투입할 gpt-5.5(codex) 에스컬레이터(오너 방침: 평상시 gemma,
	// 안될 때만 codex 최소투입). 교체(replace) 케이스에서만 발동. flag 로 끌 수 있음.
	var esc *agents.Base
	if os.Getenv("KDB_LOCALFILL_ESCALATE") != "0" {
		esc = newLocalFillEscalator()
	}
	applied := 0
	var amu sync.Mutex // applied 보호 (엔티티 fan-out)
	fillEntityOne := func(e localFillEntity) {
		var fills []localFill
		attempted := 0
		anySearched := false // 이 엔티티에서 검색이 한 번이라도 결과를 반환했는가
		for _, t := range e.targets {
			if attempted >= perEntity {
				break
			}
			attempted++
			f, searched, ok := localFillOne(ctx, ex, esc, e, t)
			if searched {
				anySearched = true
			}
			if !ok {
				continue
			}
			fills = append(fills, f...)
			log.Printf("kdb.localfill: %s[%s] → native=%q latin=%q agree=%d/%d grounded=%v",
				e.ko, t.loc, firstNative(f), firstLatin(f), f[0].Agree, f[0].Total, f[0].Grounded)
		}
		if dry {
			amu.Lock()
			applied += len(fills)
			amu.Unlock()
			return
		}
		if !anySearched {
			// 검색 자체가 0건 = 이 쿼리/시점에 결과 없음(대부분 니치어). SearXNG 는
			// limiter off·정상 200 실측(2026-07-21) — 종전 "rate-limit?" 문구는 오해 소지라 정정.
			log.Printf("kdb.localfill: %s 검색결과 0건 — 쿨다운 스킵, 다음 cycle 재시도", e.ko)
			return
		}
		if len(fills) == 0 {
			recordLocalFillAttempt(ctx, pool, e.id, field, "noop")
			return
		}
		n, err := postQAResult(ctx, e.id, e.ko, fills)
		if err != nil {
			log.Printf("kdb.localfill: post %s err=%v", e.ko, err)
			return
		}
		outcome := "noop"
		if n > 0 {
			outcome = "applied"
		}
		recordLocalFillAttempt(ctx, pool, e.id, field, outcome)
		amu.Lock()
		applied += n
		amu.Unlock()
	}
	// KDB_LOCALFILL_FANOUT>1 이면 엔티티 병렬 — websearch 는 전역 throttle(2.5s 직렬)이라
	// 검색 자체는 순차 유지(SearXNG 보호), fan-out 은 한 엔티티의 gemma 투표 대기와 다른
	// 엔티티의 검색 대기를 겹치는 효과만 낸다. 쓰기는 postQAResult→applyQAFills 가드 경유.
	workers := localFillFanout()
	if workers <= 1 || len(ents) <= 1 {
		for _, e := range ents {
			if ctx.Err() != nil {
				break
			}
			fillEntityOne(e)
		}
	} else {
		if workers > len(ents) {
			workers = len(ents)
		}
		jobs := make(chan localFillEntity, len(ents))
		for _, e := range ents {
			jobs <- e
		}
		close(jobs)
		var wg sync.WaitGroup
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for e := range jobs {
					if ctx.Err() != nil {
						return
					}
					fillEntityOne(e)
				}
			}()
		}
		wg.Wait()
	}
	return applied
}

// localFillFanout — 엔티티 병렬 폭 (기본 1 = 현행 직렬, 권장 3).
func localFillFanout() int {
	if v := os.Getenv("KDB_LOCALFILL_FANOUT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 1
}

// selectLocalFillEntities — 검색보강 대상 active 엔티티(소비자요청 우선, 오래된 순).
// reground=false: 빈 외국어 locale 을 가진 엔티티(쿨다운 field='localfill').
// reground=true:  빈칸 OR codex-fallback(LLM 합성) locale 을 가진 엔티티 — QID 없는(=
//
//	FillVerifier 사각지대) 엔티티를 우선 선택, 쿨다운 field='localfill:rg'.
func selectLocalFillEntities(ctx context.Context, pool *pgxpool.Pool, limit int, reground bool) ([]localFillEntity, error) {
	field, where, order := "localfill", "", "(CASE WHEN rq.entity_ko IS NOT NULL THEN 0 ELSE 1 END), e.updated_at ASC"
	emptyPred := `(COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_vi,'')='' OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_es,'')=''
       OR COALESCE(e.canonical_pt_br,'')='' OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')='' OR COALESCE(e.canonical_en,'')='')`
	if reground {
		field = "localfill:rg"
		// 빈칸 또는 codex-fallback/gtranslate(합성·기계번역 최하위 티어) 출처 locale 이 하나라도 있으면 대상.
		cfPred := `(ARRAY[e.canonical_en_source,e.canonical_ja_source,e.canonical_vi_source,
       e.canonical_id_source,e.canonical_es_source,e.canonical_pt_br_source,e.canonical_zh_source,e.canonical_zh_hant_source]
       && ARRAY['codex-fallback','gtranslate'])`
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
  AND e.entity_type NOT IN ('unknown','term')
  AND `+where+`
  AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                   WHERE a.entity_id = e.id AND a.field = '`+field+`'
                     AND (a.last_attempt_at > now() - interval '7 days'
                       OR (a.exhausted AND a.last_attempt_at > now() - interval '30 days')))
ORDER BY `+order+`
LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLocalFillEntityRows(rows, reground)
}

func selectLocalFillEntityByName(ctx context.Context, pool *pgxpool.Pool, ko string, reground bool) ([]localFillEntity, error) {
	ko = strings.TrimSpace(ko)
	if ko == "" {
		return nil, nil
	}
	emptyPred := `(COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_vi,'')='' OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_es,'')=''
       OR COALESCE(e.canonical_pt_br,'')='' OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')='' OR COALESCE(e.canonical_en,'')='')`
	where := emptyPred
	if reground {
		cfPred := `(ARRAY[e.canonical_en_source,e.canonical_ja_source,e.canonical_vi_source,
       e.canonical_id_source,e.canonical_es_source,e.canonical_pt_br_source,e.canonical_zh_source,e.canonical_zh_hant_source]
       && ARRAY['codex-fallback','gtranslate'])`
		where = "(" + emptyPred + " OR " + cfPred + ")"
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
WHERE e.canonical_ko = $1
  AND e.status = 'active' AND e.operator_locked = false
  AND e.entity_type NOT IN ('unknown','term')
  AND `+where+`
ORDER BY e.updated_at ASC
LIMIT 5`, ko)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanLocalFillEntityRows(rows, reground)
}

func scanLocalFillEntityRows(rows pgx.Rows, reground bool) ([]localFillEntity, error) {
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
			if empty {
				e.targets = append(e.targets, localFillTarget{loc: loc, replace: false})
			} else if reground && (srcs[loc] == "codex-fallback" || srcs[loc] == string(SourceGTranslate)) {
				e.targets = append(e.targets, localFillTarget{loc: loc, replace: true}) // 기존값 교체 → 고위험
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

// localFillOne — 한 (엔티티,locale) 검색+gemma 다회투표 추출. native(+latin) fill 반환.
// 교체(t.replace) 케이스에서 gemma 가 불확실하면(만장일치+grounded 아님) gpt-5.5(esc)로
// 교정 에스컬레이트 — 오너 방침(gemma 1차, 안될 때만 codex 최소투입) + 결과 모니터링 로그.
// 반환: (fills, searched, ok). searched=검색이 결과를 반환해 실제 평가했는지(쿨다운 판단용 —
// false 면 SearXNG 다운 등으로 미평가 → 호출측이 쿨다운 스킵).
func localFillOne(ctx context.Context, ex, esc *agents.Base, e localFillEntity, t localFillTarget) ([]localFill, bool, bool) {
	loc := t.loc
	var res []websearch.Result
	var err error
	usedQuery := ""
	sawResults := false
	for _, query := range buildLocalFillQueries(e, loc) {
		res, _, err = websearch.Default().Search(ctx, query, loc, 8)
		if err == nil && len(res) > 0 {
			sawResults = true
			res = filterLocalFillResults(e.ko, e.en, res)
			if len(res) == 0 {
				if localFillDebug() {
					log.Printf("kdb.localfill.debug: %s[%s] query=%q discarded unrelated search results", e.ko, loc, query)
				}
				continue
			}
			usedQuery = query
			break
		}
	}
	if err != nil || len(res) == 0 {
		if sawResults {
			return nil, true, false
		}
		return nil, false, false // 검색 미수행/0건 → 미평가(쿨다운 스킵)
	}
	if localFillDebug() {
		log.Printf("kdb.localfill.debug: %s[%s] query=%q results=%d first=%s", e.ko, loc, usedQuery, len(res), res[0].URL)
	}
	// 검색 텍스트(제목+스니펫) — grounding 확인용.
	var corpus strings.Builder
	hits := make([]string, 0, len(res))
	for _, r := range res {
		line := strings.TrimSpace(r.Title + " — " + r.Snippet + " — " + r.URL)
		hits = append(hits, line)
		corpus.WriteString(line)
		corpus.WriteString("\n")
		if len(hits) <= 3 && shouldFetchLocalFillEvidence(r.URL) {
			if pageText := fetchLocalFillEvidence(ctx, r.URL); pageText != "" {
				hits = append(hits, pageText)
				corpus.WriteString(pageText)
				corpus.WriteString("\n")
			}
		}
	}
	corpusLow := strings.ToLower(corpus.String())

	// gemma 다회투표 — 3표를 병렬 발사(직렬 3×~5s → ~5s). 상한은 gemma 전역 sem.
	votes := map[string]int{} // native_form → 표수
	latinOf := map[string]string{}
	total := 0
	voteErrs := 0
	firstVoteErr := ""
	var vmu sync.Mutex
	var vwg sync.WaitGroup
	for i := 0; i < localFillVotes; i++ {
		vwg.Add(1)
		go func() {
			defer vwg.Done()
			v, err := localFillVote(ctx, ex, e, loc, hits)
			if err != nil {
				vmu.Lock()
				voteErrs++
				if firstVoteErr == "" {
					firstVoteErr = err.Error()
				}
				vmu.Unlock()
				return
			}
			vmu.Lock()
			defer vmu.Unlock()
			total++
			if !v.Found {
				return
			}
			nv := strings.TrimSpace(v.Native)
			if nv == "" {
				return
			}
			votes[nv]++
			if lf := strings.TrimSpace(v.Latin); lf != "" && lf != nv {
				latinOf[nv] = lf
			}
		}()
	}
	vwg.Wait()
	if total == 0 {
		if voteErrs > 0 {
			log.Printf("kdb.localfill: %s[%s] gemma votes failed=%d first=%s", e.ko, loc, voteErrs, firstVoteErr)
		}
		return nil, true, false // 검색은 됐으나 gemma 전부 실패 → 평가함(쿨다운 대상)
	}
	// 최다 득표 native.
	best, agree := "", 0
	for nv, c := range votes {
		if c > agree {
			best, agree = nv, c
		}
	}
	// strong = 만장일치(전 투표 동의) + grounding. (downstream applyQAFills 의 승급 조건과 동일.)
	strong := best != "" && agree >= 2 && agree == total && strings.Contains(corpusLow, strings.ToLower(best))

	// ── gemma 1차 불확실 + 교체(고위험) → gpt-5.5 교정 에스컬레이트(오너 방침·모니터링) ──
	// codex 가 검색결과에 실재(grounded)하는 값을 주면 강한 증거로 승급(교정 성공).
	if !strong && t.replace && esc != nil {
		cand, clatin, cok := localFillEscalate(ctx, esc, e, loc, hits)
		if cok {
			cg := strings.Contains(corpusLow, strings.ToLower(cand))
			log.Printf("kdb.localfill.escalate: %s[%s] gemma(best=%q agree=%d/%d) → codex=%q grounded=%v %s",
				e.ko, loc, best, agree, total, cand, cg, map[bool]string{true: "교정채택", false: "미그라운딩보류"}[cg])
			if cg {
				fills := []localFill{{Locale: loc, Value: cand, Kind: "native", Agree: localFillVotes, Total: localFillVotes, Grounded: true}}
				if clatin != "" && clatin != cand {
					fills = append(fills, localFill{Locale: loc, Value: clatin, Kind: "latin", Agree: localFillVotes,
						Total: localFillVotes, Grounded: strings.Contains(corpusLow, strings.ToLower(clatin))})
				}
				return fills, true, true
			}
		}
	}

	if best == "" || agree < 2 { // gemma 과반 미달(+에스컬레이트 실패/비대상) → 스킵.
		if localFillDebug() {
			log.Printf("kdb.localfill.debug: %s[%s] no majority best=%q agree=%d/%d", e.ko, loc, best, agree, total)
		}
		return nil, true, false
	}
	grounded := strings.Contains(corpusLow, strings.ToLower(best))
	fills := []localFill{{Locale: loc, Value: best, Kind: "native", Agree: agree, Total: total, Grounded: grounded}}
	if lf := latinOf[best]; lf != "" {
		fills = append(fills, localFill{Locale: loc, Value: lf, Kind: "latin", Agree: agree, Total: total,
			Grounded: strings.Contains(corpusLow, strings.ToLower(lf))})
	}
	return fills, true, true
}

func localFillDebug() bool { return os.Getenv("KDB_LOCALFILL_DEBUG") == "1" }

// buildLocalFillQuery — 검색어는 원문 고유명사만 사용한다. 타입/locale 힌트를 붙이면
// 검색엔진이 샘플 문맥을 만들어내고, 잘못된 후보가 실체처럼 보이는 오염이 생긴다.
func buildLocalFillQuery(e localFillEntity, loc string) string {
	queries := buildLocalFillQueries(e, loc)
	if len(queries) == 0 {
		return ""
	}
	return queries[0]
}

// localFillTypeWord — entity_type → 검색 한정어(맨 이름이 일반명사·동음이의라 노이즈만
// 나오는 것을 막는다. 예: "트롤리"→전차, "아몬드"→견과류). 인물/캐릭터/브랜드는 이름만으로
// 충분(직업 다양)해 생략.
func localFillTypeWord(etype string) string {
	switch etype {
	case "drama":
		return "드라마"
	case "movie":
		return "영화"
	case "show":
		return "예능"
	case "song_album":
		return "노래"
	case "group":
		return "그룹"
	case "agency":
		return "엔터테인먼트"
	case "channel_outlet":
		return "방송"
	case "event_tour":
		return "콘서트"
	}
	return ""
}

// buildLocalFillQueries — 검색 쿼리 후보를 강한 한정 → 약한 순으로 생성. 영문명(en)과
// 유형어를 결합해 동음이의 노이즈를 줄이고, 타깃 언어 페이지가 실제로 그 엔티티를 다루게 유도.
// (2026-07-19 수리: 종전엔 맨 한국어 이름뿐이라 일반어 제목은 전부 무관결과로 폐기됐음.)
func buildLocalFillQueries(e localFillEntity, loc string) []string {
	ko := strings.TrimSpace(e.ko)
	if ko == "" {
		return nil
	}
	en := strings.TrimSpace(e.en)
	tw := localFillTypeWord(e.etype)
	var out []string
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q == "" {
			return
		}
		for _, x := range out {
			if x == q {
				return
			}
		}
		out = append(out, q)
	}
	// 1) 이름 + 영문명(강한 동일성 앵커 — 외국어 페이지도 영문명을 병기하는 경우 많음).
	if en != "" && en != ko {
		add(exactSearchTerm(ko) + " " + en)
	}
	// 2) 이름 + 유형어(동음이의 억제).
	if tw != "" {
		add(exactSearchTerm(ko) + " " + tw)
	}
	// 3) 정확 이름 단독.
	add(exactSearchTerm(ko))
	// 4) 맨 이름(최후 폴백).
	add(ko)
	return out
}

func exactSearchTerm(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, `"`) {
		return s
	}
	return `"` + s + `"`
}

// filterLocalFillResults — 무관 결과 제거(정밀도 가드). 관련 신호: 한국어 이름 포함,
// 또는 영문명(en) 포함, 또는 공식 호스트. ★2026-07-19 수리: 종전엔 한국어 이름 포함만
// 인정 → 정작 목표인 외국어 페이지(한글 미포함, 영문명·현지표기만 있음)를 전부 폐기했음.
// en 매칭 추가로 외국어 페이지를 살린다. 최종 값 채택은 하류 gemma 투표+corpus grounding 이 가드.
func filterLocalFillResults(ko, en string, res []websearch.Result) []websearch.Result {
	ko = strings.TrimSpace(ko)
	en = strings.TrimSpace(en)
	if ko == "" {
		return res
	}
	// 영문명이 너무 짧거나(2자 미만) 흔한 노이즈면 앵커로 안 씀.
	useEn := utf8.RuneCountInString(en) >= 3 && en != ko
	enLow := strings.ToLower(en)
	out := make([]websearch.Result, 0, len(res))
	for _, r := range res {
		hay := strings.TrimSpace(r.Title + " " + r.Snippet + " " + r.URL)
		if strings.Contains(hay, ko) || shouldFetchLocalFillEvidence(r.URL) {
			out = append(out, r)
			continue
		}
		if useEn && strings.Contains(strings.ToLower(hay), enLow) {
			out = append(out, r)
		}
	}
	return out
}

var (
	localFillTagRe = regexp.MustCompile(`<[^>]+>`)
	localFillWSRe  = regexp.MustCompile(`\s+`)
)

var officialLocalFillHosts = map[string]struct{}{
	"program.kbs.co.kr":  {},
	"kbsworld.kbs.co.kr": {},
	"kbs.co.kr":          {},
	"yes24livehall.com":  {},
	"cubeent.co.kr":      {},
	"music.apple.com":    {},
	"open.spotify.com":   {},
	"melon.com":          {},
	"genie.co.kr":        {},
	"music.bugs.co.kr":   {},
	"vibe.naver.com":     {},
	"y.qq.com":           {},
	"music.163.com":      {},
	"lollapalooza.com":   {},
}

func parseOfficialLocalFillURL(rawURL string) (*url.URL, bool) {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u == nil || u.Opaque != "" || u.User != nil {
		return nil, false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if strings.HasPrefix(host, "www.") {
		host = strings.TrimPrefix(host, "www.")
	}
	if _, ok := officialLocalFillHosts[host]; !ok {
		return nil, false
	}
	if port := u.Port(); port != "" && port != "80" && port != "443" {
		return nil, false
	}
	return u, true
}

func shouldFetchLocalFillEvidence(rawURL string) bool {
	_, ok := parseOfficialLocalFillURL(rawURL)
	return ok
}

var localFillEvidenceClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 4 {
			return fmt.Errorf("too many evidence redirects")
		}
		if _, ok := parseOfficialLocalFillURL(req.URL.String()); !ok {
			return fmt.Errorf("evidence redirect left official host allowlist")
		}
		return nil
	},
}

func fetchLocalFillEvidence(ctx context.Context, rawURL string) string {
	u, ok := parseOfficialLocalFillURL(rawURL)
	if !ok {
		return ""
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:122.0) Gecko/20100101 Firefox/122.0")
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := localFillEvidenceClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if contentType != "" && !strings.Contains(contentType, "text/html") &&
		!strings.Contains(contentType, "application/xhtml+xml") {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	text := localFillTagRe.ReplaceAllString(string(body), " ")
	text = strings.NewReplacer("&amp;", "&", "&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&nbsp;", " ").Replace(text)
	text = strings.TrimSpace(localFillWSRe.ReplaceAllString(text, " "))
	return summarizeLocalFillEvidence(text)
}

func capRunesLocalFill(s string, n int) string {
	if n <= 0 {
		return ""
	}
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n])
}

func summarizeLocalFillEvidence(text string) string {
	return capRunesLocalFill(text, 1800)
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

// newLocalFillEscalator — gemma 1차 불확실 시 교정 투입할 gpt-5.5(codex) 추출기.
// 오너 방침: codex 최소투입(불확실 교체 케이스만). codex breaker open 시 RoleProvider 가
// 자동으로 gemma 로 인계(자가복구) — 그 경우 효과는 gemma 재시도 수준이나 파이프라인은 안 멈춤.
func newLocalFillEscalator() *agents.Base {
	r := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("LOCALFILL_ESC", "codex")).
		WithEffort(codexcli.RoleEffort("LOCALFILL_ESC", "medium"))
	return agents.NewBase(r, agents.LLMRole{
		Role:   agents.RoleLocalFill,
		Schema: localFillSchema,
		BuildPrompt: func(in any) (string, error) {
			li, ok := in.(localFillInput)
			if !ok {
				return "", fmt.Errorf("localfill-esc: bad input")
			}
			return buildLocalFillPrompt(li), nil
		},
	})
}

// localFillEscalate — codex 1회 추출(교정). native(+latin) 반환. found=false 면 ("","",false).
func localFillEscalate(ctx context.Context, esc *agents.Base, e localFillEntity, loc string, hits []string) (string, string, bool) {
	var res localFillResult
	if err := esc.CallJSON(ctx, localFillInput{
		ko: e.ko, etype: e.etype, role: e.role, loc: loc, works: e.works, hits: hits,
	}, &res); err != nil {
		return "", "", false
	}
	nv := strings.TrimSpace(res.Native)
	if !res.Found || nv == "" {
		return "", "", false
	}
	return nv, strings.TrimSpace(res.Latin), true
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
	b.WriteString("검색결과/공식페이지 발췌(제목 — 스니펫 — URL, 필요 시 본문 일부):\n")
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

// recordLocalFillAttempt records yield as well as cooldown. Three consecutive
// evaluated no-ops back off for 30 days; an applied result resets the streak.
func recordLocalFillAttempt(ctx context.Context, pool *pgxpool.Pool, id, field, outcome string) {
	if outcome != "applied" {
		outcome = "noop"
	}
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts
  (entity_id, field, attempts, exhausted, last_attempt_at, last_source)
VALUES ($1::uuid, $2, CASE WHEN $3='applied' THEN 0 ELSE 1 END,
        false, now(), 'localfill:' || $3)
ON CONFLICT (entity_id, field)
DO UPDATE SET
  attempts = CASE WHEN $3='applied' THEN 0
                  ELSE kwave_kdb_enrich_attempts.attempts + 1 END,
  exhausted = CASE WHEN $3='applied' THEN false
                   ELSE kwave_kdb_enrich_attempts.attempts + 1 >= 3 END,
  last_attempt_at = now(),
  last_source = 'localfill:' || $3`, id, field, outcome)
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

// --- enrich L3.5 인라인 검색-그라운딩 (누수차단) ---------------------------------
//
// 문제: enrich/orchestrator 가 L3(Wikidata) 직후 바로 L4(codex-fallback, 신뢰축 최하위
// LLM 합성)로 빈칸을 메꿔, 신규 codex-fallback 이 source 에서 firehose 로 생산된다. 별도
// 비동기 LocalFill 재그라운딩 캠페인은 항상 뒤를 쫓아 구조적으로 못 따라잡는다(생산≫배수).
// 해결: enrich 가 L4 codex 합성 *전에* 같은 SearXNG+gemma 다회투표 그라운딩을 시도해
// 빈칸을 그라운딩값(강증거→local-usage, 약증거→local-search)으로 채운다 → 신규
// codex-fallback 민팅을 생산지점에서 차단. 검색 무신호(동명이인·무명)는 빈칸 유지.
// flag KDB_ENRICH_GROUND=1 게이트(off 면 완전 no-op, 기존 동작 보존).
//
// strict 모드(KDB_ENRICH_GROUND_STRICT=1, 오너 "빈칸>틀린값" 방침): grounding 이 담당한
// (이번 턴 실행 OR 7d 쿨다운=이미 담당) 엔티티는 L4 codex 합성을 스킵 → 검색 무신호 locale
// 은 codex 추측값 대신 빈칸 유지. → codex-fallback 순감소 확립. 호출측(orchestrator)이
// GroundEntity 의 handled 반환으로 판단.

// EnrichGroundStrict — strict(빈칸>틀린값) 모드 여부. on 이면 grounding 담당 엔티티의
// 잔여 빈칸에 codex-fallback 을 합성하지 않고 빈칸으로 둔다. orchestrator 가 참조.
func EnrichGroundStrict() bool { return os.Getenv("KDB_ENRICH_GROUND_STRICT") == "1" }

// GroundEntity — 단일 엔티티의 빈 locale 을 검색-그라운딩으로 채운다(enrich L3.5 용).
// reground 없음(빈칸만·교체 없음 — 인라인 보수화) + esc=nil(빈칸-채움은 codex 미투입,
// 오너 방침). perEntity 로 한 번에 처리할 locale 수 상한(enrich latency 보호). 쓰기는
// /v1/qa/result(applyQAFills 2단계 가드) 재사용.
// 반환: (채운 수, handled, err). handled=true → grounding 이 이 엔티티를 담당함(이번 턴
// 실행 OR 7d 쿨다운). orchestrator 가 strict 모드면 handled 엔티티의 L4 codex 를 스킵.
// flag off / locked·부재 → handled=false → 호출측이 기존대로 L4 codex.
func GroundEntity(ctx context.Context, pool *pgxpool.Pool, entityID string, perEntity int) (int, bool, error) {
	if pool == nil {
		return 0, false, nil // 방어(테스트 등 nil pool) — handled=false 로 호출측이 기존 폴백
	}
	if os.Getenv("KDB_ENRICH_GROUND") != "1" {
		return 0, false, nil // 기본 off — 완전 no-op(기존 enrich 동작 보존)
	}
	if perEntity <= 0 {
		perEntity = 4
	}
	e, ok, inCooldown, err := loadGroundEntity(ctx, pool, entityID)
	if err != nil || !ok {
		return 0, false, err // locked/부재 → grounding 미담당 → codex 폴백(기존)
	}
	if inCooldown || len(e.targets) == 0 {
		// 7d 내 이미 grounding 담당(잔여 빈칸은 그때 검색 무신호) → 재검색 없이 handled.
		return 0, true, nil
	}
	ex := newLocalFillExtractor()
	var fills []localFill
	done, anySearched := 0, false
	for _, t := range e.targets {
		if done >= perEntity {
			break
		}
		f, searched, ok := localFillOne(ctx, ex, nil, e, t) // esc=nil: 빈칸-채움은 codex 미투입
		if searched {
			anySearched = true
		}
		if !ok {
			continue
		}
		done++
		fills = append(fills, f...)
		log.Printf("kdb.enrichground: %s[%s] → native=%q agree=%d/%d grounded=%v",
			e.ko, t.loc, firstNative(f), f[0].Agree, f[0].Total, f[0].Grounded)
	}
	if len(fills) == 0 {
		if anySearched {
			recordLocalFillAttempt(ctx, pool, e.id, "enrichground", "noop")
		}
		return 0, true, nil // grounding 실행했으나 검색 무신호 → handled(strict 면 codex 스킵)
	}
	n, err := postQAResult(ctx, e.id, e.ko, fills)
	if err == nil {
		outcome := "noop"
		if n > 0 {
			outcome = "applied"
		}
		recordLocalFillAttempt(ctx, pool, e.id, "enrichground", outcome)
	}
	return n, true, err
}

// loadGroundEntity — 단일 active·非locked 엔티티의 빈 locale 타깃 로드 + 'enrichground'
// 쿨다운(7d) 여부. 쿨다운을 WHERE 가 아니라 컬럼으로 반환 — orchestrator 가 "grounding
// 담당(handled)" 판정에 쓰기 위함(쿨다운중이어도 handled=true → strict 면 codex 스킵).
func loadGroundEntity(ctx context.Context, pool *pgxpool.Pool, id string) (localFillEntity, bool, bool, error) {
	var e localFillEntity
	var en, ja, vi, idv, es, pt, zh, zhh string
	var enS, jaS, viS, idS, esS, ptS, zhS, zhhS string
	var inCooldown bool
	err := pool.QueryRow(ctx, `
SELECT e.id::text, e.canonical_ko, e.entity_type::text,
       COALESCE(d.primary_role::text,''), COALESCE(d.notable_works,'{}'), COALESCE(e.canonical_en,''),
       COALESCE(e.canonical_en,''), COALESCE(e.canonical_ja,''), COALESCE(e.canonical_vi,''),
       COALESCE(e.canonical_id,''), COALESCE(e.canonical_es,''), COALESCE(e.canonical_pt_br,''),
       COALESCE(e.canonical_zh,''), COALESCE(e.canonical_zh_hant,''),
       COALESCE(e.canonical_en_source,''), COALESCE(e.canonical_ja_source,''), COALESCE(e.canonical_vi_source,''),
       COALESCE(e.canonical_id_source,''), COALESCE(e.canonical_es_source,''), COALESCE(e.canonical_pt_br_source,''),
       COALESCE(e.canonical_zh_source,''), COALESCE(e.canonical_zh_hant_source,''),
       EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a
                WHERE a.entity_id = e.id AND a.field = 'enrichground'
                  AND (a.last_attempt_at > now() - interval '7 days'
                    OR (a.exhausted AND a.last_attempt_at > now() - interval '30 days')))
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
-- status: active + candidate 둘 다. research/discovery 워커가 candidate 상태로 생성→그
-- 상태로 enrich(codex-fallback 민팅) 후 active 승급하므로, candidate 단계에서 grounding 이
-- handled 되지 않으면 strict 가 codex 를 못 막아 codex-fallback 을 달고 승급한다(제3 누수 경로).
WHERE e.id=$1::uuid AND e.status IN ('active','candidate') AND e.operator_locked = false`, id).
		Scan(&e.id, &e.ko, &e.etype, &e.role, &e.works, &e.en,
			&en, &ja, &vi, &idv, &es, &pt, &zh, &zhh,
			&enS, &jaS, &viS, &idS, &esS, &ptS, &zhS, &zhhS, &inCooldown)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return e, false, false, nil // locked/부재 → grounding 미담당
		}
		return e, false, false, err
	}
	vals := map[string]string{"en": en, "ja": ja, "vi": vi, "id": idv, "es": es, "pt_br": pt, "zh": zh, "zh_hant": zhh}
	srcs := map[string]string{"en": enS, "ja": jaS, "vi": viS, "id": idS, "es": esS, "pt_br": ptS, "zh": zhS, "zh_hant": zhhS}
	for _, loc := range localFillLocales {
		if strings.TrimSpace(vals[loc]) == "" {
			e.targets = append(e.targets, localFillTarget{loc: loc, replace: false})
		} else if srcs[loc] == string(SourceGTranslate) {
			// 기계번역 폴백(enrich L5)이 채운 칸 — 검색그라운드 증거가 있으면 교체(업그레이드).
			e.targets = append(e.targets, localFillTarget{loc: loc, replace: true})
		}
	}
	return e, true, inCooldown, nil
}
