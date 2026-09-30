package kdb

// anchor_review_lane — 앵커 검수를 **사람 대신 레인이** 한다: 감사 → 이름항목 철회 → 판정·집행.
//
// ★계기 (2026-09-30, 오너: "검수하는 대상을 파악해서 너가 결정할 것은 결정해").
//   관리 화면의 검수 대기열 일곱 가운데 자동 처리기가 **하나도 없는** 것이 앵커 검수였다.
//     신규 후보   gatekeeper · cand-evidence · review-judge
//     발굴 큐     research worker
//     동명이인    disambiguator
//     교정요청    RetryStuckPending · DrainWikidataVerified · ReapStale
//     검증 등급   verify-sweep
//     언어 누락   enrich · localfill · suggestion-fill
//     앵커 검수   anchor-audit · anchor-enforce · anchor-judge · anchor-apply — **전부 손으로 돌리는 명령**
//   그래서 자동 레인(wikidata-active-anchor 등)이 붙이는 새 앵커는 감사 없이 «검증»으로 나갈 수
//   있었고, 09-24 에 감사 받은 적 없던 앵커 892개 · 틀린 앵커 436개가 서빙 중이었다.
//
// ★한 회차 (규칙은 새로 만들지 않는다 — 전부 이미 운영에서 쓴 함수다).
//   ① 감사     AuditPersonAnchors — 안 본 것·낡은 것부터 KDB_ANCHOR_AUDIT_BATCH(100)개.
//              P31 로 어긋남을 판정해 저장한다(외부 호출: 위키데이터만).
//   ② 이름항목  EnforceStoredAnchorVerdicts — «한국 이름 글자» 항목이 붙은 것만 자동 철회
//              (09-15 에 정한 유일한 자동 철회 규칙, ref 가 정확히 하나일 때만).
//   ③ 판정     DrainAnchorJudge — 나머지 어긋남을 gpt-6-luna 가 «앵커가 틀림/유형이 틀림/불명»
//              으로 가르고 집행한다. KDB_ANCHOR_JUDGE_BATCH(10)건. GPT 가 아니면 집행하지 않는다.
//              판정한 것은 30일간 다시 묻지 않는다(0157 judged_at).
//
// ★예산. 판정은 codex 일일 상한(KDB_CODEX_DAILY_CALLS)을 review-judge(회차당 30)와 나눠 쓴다.
//   10건 × 48회 = 하루 480. 상한을 넘기면 gemma 로 내려가고, 그 답으로는 집행하지 않는다.
//
// 끄기: KDB_ANCHOR_REVIEW_ENABLED=0. 수동: `kdb-app anchor-review [go]` (기본 dry).

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// AnchorReviewResult — 한 회차의 결과.
type AnchorReviewResult struct {
	Audited, Mismatched     int // ① 조회한 앵커 · 그중 어긋남
	NameWithdrawn, NameCell int // ② 이름항목 철회 · 비운 칸
	Judge                   AnchorJudgeResult
}

// AnchorReviewEnabled — 기본 켜짐. KDB_ANCHOR_REVIEW_ENABLED=0 이면 끈다.
func AnchorReviewEnabled() bool {
	return strings.TrimSpace(os.Getenv("KDB_ANCHOR_REVIEW_ENABLED")) != "0"
}

func anchorReviewBatch(env string, def int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(env))); err == nil && v >= 0 {
		return v
	}
	return def
}

// DrainAnchorReview — 감사 → 이름항목 철회 → 판정·집행 한 회차. dry 면 ①만 저장하고(감사 기록은
// 관측이다) ②③ 은 무엇을 할지 세기만 한다.
func DrainAnchorReview(ctx context.Context, pool *pgxpool.Pool, cl *wikidata.Client, dry bool) AnchorReviewResult {
	var res AnchorReviewResult
	if pool == nil || cl == nil {
		return res
	}
	run := NewLaneRun("anchor-review", dry)
	defer run.Record(ctx, pool)

	if n := anchorReviewBatch("KDB_ANCHOR_AUDIT_BATCH", 100); n > 0 {
		bad, checked := AuditPersonAnchors(ctx, pool, cl, n)
		res.Audited, res.Mismatched = checked, len(bad)
		run.Scan(checked)
	}
	if ctx.Err() != nil {
		return res
	}
	if n := anchorReviewBatch("KDB_ANCHOR_ENFORCE_BATCH", 50); n > 0 {
		w := EnforceStoredAnchorVerdicts(ctx, pool, n, dry)
		res.NameWithdrawn, res.NameCell = w.Withdrawn, w.CellsCleared
		for i := 0; i < w.Withdrawn; i++ {
			run.Apply()
		}
	}
	if ctx.Err() != nil {
		return res
	}
	if n := anchorReviewBatch("KDB_ANCHOR_JUDGE_BATCH", 10); n > 0 {
		res.Judge = DrainAnchorJudge(ctx, pool, cl, n, dry)
		for i := 0; i < res.Judge.AnchorWrong+res.Judge.TypeWrong; i++ {
			run.Apply()
		}
		for i := 0; i < res.Judge.Unclear; i++ {
			run.Skip("unclear")
		}
		for i := 0; i < res.Judge.NotGPT; i++ {
			run.Skip("not-gpt")
		}
	}
	if res.Mismatched > 0 || res.NameWithdrawn > 0 || res.Judge.Checked > 0 || dry {
		log.Printf("kdb.anchor-review: 감사 %d(어긋남 %d) · 이름항목 철회 %d(칸 %d) · 판정 %d(앵커오류 %d·유형오류 %d·불명 %d·GPT아님 %d, 칸 %d) (dry=%v)",
			res.Audited, res.Mismatched, res.NameWithdrawn, res.NameCell,
			res.Judge.Checked, res.Judge.AnchorWrong, res.Judge.TypeWrong, res.Judge.Unclear, res.Judge.NotGPT,
			res.Judge.CellsCleared, dry)
	}
	return res
}
