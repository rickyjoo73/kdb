package kdbadmin

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Counts have different units and must never be summed into a "tasks" total.
// Tier is entity-level legacy classification, not per-locale verification.
type dashboardOverview struct {
	Active, LegacyPersons, Candidates, Corrections, ConflictGroups int64
	Authoritative, Evidenced, Unverified, Untiered                 int64
	Pending, InProgress, Finished24h, OverTwoMinutes24h            int64
}

func loadDashboardOverview(ctx context.Context, pool *pgxpool.Pool) (dashboardOverview, error) {
	var o dashboardOverview
	err := pool.QueryRow(ctx, `
WITH entity_counts AS (
 SELECT count(*) FILTER(WHERE status='active') AS active,
 count(*) FILTER(WHERE status='candidate') AS candidates,
 count(*) FILTER(WHERE status='active' AND verification_tier='authoritative') AS auth,
 count(*) FILTER(WHERE status='active' AND verification_tier='evidenced') AS evid,
 count(*) FILTER(WHERE status='active' AND verification_tier='unverified') AS unver,
 count(*) FILTER(WHERE status='active' AND COALESCE(verification_tier,'') NOT IN
 ('authoritative','evidenced','unverified')) AS untiered FROM kwave_entities
), queue_counts AS (
 SELECT count(*) FILTER(WHERE status='pending') AS pending,
 count(*) FILTER(WHERE status='in_progress') AS running,
 count(*) FILTER(WHERE finished_at>=now()-interval '24 hours') AS finished,
 count(*) FILTER(WHERE finished_at>=now()-interval '24 hours'
  AND finished_at-created_at>interval '2 minutes') AS slow
 FROM kwave_entity_research_queue
)
SELECT e.active,(SELECT count(*) FROM kwave_persons),e.candidates,
 (SELECT count(*) FROM kwave_kdb_corrections WHERE status IN ('pending','proposed')),
 (SELECT count(*) FROM (SELECT canonical_ko FROM kwave_entities WHERE status='active'
  AND COALESCE(disambig,'')='' GROUP BY canonical_ko HAVING count(*)>1) ko)
 + (SELECT count(*) FROM (SELECT lower(canonical_en),entity_type FROM kwave_entities
  WHERE status='active' AND COALESCE(disambig,'')='' AND COALESCE(canonical_en,'')<>''
  GROUP BY lower(canonical_en),entity_type HAVING count(*)>1) en),
 e.auth,e.evid,e.unver,e.untiered,q.pending,q.running,q.finished,q.slow
FROM entity_counts e CROSS JOIN queue_counts q`).Scan(
		&o.Active, &o.LegacyPersons, &o.Candidates, &o.Corrections, &o.ConflictGroups,
		&o.Authoritative, &o.Evidenced, &o.Unverified, &o.Untiered,
		&o.Pending, &o.InProgress, &o.Finished24h, &o.OverTwoMinutes24h)
	return o, err
}
