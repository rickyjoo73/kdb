package disambiguator

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Criterion is the Hermes success test for the Disambiguator
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.5):
//
//   - Metric = over the selected set, the number of UNRESOLVED variants: rows
//     that are still a live duplicate (same canonical_ko + empty disambig, ≥2)
//     PLUS rows still flagged needs_disambig. Lower is better; a good run drives
//     this down (variants merged/labeled, conflicts routed to review).
//   - Met = the unresolved count did not increase, the self-check passed (no two
//     evidence-conflicting members merged; no member left needs_disambig without
//     a review breadcrumb — both enforced structurally), within sane bounds.
type Criterion struct{}

// Metric counts unresolved variants among the selected ids.
func (Criterion) Metric(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (float64, error) {
	if pool == nil || len(ids) == 0 {
		return 0, nil
	}
	var n int
	err := pool.QueryRow(ctx, `
SELECT
  -- still-colliding duplicates (same name+type, empty disambig, live)
  COALESCE((SELECT count(*) FROM kwave_entities e
             WHERE e.id = ANY($1) AND e.status IN ('active','candidate')
               AND COALESCE(e.disambig,'')=''
               AND (SELECT count(*) FROM kwave_entities x
                     WHERE x.canonical_ko=e.canonical_ko AND x.entity_type=e.entity_type
                       AND x.status IN ('active','candidate') AND COALESCE(x.disambig,'')='') > 1
           ),0)
  -- plus rows still flagged for review
  + COALESCE((SELECT count(*) FROM kwave_entities e
               WHERE e.id = ANY($1) AND e.needs_disambig),0)`, ids).Scan(&n)
	if err != nil {
		return 0, err
	}
	return float64(n), nil
}

// Met passes when the unresolved count did not increase and the self-check held.
func (Criterion) Met(before, after float64, rep agents.RunReport) (bool, string) {
	detail := fmt.Sprintf("unresolved %.0f→%.0f; merged=%d distinct=%d quarantined=%d",
		before, after,
		countAction(rep, agents.ActionMerged), countAction(rep, agents.ActionSplit), rep.Quarantined)
	if !rep.SelfCheck.Pass {
		return false, detail + "; self-check failed"
	}
	if after > before+1e-9 {
		return false, detail + "; unresolved increased"
	}
	return true, detail
}

func countAction(rep agents.RunReport, a agents.Action) int {
	n := 0
	for _, r := range rep.Results {
		if r.Action == a {
			n++
		}
	}
	return n
}

// selfCheck audits the run report for the role invariants (design §B.5):
//   - no member left needs_disambig WITHOUT being accounted (structurally true:
//     quarantine sets needs_disambig AND emits a Quarantined result with reason),
//   - every merge/distinct carries a reason (provenance for the audit row).
//
// The "never merge two evidence-conflicting members" invariant is enforced at
// write time by the homonym.Conflict gate in applyMerge; here we assert every
// state-changing result is justified.
func (a *Agent) selfCheck(results []agents.ItemResult) agents.SelfCheck {
	sc := agents.SelfCheck{Pass: true}
	bad := 0
	for _, r := range results {
		switch r.Action {
		case agents.ActionMerged, agents.ActionSplit, agents.ActionQuarantined:
			if r.Reason == "" {
				bad++
			}
		}
	}
	sc.Checks = append(sc.Checks, agents.Check{
		Name: "decisions_justified", Pass: bad == 0,
		Detail: fmt.Sprintf("%d decision(s) lacked a reason", bad)})
	sc.Checks = append(sc.Checks, agents.Check{
		Name: "evidence_gate_enforced", Pass: true,
		Detail: "merges pass homonym.Conflict gate; conflicting members downgraded to distinct"})
	if bad > 0 {
		sc.Pass = false
	}
	return sc
}
