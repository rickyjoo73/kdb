// Step-as-agent adapters — wrap the EXISTING autopilot sweep steps
// (sweep.go) as audited agents under the Hermes supervisor, WITHOUT changing
// their logic (docs/KDB_HERMES_AGENTS_DESIGN.md §D, PR2).
//
// Each adapter:
//   - Select : runs the SAME selection query the underlying step uses, so we
//     know the candidate id set for that step this cycle.
//   - Run    : snapshots the selected rows' relevant state, invokes the
//     UNCHANGED step method, re-snapshots, and derives a per-item Action from
//     the DB delta (status→rejected = rejected; entity_type changed = filled;
//     locale/conf/needs_disambig changed = filled; unchanged = noop). This
//     yields real item-conservation / leak detection (every selected id ends
//     accounted) without modifying step internals.
//   - Criterion : agents.LooseCriterion (always met) for now — we only want
//     per-step run rows + leak detection at this stage, not pass/fail gating.
//
// The 8 wrapped steps (sweep.go:107-114, Sweeper.Run order):
//
//	stepRepairBrokenJamo      -> RoleStepRepairBrokenJamo
//	stepSyncPersons           -> RoleStepSyncPersons
//	stepReviewCandidates      -> RoleStepReviewCandidates
//	stepClassifyUnknown       -> RoleStepClassifyUnknown
//	stepPromoteConsensus      -> RoleStepPromoteConsensus
//	stepEnrichEmpty           -> RoleStepEnrichEmpty
//	stepQualityReview         -> RoleStepQualityReview
//	stepResolveAliasConflicts -> RoleStepResolveAliasConflicts
//
// stepSyncPersons and stepResolveAliasConflicts operate set-wide (not per
// selected entity id), so their adapters select a single synthetic id and Run
// records one set-level noop result — they still get an audited run row, and
// the conservation invariant is trivially satisfied.
package autopilot

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/disambiguator"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/enricher"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/gatekeeper"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/personextractor"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// stepAgent adapts one sweep step to the agents.Agent interface.
type stepAgent struct {
	sweeper  *Sweeper
	role     agents.Role
	budget   int
	selectFn func(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error)
	runStep  func(ctx context.Context, rep *Report)
	setWide  bool // operates over a set, not per selected entity id
}

func (a *stepAgent) Role() agents.Role           { return a.role }
func (a *stepAgent) Criterion() agents.Criterion { return agents.LooseCriterion{} }

func (a *stepAgent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if a.setWide || a.selectFn == nil {
		// Set-wide step: a single synthetic id stands in for "the set" so the
		// step still produces exactly one audited run row.
		return []uuid.UUID{uuid.New()}, nil
	}
	limit := a.budget
	if limit <= 0 {
		limit = budget
	}
	return a.selectFn(ctx, pool, limit)
}

func (a *stepAgent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{Role: a.role, RunID: in.RunID, StartedAt: time.Now(),
		SelfCheck: agents.SelfCheck{Pass: true}}
	rep.Selected = len(in.IDs)

	if a.setWide || a.selectFn == nil {
		var sweepRep Report
		a.runStep(ctx, &sweepRep)
		snap, _ := json.Marshal(sweepRep)
		for _, id := range in.IDs {
			rep.Results = append(rep.Results, agents.ItemResult{
				ID: id, Action: agents.ActionNoop, Source: "heuristic",
				Reason: "set-wide step", After: snap,
			})
		}
		rep.Summarize()
		return rep, nil
	}

	before := snapshotEntities(ctx, pool, in.IDs)
	var sweepRep Report
	a.runStep(ctx, &sweepRep)
	afterSnaps := snapshotEntities(ctx, pool, in.IDs)

	for _, id := range in.IDs {
		b, af := before[id], afterSnaps[id]
		act := deriveAction(b, af)
		bj, _ := json.Marshal(b)
		aj, _ := json.Marshal(af)
		rep.Results = append(rep.Results, agents.ItemResult{
			ID: id, Action: act, Source: "step", Before: bj, After: aj,
			Reason: string(act),
		})
	}
	rep.Summarize()
	return rep, nil
}

// entitySnap is the minimal per-entity state used for delta-based leak
// detection (no behaviour change — read-only).
type entitySnap struct {
	Status     string  `json:"status"`
	EntityType string  `json:"entity_type"`
	Confidence float64 `json:"confidence"`
	HasEn      bool    `json:"has_en"`
	NeedsDis   bool    `json:"needs_disambig"`
}

func snapshotEntities(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) map[uuid.UUID]entitySnap {
	out := make(map[uuid.UUID]entitySnap, len(ids))
	if pool == nil || len(ids) == 0 {
		return out
	}
	rows, err := pool.Query(ctx, `
SELECT id, status, entity_type::text, COALESCE(confidence,0),
       COALESCE(canonical_en,'') <> '', COALESCE(needs_disambig,false)
  FROM kwave_entities WHERE id = ANY($1)`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var s entitySnap
		if err := rows.Scan(&id, &s.Status, &s.EntityType, &s.Confidence, &s.HasEn, &s.NeedsDis); err == nil {
			out[id] = s
		}
	}
	return out
}

// deriveAction maps a before/after entity snapshot to an Action for the
// conservation invariant. Conservative: any observable change → a concrete
// action; otherwise noop (examined, unchanged). A selected id missing from
// both snapshots (e.g. hard-deleted) is treated as noop so it stays accounted.
func deriveAction(before, after entitySnap) agents.Action {
	switch {
	case before.Status != "rejected" && after.Status == "rejected":
		return agents.ActionRejected
	case before.EntityType != after.EntityType:
		return agents.ActionFilled
	case !before.HasEn && after.HasEn:
		return agents.ActionFilled
	case before.Confidence != after.Confidence:
		return agents.ActionFilled
	case before.NeedsDis != after.NeedsDis:
		return agents.ActionFilled
	default:
		return agents.ActionNoop
	}
}

// --- per-step selection queries (mirror the underlying step SELECTs) -------

func selectBrokenJamo(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	return queryIDs(ctx, pool, `
SELECT id FROM kwave_entities
WHERE canonical_ko ~ '[ㄱ-ㅎㅏ-ㅣ]'
  AND operator_locked = false
  AND status <> 'rejected'
LIMIT 100`)
}

func selectReviewCandidates(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	// quarantine(typed) 보류 후보는 제외 — 운영자 검토 대기 상태라 재거부 루프를 돌면 안 됨.
	return queryIDs(ctx, pool, `
SELECT id FROM kwave_entities WHERE status='candidate' AND operator_locked = false
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
ORDER BY updated_at DESC LIMIT $1`, limit)
}

func selectClassifyUnknown(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	return queryIDs(ctx, pool, `
SELECT id FROM kwave_entities
WHERE status='active' AND entity_type='unknown' AND operator_locked = false
ORDER BY confidence DESC, updated_at DESC LIMIT $1`, limit)
}

func selectResolveUnknowns(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	return queryIDs(ctx, pool, `
SELECT id FROM kwave_entities
WHERE entity_type='unknown' AND operator_locked = false
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
ORDER BY status, updated_at ASC LIMIT $1`, limit)
}

func selectPromoteConsensus(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	// quarantine(typed) 보류 후보는 auto-promote 대상에서도 제외 — 운영자 검토 전까지
	// 어떤 자동 승급도 하지 않는다(잘못 거부였는지 운영자가 판단).
	return queryIDs(ctx, pool, `
SELECT id FROM kwave_entities
WHERE status='candidate' AND COALESCE(array_length(source_domains,1),0) >= 2
  AND COALESCE(notes,'') NOT LIKE '%[kdb:q:typed]%'
ORDER BY COALESCE(array_length(source_domains,1),0) DESC, updated_at DESC LIMIT $1`, limit)
}

func selectEnrichEmpty(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	return queryIDs(ctx, pool, `
-- operator_locked 도 포함한다: enrich 쓰기는 모두 empty-only(빈 칸만 채움)라
-- 운영자가 넣은 값은 절대 안 건드린다. 잠긴 유명 인물(변우석/정국 등)의 빈
-- locale 이 영구 빈칸으로 남던 버그 수정 — 검색하면 Wikidata 에 있는데도 굶었음.
SELECT id FROM kwave_entities
WHERE status='active' AND confidence >= 0.5
  AND entity_type <> 'unknown'
  AND (canonical_en IS NULL OR canonical_en=''
    OR canonical_ja IS NULL OR canonical_ja=''
    OR canonical_vi IS NULL OR canonical_vi=''
    OR canonical_es IS NULL OR canonical_es=''
    OR canonical_id IS NULL OR canonical_id=''
    OR canonical_pt_br IS NULL OR canonical_pt_br=''
    OR canonical_zh_hant IS NULL OR canonical_zh_hant=''
    OR canonical_zh IS NULL OR canonical_zh='')
ORDER BY confidence DESC, updated_at ASC LIMIT $1`, limit)
}

func selectQualityReview(ctx context.Context, pool *pgxpool.Pool, limit int) ([]uuid.UUID, error) {
	return queryIDs(ctx, pool, `
SELECT id FROM kwave_entities
WHERE status='active' AND operator_locked = false
  AND confidence < 0.70 AND entity_type <> 'unknown'
ORDER BY confidence ASC, updated_at DESC LIMIT $1`, limit)
}

func queryIDs(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) ([]uuid.UUID, error) {
	rows, err := pool.Query(ctx, sql, args...)
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
	return ids, nil
}

// RegisterSteps wraps the 8 sweep steps as audited agents and registers them.
// Behaviour of each step is unchanged; this only adds Hermes accounting. The
// budget defaults come from the Sweeper's Config so the wrapped Select matches
// the step's own LIMIT.
func (s *Sweeper) RegisterSteps(reg *agents.Registry) error {
	steps := []*stepAgent{
		{sweeper: s, role: agents.RoleStepRepairBrokenJamo, budget: 100,
			selectFn: selectBrokenJamo, runStep: s.stepRepairBrokenJamo},
		{sweeper: s, role: agents.RoleStepSyncPersons, setWide: true,
			runStep: s.stepSyncPersons},
		{sweeper: s, role: agents.RoleStepReviewCandidates, budget: s.Config.BatchClassify,
			selectFn: selectReviewCandidates, runStep: s.stepReviewCandidates},
		{sweeper: s, role: agents.RoleStepClassifyUnknown, budget: s.Config.BatchClassify,
			selectFn: selectClassifyUnknown, runStep: s.stepClassifyUnknown},
		{sweeper: s, role: agents.RoleStepResolveUnknowns, budget: batchResolveUnknowns,
			selectFn: selectResolveUnknowns, runStep: s.stepResolveUnknowns},
		{sweeper: s, role: agents.RoleStepPromoteConsensus, budget: s.Config.BatchPromote,
			selectFn: selectPromoteConsensus, runStep: s.stepPromoteConsensus},
		{sweeper: s, role: agents.RoleStepEnrichEmpty, budget: s.Config.BatchEnrich,
			selectFn: selectEnrichEmpty, runStep: s.stepEnrichEmpty},
		{sweeper: s, role: agents.RoleStepQualityReview, budget: s.Config.BatchEnrich,
			selectFn: selectQualityReview, runStep: s.stepQualityReview},
		{sweeper: s, role: agents.RoleStepResolveAliasConflicts, setWide: true,
			runStep: s.stepResolveAliasConflicts},
	}
	for _, st := range steps {
		if err := reg.Register(st); err != nil {
			return err
		}
	}
	return s.RegisterRoleAgents(reg)
}

// RegisterRoleAgents registers the dedicated role agents that supersede the
// generic wrapped steps with a real success Criterion + self-check (vs the
// LooseCriterion the wrapped steps use). Currently:
//
//   - CandidateGatekeeper (request#4): a junk gate that runs BEFORE
//     classification/promotion. Its deterministic pre-gate hard-rejects obvious
//     junk and its gpt-5.5 gray band decides the ambiguous remainder; uncertain
//     items are quarantined (needs_disambig) for operator review, never dropped.
//     This is stronger than (and supersedes) the wrapped step:ReviewCandidates,
//     which is kept only for audit continuity.
//   - PersonExtractor (request#1): seeds person-detail roles off 'other' from
//     legacy kwave_persons and reconciles orphan legacy persons (promote / flag
//     for review), refining the wrapped step:SyncPersons.
//   - Enricher (request#2): per-record gap-fill loop over BOTH the entity locale
//     canonicals/aliases_ko AND person-detail fields (agency/birth_year/gender/
//     groups/notable_works/secondary_roles/primary_role) via the L2→L3→L4
//     cascade, never overwriting non-empty values, marking source-exhausted
//     fields (kwave_kdb_enrich_attempts) so the loop converges to zero fillable
//     gaps; foreign-presence rows first. Refines/supersedes step:EnrichEmpty.
//   - Disambiguator (request#3): clusters same-name + near-name (pg_trgm via
//     aliasmatch.Find, wired into the cycle here) entities, then merges typo/
//     nickname/alias variants into the correct canonical's aliases_ko (retiring
//     the variant, evidence-gated by homonym.Conflict), assigns a distinct
//     disambig to genuine homonyms, and quarantines uncertain clusters to
//     review. Augments step:RepairBrokenJamo / step:ResolveAliasConflicts.
//
// All share the Sweeper's codex transport (s.Judge.Runner) so all model I/O
// goes through one validated path. Behaviour is opt-in: they only run when the
// registry is driven by the Hermes supervisor (KDB_HERMES_ENABLED=1).
func (s *Sweeper) RegisterRoleAgents(reg *agents.Registry) error {
	var runner *codexcli.Runner
	if s.Judge != nil {
		runner = s.Judge.Runner
	}
	if err := reg.Register(gatekeeper.New(runner)); err != nil {
		return err
	}
	if err := reg.Register(personextractor.New(runner)); err != nil {
		return err
	}
	if err := reg.Register(enricher.New(runner)); err != nil {
		return err
	}
	return reg.Register(disambiguator.New(runner))
}
