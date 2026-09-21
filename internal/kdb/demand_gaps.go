package kdb

// demand_gaps — **지금 못 답하는 수요를 시스템이 스스로 잰다.**
//
// ★성장 고리에서 이 자리 (2026-09-20):
//
//	요청 로그 → [결핍 진단] → 공급원 선택 → 레인 실행 → 수확 측정(0151) → 다음 대상
//	              ↑ 여기를 사람이 매 회차 SQL 로 팠다
//
// ★두 결핍을 **절대 섞지 않는다.** 처방이 다르기 때문이다.
//
//	unmet       요청이 왔는데 그 대상을 아예 모른다  → 발굴·공급원을 늘려야 한다
//	locale_gap  대상은 아는데 그 언어 표기가 없다    → 표기 레인을 고쳐야 한다
//
//	「해소율 65%」 하나로 말하면 무엇을 하라는 뜻인지 알 수 없다. 실제로 그래서
//	6회차에 위키데이터 레인을 넓혔다가 수확 0/50 을 얻었다 — 그 유형의 결핍은
//	unmet 이었는데 표기 레인을 건드렸기 때문이다.
//
// ★이름 대조는 서빙 경로와 같은 정규화를 쓴다(gatekeeper.NormalizedKey 와 같은 규칙:
//	소문자·공백·부호 제거). 다른 규칙을 쓰면 결핍이 실제보다 크게 보인다 — 처음에
//	정확일치로 재서 미해소를 69건 부풀린 적이 있다.

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DemandGap — 한 칸의 결핍 관측.
type DemandGap struct {
	Kind       string // unmet | locale_gap
	EntityType string
	Locale     string
	Terms      int
	Requests   int
	TopTerms   []string
}

// demandGapLocales — locale_gap 을 재는 칸. 소비자가 실제로 요청하는 8개다.
var demandGapLocales = []string{"en", "ja", "vi", "zh", "zh_hant", "es", "id", "pt_br"}

// MeasureDemandGaps — 결핍을 재서 원장에 적는다. **데이터 변경은 이 표에만 한다.**
//
// windowDays 는 행에 함께 적는다 — 7일과 30일은 다른 이야기이고, 표에 없으면 나중에
// 두 수를 섞어 본다.
func MeasureDemandGaps(ctx context.Context, pool *pgxpool.Pool, windowDays int) ([]DemandGap, error) {
	if pool == nil {
		return nil, nil
	}
	if windowDays <= 0 {
		windowDays = 7
	}
	gaps, err := measureUnmet(ctx, pool, windowDays)
	if err != nil {
		return nil, err
	}
	lg, err := measureLocaleGaps(ctx, pool, windowDays)
	if err != nil {
		return gaps, err
	}
	gaps = append(gaps, lg...)

	// ★한 회차는 **같은 시각**으로 묶는다 (2026-09-20 끝단에서 잡았다).
	//
	//   처음엔 measured_at 을 DB 기본값 now() 에 맡겼다. Exec 마다 트랜잭션이 달라
	//   51칸이 51개의 서로 다른 시각을 갖게 됐고, 「가장 최근 관측」을 묻는 조회가
	//   **마지막 한 칸만** 돌려줬다. 51칸을 적고 1칸을 읽는 표가 된 것이다.
	measuredAt := time.Now()
	for _, g := range gaps {
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_demand_gaps (measured_at, window_days, kind, entity_type, locale, distinct_terms, requests, top_terms)
VALUES ($8,$1,$2,$3,$4,$5,$6,$7)`,
			windowDays, g.Kind, g.EntityType, g.Locale, g.Terms, g.Requests, g.TopTerms, measuredAt); err != nil {
			log.Printf("kdb.demand-gaps: 기록 실패 %s/%s/%s: %v", g.Kind, g.EntityType, g.Locale, err)
		}
	}
	log.Printf("kdb.demand-gaps: %d일 창 — %d칸 기록", windowDays, len(gaps))
	return gaps, nil
}

// measureUnmet — 요청이 왔는데 **active 원장이 그 이름을 모르는** 것. 유형별.
func measureUnmet(ctx context.Context, pool *pgxpool.Pool, windowDays int) ([]DemandGap, error) {
	rows, err := pool.Query(ctx, `
WITH t AS (
  SELECT term_ko,
         coalesce(nullif(term_type,''),'') tt,
         count(*)::int req,
         lower(regexp_replace(term_ko,'[[:space:][:punct:]]','','g')) k
    FROM kwave_kdb_request_terms
   WHERE created_at > now() - make_interval(days => $1)
     AND origin IN ('prepare','lookup')
     AND term_ko <> ''
   GROUP BY 1,2
), known AS (
  SELECT DISTINCT lower(regexp_replace(x,'[[:space:][:punct:]]','','g')) k
    FROM kwave_entities e,
         LATERAL unnest(array[e.canonical_ko] || coalesce(e.aliases_ko,'{}')) x
   WHERE e.status='active' AND x <> ''
), unmet AS (
  SELECT t.* FROM t LEFT JOIN known ON known.k = t.k WHERE known.k IS NULL
)
SELECT tt,
       count(*)::int,
       coalesce(sum(req),0)::int,
       (array_agg(term_ko ORDER BY req DESC, term_ko))[1:10]
  FROM unmet GROUP BY tt ORDER BY coalesce(sum(req),0) DESC`, windowDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DemandGap
	for rows.Next() {
		g := DemandGap{Kind: "unmet"}
		if err := rows.Scan(&g.EntityType, &g.Terms, &g.Requests, &g.TopTerms); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// measureLocaleGaps — 대상은 **아는데** 그 언어 칸이 빈 것. 유형×locale.
//
// 요청 로그에는 locale 이 없다(요청 본문에만 있다). 그래서 「요청된 낱말이 가리키는
// active 대상의 빈 칸」을 센다 — 소비자가 그 언어를 물었는지는 모르지만, 그 대상을
// 물었다는 것은 안다. 이것이 지금 셀 수 있는 가장 정직한 근사다.
func measureLocaleGaps(ctx context.Context, pool *pgxpool.Pool, windowDays int) ([]DemandGap, error) {
	var out []DemandGap
	for _, loc := range demandGapLocales {
		col := "canonical_" + loc
		rows, err := pool.Query(ctx, `
WITH t AS (
  SELECT term_ko, count(*)::int req,
         lower(regexp_replace(term_ko,'[[:space:][:punct:]]','','g')) k
    FROM kwave_kdb_request_terms
   WHERE created_at > now() - make_interval(days => $1)
     AND origin IN ('prepare','lookup') AND term_ko <> ''
   GROUP BY 1
), hit AS (
  SELECT t.term_ko, t.req, e.entity_type::text et, coalesce(e.`+col+`,'') val
    FROM t
    JOIN kwave_entities e
      ON e.status='active'
     AND lower(regexp_replace(e.canonical_ko,'[[:space:][:punct:]]','','g')) = t.k
)
SELECT et, count(*)::int, coalesce(sum(req),0)::int,
       (array_agg(term_ko ORDER BY req DESC, term_ko))[1:10]
  FROM hit WHERE val = '' GROUP BY et ORDER BY coalesce(sum(req),0) DESC`, windowDays)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			g := DemandGap{Kind: "locale_gap", Locale: loc}
			if err := rows.Scan(&g.EntityType, &g.Terms, &g.Requests, &g.TopTerms); err != nil {
				continue
			}
			out = append(out, g)
		}
		rows.Close()
	}
	return out, nil
}

// TopDemandGaps — 가장 최근 관측에서 수요가 큰 칸부터. 화면과 다음 대상 선정이 쓴다.
func TopDemandGaps(ctx context.Context, pool *pgxpool.Pool, kind string, limit int) ([]DemandGap, error) {
	if pool == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}
	rows, err := pool.Query(ctx, `
WITH latest AS (
  SELECT max(measured_at) m FROM kwave_kdb_demand_gaps WHERE kind = $1
)
SELECT entity_type, locale, distinct_terms, requests, top_terms
  FROM kwave_kdb_demand_gaps, latest
 WHERE kind = $1 AND measured_at = latest.m
 ORDER BY requests DESC LIMIT $2`, kind, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DemandGap
	for rows.Next() {
		g := DemandGap{Kind: kind}
		if err := rows.Scan(&g.EntityType, &g.Locale, &g.Terms, &g.Requests, &g.TopTerms); err != nil {
			continue
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// demandGapInterval — 결핍을 다시 재는 주기. 수요는 시간 단위로 출렁이지 않는다.
const demandGapInterval = 6 * time.Hour

// MaybeMeasureDemandGaps — 마지막 관측이 오래됐으면 **스스로** 다시 잰다.
//
// ★왜 스케줄러를 새로 두지 않나 (0151 과 같은 이유). 「장치는 있는데 아무도 안 켠」
// 경우를 하루에 여덟 번 만났다. 레인은 어차피 주기적으로 돈다 — 그 등에 업히면
// 켜는 사람이 필요 없다.
//
// ★레인을 늦추면 안 된다. 고루틴으로 띄우고 제 시간 예산을 따로 갖는다. 실패해도
// 레인은 이미 제 갈 길을 갔다.
func MaybeMeasureDemandGaps(pool *pgxpool.Pool) {
	if pool == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	var last *time.Time
	err := pool.QueryRow(ctx, `SELECT max(measured_at) FROM kwave_kdb_demand_gaps`).Scan(&last)
	cancel()
	if err != nil {
		return // 표가 아직 없거나 조회 실패 — 레인을 막지 않는다
	}
	if last != nil && time.Since(*last) < demandGapInterval {
		return
	}
	// ★한 주기에 **한 번만** 잰다 (2026-09-21 40회차에 고쳤다).
	//
	//	위의 「오래됐나」만으로는 막지 못한다. 이 함수는 레인이 원장에 적을 때마다
	//	불리는데, 레인 여럿이 **같은 초에** 적으면 전부 같은 옛 timestamp 를 보고
	//	전부 고루틴을 띄운다. 실측에서 매 주기가 그랬다:
	//
	//	    17:28 3회 · 23:29 2회 · 05:31 2회 · 11:32 **4회**
	//
	//	네 번 모두 내용이 같았으니 읽기가 틀어지진 않았다. 다만 같은 일을 네 번 한다 —
	//	한 번이 요청 로그를 9번 훑는다(unmet 1 + locale_gap 8). 그리고 표에 같은 관측이
	//	여러 벌 쌓여 나중에 읽는 사람을 헷갈리게 한다.
	//
	//	CAS 로 한 마리만 통과시킨다. 먼저 끝난 쪽이 새 timestamp 를 남기므로, 뒤늦게
	//	온 레인은 위의 「오래됐나」에서 걸러진다.
	if !demandGapRunning.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer demandGapRunning.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		if _, err := MeasureDemandGaps(ctx, pool, 7); err != nil {
			log.Printf("kdb.demand-gaps: 측정 실패: %v", err)
		}
	}()
}

// demandGapRunning — 측정이 지금 돌고 있는가. 한 주기에 한 번만 재기 위한 빗장이다.
var demandGapRunning atomic.Bool
