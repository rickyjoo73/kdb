package disambiguator

import (
	"context"
	"regexp"
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
	qid        string // wikidata QID — used for the same-anchor cluster source (4)
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

	// (4) same Wikidata QID + entity_type, different canonical_ko.
	//
	// ★왜 이 소스가 필요한가(2026-08-15 실측): (1)~(3) 은 전부 **이름 표기**로 군집한다 —
	// 같은 ko, 유사 ko(trigram), 같은 en. 그래서 **본명 ↔ 활동명** 중복은 셋 다 놓친다:
	// `정호석`/`제이홉`, `김남준`/`RM`, `도경수`/`디오`, `안희연`/`하니` 는 ko 도 en 도
	// 겹치지 않고 trigram 도 0 이다. 실측 46쌍 중 34쌍이 기존 소스에 안 잡혔다.
	//
	// QID 동일은 "같은 실체"에 대한 **결정론적** 주장이라 LLM 없이 고를 수 있다. ja 표기
	// 충돌을 소스로 쓰려다 접었는데, 표본을 읽어보니 그쪽은 대부분 진짜 별인이었다
	// (`이제훈`/`이재훈`, `김선호`/`김성호`, `김소현`/`김서형` — 한→일 음역이 같아질 뿐이다).
	// 별인을 자동 병합 레인에 밀어넣는 건 근거 게이트가 있어도 낭비이자 위험이다.
	//
	// 주의: QID 자체가 오부착일 수 있다(한쪽에만 잘못 붙은 경우). 그래서 여기서 병합을
	// 단정하지 않고 **군집만 만들어** Disambiguator 의 근거 게이트·격리에 넘긴다.
	qidRows, err := pool.Query(ctx, `
SELECT r.external_id, array_agg(e.id ORDER BY e.confidence DESC)
  FROM kwave_entity_external_refs r
  JOIN kwave_entities e ON e.id = r.entity_id
 WHERE r.provider = 'wikidata' AND r.external_id ~ '^Q[0-9]+$'
   AND e.status IN ('active','candidate') AND e.operator_locked = false
   AND (e.disambig_reviewed_at IS NULL OR e.disambig_reviewed_at < now() - $2::interval)
 GROUP BY r.external_id, e.entity_type
HAVING count(*) > 1 AND count(DISTINCT e.canonical_ko) > 1
 LIMIT $1`, budget, reviewCooldown)
	if err == nil {
		seenIDs := map[uuid.UUID]bool{}
		for _, cl := range clusters {
			for _, m := range cl.members {
				seenIDs[m.id] = true
			}
		}
		type qidGroup struct {
			qid string
			ids []uuid.UUID
		}
		var qidGroups []qidGroup
		for qidRows.Next() {
			var g qidGroup
			if qidRows.Scan(&g.qid, &g.ids) == nil && len(g.ids) >= 2 {
				qidGroups = append(qidGroups, g)
			}
		}
		qidRows.Close()
		for _, g := range qidGroups {
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
				clusters = append(clusters, cluster{name: "qid:" + g.qid, members: withWellFormed(ms)})
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
	// group by exact normalized name first. ★키는 normKey(공백·따옴표 무시), 라벨은
	// 실제 이름을 쓴다 — 키를 라벨로 쓰면 프롬프트에 `스트레이키즈` 처럼 붙여쓴 이름이 간다.
	byName := map[string][]member{}
	for _, m := range members {
		byName[normKey(m.ko)] = append(byName[normKey(m.ko)], m)
	}
	var clusters []cluster
	grouped := map[uuid.UUID]bool{}
	for _, ms := range byName {
		if len(ms) > 1 {
			for _, m := range ms {
				grouped[m.id] = true
			}
			clusters = append(clusters, cluster{name: norm(ms[0].ko), members: withWellFormed(ms)})
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

	// same-QID: 남은 멤버 중 같은 Wikidata 앵커를 공유하는 것들. buildClusters 의 소스 (4)
	// 와 **짝이 맞아야 한다** — Select 가 QID 로 고른 멤버를 Run 이 다시 묶지 못하면
	// 선정만 되고 아무 일도 일어나지 않는다(본명/활동명은 ko·en·trigram 어디에도 안 걸린다).
	var qidRest []member
	for _, m := range rest {
		if !grouped[m.id] {
			qidRest = append(qidRest, m)
		}
	}
	for _, ms := range groupByQID(qidRest) {
		for _, m := range ms {
			grouped[m.id] = true
		}
		clusters = append(clusters, cluster{name: "qid:" + ms[0].qid, members: withWellFormed(ms)})
	}

	return clusters
}

// groupByQID — 같은 Wikidata QID + 같은 entity_type 인 멤버를 묶는다(2명 이상만).
// QID 가 없는 멤버는 제외한다 — 빈 문자열끼리 뭉치면 무관한 엔티티가 한 군집이 된다.
func groupByQID(ms []member) [][]member {
	by := map[string][]member{}
	var order []string
	for _, m := range ms {
		if strings.TrimSpace(m.qid) == "" {
			continue
		}
		k := m.qid + "\x00" + m.entityType
		if _, seen := by[k]; !seen {
			order = append(order, k)
		}
		by[k] = append(by[k], m)
	}
	var out [][]member
	for _, k := range order {
		if len(by[k]) >= 2 {
			out = append(out, by[k])
		}
	}
	return out
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
SELECT e.id, e.canonical_ko, COALESCE(e.canonical_en,''),
       COALESCE((SELECT r.external_id FROM kwave_entity_external_refs r
                  WHERE r.entity_id = e.id AND r.provider = 'wikidata' LIMIT 1),''),
       e.status, e.entity_type::text,
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
		if err := rows.Scan(&m.id, &m.ko, &m.en, &m.qid, &m.status, &m.entityType,
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

// normKeyRe — 군집 키에서 지우는 문자: 모든 공백류 + 아포스트로피/따옴표 변형.
var normKeyRe = regexp.MustCompile(`[\s'` + "`" + `\x{2018}\x{2019}\x{201C}\x{201D}\x{FF07}\x{00B4}"]+`)

// normKey — **군집 키 전용** 정규화. norm(라벨용, TrimSpace) 과 일부러 분리했다.
//
// ★왜(2026-08-09): active 중복 13쌍을 전수로 뽑아보니 **전부 공백 아니면 따옴표 변형만
// 다른 같은 엔티티**였다(`스트레이 키즈`/`스트레이키즈` · `신병4: 사보타주`/`신병4 : 사보타주`
// · `Time’s Tickin’`/`Time’s Tickin'`). 종전 키는 양끝 공백만 잘라서 이들이 **애초에 같은
// 군집에 못 들어갔다.**
//
// 폴백인 trigramClose 도 못 잡았다. 공백이 가짜 bigram 을 만들기 때문이다 —
// `스트레이 키즈`(스트,트레,레이,"이 "," 키",키즈) vs `스트레이키즈`(스트,트레,레이,이키,키즈)
// 는 교집합 4·합집합 7 로 **Jaccard 0.571**, 임계 0.6 에 아슬하게 미달한다. 공백 하나가
// 유사도를 깎아 내리는 구조라 임계를 낮추는 건 답이 아니다 — 키에서 공백을 빼야 한다.
//
// ★이 확장은 "자동 병합"이 아니라 "판정 후보 증가"다. 군집은 decide 로 가고, 실제 병합은
// applyMerge 의 homonym.Conflict 게이트가 막는다(criterion.go 주석). 근거가 충돌하는 둘은
// 여기서 같은 키가 돼도 distinct 로 내려간다.
func normKey(s string) string {
	return strings.ToLower(normKeyRe.ReplaceAllString(strings.TrimSpace(s), ""))
}

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
