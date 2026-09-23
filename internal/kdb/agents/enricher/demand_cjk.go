package enricher

// demand_cjk — **요청이 온 대상의 일본어·중국어 빈칸**을 GPT 로 잠정 채움한다.
//
// ★계기 (2026-09-23). 수요 우선 정렬(#10)과 40분 드레인 뒤에도 요청 대상 빈칸이
//   zh 443 · zh_hant 435 · ja 109 로 남았고, 마지막 라운드는 200건을 보고 0칸을 채웠다.
//   남은 것은 gemma 가 답하지 않은 롱테일이다(무명 인물의 한자, 공식 일본 제목이 없는 작품).
//   운영자 지시로 이 몫을 gpt-6-luna 에 맡긴다.
//
// ★값의 등급은 그대로 잠정이다. source=llm-provisional(최하위) — 권위 값이 오면 밀린다.
//   모델이 더 좋아졌다고 등급을 올리지 않는다. 빈 칸에만 쓴다(writeLocale 의 가드).
//
// ★같은 대상을 거듭 묻지 않는다. 시도는 enrich_attempts 에 last_source='gpt-provisional'
//   로 남기고, 7일 안에 그렇게 시도한 칸은 다시 고르지 않는다.
//
// ★GPT 가 아닌 모델이 답하면(상한 소진 → gemma) 쓰지 않는다 — 그 몫은 이미 gemma 가
//   답하지 않은 칸이다.

import (
	"context"
	"encoding/json"
	"log"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

var demandCJKCols = []string{"canonical_ja", "canonical_zh", "canonical_zh_hant"}

// DemandCJKResult — 한 번 돈 결과.
type DemandCJKResult struct {
	Checked, Filled, NoAnswer, NotGPT int
	ByLocale                          map[string]int
	Samples                           []string
}

// DrainDemandCJK — 최근 14일 요청 대상 중 ja/zh/zh_hant 빈칸을 GPT 로 채운다.
func DrainDemandCJK(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) DemandCJKResult {
	res := DemandCJKResult{ByLocale: map[string]int{}}
	if pool == nil || limit <= 0 {
		return res
	}
	runner := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("DEMANDFILL", "codex")).
		WithEffort(codexcli.RoleEffort("DEMANDFILL", "medium"))
	role := localeLLMRole()
	a := &Agent{localeBase: agents.NewBase(runner, role)}

	rows, err := pool.Query(ctx, `
SELECT DISTINCT e.id
  FROM kwave_entities e
  JOIN kwave_kdb_request_terms r ON r.term_ko = e.canonical_ko AND r.created_at > now() - interval '14 days'
 WHERE e.status = 'active' AND e.entity_type NOT IN ('unknown','term')
   AND (COALESCE(e.canonical_ja,'') = '' OR COALESCE(e.canonical_zh,'') = '' OR COALESCE(e.canonical_zh_hant,'') = '')
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts x
                    WHERE x.entity_id = e.id AND x.last_source = 'gpt-provisional'
                      AND x.last_attempt_at > now() - interval '7 days')
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.demand-cjk: select: %v", err)
		return res
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		r, ok := a.loadRecord(ctx, pool, id)
		if !ok {
			continue
		}
		var codes, cols []string
		for _, c := range demandCJKCols {
			if strings.TrimSpace(r.localeVals[c]) == "" {
				codes = append(codes, localeToCode[c])
				cols = append(cols, c)
			}
		}
		if len(codes) == 0 {
			continue
		}
		res.Checked++
		prompt, _ := role.BuildPrompt(makeFillInput(r, codes, nil, nil))
		raw, by, rerr := runner.RunP(ctx, prompt, role.Schema)
		if rerr != nil {
			continue
		}
		if !strings.HasPrefix(by, "codex(") {
			res.NotGPT++
			continue
		}
		var fr aijudge.FillResult
		if err := json.Unmarshal(raw, &fr); err != nil {
			continue
		}
		got := 0
		for _, sp := range fr.Spellings {
			col := "canonical_" + sp.Locale
			v := strings.TrimSpace(sp.Value)
			if v == "" || !contains(cols, col) {
				continue
			}
			if len(res.Samples) < 60 {
				res.Samples = append(res.Samples, r.ko+" ["+r.entityType+"] "+sp.Locale+"="+v)
			}
			if dry {
				if kdb.IsValidSpellingForLocale(sp.Locale, v) {
					got++
					res.ByLocale[sp.Locale]++
				}
				continue
			}
			if a.writeLocale(ctx, pool, r, col, v, string(kdb.SourceLLMProvisional)) {
				got++
				res.ByLocale[sp.Locale]++
			}
		}
		if !dry {
			// 한쪽 한자만 왔으면 다른 쪽을 결정적으로 맞춘다(잠정 등급을 물려받는다).
			a.fillZhVariants(ctx, pool, r, map[string]string{}, map[string]string{})
			for _, c := range cols {
				_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1, $2, 1, now(), 'gpt-provisional')
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1, last_attempt_at = now(), last_source = 'gpt-provisional'`,
					id, c)
			}
		}
		if got > 0 {
			res.Filled++
		} else {
			res.NoAnswer++
		}
	}
	return res
}
