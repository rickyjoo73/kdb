package gatekeeper

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
)

// Criterion is the Hermes success test for the CandidateGatekeeper
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.2):
//
//   - Metric = the junk ratio over the selected set: fraction of the originally
//     selected rows that STILL trip a deterministic hard junk signal AND are
//     still a live candidate. After a good run, every hard-junk row is either
//     rejected (no longer a candidate) or quarantined, so the live-junk ratio
//     drops toward 0.
//   - Met = junk ratio did not increase AND no accepted candidate still trips a
//     hard junk signal (the self-check), within sane bounds: the reject rate is
//     not pathological (we don't expect to reject ~100% of a real-name batch,
//     nor 0% of a known-junky pool — but we only fail on the leak/self-check,
//     keeping the criterion robust to batch composition).
type Criterion struct{}

// Metric measures the fraction of selected ids that are STILL a live candidate
// AND trip a deterministic hard junk signal. Lower is better.
func (Criterion) Metric(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (float64, error) {
	if pool == nil || len(ids) == 0 {
		return 0, nil
	}
	var junk int
	err := pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities
 WHERE id = ANY($1)
   AND status = 'candidate'
   AND (
     canonical_ko ~ '[ㄱ-ㅎㅏ-ㅣ]'                              -- lone jamo
     OR char_length(canonical_ko) > $2                          -- over length
     OR canonical_ko ~ '[.!?]'                                  -- sentence punct
     OR array_length(regexp_split_to_array(trim(canonical_ko), '\s+'), 1) >= 4  -- 4+ words
   )`, ids, MaxNameRunes).Scan(&junk)
	if err != nil {
		return 0, err
	}
	return float64(junk) / float64(len(ids)), nil
}

// Met passes when the live-junk ratio did not increase and the agent's
// self-check (no accepted candidate trips a hard signal) passed.
func (Criterion) Met(before, after float64, rep agents.RunReport) (bool, string) {
	detail := fmt.Sprintf("junk_ratio %.3f→%.3f; rejected=%d quarantined=%d kept=%d",
		before, after, countAction(rep, agents.ActionRejected),
		rep.Quarantined, countAction(rep, agents.ActionKept))
	if !rep.SelfCheck.Pass {
		return false, detail + "; self-check failed (accepted item trips hard signal)"
	}
	if after > before+1e-9 {
		return false, detail + "; junk ratio increased"
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
