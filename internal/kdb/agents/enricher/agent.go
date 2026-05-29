// Package enricher — the Enricher role agent
// (docs/KDB_HERMES_AGENTS_DESIGN.md §B.4, owner request #2).
//
// Goal: every record in BOTH the entity DB (kwave_entities locale canonicals +
// aliases_ko) AND the person DB (kwave_entity_person_details) ends with NO
// fillable empty field — looping across Hermes cycles until no fillable gap
// remains or every remaining gap is provably source-exhausted.
//
// Per target record the agent:
//  1. computes the set of EMPTY fillable fields, skipping ones already marked
//     source-exhausted (the kwave_kdb_enrich_attempts ledger, migration 0062),
//  2. runs the cascade for ONLY the still-empty fields:
//     L2 MusicBrainz → L3 Wikidata claims → L4 gpt-5.5 (locale + person fill),
//  3. persists, NEVER overwriting an existing non-empty value, and
//  4. records per-field attempts; a field tried by every layer and still empty
//     is marked exhausted so the loop terminates and converges to 0 gaps.
//
// Prioritization (per owner): foreign-presence rows (canonical_en<>”) first;
// Korean-only rows are low priority (still filled if selected, never block).
//
// Wired under Hermes (opt-in, KDB_HERMES_ENABLED) refining step:EnrichEmpty.
package enricher

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// maxAttempts — a field tried this many times across cascades without success
// is marked source-exhausted (terminates the loop on genuinely unknowable
// fields). Each Run is at most one cascade pass per field.
const maxAttempts = 2

// totalFillableFields is the count of distinct fillable fields a person entity
// can have (9 locale/alias + 7 person-detail). Used by Select's convergence
// guard: a row whose exhausted-field count reaches this has no actionable gap.
const totalFillableFields = 16

var errBadInput = errBadInputT{}

type errBadInputT struct{}

func (errBadInputT) Error() string { return "enricher: bad prompt input type" }

// localeFields are the fillable canonical_* locale columns (request#2: en/ja/
// vi/id/es/pt_br/zh/zh_hant) plus aliases_ko.
var localeFields = []string{
	"canonical_en", "canonical_ja", "canonical_vi", "canonical_id",
	"canonical_es", "canonical_pt_br", "canonical_zh", "canonical_zh_hant",
	"aliases_ko",
}

// personFields are the fillable kwave_entity_person_details columns.
var personFields = []string{
	"agency", "birth_year", "gender", "groups",
	"notable_works", "secondary_roles", "primary_role",
}

// localeToCode maps a canonical_* column to the locale code used by the fill
// cascade / orchestrator helpers.
var localeToCode = map[string]string{
	"canonical_en": "en", "canonical_ja": "ja", "canonical_vi": "vi",
	"canonical_id": "id", "canonical_es": "es", "canonical_pt_br": "pt_br",
	"canonical_zh": "zh", "canonical_zh_hant": "zh_hant",
}

// sourceClients abstracts the external lookup sources so tests inject fakes.
type sourceClients struct {
	mb *musicbrainz.Client
	wd *wikidata.Client
}

// Agent is the Enricher role agent.
type Agent struct {
	localeBase *agents.Base // L4 locale fill (kdb_fill_locale)
	personBase *agents.Base // L4 person-detail fill (kdb_fill_person)
	src        sourceClients
}

// New builds an Enricher with the default codex transport + live source clients.
func New(r *codexcli.Runner) *Agent {
	return &Agent{
		localeBase: agents.NewBase(r, localeLLMRole()),
		personBase: agents.NewBase(r, personLLMRole()),
		src:        sourceClients{mb: musicbrainz.New(), wd: wikidata.New()},
	}
}

// NewWith builds an Enricher from explicit bases (tests inject fake runners and
// nil source clients to exercise the cascade deterministically).
func NewWith(localeBase, personBase *agents.Base, mb *musicbrainz.Client, wd *wikidata.Client) *Agent {
	return &Agent{localeBase: localeBase, personBase: personBase, src: sourceClients{mb: mb, wd: wd}}
}

func localeLLMRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleEnricher,
		Schema: codexcli.FillLocaleSchema,
		BuildPrompt: func(in any) (string, error) {
			fi, ok := in.(*aijudge.FillInput)
			if !ok {
				return "", errBadInput
			}
			var wd *codexcli.Wikidata
			if fi.Wikidata != nil {
				wd = &codexcli.Wikidata{QID: fi.Wikidata.QID, Label: fi.Wikidata.Label, Description: fi.Wikidata.Description}
			}
			return codexcli.BuildFillLocalePrompt(fi.Ko, fi.EntityType, fi.PrimaryRole, fi.AliasesKo, fi.Known, fi.Missing, wd, fi.Sitelinks), nil
		},
	}
}

func personLLMRole() agents.LLMRole {
	return agents.LLMRole{
		Role:   agents.RoleEnricher,
		Schema: codexcli.FillPersonSchema,
		BuildPrompt: func(in any) (string, error) {
			pi, ok := in.(personFillInput)
			if !ok {
				return "", errBadInput
			}
			return codexcli.BuildFillPersonPrompt(pi.Ko, pi.PrimaryRole, pi.Agency, pi.Groups, pi.Works, pi.Missing, nil), nil
		},
	}
}

func (a *Agent) Role() agents.Role           { return agents.RoleEnricher }
func (a *Agent) Criterion() agents.Criterion { return Criterion{} }

// Select returns up to budget ids to enrich this cycle. Foreign-presence rows
// (canonical_en<>”) that still have a non-exhausted fillable gap come first;
// then Korean-only rows. A row whose every gap is already exhausted within the
// cooldown is excluded so the agent converges instead of re-selecting forever.
func (a *Agent) Select(ctx context.Context, pool *pgxpool.Pool, budget int) ([]uuid.UUID, error) {
	if pool == nil {
		return nil, nil
	}
	if budget <= 0 {
		budget = 20
	}
	rows, err := pool.Query(ctx, `
SELECT e.id
  FROM kwave_entities e
  LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
 WHERE e.status='active' AND e.operator_locked = false
   AND e.entity_type <> 'unknown'
   AND (
     COALESCE(e.canonical_en,'')='' OR COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_vi,'')=''
     OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_es,'')='' OR COALESCE(e.canonical_pt_br,'')=''
     OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')=''
     OR e.aliases_ko IS NULL OR array_length(e.aliases_ko,1) IS NULL
     OR (e.entity_type='person' AND (
          COALESCE(d.agency,'')='' OR d.birth_year IS NULL OR COALESCE(d.gender,'')=''
          OR array_length(d.groups,1) IS NULL OR array_length(d.notable_works,1) IS NULL
          OR array_length(d.secondary_roles,1) IS NULL
          OR d.primary_role IS NULL OR d.primary_role='other'))
   )
   AND ($2 - COALESCE((SELECT count(*) FROM kwave_kdb_enrich_attempts a
                        WHERE a.entity_id = e.id AND a.exhausted = true
                          AND a.last_attempt_at > now() - interval '7 days'), 0)) > 0
 ORDER BY (COALESCE(e.canonical_en,'')<>'') DESC, e.confidence DESC, e.updated_at ASC
 LIMIT $1`, budget, totalFillableFields)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	return ids, rows.Err()
}

// Run enriches each selected id; every id ends with exactly one accounted
// Action (filled when a gap was closed, noop when nothing fillable remained,
// skipped when all gaps are source-exhausted, errored on a row-load failure).
func (a *Agent) Run(ctx context.Context, pool *pgxpool.Pool, in agents.RunInput) (agents.RunReport, error) {
	rep := agents.RunReport{
		Role: a.Role(), RunID: in.RunID, StartedAt: time.Now(),
		Selected: len(in.IDs), SelfCheck: agents.SelfCheck{Pass: true},
	}
	for _, id := range in.IDs {
		rep.Results = append(rep.Results, a.enrichOne(ctx, pool, id))
	}
	rep.SelfCheck = a.selfCheck(rep.Results)
	rep.Summarize()
	return rep, nil
}
