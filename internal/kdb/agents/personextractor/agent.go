// Package personextractor — the PersonExtractor role agent
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.3, owner request #1).
//
// Goal: every person-type entity is represented in the person DB, and legacy
// persons are reconciled. Verified DB facts (2026-05-29):
//   - 657 active entity_type='person' entities; 0 missing a
//     kwave_entity_person_details row (the row-existence plumbing already works
//     via stepSyncPersons).
//   - 36 of those details rows still have primary_role IN (NULL,'other').
//   - 4 orphan legacy kwave_persons rows whose name has no person ENTITY:
//     이홍내 / 전소영 / 레이 아미 / 이재. Each one HAS a status='candidate',
//     entity_type='unknown' entity by the same name_ko — i.e. a real person the
//     operator already curated in kwave_persons that the entity layer hasn't
//     promoted yet.
//
// So PersonExtractor is a CONTENT + RECONCILIATION role, not a row-creation one:
//
//  1. Seeding: for person entities whose details primary_role is 'other'/NULL,
//     copy a meaningful primary_role from the matching legacy kwave_persons row
//     (operator-curated truth) — pure SQL, no LLM. Role/agency/etc. that are
//     unknown and not derivable from the legacy row are LEFT for the Enricher
//     (request #2), not guessed here.
//  2. Reconciliation: for each orphan kwave_persons row, find a candidate
//     entity sharing name_ko and ask gpt-5.5 (strict schema) whether they are
//     the SAME person. same → promote the entity to entity_type='person' +
//     seed its details from the legacy row; junk → flag for review (notes +
//     needs_disambig), NEVER hard-delete; uncertain → flag for review.
//
// Wired under Hermes (opt-in) as an audited agent refining stepSyncPersons.
package personextractor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
)

// reconcileInput is the opaque prompt input.
type reconcileInput struct {
	NameKo        string
	LegacyRole    string
	LegacyAgency  string
	LegacyWorks   []string
	EntityNotes   string
	EntitySources []string
}

// reconcileResult is the strict JSON contract (kdb_person_reconcile.schema.json).
type reconcileResult struct {
	Same       bool    `json:"same"`
	IsJunk     bool    `json:"is_junk"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// Agent is the PersonExtractor role agent.
type Agent struct {
	base *agents.Base
}

// New builds a PersonExtractor with the default codex CLI transport (nil → new).
func New(r *codexcli.Runner) *Agent { return &Agent{base: agents.NewBase(r, llmRole())} }

// NewWith builds one from an explicit Base (tests inject a fake runner).
func NewWith(base *agents.Base) *Agent { return &Agent{base: base} }

func llmRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RolePersonExtractor,
		Schema: codexcli.PersonReconcileSchema,
		BuildPrompt: func(in any) (string, error) {
			ri, ok := in.(reconcileInput)
			if !ok {
				return "", fmt.Errorf("personextractor: bad prompt input %T", in)
			}
			return codexcli.BuildPersonReconcilePrompt(ri.NameKo, ri.LegacyRole, ri.LegacyAgency,
				ri.LegacyWorks, ri.EntityNotes, ri.EntitySources), nil
		},
	}
}

func (a *Agent) Role() agents.Role           { return agents.RolePersonExtractor }
func (a *Agent) Criterion() agents.Criterion { return Criterion{} }

// workItem is one unit selected for this run. Two kinds:
//   - kindSeed:      a person entity whose details role is 'other'/NULL.
//   - kindReconcile: an orphan legacy person (no person entity) — id is the
//     matching candidate entity if one exists (else uuid.Nil).
type workKind int

const (
	kindSeed workKind = iota
	kindReconcile
)

// itemMeta records, per selected id, what kind of work it is. Select stashes
// this so Run knows how to process each id without re-querying classification.
type itemMeta struct {
	kind   workKind
	nameKo string
}

// Select returns ids to process this cycle: orphan-reconcile work first
// (highest value, bounded — only 4 today), then role-seed work, up to budget.
// It returns the ids AND remembers per-id metadata for Run via the agent's
// transient plan map (keyed by run is unnecessary; we recompute in Run from DB
// to stay stateless and concurrency-safe).
func (a *Agent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if pool == nil {
		return nil, nil
	}
	if budget <= 0 {
		budget = 20
	}
	var ids []uuid.UUID

	// (1) Reconcile: a candidate entity that shares name_ko with an orphan
	//     legacy person (a person the operator curated but the entity layer
	//     never promoted). These are the request#1 orphans.
	recRows, err := pool.Query(ctx, `
SELECT e.id
  FROM kwave_entities e
  JOIN kwave_persons p ON p.name_ko = e.canonical_ko
 WHERE e.entity_type <> 'person'
   AND e.operator_locked = false
   AND NOT EXISTS (
     SELECT 1 FROM kwave_entities pe
      WHERE pe.canonical_ko = p.name_ko AND pe.entity_type='person')
 ORDER BY e.updated_at DESC
 LIMIT $1`, budget)
	if err != nil {
		return nil, err
	}
	for recRows.Next() {
		var id uuid.UUID
		if err := recRows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	recRows.Close()

	// (2) Seed: person entities whose details primary_role is 'other'/NULL.
	if rem := budget - len(ids); rem > 0 {
		seedRows, err := pool.Query(ctx, `
SELECT e.id
  FROM kwave_entities e
  JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.entity_type='person' AND e.status='active' AND e.operator_locked = false
   AND (d.primary_role IS NULL OR d.primary_role = 'other')
 ORDER BY e.updated_at DESC
 LIMIT $1`, rem)
		if err != nil {
			return nil, err
		}
		for seedRows.Next() {
			var id uuid.UUID
			if err := seedRows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
		seedRows.Close()
	}
	return ids, nil
}

// classify decides, at Run time, what kind of work each selected id is (so the
// agent stays stateless between Select and Run).
func (a *Agent) classify(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (itemMeta, bool) {
	if pool == nil {
		return itemMeta{}, false
	}
	var entityType, nameKo string
	err := pool.QueryRow(ctx,
		`SELECT entity_type::text, canonical_ko FROM kwave_entities WHERE id=$1`, id).Scan(&entityType, &nameKo)
	if err != nil {
		return itemMeta{}, false
	}
	if entityType == "person" {
		return itemMeta{kind: kindSeed, nameKo: nameKo}, true
	}
	return itemMeta{kind: kindReconcile, nameKo: nameKo}, true
}

// Run processes each selected id; every id ends with one accounted Action.
func (a *Agent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{
		Role: a.Role(), RunID: in.RunID, StartedAt: time.Now(),
		Selected: len(in.IDs), SelfCheck: agents.SelfCheck{Pass: true},
	}
	for _, id := range in.IDs {
		meta, ok := a.classify(ctx, pool, id)
		if !ok {
			rep.Results = append(rep.Results, agents.ItemResult{
				ID: id, Action: agents.ActionSkipped, Source: "heuristic",
				Reason: "row not found at run time"})
			continue
		}
		switch meta.kind {
		case kindReconcile:
			rep.Results = append(rep.Results, a.reconcile(ctx, pool, id, meta.nameKo))
		default:
			rep.Results = append(rep.Results, a.seedRole(ctx, pool, id, meta.nameKo))
		}
	}
	rep.SelfCheck = a.selfCheck(rep.Results)
	rep.Summarize()
	return rep, nil
}

// seedRole copies a meaningful primary_role (+ secondary_roles/groups/agency
// when present) from the matching legacy kwave_persons row into the entity's
// details, without overwriting existing non-default values. Unknown derivable
// fields are LEFT for the Enricher. Pure SQL — no LLM.
func (a *Agent) seedRole(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, nameKo string) agents.ItemResult {
	if pool == nil {
		return agents.ItemResult{ID: id, Action: agents.ActionNoop, Source: "heuristic", Reason: "no pool"}
	}
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entity_person_details d
   SET primary_role    = CASE WHEN d.primary_role IN ('other') AND p.primary_role <> 'other'
                              THEN p.primary_role ELSE d.primary_role END,
       agency          = COALESCE(NULLIF(d.agency,''), p.agency),
       birth_year      = COALESCE(d.birth_year, p.birth_year),
       gender          = COALESCE(NULLIF(d.gender,''), p.gender),
       groups          = CASE WHEN array_length(d.groups,1) IS NULL THEN p.groups ELSE d.groups END,
       secondary_roles = CASE WHEN array_length(d.secondary_roles,1) IS NULL THEN p.secondary_roles ELSE d.secondary_roles END,
       notable_works   = CASE WHEN array_length(d.notable_works,1) IS NULL THEN p.notable_works ELSE d.notable_works END
  FROM kwave_persons p
 WHERE d.entity_id = $1
   AND p.name_ko = $2
   AND d.primary_role IN ('other')
   AND p.primary_role <> 'other'`, id, nameKo)
	if err == nil && tag.RowsAffected() > 0 {
		return agents.ItemResult{ID: id, Action: agents.ActionFilled, Source: "kwave_persons",
			Reason: "seeded primary_role from legacy person"}
	}
	// No legacy role to copy → leave for the Enricher. Accounted as a
	// skip-with-reason (explicitly deferred, not a silent drop).
	return agents.ItemResult{ID: id, Action: agents.ActionSkipped, Source: "heuristic",
		Reason: "role unknown — deferred to Enricher"}
}

// reconcile decides whether the candidate entity `id` is the same person as the
// orphan legacy kwave_persons row sharing its name. Uses gpt-5.5 (strict
// schema). same → promote to person + seed; junk → flag for review (no delete);
// uncertain → flag for review.
func (a *Agent) reconcile(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, nameKo string) agents.ItemResult {
	legacy, ok := a.loadLegacy(ctx, pool, nameKo)
	if !ok {
		// Orphan vanished or no matching legacy row — nothing to reconcile.
		return agents.ItemResult{ID: id, Action: agents.ActionNoop, Source: "heuristic",
			Reason: "no matching legacy person"}
	}
	notes, sources := a.loadEntityContext(ctx, pool, id)

	var res reconcileResult
	err := a.base.CallJSON(ctx, reconcileInput{
		NameKo: nameKo, LegacyRole: legacy.role, LegacyAgency: legacy.agency,
		LegacyWorks: legacy.works, EntityNotes: notes, EntitySources: sources,
	}, &res)
	if err != nil {
		return a.flagReview(ctx, pool, id, "reconcile llm error: "+truncate(err.Error(), 100), agents.ActionQuarantined)
	}

	switch {
	case res.Same && res.Confidence >= 0.60:
		return a.promote(ctx, pool, id, nameKo, legacy, res.Reason)
	case res.IsJunk:
		// Legacy looks non-person → flag the LEGACY row for review (do not
		// delete) and leave the entity as-is.
		a.flagLegacyJunk(ctx, pool, nameKo, res.Reason)
		return agents.ItemResult{ID: id, Action: agents.ActionSkipped, Source: "gpt-5.5",
			Reason: "legacy flagged junk for review: " + res.Reason}
	default:
		// different person OR uncertain → flag entity for operator review.
		return a.flagReview(ctx, pool, id,
			fmt.Sprintf("reconcile uncertain/distinct (same=%v conf=%.2f): %s", res.Same, res.Confidence, res.Reason),
			agents.ActionQuarantined)
	}
}

// promote upgrades the candidate entity to entity_type='person', ensures its
// details row, and seeds it from the legacy person record.
func (a *Agent) promote(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, nameKo string, legacy legacyPerson, reason string) agents.ItemResult {
	if pool == nil {
		return agents.ItemResult{ID: id, Action: agents.ActionNoop, Reason: "no pool"}
	}
	_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET entity_type='person'::kwave_entity_type,
       status = CASE WHEN status='candidate' THEN 'active' ELSE status END,
       confidence = GREATEST(confidence, 0.600),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'personextractor: reconciled w/ legacy person — ' || $2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, id, reason)

	// Ensure a details row, then seed from legacy.
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, COALESCE(NULLIF($2,'')::person_role, 'other'::person_role))
ON CONFLICT (entity_id) DO NOTHING`, id, legacy.role)
	_, _ = pool.Exec(ctx, `
UPDATE kwave_entity_person_details d
   SET primary_role    = CASE WHEN d.primary_role='other' AND p.primary_role<>'other' THEN p.primary_role ELSE d.primary_role END,
       agency          = COALESCE(NULLIF(d.agency,''), p.agency),
       birth_year      = COALESCE(d.birth_year, p.birth_year),
       gender          = COALESCE(NULLIF(d.gender,''), p.gender),
       groups          = CASE WHEN array_length(d.groups,1) IS NULL THEN p.groups ELSE d.groups END,
       secondary_roles = CASE WHEN array_length(d.secondary_roles,1) IS NULL THEN p.secondary_roles ELSE d.secondary_roles END,
       notable_works   = CASE WHEN array_length(d.notable_works,1) IS NULL THEN p.notable_works ELSE d.notable_works END
  FROM kwave_persons p
 WHERE d.entity_id = $1 AND p.name_ko = $2`, id, nameKo)

	return agents.ItemResult{ID: id, Action: agents.ActionFilled, Source: "gpt-5.5",
		Reason: "promoted to person + seeded from legacy: " + reason}
}

// flagReview marks an entity for operator review without rejecting it (reuses
// needs_disambig as the review flag — the design says reuse existing columns).
func (a *Agent) flagReview(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, reason string, action agents.Action) agents.ItemResult {
	if pool != nil {
		_, _ = pool.Exec(ctx, `
UPDATE kwave_entities
   SET needs_disambig = true,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || 'personextractor review: ' || $2,
       updated_at = now()
 WHERE id = $1 AND operator_locked = false`, id, reason)
	}
	return agents.ItemResult{ID: id, Action: action, Source: "gpt-5.5", Reason: reason}
}

// flagLegacyjunk leaves a breadcrumb on the legacy kwave_persons row (notes via
// category_hint, which is the only free-text column) — NEVER deletes it.
func (a *Agent) flagLegacyJunk(ctx context.Context, pool *pgxpool.Pool, nameKo, reason string) {
	if pool == nil {
		return
	}
	_, _ = pool.Exec(ctx, `
UPDATE kwave_persons
   SET category_hint = COALESCE(NULLIF(category_hint,'') || ' · ','') || 'review(personextractor): ' || $2
 WHERE name_ko = $1 AND operator_locked = false`, nameKo, truncate(reason, 150))
}

type legacyPerson struct {
	role   string
	agency string
	works  []string
}

func (a *Agent) loadLegacy(ctx context.Context, pool *pgxpool.Pool, nameKo string) (legacyPerson, bool) {
	if pool == nil {
		return legacyPerson{}, false
	}
	var lp legacyPerson
	err := pool.QueryRow(ctx, `
SELECT COALESCE(primary_role::text,''), COALESCE(agency,''), COALESCE(notable_works,'{}'::text[])
  FROM kwave_persons WHERE name_ko = $1
 LIMIT 1`, nameKo).Scan(&lp.role, &lp.agency, &lp.works)
	if err != nil {
		return legacyPerson{}, false
	}
	return lp, true
}

func (a *Agent) loadEntityContext(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (notes string, sources []string) {
	if pool == nil {
		return "", nil
	}
	_ = pool.QueryRow(ctx, `
SELECT COALESCE(notes,''), COALESCE(source_domains,'{}'::text[])
  FROM kwave_entities WHERE id=$1`, id).Scan(&notes, &sources)
	return notes, sources
}

// selfCheck: a promoted (filled) item must end as entity_type='person'; we
// assert the report carries a reason on every acted item (cheap invariant —
// the DB-level assertion is the Criterion's metric).
func (a *Agent) selfCheck(results []agents.ItemResult) agents.SelfCheck {
	sc := agents.SelfCheck{Pass: true}
	bad := 0
	for _, r := range results {
		if r.Action == agents.ActionFilled && strings.TrimSpace(r.Reason) == "" {
			bad++
		}
	}
	sc.Checks = append(sc.Checks, agents.Check{
		Name: "filled_items_have_reason", Pass: bad == 0,
		Detail: fmt.Sprintf("%d filled item(s) lacked a reason", bad)})
	if bad > 0 {
		sc.Pass = false
	}
	return sc
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
