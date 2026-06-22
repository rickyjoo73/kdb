package fillverifier

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Criterion measures success as the total count of codex-fallback locale values
// over the selected entities: lower-is-better. A run is met when the count did
// not increase (this agent only ever shrinks it — upgrade/replace remove
// codex-fallback labels, nothing adds them).
type Criterion struct{}

// Metric counts codex-fallback locale values across the selected ids.
func (Criterion) Metric(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (float64, error) {
	if pool == nil || len(ids) == 0 {
		return 0, nil
	}
	const q = `
SELECT COALESCE(sum(
   (canonical_en_source='codex-fallback')::int + (canonical_ja_source='codex-fallback')::int
 + (canonical_vi_source='codex-fallback')::int + (canonical_id_source='codex-fallback')::int
 + (canonical_es_source='codex-fallback')::int + (canonical_pt_br_source='codex-fallback')::int
 + (canonical_zh_source='codex-fallback')::int + (canonical_zh_hant_source='codex-fallback')::int
 ),0)
  FROM kwave_entities WHERE id = ANY($1)`
	var n int
	if err := pool.QueryRow(ctx, q, ids).Scan(&n); err != nil {
		return 0, err
	}
	return float64(n), nil
}

// Met passes when the codex-fallback count did not grow.
func (Criterion) Met(before, after float64, _ agents.RunReport) (bool, string) {
	if after <= before {
		return true, "codex-fallback 라벨 수 유지/감소"
	}
	return false, "codex-fallback 라벨 수 증가(예상치 못함)"
}
