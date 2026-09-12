package kdbadmin

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb"
)

type wfSupply struct {
	Locale                                              string
	Missing, Eligible, Blocked, PolicyBlocked, Excluded int64
}

// Inventory of the actual Enricher selector, not an estimate of queue length.
// Policy-skipped is not a network failure or proof that no name exists.
func loadWorkflowSupply(ctx context.Context, pool *pgxpool.Pool) ([]wfSupply, error) {
	rows, err := pool.Query(ctx, `
WITH gaps AS (
 SELECT e.entity_type, g.locale, g.field,
        NOT (`+kdb.FillRetryPredicate("e", "g.field")+`) AS blocked,
        EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts p
                 WHERE p.entity_id=e.id AND p.field=g.field
                   AND p.input_hash=e.fill_input_hash AND p.last_source='ground-strict-skip') AS policy
 FROM kwave_entities e
 CROSS JOIN LATERAL (VALUES
 ('en','canonical_en',e.canonical_en), ('ja','canonical_ja',e.canonical_ja),
 ('vi','canonical_vi',e.canonical_vi), ('zh','canonical_zh',e.canonical_zh),
 ('zh-hant','canonical_zh_hant',e.canonical_zh_hant), ('es','canonical_es',e.canonical_es),
 ('id','canonical_id',e.canonical_id), ('pt-br','canonical_pt_br',e.canonical_pt_br)
 ) g(locale,field,value)
 WHERE e.status='active' AND COALESCE(g.value,'')=''
)
SELECT locale,count(*),
 count(*) FILTER(WHERE entity_type NOT IN ('unknown','term') AND NOT blocked),
 count(*) FILTER(WHERE entity_type NOT IN ('unknown','term') AND blocked),
 count(*) FILTER(WHERE entity_type NOT IN ('unknown','term') AND blocked AND policy),
 count(*) FILTER(WHERE entity_type IN ('unknown','term'))
FROM gaps GROUP BY locale ORDER BY locale`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []wfSupply
	for rows.Next() {
		var r wfSupply
		if err := rows.Scan(&r.Locale, &r.Missing, &r.Eligible, &r.Blocked, &r.PolicyBlocked, &r.Excluded); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type wfReadiness struct {
	Requests, GateStopped, Missing, Ambiguous, Candidate, ActiveGaps, FormsPresent int64
}

// A hashable name set avoids scanning every active entity for each request.
// This is only a presence check; it deliberately does not select an identity.
const workflowGateNotServed = `NOT EXISTS (
 SELECT 1 FROM (
  SELECT canonical_ko AS name FROM kwave_entities WHERE status='active'
  UNION SELECT unnest(aliases_ko) AS name FROM kwave_entities WHERE status='active'
 ) names WHERE names.name=q.entity_ko)`

// CURRENT name-based inventory of the last 24h intake, not per-article identity
// resolution, verification, or readiness latency. The legacy queue does not
// store the resolved entity ID, requested locales, or ready-at timestamp.
func loadWorkflowReadiness(ctx context.Context, pool *pgxpool.Pool) (wfReadiness, error) {
	var r wfReadiness
	err := pool.QueryRow(ctx, `
WITH forms AS MATERIALIZED (
 SELECT id,canonical_ko,aliases_ko,status,needs_disambig,
  (COALESCE(canonical_en,'')<>'' AND COALESCE(canonical_ja,'')<>''
   AND COALESCE(canonical_vi,'')<>'' AND COALESCE(canonical_zh,'')<>''
   AND COALESCE(canonical_zh_hant,'')<>'' AND COALESCE(canonical_es,'')<>''
   AND COALESCE(canonical_id,'')<>'' AND COALESCE(canonical_pt_br,'')<>'') AS full
 FROM kwave_entities WHERE status IN ('active','candidate')
), names AS (
 SELECT id,canonical_ko AS name FROM forms
 UNION SELECT id,unnest(aliases_ko) AS name FROM forms
), matches AS (
 SELECT n.name,count(*) AS n,bool_or(e.needs_disambig) AS review,
  bool_or(e.status='active') AS active,bool_and(e.full) AS full
 FROM names n JOIN forms e USING(id) GROUP BY n.name
), states AS (
 SELECT CASE
  WHEN q.precheck_status IN ('reject','review') THEN 'gate'
  WHEN COALESCE(m.n,0)=0 THEN 'missing'
  WHEN m.n>1 OR m.review THEN 'ambiguous'
  WHEN NOT m.active THEN 'candidate'
  WHEN m.full THEN 'forms' ELSE 'gaps' END AS state
 FROM kwave_entity_research_queue q LEFT JOIN matches m ON m.name=q.entity_ko
 WHERE q.created_at>=now()-interval '24 hours'
)
SELECT count(*),count(*) FILTER(WHERE state='gate'),count(*) FILTER(WHERE state='missing'),
 count(*) FILTER(WHERE state='ambiguous'),count(*) FILTER(WHERE state='candidate'),
 count(*) FILTER(WHERE state='gaps'),count(*) FILTER(WHERE state='forms') FROM states`).Scan(
		&r.Requests, &r.GateStopped, &r.Missing, &r.Ambiguous, &r.Candidate, &r.ActiveGaps, &r.FormsPresent)
	return r, err
}
