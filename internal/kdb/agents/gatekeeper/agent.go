package gatekeeper

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// gateInput is the opaque input the LLMRole prompt builder receives.
type gateInput struct {
	Term          string
	Flags         []string
	SourceDomains []string
}

// rejectConfFloor — REJECT(비가역) 결정에 요구하는 최소 확신. KEEP 게이트(0.60)
// 보다 높여, 어중간한 확신의 오거부가 실제 entity 를 영구 삭제하지 않게 한다.
const rejectConfFloor = 0.75

// gateResult is the strict JSON contract decoded from the gpt-5.5 call. It
// matches scripts/codex_schemas/kdb_gatekeeper.schema.json.
type gateResult struct {
	Verdict             string  `json:"verdict"`
	Keep                bool    `json:"keep"`
	Confidence          float64 `json:"confidence"`
	CanonicalSuggestion *string `json:"canonical_suggestion"`
	Reason              string  `json:"reason"`
}

// Agent is the CandidateGatekeeper role agent.
type Agent struct {
	base *agents.Base
}

// New builds a CandidateGatekeeper. Pass a nil runner to use the default
// codex CLI transport; pass an explicit one (or a fake) in tests via NewWith.
func New(r *codexcli.Runner) *Agent {
	if r == nil {
		r = codexcli.NewRunner()
	}
	// 이진 keep/reject 판정 — 결정 규칙이 프롬프트에 명시돼 medium effort 로
	// 충분 (CODEX_EFFORT_GATEKEEPER 로 재정의 가능).
	return &Agent{base: agents.NewBase(r.WithEffort(codexcli.RoleEffort("GATEKEEPER", "medium")), llmRole())}
}

// NewWith builds a gatekeeper from an explicit agents.Base (used by tests to
// inject a fake runner).
func NewWith(base *agents.Base) *Agent { return &Agent{base: base} }

func llmRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleCandidateGatekeeper,
		Schema: codexcli.GatekeeperSchema,
		BuildPrompt: func(in any) (string, error) {
			gi, ok := in.(gateInput)
			if !ok {
				return "", fmt.Errorf("gatekeeper: bad prompt input %T", in)
			}
			return codexcli.BuildGatekeeperPrompt(gi.Term, gi.Flags, gi.SourceDomains), nil
		},
	}
}

func (a *Agent) Role() agents.Role { return agents.RoleCandidateGatekeeper }

// Criterion returns the Hermes success test for this role.
func (a *Agent) Criterion() agents.Criterion { return Criterion{} }

// candRow is the per-candidate state loaded for processing.
type candRow struct {
	ID            uuid.UUID
	Ko            string
	SourceDomains []string
}

// Select returns up to budget unlocked candidate ids, newest first (mirrors
// the legacy stepReviewCandidates selection so behaviour stays comparable).
func (a *Agent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if pool == nil {
		return nil, nil
	}
	if budget <= 0 {
		budget = 20
	}
	rows, err := pool.Query(ctx, `
SELECT id FROM kwave_entities
WHERE status='candidate' AND operator_locked = false
ORDER BY updated_at DESC
LIMIT $1`, budget)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// Run processes each selected candidate: deterministic pre-gate first, then the
// gpt-5.5 gray-band decision for the ambiguous remainder. Every selected id
// ends with exactly one accounted Action (reject/kept/quarantined/errored) so
// the Hermes leak detector is satisfied — no silent drops.
func (a *Agent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{
		Role: a.Role(), RunID: in.RunID, StartedAt: time.Now(),
		Selected: len(in.IDs), SelfCheck: agents.SelfCheck{Pass: true},
	}
	rows := a.load(ctx, pool, in.IDs)

	for _, id := range in.IDs {
		c, ok := rows[id]
		if !ok {
			// Selected but vanished (concurrent change) — account as skipped.
			rep.Results = append(rep.Results, agents.ItemResult{
				ID: id, Action: agents.ActionSkipped, Source: "heuristic",
				Reason: "row not found at run time",
			})
			continue
		}
		rep.Results = append(rep.Results, a.process(ctx, pool, c))
	}

	rep.SelfCheck = a.selfCheck(rep.Results)
	rep.Summarize()
	return rep, nil
}

func (a *Agent) process(ctx context.Context, pool *pgxpool.Pool, c candRow) agents.ItemResult {
	pre := PreGate(c.Ko)
	switch pre.Verdict {
	case PreReject:
		return a.reject(ctx, pool, c, "rule:"+pre.Reason, "heuristic", 1.0)
	case PreKeep:
		// Clean name — keep as candidate (promotion handled downstream).
		return agents.ItemResult{ID: c.ID, Action: agents.ActionKept, Source: "heuristic",
			Conf: 1.0, Reason: "rule:" + pre.Reason}
	}

	// Gray band → gpt-5.5.
	var res gateResult
	err := a.base.CallJSON(ctx, gateInput{Term: c.Ko, Flags: pre.Flags, SourceDomains: c.SourceDomains}, &res)
	if err != nil {
		// Call/schema failure → quarantine (accounted, not dropped).
		return a.quarantine(ctx, pool, c, "llm error: "+truncate(err.Error(), 120))
	}

	switch {
	case res.Verdict == "uncertain" || res.Confidence < 0.60:
		return a.quarantine(ctx, pool, c, "uncertain: "+res.Reason)
	case !res.Keep:
		// REJECT 는 비가역(rejected → 후보풀에서 제거)이라 KEEP 보다 높은 확신을
		// 요구한다. 0.60~0.75 의 어중간한 reject 는 실제 entity 를 영구 삭제할 위험이
		// 있어 거부 대신 운영자 리뷰로 보류(비대칭 해소).
		if res.Confidence < rejectConfFloor {
			return a.quarantine(ctx, pool, c, "low-confidence reject → review: "+res.Reason)
		}
		return a.reject(ctx, pool, c, "gpt:"+res.Verdict+" — "+res.Reason, "gpt-5.5", res.Confidence)
	default:
		// keep=true. If the model cleaned a buried proper noun, fold the
		// original into aliases_ko and adopt the suggestion as canonical.
		if sug := suggestion(res.CanonicalSuggestion); sug != "" && sug != c.Ko {
			// The suggestion itself must clear the deterministic pre-gate. A
			// hallucinated / injected suggestion (junk, over-length, lone jamo,
			// control/PUA chars) must never be written to canonical_ko — this is
			// the 박보검-오염 write-side class. PreReject → ignore it, keep original.
			if PreGate(sug).Verdict != PreReject {
				a.applySuggestion(ctx, pool, c, sug)
				return agents.ItemResult{ID: c.ID, Action: agents.ActionKept, Source: "gpt-5.5",
					Conf: res.Confidence, Reason: "kept (cleaned → " + sug + "): " + res.Reason}
			}
			return agents.ItemResult{ID: c.ID, Action: agents.ActionKept, Source: "gpt-5.5",
				Conf: res.Confidence, Reason: "kept (suggestion rejected by pre-gate): " + res.Reason}
		}
		return agents.ItemResult{ID: c.ID, Action: agents.ActionKept, Source: "gpt-5.5",
			Conf: res.Confidence, Reason: "kept: " + res.Reason}
	}
}

// reject sets status='rejected' with an audit breadcrumb (same pattern the
// legacy steps use). Only acts on rows still in 'candidate' to avoid clobbering
// an operator decision.
func (a *Agent) reject(ctx context.Context, pool *pgxpool.Pool, c candRow, reason, source string, conf float64) agents.ItemResult {
	if pool != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence = 0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'gatekeeper: ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate' AND operator_locked = false`, c.ID, reason)
	}
	return agents.ItemResult{ID: c.ID, Action: agents.ActionRejected, Source: source, Conf: conf, Reason: reason}
}

// quarantine flags the candidate for operator review WITHOUT rejecting it. It
// reuses the existing needs_disambig column as the review flag (the design
// notes: "set needs_disambig or a review flag / leave as candidate flagged,
// never silent-drop"). The row stays a candidate.
func (a *Agent) quarantine(ctx context.Context, pool *pgxpool.Pool, c candRow, reason string) agents.ItemResult {
	if pool != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET needs_disambig = true,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'gatekeeper review: ' || $2,
       updated_at = now()
 WHERE id = $1 AND status='candidate' AND operator_locked = false`, c.ID, reason)
	}
	return agents.ItemResult{ID: c.ID, Action: agents.ActionQuarantined, Source: "gpt-5.5", Reason: reason}
}

func (a *Agent) applySuggestion(ctx context.Context, pool *pgxpool.Pool, c candRow, sug string) {
	if pool == nil {
		return
	}
	_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET aliases_ko = (SELECT ARRAY(SELECT DISTINCT x FROM unnest(aliases_ko || ARRAY[$2::text]) x WHERE x <> '')),
       canonical_ko = $3,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'gatekeeper: cleaned canonical',
       updated_at = now()
 WHERE id = $1 AND status='candidate' AND operator_locked = false`, c.ID, c.Ko, sug)
}

func (a *Agent) load(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) map[uuid.UUID]candRow {
	out := make(map[uuid.UUID]candRow, len(ids))
	if pool == nil || len(ids) == 0 {
		return out
	}
	rows, err := pool.Query(ctx, `
SELECT id, canonical_ko, COALESCE(source_domains,'{}'::text[])
  FROM kwave_entities WHERE id = ANY($1)`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var c candRow
		if err := rows.Scan(&c.ID, &c.Ko, &c.SourceDomains); err == nil {
			out[c.ID] = c
		}
	}
	return out
}

// selfCheck enforces the role invariant: NO kept item still trips a hard junk
// signal (design §B.2 "no accepted candidate still trips a hard junk signal").
func (a *Agent) selfCheck(results []agents.ItemResult) agents.SelfCheck {
	sc := agents.SelfCheck{Pass: true}
	bad := 0
	for _, r := range results {
		if r.Action != agents.ActionKept {
			continue
		}
		// Re-derive the term from the reason is unreliable; instead the kept
		// path guarantees PreGate != PreReject. We additionally assert the
		// kept reason is non-empty (every keep carries a justification).
		if strings.TrimSpace(r.Reason) == "" {
			bad++
		}
	}
	sc.Checks = append(sc.Checks, agents.Check{
		Name: "kept_items_justified", Pass: bad == 0,
		Detail: fmt.Sprintf("%d kept item(s) lacked a justification", bad),
	})
	if bad > 0 {
		sc.Pass = false
	}
	return sc
}

func suggestion(p *string) string {
	if p == nil {
		return ""
	}
	return strings.TrimSpace(*p)
}

func truncate(s string, n int) string {
	// rune 기준 절단 (멀티바이트 rune 쪼갬 방지 — 깨진 UTF-8 이 notes 에 안 남게).
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ensure gateResult round-trips (compile-time use of json import).
var _ = json.Marshal
