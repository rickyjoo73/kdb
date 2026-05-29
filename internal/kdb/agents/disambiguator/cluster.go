package disambiguator

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/aliasmatch"
	"github.com/rickyjoo73/kdb/internal/kdb/hangul"
)

// member is one entity in a cluster, with the weak identity signals used for
// the evidence gate + the well-formed flag (a malformed jamo/typo form can
// never be the merge winner).
type member struct {
	id         uuid.UUID
	ko         string
	status     string
	entityType string
	role       string
	agency     string
	birthYear  int
	works      []string
	wellFormed bool
	aliasScore float64 // similarity to the cluster anchor (1.0 = exact/anchor)
}

type cluster struct {
	name    string // normalized cluster name (anchor canonical_ko)
	members []member
}

// buildClusters discovers clusters for selection. Two sources, deduped:
//
//  1. exact same canonical_ko among active+candidate entities (≥2 rows),
//  2. near-name: for each active/candidate name, aliasmatch.Find surfaces typo
//     (pg_trgm ≥0.6) / abbreviation / alias matches against active canonicals —
//     this is the wiring the design says is missing from the cycle today.
func (a *Agent) buildClusters(ctx context.Context, pool *pgxpool.Pool, budget int) ([]cluster, error) {
	var clusters []cluster

	// (1) exact same-name clusters.
	rows, err := pool.Query(ctx, `
SELECT canonical_ko
  FROM kwave_entities
 WHERE status IN ('active','candidate') AND operator_locked = false
 GROUP BY canonical_ko
HAVING count(*) > 1
 LIMIT $1`, budget)
	if err != nil {
		return nil, err
	}
	var exactNames []string
	for rows.Next() {
		var n string
		if rows.Scan(&n) == nil {
			exactNames = append(exactNames, n)
		}
	}
	rows.Close()
	for _, n := range exactNames {
		if cl := a.loadExactCluster(ctx, pool, n); cl != nil {
			clusters = append(clusters, *cl)
		}
	}

	// (2) near-name clusters via aliasmatch.Find. Seed from candidates +
	//     jamo-broken actives (the variants most likely to be a typo/nickname).
	seedRows, err := pool.Query(ctx, `
SELECT id, canonical_ko FROM kwave_entities
 WHERE status IN ('active','candidate') AND operator_locked = false
   AND (status='candidate' OR canonical_ko ~ '[ㄱ-ㅎㅏ-ㅣ]')
 ORDER BY updated_at DESC
 LIMIT $1`, budget)
	if err == nil {
		type seed struct {
			id uuid.UUID
			ko string
		}
		var seeds []seed
		for seedRows.Next() {
			var s seed
			if seedRows.Scan(&s.id, &s.ko) == nil {
				seeds = append(seeds, s)
			}
		}
		seedRows.Close()
		for _, s := range seeds {
			// repaired form helps trigram matching for jamo-broken strings.
			query := s.ko
			if repaired := hangul.StripLoneJamo(s.ko); strings.TrimSpace(repaired) != "" && repaired != s.ko {
				query = repaired
			}
			matches, _ := aliasmatch.Find(ctx, pool, query)
			if cl := a.clusterFromMatches(ctx, pool, s.id, s.ko, matches); cl != nil {
				clusters = append(clusters, *cl)
			}
		}
	}
	return clusters, nil
}

// clustersFromIDs rebuilds clusters limited to the selected id set (Run uses the
// same exact + near-name grouping so it processes the same clusters Select saw,
// but only over ids that were actually selected this cycle).
func (a *Agent) clustersFromIDs(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) []cluster {
	if pool == nil || len(ids) == 0 {
		return nil
	}
	members := a.loadMembers(ctx, pool, ids)
	// group by exact normalized name first.
	byName := map[string][]member{}
	for _, m := range members {
		byName[norm(m.ko)] = append(byName[norm(m.ko)], m)
	}
	var clusters []cluster
	grouped := map[uuid.UUID]bool{}
	for name, ms := range byName {
		if len(ms) > 1 {
			for _, m := range ms {
				grouped[m.id] = true
			}
			clusters = append(clusters, cluster{name: name, members: withWellFormed(ms)})
		}
	}
	// remaining ungrouped ids: try trigram against the selected set so a typo
	// variant pairs with its canonical even if names are not byte-equal.
	var rest []member
	for _, m := range members {
		if !grouped[m.id] {
			rest = append(rest, m)
		}
	}
	for i := range rest {
		var grp []member
		for j := range rest {
			if i == j {
				continue
			}
			if trigramClose(rest[i].ko, rest[j].ko) {
				grp = append(grp, rest[j])
			}
		}
		if len(grp) > 0 && !grouped[rest[i].id] {
			grp = append(grp, rest[i])
			for _, m := range grp {
				grouped[m.id] = true
			}
			clusters = append(clusters, cluster{name: norm(rest[i].ko), members: withWellFormed(grp)})
		}
	}
	return clusters
}

func (a *Agent) loadExactCluster(ctx context.Context, pool *pgxpool.Pool, name string) *cluster {
	ids := a.idsByName(ctx, pool, name)
	if len(ids) < 2 {
		return nil
	}
	ms := a.loadMembers(ctx, pool, ids)
	if len(ms) < 2 {
		return nil
	}
	return &cluster{name: norm(name), members: withWellFormed(ms)}
}

func (a *Agent) clusterFromMatches(ctx context.Context, pool *pgxpool.Pool, seedID uuid.UUID, seedKo string, matches []aliasmatch.Match) *cluster {
	ids := []uuid.UUID{seedID}
	scores := map[uuid.UUID]float64{seedID: 1.0}
	for _, m := range matches {
		if m.EntityID == seedID {
			continue
		}
		ids = append(ids, m.EntityID)
		scores[m.EntityID] = m.Score
	}
	if len(ids) < 2 {
		return nil
	}
	ms := a.loadMembers(ctx, pool, ids)
	if len(ms) < 2 {
		return nil
	}
	for i := range ms {
		if s, ok := scores[ms[i].id]; ok {
			ms[i].aliasScore = s
		}
	}
	return &cluster{name: norm(seedKo), members: withWellFormed(ms)}
}

func (a *Agent) idsByName(ctx context.Context, pool *pgxpool.Pool, name string) []uuid.UUID {
	rows, err := pool.Query(ctx, `
SELECT id FROM kwave_entities
 WHERE canonical_ko=$1 AND status IN ('active','candidate') AND operator_locked = false`, name)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// loadMembers loads member rows + their person-detail evidence.
func (a *Agent) loadMembers(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) []member {
	if pool == nil || len(ids) == 0 {
		return nil
	}
	rows, err := pool.Query(ctx, `
SELECT e.id, e.canonical_ko, e.status, e.entity_type::text,
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.birth_year,0), COALESCE(d.notable_works,'{}'::text[])
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.id = ANY($1)`, ids)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []member
	for rows.Next() {
		var m member
		if err := rows.Scan(&m.id, &m.ko, &m.status, &m.entityType,
			&m.role, &m.agency, &m.birthYear, &m.works); err == nil {
			out = append(out, m)
		}
	}
	return out
}

// withWellFormed marks each member's well-formed flag: a name carrying a lone
// jamo (broken RSS/OCR) is malformed and can never be the merge winner.
func withWellFormed(ms []member) []member {
	for i := range ms {
		ms[i].wellFormed = hangul.IsCleanKorean(ms[i].ko)
	}
	return ms
}

func norm(s string) string { return strings.TrimSpace(s) }

// trigramClose is a cheap in-memory near-name test for the Run-stage regroup
// (Select already used pg_trgm via aliasmatch; this avoids a DB round-trip per
// pair). It approximates similarity by shared rune bigrams ≥ 0.6 Jaccard.
func trigramClose(a, b string) bool {
	a, b = norm(a), norm(b)
	if a == "" || b == "" || a == b {
		return a == b && a != ""
	}
	ba, bb := bigrams(a), bigrams(b)
	if len(ba) == 0 || len(bb) == 0 {
		return false
	}
	inter := 0
	for g := range ba {
		if bb[g] {
			inter++
		}
	}
	union := len(ba) + len(bb) - inter
	if union == 0 {
		return false
	}
	return float64(inter)/float64(union) >= 0.6
}

func bigrams(s string) map[string]bool {
	r := []rune(s)
	out := map[string]bool{}
	for i := 0; i+1 < len(r); i++ {
		out[string(r[i:i+2])] = true
	}
	if len(r) == 1 {
		out[string(r)] = true
	}
	return out
}
