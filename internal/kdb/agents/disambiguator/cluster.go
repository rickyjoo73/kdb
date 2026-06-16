package disambiguator

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/aliasmatch"
	"github.com/rickyjoo73/kdb/internal/kdb/hangul"
)

// reviewCooldown — how long a member is skipped after a Disambiguator verdict
// before it may be re-selected. Resolved members (merge/distinct) clear
// needs_disambig and drop out anyway; this only governs re-evaluation of
// quarantined (uncertain) members, giving enrich a window to add the metadata
// that would let a later pass actually resolve them. As a Postgres interval
// literal (used in the Select WHERE).
const reviewCooldown = "14 days"

// member is one entity in a cluster, with the weak identity signals used for
// the evidence gate + the well-formed flag (a malformed jamo/typo form can
// never be the merge winner).
type member struct {
	id         uuid.UUID
	ko         string
	en         string // canonical_en — used for cross-script cluster grouping
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

// buildClusters discovers clusters for selection. Three sources, deduped:
//
//  1. exact same canonical_ko among active+candidate entities (≥2 rows),
//  2. near-name: for each active/candidate name, aliasmatch.Find surfaces typo
//     (pg_trgm ≥0.6) / abbreviation / alias matches against active canonicals,
//  3. same canonical_en + same entity_type but different canonical_ko (cross-
//     script pairs like 보이넥스트도어 ↔ BOYNEXTDOOR, 미야오 ↔ MEOVV).
func (a *Agent) buildClusters(ctx context.Context, pool *pgxpool.Pool, budget int) ([]cluster, error) {
	var clusters []cluster

	// (1) exact same-name clusters. The cooldown filter (disambig_reviewed_at)
	// drops members the Disambiguator already judged within reviewCooldown so an
	// unresolvable (metadata-less) cluster does not get re-selected every cycle.
	rows, err := pool.Query(ctx, `
SELECT canonical_ko
  FROM kwave_entities
 WHERE status IN ('active','candidate') AND operator_locked = false
   AND (disambig_reviewed_at IS NULL OR disambig_reviewed_at < now() - $2::interval)
 GROUP BY canonical_ko
HAVING count(*) > 1
 LIMIT $1`, budget, reviewCooldown)
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
   AND (disambig_reviewed_at IS NULL OR disambig_reviewed_at < now() - $2::interval)
 ORDER BY updated_at DESC
 LIMIT $1`, budget, reviewCooldown)
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
	// (3) same canonical_en + entity_type clusters (cross-script).
	// 보이넥스트도어↔BOYNEXTDOOR 같이 한글명·영문명 표기가 혼용되는 케이스를 처리한다.
	// 한글 trigram 은 공유 bigram이 0 이라 (2)에서 매칭 불가 — 이 경로만이 유일한 해소 경로.
	enRows, err := pool.Query(ctx, `
SELECT lower(canonical_en), array_agg(id ORDER BY confidence DESC)
  FROM kwave_entities
 WHERE status IN ('active','candidate') AND operator_locked = false
   AND COALESCE(canonical_en,'') <> ''
   AND (disambig_reviewed_at IS NULL OR disambig_reviewed_at < now() - $2::interval)
 GROUP BY lower(canonical_en), entity_type
HAVING count(*) > 1
 LIMIT $1`, budget, reviewCooldown)
	if err == nil {
		seenIDs := map[uuid.UUID]bool{}
		for _, cl := range clusters {
			for _, m := range cl.members {
				seenIDs[m.id] = true
			}
		}
		type enGroup struct {
			en  string
			ids []uuid.UUID
		}
		var enGroups []enGroup
		for enRows.Next() {
			var g enGroup
			if enRows.Scan(&g.en, &g.ids) == nil && len(g.ids) >= 2 {
				enGroups = append(enGroups, g)
			}
		}
		enRows.Close()
		for _, g := range enGroups {
			// 이미 ko 기반 클러스터에서 처리된 멤버는 제외.
			var fresh []uuid.UUID
			for _, id := range g.ids {
				if !seenIDs[id] {
					fresh = append(fresh, id)
				}
			}
			if len(fresh) < 2 {
				continue
			}
			ms := a.loadMembers(ctx, pool, fresh)
			if len(ms) >= 2 {
				clusters = append(clusters, cluster{name: "en:" + g.en, members: withWellFormed(ms)})
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

	// cross-script: remaining members that share canonical_en + entity_type.
	var enRest []member
	for _, m := range rest {
		if !grouped[m.id] {
			enRest = append(enRest, m)
		}
	}
	byEn := map[string][]member{}
	for _, m := range enRest {
		if strings.TrimSpace(m.en) == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(m.en)) + "\x00" + m.entityType
		byEn[key] = append(byEn[key], m)
	}
	for _, ms := range byEn {
		if len(ms) < 2 {
			continue
		}
		for _, m := range ms {
			grouped[m.id] = true
		}
		clusters = append(clusters, cluster{
			name:    "en:" + strings.ToLower(strings.TrimSpace(ms[0].en)),
			members: withWellFormed(ms),
		})
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
SELECT e.id, e.canonical_ko, COALESCE(e.canonical_en,''), e.status, e.entity_type::text,
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
		if err := rows.Scan(&m.id, &m.ko, &m.en, &m.status, &m.entityType,
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
