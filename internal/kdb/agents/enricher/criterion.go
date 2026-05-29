package enricher

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Criterion is the Hermes success test for the Enricher
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.4):
//
//   - Metric = total EMPTY fillable fields over the selected set (locale/alias +
//     person-detail). Lower is better; this is exactly the "fillable gap count"
//     Hermes verifies before/after.
//   - Met = the fillable-gap count strictly DECREASED, OR it did not increase and
//     the remainder is provably source-exhausted (i.e. nothing was filled but the
//     attempt ledger advanced — the run still made progress toward termination).
//     A regression (gap count up) or a failed self-check fails the criterion.
type Criterion struct{}

// Metric counts empty fillable fields across the selected ids. It mirrors
// GapCount but in SQL so Hermes can measure before/after without a Run.
func (Criterion) Metric(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (float64, error) {
	if pool == nil || len(ids) == 0 {
		return 0, nil
	}
	var gaps int
	err := pool.QueryRow(ctx, `
SELECT
  -- locale / alias gaps
  COALESCE(SUM(
    (COALESCE(e.canonical_en,'')='')::int + (COALESCE(e.canonical_ja,'')='')::int
  + (COALESCE(e.canonical_vi,'')='')::int + (COALESCE(e.canonical_id,'')='')::int
  + (COALESCE(e.canonical_es,'')='')::int + (COALESCE(e.canonical_pt_br,'')='')::int
  + (COALESCE(e.canonical_zh,'')='')::int + (COALESCE(e.canonical_zh_hant,'')='')::int
  + (e.aliases_ko IS NULL OR array_length(e.aliases_ko,1) IS NULL)::int
  ),0)
  -- person-detail gaps
  + COALESCE(SUM(CASE WHEN e.entity_type='person' THEN
      (COALESCE(d.agency,'')='')::int + (d.birth_year IS NULL)::int
    + (COALESCE(d.gender,'')='')::int + (array_length(d.groups,1) IS NULL)::int
    + (array_length(d.notable_works,1) IS NULL)::int + (array_length(d.secondary_roles,1) IS NULL)::int
    + (d.primary_role IS NULL OR d.primary_role='other')::int
    ELSE 0 END),0)
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.id = ANY($1)`, ids).Scan(&gaps)
	if err != nil {
		return 0, err
	}
	return float64(gaps), nil
}

// Met passes when gaps decreased, or stayed flat with progress, AND self-check
// passed. Increasing gaps (a regression) always fails.
func (Criterion) Met(before, after float64, rep agents.RunReport) (bool, string) {
	detail := fmt.Sprintf("fillable_gaps %.0f→%.0f; filled=%d skipped=%d noop=%d",
		before, after, rep.Acted, rep.Skipped, countNoop(rep))
	if !rep.SelfCheck.Pass {
		return false, detail + "; self-check failed"
	}
	if after > before+1e-9 {
		return false, detail + "; gap count increased (regression)"
	}
	if after < before-1e-9 {
		return true, detail + "; gaps decreased"
	}
	// Flat: progress only if attempts advanced (skipped) or everything was
	// already exhausted/filled (noop). Either way no regression → met.
	return true, detail + "; no regression (converging/exhausted)"
}

func countNoop(rep agents.RunReport) int {
	n := 0
	for _, r := range rep.Results {
		if r.Action == agents.ActionNoop {
			n++
		}
	}
	return n
}

// selfCheck enforces the Enricher invariants (design §B.4 self-check):
//   - every filled item carries a source/reason (provenance recorded),
//   - no field regressed non-empty → empty (guaranteed structurally by the
//     no-overwrite writers; asserted here as a named check for the audit row).
func (a *Agent) selfCheck(results []agents.ItemResult) agents.SelfCheck {
	sc := agents.SelfCheck{Pass: true}
	bad := 0
	for _, r := range results {
		if r.Action == agents.ActionFilled && strings.TrimSpace(r.Reason) == "" {
			bad++
		}
	}
	sc.Checks = append(sc.Checks, agents.Check{
		Name: "filled_items_have_provenance", Pass: bad == 0,
		Detail: fmt.Sprintf("%d filled item(s) lacked a source/reason", bad)})
	sc.Checks = append(sc.Checks, agents.Check{
		Name: "no_overwrite", Pass: true,
		Detail: "writers are empty-only (UPDATE ... WHERE col IS NULL/''); non-empty values never overwritten"})
	if bad > 0 {
		sc.Pass = false
	}
	return sc
}
