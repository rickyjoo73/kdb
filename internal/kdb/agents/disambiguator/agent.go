// Package disambiguator — the Disambiguator role agent
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.5, owner request #3).
//
// Goal: for each same-name / near-same-name cluster, decide per member:
//   - same person (TYPO / NICKNAME / alternate spelling of an existing
//     canonical) → MERGE the variant into the correct canonical's aliases_ko and
//     retire the variant entity (status='rejected' + notes breadcrumb, NEVER a
//     hard delete; reassign any person-detail row), so the wrong form stops
//     being used as its own entity; only the correct canonical is "used".
//   - genuinely DISTINCT person → assign a distinct `disambig` label (reuses the
//     disambig column + the homonym unique index from migration 0060).
//   - uncertain → quarantine for review (needs_disambig=true), accounted.
//
// KEY (fixes design A.3): aliasmatch.Find is wired INTO the cycle here so typo/
// abbreviation/near-duplicate clusters are actually detected — today it is only
// called from candidate ingestion, never from autopilot.
//
// Evidence gate: NEVER merge two members whose agency / primary_role /
// notable_works conflict (homonym.Conflict) — those stay distinct/uncertain.
// The winner of a merge is always the well-formed, highest-evidence form, never
// a malformed (jamo/typo) variant.
//
// Wired under Hermes (opt-in, KDB_HERMES_ENABLED) augmenting
// step:RepairBrokenJamo and step:ResolveAliasConflicts.
package disambiguator

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// memberResult mirrors one element of kdb_disambiguate.schema.json assignments.
type memberResult struct {
	ID         string  `json:"id"`
	Decision   string  `json:"decision"` // merge | distinct | uncertain
	Relation   *string `json:"relation"`
	SameAs     *string `json:"same_as"`
	Disambig   *string `json:"disambig"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

type disambigResult struct {
	Assignments []memberResult `json:"assignments"`
}

// clusterInput is the opaque prompt input (one cluster).
type clusterInput struct {
	Name    string
	Members []codexcli.DisambigMember
}

// Agent is the Disambiguator role agent.
type Agent struct {
	base *agents.Base
}

// New builds a Disambiguator. 동명이인 판단(표기 변종 vs 진짜 다른 사람, 작품
// 공식명 통일)은 고난도라 codex(gpt-5.5)로 라우팅(KDB_LLM_DISAMBIG 로 재정의).
func New(r *codexcli.Runner) *Agent {
	if r == nil {
		r = codexcli.NewRunner()
	}
	return &Agent{base: agents.NewBase(r.WithProvider(codexcli.RoleProvider("DISAMBIG", "codex")), llmRole())}
}

// NewWith builds one from an explicit Base (tests inject a fake runner).
func NewWith(base *agents.Base) *Agent { return &Agent{base: base} }

func llmRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleDisambiguator,
		Schema: codexcli.DisambiguateSchema,
		BuildPrompt: func(in any) (string, error) {
			ci, ok := in.(clusterInput)
			if !ok {
				return "", fmt.Errorf("disambiguator: bad prompt input %T", in)
			}
			return codexcli.BuildDisambiguatePrompt(ci.Name, ci.Members), nil
		},
	}
}

func (a *Agent) Role() agents.Role           { return agents.RoleDisambiguator }
func (a *Agent) Criterion() agents.Criterion { return Criterion{} }

// Select returns the ids that are cluster members this cycle. A "cluster" is a
// set of ≥2 entities that either share an exact canonical_ko OR are within
// pg_trgm typo distance (via aliasmatch.Find) of an active entity. The returned
// ids are the union of all such members (active + the variant candidate/active),
// bounded by budget; Run re-clusters them deterministically.
func (a *Agent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if pool == nil {
		return nil, nil
	}
	if budget <= 0 {
		budget = 30
	}
	clusters, err := a.buildClusters(ctx, pool, budget)
	if err != nil {
		return nil, err
	}
	seen := map[uuid.UUID]bool{}
	var ids []uuid.UUID
	for _, cl := range clusters {
		for _, m := range cl.members {
			if !seen[m.id] {
				seen[m.id] = true
				ids = append(ids, m.id)
			}
		}
	}
	return ids, nil
}

// Run re-clusters the selected ids and processes each cluster. Every selected id
// ends with exactly one accounted Action (merged / split / quarantined / noop /
// skipped) so the Hermes leak detector is satisfied.
func (a *Agent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{
		Role: a.Role(), RunID: in.RunID, StartedAt: time.Now(),
		Selected: len(in.IDs), SelfCheck: agents.SelfCheck{Pass: true},
	}
	selSet := map[uuid.UUID]bool{}
	for _, id := range in.IDs {
		selSet[id] = true
	}

	clusters := a.clustersFromIDs(ctx, pool, in.IDs)
	handled := map[uuid.UUID]bool{}

	for _, cl := range clusters {
		results := a.processCluster(ctx, pool, cl)
		for _, r := range results {
			if selSet[r.ID] && !handled[r.ID] {
				rep.Results = append(rep.Results, r)
				handled[r.ID] = true
			}
		}
	}
	// Any selected id not covered by a cluster (e.g. its partner vanished) is a
	// no-op (examined, nothing to disambiguate) — accounted, never dropped.
	for _, id := range in.IDs {
		if !handled[id] {
			rep.Results = append(rep.Results, agents.ItemResult{
				ID: id, Action: agents.ActionNoop, Source: "heuristic",
				Reason: "no live cluster partner at run time"})
		}
	}

	rep.SelfCheck = a.selfCheck(rep.Results)
	rep.Summarize()
	return rep, nil
}
