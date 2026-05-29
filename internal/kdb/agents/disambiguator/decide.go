package disambiguator

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/homonym"
)

// mergeConfThreshold — minimum LLM confidence to act on a merge (design §B.5).
const mergeConfThreshold = 0.70

// processCluster asks gpt-5.5 to judge the cluster, then applies each decision
// under the evidence gate. Returns one ItemResult per cluster member.
func (a *Agent) processCluster(ctx context.Context, pool *pgxpool.Pool, cl cluster) []agents.ItemResult {
	if len(cl.members) < 2 {
		var out []agents.ItemResult
		for _, m := range cl.members {
			out = append(out, agents.ItemResult{ID: m.id, Action: agents.ActionNoop,
				Source: "heuristic", Reason: "singleton cluster"})
		}
		return out
	}

	in := clusterInput{Name: cl.name, Members: toPromptMembers(cl.members)}
	var res disambigResult
	if err := a.base.CallJSON(ctx, in, &res); err != nil {
		// LLM failure → quarantine every member (accounted, not dropped).
		var out []agents.ItemResult
		for _, m := range cl.members {
			out = append(out, a.quarantine(ctx, pool, m.id, "disambig llm error: "+truncate(err.Error(), 100)))
		}
		return out
	}

	byID := map[string]member{}
	for _, m := range cl.members {
		byID[m.id.String()] = m
	}
	decided := map[uuid.UUID]agents.ItemResult{}
	for _, asg := range res.Assignments {
		mid, err := uuid.Parse(asg.ID)
		if err != nil {
			continue
		}
		m, ok := byID[asg.ID]
		if !ok {
			continue
		}
		decided[mid] = a.applyDecision(ctx, pool, cl, m, asg, byID)
	}
	// Any member the model omitted → quarantine (accounted).
	var out []agents.ItemResult
	for _, m := range cl.members {
		if r, ok := decided[m.id]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, a.quarantine(ctx, pool, m.id, "no assignment returned by model"))
	}
	return out
}

// applyDecision enforces the evidence gate then writes the decision.
func (a *Agent) applyDecision(ctx context.Context, pool *pgxpool.Pool, cl cluster, m member, asg memberResult, byID map[string]member) agents.ItemResult {
	switch asg.Decision {
	case "merge":
		return a.applyMerge(ctx, pool, m, asg, byID)
	case "distinct":
		return a.applyDistinct(ctx, pool, m, asg)
	default: // uncertain (or unknown)
		return a.quarantine(ctx, pool, m.id, "uncertain: "+asg.Reason)
	}
}

// applyMerge folds the variant member into the canonical winner's aliases_ko and
// retires the variant — but only after the evidence gate passes and the winner
// is well-formed. Violations downgrade to distinct/quarantine.
func (a *Agent) applyMerge(ctx context.Context, pool *pgxpool.Pool, loser member, asg memberResult, byID map[string]member) agents.ItemResult {
	if asg.Confidence < mergeConfThreshold {
		return a.quarantine(ctx, pool, loser.id, "merge below confidence threshold")
	}
	if asg.SameAs == nil || strings.TrimSpace(*asg.SameAs) == "" {
		return a.quarantine(ctx, pool, loser.id, "merge missing same_as winner")
	}
	winner, ok := byID[strings.TrimSpace(*asg.SameAs)]
	if !ok || winner.id == loser.id {
		return a.quarantine(ctx, pool, loser.id, "merge same_as not in cluster")
	}
	// The winner must be the well-formed form; never retire onto a malformed one.
	if !winner.wellFormed && loser.wellFormed {
		return a.quarantine(ctx, pool, loser.id, "refused: proposed winner is malformed")
	}
	// EVIDENCE GATE: never merge two members whose identity signals conflict.
	if homonym.Conflict(signals(loser), signals(winner)) {
		return a.applyDistinct(ctx, pool, loser, memberResult{
			Disambig: asg.Disambig, Reason: "evidence conflict → kept distinct (not merged)"})
	}
	if pool != nil {
		// Append loser's canonical (+ its aliases) to the winner's aliases_ko.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities w
   SET aliases_ko = (SELECT ARRAY(SELECT DISTINCT x
                       FROM unnest(COALESCE(w.aliases_ko,'{}'::text[]) || ARRAY[$2::text] || COALESCE(l.aliases_ko,'{}'::text[])) x
                      WHERE x <> '' AND x <> w.canonical_ko)),
       updated_at = now()
  FROM kwave_entities l
 WHERE w.id = $1 AND l.id = $3`, winner.id, loser.ko, loser.id)
		// Retire the loser entity (no hard delete) with a breadcrumb.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', needs_disambig=false,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator: merged into '||$2||' ('||$3||')',
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, loser.id, winner.ko, relationOf(asg))
		// Reassign the loser's person-detail row to the winner only if the
		// winner has none (never clobber a richer winner record).
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_person_details d
   SET entity_id = $2
 WHERE d.entity_id = $1
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_person_details w WHERE w.entity_id = $2)`,
			loser.id, winner.id)
	}
	return agents.ItemResult{ID: loser.id, Action: agents.ActionMerged, Source: "gpt-5.5",
		Conf: asg.Confidence, Reason: "merged into " + winner.ko + " (" + relationOf(asg) + "): " + asg.Reason}
}

// applyDistinct sets a distinct disambig label so the homonym unique index
// (migration 0060) admits the row as a legitimate homonym. Clears needs_disambig.
func (a *Agent) applyDistinct(ctx context.Context, pool *pgxpool.Pool, m member, asg memberResult) agents.ItemResult {
	label := ""
	if asg.Disambig != nil {
		label = strings.TrimSpace(*asg.Disambig)
	}
	if label == "" {
		label = homonym.SuggestDisambig(signals(m))
	}
	if label == "" {
		// Genuinely distinct but no label derivable → quarantine for review so
		// the homonym index does not collide two empty-disambig rows.
		return a.quarantine(ctx, pool, m.id, "distinct but no disambig label derivable")
	}
	if pool != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET disambig = $2, needs_disambig = false,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator: distinct '||$2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, m.id, label)
	}
	return agents.ItemResult{ID: m.id, Action: agents.ActionSplit, Source: "gpt-5.5",
		Reason: "distinct person → disambig " + label + ": " + asg.Reason}
}

// quarantine flags a member for operator review (needs_disambig) WITHOUT merging
// or splitting — accounted, never a silent drop (fixes the A.3 leak: every
// quarantined member carries a review breadcrumb).
func (a *Agent) quarantine(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, reason string) agents.ItemResult {
	if pool != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET needs_disambig = true,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator review: ' || $2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, id, truncate(reason, 150))
	}
	return agents.ItemResult{ID: id, Action: agents.ActionQuarantined, Source: "gpt-5.5", Reason: reason}
}

func signals(m member) homonym.PersonSignals {
	return homonym.PersonSignals{
		PrimaryRole:  m.role,
		Agency:       m.agency,
		NotableWorks: m.works,
		BirthYear:    m.birthYear,
	}
}

func toPromptMembers(ms []member) []codexcli.DisambigMember {
	out := make([]codexcli.DisambigMember, 0, len(ms))
	for _, m := range ms {
		out = append(out, codexcli.DisambigMember{
			ID: m.id.String(), Name: m.ko, Role: m.role, Agency: m.agency,
			Works: m.works, WellFormed: m.wellFormed, AliasScore: m.aliasScore,
		})
	}
	return out
}

func relationOf(asg memberResult) string {
	if asg.Relation != nil && strings.TrimSpace(*asg.Relation) != "" {
		return strings.TrimSpace(*asg.Relation)
	}
	return "same"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
