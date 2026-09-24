package kdb

// review_judge — **소비자가 물었는데 답하지 못한 이름**을 판정 모델이 가르고 집행한다.
//
// ★계기 (2026-09-24). 최근 7일 prepare 이름 4,336개 중 1,416개(33%)가 조회되지 않았다.
//   발굴 큐가 «review» 로 끝낸 것이 대부분이었고(근거 부족 210 · 유형 단서 부족 83 …),
//   그 검수를 할 사람이 없었다 — review 는 사실상 «무시» 였다. 오너: "대답 못하고
//   무시하면 어떻게 하자고."
//
// ★이 레인이 그 자리를 채운다. 30분마다 «지금 조회해도 안 나오는» 요청어를 요청 많은
//   순으로 골라, 기사 문맥과 요청 유형을 붙여 판정 모델(gpt-6-luna)에게 묻는다:
//     register · promote · reopen · reject · foreign · skip
//   집행은 demand-register 와 **같은 함수**(ApplyRegisterDecisions)라 가드·스냅샷이 같다.
//
// ★GPT 가 아닌 모델이 답하면(상한 소진 → gemma) 집행하지 않고 기록도 남기지 않는다 —
//   다음 회차에 다시 묻는다. 판정한 이름은 kwave_kdb_review_judgments 에 적어 7일간 다시
//   묻지 않는다.

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

type reviewJudgeAnswer struct {
	Action string `json:"action"`
	Type   string `json:"type"`
	EN     string `json:"en"`
	JA     string `json:"ja"`
	ZH     string `json:"zh"`
	ZHHant string `json:"zh_hant"`
	Reason string `json:"reason"`
}

func reviewJudgeSchema() []byte {
	var enum strings.Builder
	for _, t := range AssignableEntityTypes() {
		enum.WriteString(`"` + t + `",`)
	}
	enum.WriteString(`""`)
	return []byte(`{"type":"object","additionalProperties":false,
"properties":{
 "action":{"type":"string","enum":["register","promote","reopen","reject","foreign","skip"]},
 "type":{"type":"string","enum":[` + enum.String() + `]},
 "en":{"type":"string"},"ja":{"type":"string"},"zh":{"type":"string"},"zh_hant":{"type":"string"},
 "reason":{"type":"string"}},
"required":["action","type","en","ja","zh","zh_hant","reason"]}`)
}

type reviewTerm struct {
	Ko, Types, Existing, Precheck, Context, SourceURL string
	Requests                                          int
}

func buildReviewJudgePrompt(t reviewTerm) string {
	var b strings.Builder
	b.WriteString("한국 고유명사 DB(KDB)에 소비자가 이 이름의 다국어 표기를 요청했는데 우리가 답하지 못했다. 어떻게 할지 정하라.\n\n")
	b.WriteString("이름: " + t.Ko + "\n")
	b.WriteString("최근 7일 요청 " + strconv.Itoa(t.Requests) + "회 · 소비자가 붙인 유형: " + t.Types + "\n")
	b.WriteString("원장의 기존 행: " + t.Existing + "\n")
	if t.Precheck != "" {
		b.WriteString("발굴 큐 판정: " + t.Precheck + "\n")
	}
	if t.Context != "" {
		b.WriteString("기사 문맥: " + truncRunes(t.Context, 400) + "\n")
	}
	if t.SourceURL != "" {
		b.WriteString("기사 URL: " + t.SourceURL + "\n")
	}
	b.WriteString(`
범위: 한국의 인물(연예인·정치인·기자·임원·선수·일반 출연자 포함)·작품·조직·기관·브랜드·게임·행사·채널·팬덤명. 해외 대상은 범위 밖이다.
action:
- register: 원장에 행이 없고 실재하는 한국 고유명사
- promote: 기존 행이 candidate 이고 실재하는 고유명사
- reopen: 기존 행이 rejected 인데 잘못 기각된 실재 고유명사
- reject: 고유명사가 아니다(일반 낱말, 문장 조각, 추출 잔재, 회차 제목, 호칭)
- foreign: 해외 인물·대상
- skip: 근거로 가를 수 없다 — 억지로 고르지 마라
register/promote/reopen 이면 type(맞는 유형)과 표기를 채운다: en(공식 영문명, 없으면 표준 로마자·자연스러운 번역), ja(인물은 가타카나, 성과 이름 사이 ・), zh(간체), zh_hant(번체). 한국 인명은 알려진 한자, 모르면 흔한 한자. 공식 라틴 표기는 그대로. 표기에 한글을 쓰지 마라. 그 밖의 action 이면 type 과 표기는 빈칸.
reason 은 한 줄.
`)
	return b.String()
}

// reviewJudgeBatch — 한 회차에 물을 수(기본 30). KDB_REVIEW_JUDGE_BATCH 로 조절.
func reviewJudgeBatch() int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv("KDB_REVIEW_JUDGE_BATCH"))); err == nil && v > 0 {
		return v
	}
	return 30
}

// ReviewJudgeEnabled — 기본 켜짐. KDB_REVIEW_JUDGE_ENABLED=0 이면 끈다.
func ReviewJudgeEnabled() bool {
	return strings.TrimSpace(os.Getenv("KDB_REVIEW_JUDGE_ENABLED")) != "0"
}

// DrainReviewJudge — 답하지 못한 요청어를 판정·집행한다. limit<=0 이면 기본 배치.
func DrainReviewJudge(ctx context.Context, pool *pgxpool.Pool, limit int, dry bool) (RegisterResult, map[string]int) {
	var total RegisterResult
	actions := map[string]int{}
	if pool == nil {
		return total, actions
	}
	if limit <= 0 {
		limit = reviewJudgeBatch()
	}
	run := NewLaneRun("review-judge", dry)
	defer run.Record(ctx, pool)

	rows, err := pool.Query(ctx, `
WITH t AS (
  SELECT term_ko, count(*) n, string_agg(DISTINCT NULLIF(term_type,''), ',') types,
         regexp_replace(lower(term_ko),'[[:space:][:punct:]·]','','g') k
    FROM kwave_kdb_request_terms
   WHERE origin = 'prepare' AND created_at > now() - interval '7 days'
   GROUP BY term_ko),
keys AS (
  SELECT regexp_replace(lower(canonical_ko),'[[:space:][:punct:]·]','','g') k FROM kwave_entities WHERE status = 'active'
  UNION SELECT regexp_replace(lower(canonical_en),'[[:space:][:punct:]·]','','g') FROM kwave_entities WHERE status = 'active' AND COALESCE(canonical_en,'') <> ''
  UNION SELECT regexp_replace(lower(a),'[[:space:][:punct:]·]','','g') FROM kwave_entities, unnest(aliases_ko) a WHERE status = 'active')
SELECT t.term_ko, t.n, COALESCE(t.types,''),
  COALESCE((SELECT e.status::text || '|' || e.entity_type::text || '|en=' || COALESCE(e.canonical_en,'') || '|' || left(COALESCE(e.notes,''),160)
              FROM kwave_entities e WHERE e.canonical_ko = t.term_ko AND e.status IN ('candidate','rejected')
             ORDER BY (e.status = 'candidate') DESC, e.updated_at DESC LIMIT 1), '(없음)'),
  COALESCE((SELECT q.precheck_status || '/' || q.precheck_reason FROM kwave_entity_research_queue q
             WHERE q.entity_ko = t.term_ko ORDER BY q.created_at DESC LIMIT 1), ''),
  COALESCE((SELECT q.context_hint FROM kwave_entity_research_queue q
             WHERE q.entity_ko = t.term_ko AND COALESCE(q.context_hint,'') <> '' ORDER BY q.created_at DESC LIMIT 1), ''),
  COALESCE((SELECT r.source_url FROM kwave_kdb_request_terms r
             WHERE r.term_ko = t.term_ko AND COALESCE(r.source_url,'') <> '' ORDER BY r.created_at DESC LIMIT 1), '')
  FROM t
 WHERE NOT EXISTS (SELECT 1 FROM keys WHERE keys.k = t.k)
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_review_judgments j
                    WHERE j.term_ko = t.term_ko AND j.judged_at > now() - interval '7 days')
 ORDER BY t.n DESC, t.term_ko
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.review-judge: select: %v", err)
		return total, actions
	}
	var terms []reviewTerm
	for rows.Next() {
		var t reviewTerm
		if rows.Scan(&t.Ko, &t.Requests, &t.Types, &t.Existing, &t.Precheck, &t.Context, &t.SourceURL) == nil {
			terms = append(terms, t)
		}
	}
	rows.Close()
	run.Scan(len(terms))

	runner := codexcli.NewRunner().
		WithProvider(codexcli.RoleProvider("REVIEWJUDGE", "codex")).
		WithEffort(codexcli.RoleEffort("REVIEWJUDGE", "medium"))
	schema := reviewJudgeSchema()

	for _, t := range terms {
		if ctx.Err() != nil {
			break
		}
		raw, by, err := runner.RunP(ctx, buildReviewJudgePrompt(t), schema)
		if err != nil || !strings.HasPrefix(by, "codex(") {
			run.Skip("not-gpt")
			continue // 다음 회차에 다시 묻는다
		}
		var a reviewJudgeAnswer
		if json.Unmarshal(raw, &a) != nil {
			run.Skip("bad-json")
			continue
		}
		actions[a.Action]++
		d := RegisterDecision{Ko: t.Ko, Action: a.Action, Type: a.Type, EN: a.EN, JA: a.JA, ZH: a.ZH, ZHHant: a.ZHHant, Reason: a.Reason}
		r := ApplyRegisterDecisions(ctx, pool, []RegisterDecision{d}, by, dry)
		total.Registered += r.Registered
		total.Promoted += r.Promoted
		total.Reopened += r.Reopened
		total.Rejected += r.Rejected
		total.Retyped += r.Retyped
		total.Cells += r.Cells
		total.Skipped += r.Skipped
		if r.Registered+r.Promoted+r.Reopened+r.Rejected > 0 {
			run.Apply()
		} else {
			run.Skip(a.Action)
		}
		if dry {
			continue
		}
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_review_judgments (term_ko, action, entity_type, reason, model, judged_at)
VALUES ($1, $2, $3, $4, $5, now())
ON CONFLICT (term_ko) DO UPDATE SET action = EXCLUDED.action, entity_type = EXCLUDED.entity_type,
  reason = EXCLUDED.reason, model = EXCLUDED.model, judged_at = now()`,
			t.Ko, a.Action, a.Type, truncRunes(a.Reason, 200), by)
	}
	return total, actions
}
