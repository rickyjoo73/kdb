package personextractor

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Criterion is the Hermes success test for PersonExtractor
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.3):
//
//   - Metric = over the selected set, the count of rows that are STILL a gap:
//     a non-person entity that still has an unreconciled orphan legacy person,
//     OR a person entity whose details primary_role is still 'other'/NULL.
//     Lower is better; a good run drives this down.
//   - Met = the gap metric did not increase, self-check passed, and (the global
//     invariant) every active person entity has a details row.
type Criterion struct{}

// Metric counts remaining gaps among the selected ids.
func (Criterion) Metric(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (float64, error) {
	if pool == nil || len(ids) == 0 {
		return 0, nil
	}
	var gaps int
	err := pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities e
 WHERE e.id = ANY($1)
   AND (
     -- person entity still defaulted to 'other'/NULL role
     (e.entity_type='person' AND e.status='active'
        AND EXISTS (SELECT 1 FROM kwave_entity_person_details d
                     WHERE d.entity_id=e.id AND (d.primary_role IS NULL OR d.primary_role='other')))
     OR
     -- still a non-person entity that shares a name with an unreconciled legacy person
     (e.entity_type <> 'person'
        AND EXISTS (SELECT 1 FROM kwave_persons p WHERE p.name_ko=e.canonical_ko)
        AND NOT EXISTS (SELECT 1 FROM kwave_entities pe
                         WHERE pe.canonical_ko=e.canonical_ko AND pe.entity_type='person'))
   )`, ids).Scan(&gaps)
	if err != nil {
		return 0, err
	}
	return float64(gaps), nil
}

// Met passes when gaps did not increase and the self-check held.
func (Criterion) Met(before, after float64, rep agents.RunReport) (bool, string) {
	detail := fmt.Sprintf("gaps %.0f→%.0f; filled=%d quarantined=%d skipped=%d",
		before, after, rep.Acted, rep.Quarantined, rep.Skipped)
	if !rep.SelfCheck.Pass {
		return false, detail + "; self-check failed"
	}
	if after > before+1e-9 {
		return false, detail + "; gap count increased"
	}
	return true, detail
}
