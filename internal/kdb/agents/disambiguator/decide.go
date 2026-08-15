package disambiguator

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/homonym"
)

// mergeConfThreshold — minimum LLM confidence to act on a merge (design §B.5).
const mergeConfThreshold = 0.70

// processCluster asks gpt-5.5 to judge the cluster, then applies each decision
// under the evidence gate. Returns one ItemResult per cluster member.
func (a *Agent) processCluster(ctx context.Context, pool *pgxpool.Pool, cl cluster) []agents.ItemResult {
	if len(cl.members) < 2 {
		var out []agents.ItemResult
		for _, m := range cl.members {
			out = append(out, agents.ItemResult{ID: m.id, Action: agents.ActionNoop,
				Source: "heuristic", Reason: "singleton cluster"})
		}
		return out
	}

	in := clusterInput{Name: cl.name, Members: toPromptMembers(cl.members)}
	var res disambigResult
	if err := a.base.CallJSON(ctx, in, &res); err != nil {
		// LLM failure → quarantine every member (accounted, not dropped).
		var out []agents.ItemResult
		for _, m := range cl.members {
			out = append(out, a.quarantine(ctx, pool, m.id, "disambig llm error: "+truncate(err.Error(), 100)))
		}
		return out
	}

	byID := map[string]member{}
	for _, m := range cl.members {
		byID[m.id.String()] = m
	}
	decided := map[uuid.UUID]agents.ItemResult{}
	for _, asg := range res.Assignments {
		mid, err := uuid.Parse(asg.ID)
		if err != nil {
			continue
		}
		m, ok := byID[asg.ID]
		if !ok {
			continue
		}
		decided[mid] = a.applyDecision(ctx, pool, cl, m, asg, byID)
	}
	// Any member the model omitted → quarantine (accounted).
	var out []agents.ItemResult
	for _, m := range cl.members {
		if r, ok := decided[m.id]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, a.quarantine(ctx, pool, m.id, "no assignment returned by model"))
	}
	return out
}

// applyDecision enforces the evidence gate then writes the decision.
func (a *Agent) applyDecision(ctx context.Context, pool *pgxpool.Pool, cl cluster, m member, asg memberResult, byID map[string]member) agents.ItemResult {
	switch asg.Decision {
	case "merge":
		return a.applyMerge(ctx, pool, m, asg, byID)
	case "distinct":
		return a.applyDistinct(ctx, pool, m, asg)
	default: // uncertain (or unknown)
		return a.quarantine(ctx, pool, m.id, "uncertain: "+asg.Reason)
	}
}

// applyMerge folds the variant member into the canonical winner's aliases_ko and
// retires the variant — but only after the evidence gate passes and the winner
// is well-formed. Violations downgrade to distinct/quarantine.
func (a *Agent) applyMerge(ctx context.Context, pool *pgxpool.Pool, loser member, asg memberResult, byID map[string]member) agents.ItemResult {
	if asg.Confidence < mergeConfThreshold {
		return a.quarantine(ctx, pool, loser.id, "merge below confidence threshold")
	}
	if asg.SameAs == nil || strings.TrimSpace(*asg.SameAs) == "" {
		return a.quarantine(ctx, pool, loser.id, "merge missing same_as winner")
	}
	winner, ok := byID[strings.TrimSpace(*asg.SameAs)]
	if !ok || winner.id == loser.id {
		return a.quarantine(ctx, pool, loser.id, "merge same_as not in cluster")
	}
	// The winner must be the well-formed form; never retire onto a malformed one.
	if !winner.wellFormed && loser.wellFormed {
		return a.quarantine(ctx, pool, loser.id, "refused: proposed winner is malformed")
	}
	// EVIDENCE GATE: never merge two members whose identity signals conflict.
	if homonym.Conflict(signals(loser), signals(winner)) {
		return a.applyDistinct(ctx, pool, loser, memberResult{
			Disambig: asg.Disambig, Reason: "evidence conflict → kept distinct (not merged)"})
	}
	// 정확히 같은 이름(진짜 동명이인)인데 양성 증거(같은 agency/birth_year/role/작품)가
	// 전혀 없으면, LLM 추정만으로 서로 다른 두 사람을 합칠 위험이 크다 → 운영자 리뷰
	// 보류. 스펠링 변형 merge(loser.ko ≠ winner.ko)는 이 가드에 걸리지 않는다.
	if strings.TrimSpace(loser.ko) == strings.TrimSpace(winner.ko) &&
		!homonym.Compatible(signals(loser), signals(winner)) {
		return a.quarantine(ctx, pool, loser.id,
			"exact homonym without corroborating evidence — review before merge")
	}
	if pool != nil {
		// ★패자의 신원 근거를 먼저 승자로 옮긴다(2026-08-15). 은퇴시키기 전에 해야 한다.
		carryEvidence(ctx, pool, loser.id, winner.id)
		// Append loser's canonical (+ its aliases) to the winner's aliases_ko.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities w
   SET aliases_ko = (SELECT ARRAY(SELECT DISTINCT x
                       FROM unnest(COALESCE(w.aliases_ko,'{}'::text[]) || ARRAY[$2::text] || COALESCE(l.aliases_ko,'{}'::text[])) x
                      WHERE x <> '' AND x <> w.canonical_ko)),
       updated_at = now()
  FROM kwave_entities l
 WHERE w.id = $1 AND l.id = $3`, winner.id, loser.ko, loser.id)
		// Retire the loser entity (no hard delete) with a breadcrumb.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', needs_disambig=false,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator: merged into '||$2||' ('||$3||')',
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, loser.id, winner.ko, relationOf(asg))
		// Reassign the loser's person-detail row to the winner only if the
		// winner has none (never clobber a richer winner record).
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_person_details d
   SET entity_id = $2
 WHERE d.entity_id = $1
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_person_details w WHERE w.entity_id = $2)`,
			loser.id, winner.id)
	}
	return agents.ItemResult{ID: loser.id, Action: agents.ActionMerged, Source: "gpt-5.5",
		Conf: asg.Confidence, Reason: "merged into " + winner.ko + " (" + relationOf(asg) + "): " + asg.Reason}
}

// carryLocales — 패자에서 승자로 승계할 canonical 로케일. `ko` 는 뺀다 — 승자의 ko 는
// 그 엔티티의 정체이고, 패자의 ko 는 이미 aliases_ko 로 들어간다.
var carryLocales = []string{"en", "ja", "zh", "zh_hant", "vi", "es", "id", "pt_br"}

// tierRank — verification_tier 의 서열. 승계에서 "더 강한 쪽"을 고르는 데 쓴다.
func tierRank(t string) int {
	switch t {
	case "authoritative":
		return 3
	case "evidenced":
		return 2
	case "unverified":
		return 1
	}
	return 0
}

// carryEvidence — 병합에서 은퇴하는 패자의 **신원 근거를 승자로 옮긴다.**
//
// ★왜 필요한가(2026-08-15 실측). 종전 applyMerge 는 `aliases_ko` 와 person_details 만
// 옮기고 나머지를 패자와 함께 죽였다. 승자는 LLM 의 `same_as` 가 지목하고 가드는
// `wellFormed` 하나뿐이라, **근거가 없는 쪽이 승자로 뽑히면 근거가 통째로 사라진다.**
//
// 실제로 그렇게 됐다:
//
//	MONSTA X   (source_urls 9 · wikidata ref 1 · authoritative) → rejected
//	몬스타엑스  (source_urls 9 · ref 1)                          → rejected
//	몬스타X    (0 · 0 · unverified)                             → active (서빙 중)
//
// 정규화 동명으로만 세어도 같은 꼴이 6건 더 있다(`호텔 델 루나`↔`호텔 델루나` 근거 11,
// `YG엔터테인먼트`↔`YG 엔터테인먼트` 10, `보아` 10, `KBS2TV` 9 …). 한글↔라틴 변형은
// 그 정규화로 안 잡히니 실제 규모는 더 크다.
//
// **승자를 바꾸지 않고 근거를 옮기는 이유**: 어느 쪽이 살아남을지는 정체 판단이 아니라
// 표기 선택이고, 한국어 DB 에서는 한글 표기가 canonical_ko 로 남는 편이 옳다
// (`몬스타X` > `MONSTA X`). 피해는 이름이 아니라 **근거 소실**이므로 거기를 고친다.
//
// 모든 이관은 **승자가 비어 있을 때만** 쓴다 — 더 풍부한 승자를 덮지 않는다
// (person_details 이관이 이미 쓰던 원칙과 같다).
func carryEvidence(ctx context.Context, pool *pgxpool.Pool, loserID, winnerID uuid.UUID) {
	if pool == nil {
		return
	}
	// 1) 외부 식별자 — 승자에게 없는 provider 만. PK 가 (entity_id, provider) 다.
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
SELECT $2, r.provider, r.external_id, r.url, r.confidence, COALESCE(r.raw_payload,'{}'::jsonb), r.fetched_at
  FROM kwave_entity_external_refs r
 WHERE r.entity_id = $1
ON CONFLICT (entity_id, provider) DO NOTHING`, loserID, winnerID)

	// 2) 근거 URL 목록 — 합집합.
	_, _ = pool.Exec(ctx, `
UPDATE kwave_entities w
   SET source_urls = (SELECT ARRAY(SELECT DISTINCT u
                        FROM unnest(COALESCE(w.source_urls,'{}'::text[]) || COALESCE(l.source_urls,'{}'::text[])) u
                       WHERE u <> '')),
       updated_at = now()
  FROM kwave_entities l
 WHERE w.id = $2 AND l.id = $1
   AND COALESCE(array_length(l.source_urls,1),0) > 0`, loserID, winnerID)

	// 3) 근거 대장 — (entity_id, url) 유니크라 중복은 건너뛴다.
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_evidence_refs (entity_id, lane, url, title, provider, snippet)
SELECT $2, v.lane, v.url, v.title, v.provider, v.snippet
  FROM kwave_kdb_evidence_refs v
 WHERE v.entity_id = $1
ON CONFLICT (entity_id, url) DO NOTHING`, loserID, winnerID)

	// 4) 로케일 표기 — 승자의 **빈칸만** 채운다. 출처 라벨도 함께 옮겨야 값의 내력이 남는다.
	//
	// ★출처로 거르지 않는다(2026-08-15, 실측으로 결론). 처음엔 기계추측 출처
	// (gtranslate·codex-fallback)를 빼려 했는데, 실제로 이관된 16칸을 전건 확인하니
	// **14칸이 정확했다** — `헌트릭스→ハントリックス/HUNTR/X` `동치미→ドンチミ`
	// `TWS→TWS` `라이즈→RIIZE` `10CM→10cm`. 틀린 건 `동치미 zh=洞察力` 하나다.
	// 좋은 값 14개를 버려 나쁜 값 1개를 막는 건 잘못된 거래다.
	//
	// 애초에 이 값들은 패자가 **active 였을 때 그 시점 게이트를 통과해 쓰인 것**이고,
	// 승자가 나중에 앵커를 얻으면 wd-locale·tmdb-locale 이 기계추측 출처를 덮어쓴다
	// (wikidataOverwritableSources 에 둘 다 있다). 즉 약한 값이라도 고착되지 않는다.
	for _, loc := range carryLocales {
		col, src := "canonical_"+loc, "canonical_"+loc+"_source"
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities w
   SET `+col+` = l.`+col+`, `+src+` = l.`+src+`, updated_at = now()
  FROM kwave_entities l
 WHERE w.id = $2 AND l.id = $1
   AND COALESCE(w.`+col+`,'') = '' AND COALESCE(l.`+col+`,'') <> ''`, loserID, winnerID)
	}

	// 5) 검증 등급 — 패자가 더 높으면 승계한다. 위에서 ref/URL 을 이미 옮겼으므로
	//    등급의 지시대상도 함께 왔다(등급만 올라가 근거가 비는 일은 생기지 않는다).
	var loserTier, winnerTier, loserEv string
	if err := pool.QueryRow(ctx, `
SELECT COALESCE(l.verification_tier,''), COALESCE(w.verification_tier,''), COALESCE(l.verification_evidence,'')
  FROM kwave_entities l, kwave_entities w WHERE l.id=$1 AND w.id=$2`,
		loserID, winnerID).Scan(&loserTier, &winnerTier, &loserEv); err == nil &&
		tierRank(loserTier) > tierRank(winnerTier) {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET verification_tier=$2, verification_evidence=$3, verified_tier_at=now(), updated_at=now()
 WHERE id=$1`, winnerID, loserTier, loserEv)
	}
}

// applyDistinct sets a distinct disambig label so the homonym unique index
// (migration 0060) admits the row as a legitimate homonym. Clears needs_disambig.
func (a *Agent) applyDistinct(ctx context.Context, pool *pgxpool.Pool, m member, asg memberResult) agents.ItemResult {
	label := ""
	if asg.Disambig != nil {
		label = strings.TrimSpace(*asg.Disambig)
	}
	if label == "" {
		label = homonym.SuggestDisambig(signals(m))
	}
	if label == "" {
		// Genuinely distinct but no label derivable → quarantine for review so
		// the homonym index does not collide two empty-disambig rows.
		return a.quarantine(ctx, pool, m.id, "distinct but no disambig label derivable")
	}
	if pool != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET disambig = $2, needs_disambig = false,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator: distinct '||$2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, m.id, label)
	}
	return agents.ItemResult{ID: m.id, Action: agents.ActionSplit, Source: "gpt-5.5",
		Reason: "distinct person → disambig " + label + ": " + asg.Reason}
}

// quarantine flags a member for operator review (needs_disambig) WITHOUT merging
// or splitting — accounted, never a silent drop (fixes the A.3 leak: every
// quarantined member carries a review breadcrumb).
func (a *Agent) quarantine(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, reason string) agents.ItemResult {
	if pool != nil {
		// disambig_reviewed_at stamps the cooldown so this metadata-less cluster
		// is not re-selected and re-judged every cycle (migration 0065). NOTE:
		// updated_at is intentionally NOT bumped — the near-name seed Select
		// orders by updated_at DESC, and bumping it would push a quarantined item
		// to the front of the queue (the original spin). The review breadcrumb in
		// notes + needs_disambig + disambig_reviewed_at are the audit trail.
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET needs_disambig = true,
       disambig_reviewed_at = now(),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator review: ' || $2
 WHERE id = $1 AND operator_locked = false`, id, truncate(reason, 150))
	}
	return agents.ItemResult{ID: id, Action: agents.ActionQuarantined, Source: "gpt-5.5", Reason: reason}
}

func signals(m member) homonym.PersonSignals {
	return homonym.PersonSignals{
		PrimaryRole:  m.role,
		Agency:       m.agency,
		NotableWorks: m.works,
		BirthYear:    m.birthYear,
	}
}

func toPromptMembers(ms []member) []codexcli.DisambigMember {
	out := make([]codexcli.DisambigMember, 0, len(ms))
	for _, m := range ms {
		out = append(out, codexcli.DisambigMember{
			ID: m.id.String(), Name: m.ko, Role: m.role, Agency: m.agency,
			Works: m.works, WellFormed: m.wellFormed, AliasScore: m.aliasScore,
		})
	}
	return out
}

func relationOf(asg memberResult) string {
	if asg.Relation != nil && strings.TrimSpace(*asg.Relation) != "" {
		return strings.TrimSpace(*asg.Relation)
	}
	return "same"
}

func truncate(s string, n int) string {
	// rune 기준 절단 (멀티바이트 rune 쪼갬 방지 — 깨진 UTF-8 이 notes 에 안 남게).
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
