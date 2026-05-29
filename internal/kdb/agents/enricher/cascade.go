package enricher

import (
	"context"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
)

// record is the per-entity state the cascade reads + the gap set.
type record struct {
	id          uuid.UUID
	ko          string
	entityType  string
	aliasesKo   []string
	localeVals  map[string]string // canonical_<loc> column → current value ("" = empty)
	localeSrc   map[string]string // canonical_<loc>_source
	primaryRole string
	agency      string
	gender      string
	birthYear   int
	groups      []string
	works       []string
	secondary   []string
	isPerson    bool
}

// personFillInput is the opaque input to the L4 person-detail prompt.
type personFillInput struct {
	Ko          string
	PrimaryRole string
	Agency      string
	Groups      []string
	Works       []string
	Missing     []string
}

// personFillResult mirrors kdb_fill_person.schema.json.
type personFillResult struct {
	Fields struct {
		Agency         *string  `json:"agency"`
		Gender         *string  `json:"gender"`
		BirthYear      *int     `json:"birth_year"`
		PrimaryRole    *string  `json:"primary_role"`
		SecondaryRoles []string `json:"secondary_roles"`
		Groups         []string `json:"groups"`
		NotableWorks   []string `json:"notable_works"`
	} `json:"fields"`
	Skipped []string `json:"skipped"`
}

// loadRecord reads the current entity + person-detail state.
func (a *Agent) loadRecord(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) (*record, bool) {
	if pool == nil {
		return nil, false
	}
	r := &record{id: id, localeVals: map[string]string{}, localeSrc: map[string]string{}}
	var en, enS, ja, jaS, vi, viS, idl, idS, es, esS, pt, ptS, zh, zhS, zhh, zhhS string
	err := pool.QueryRow(ctx, `
SELECT canonical_ko, entity_type::text, COALESCE(aliases_ko,'{}'::text[]),
       COALESCE(canonical_en,''),      COALESCE(canonical_en_source,''),
       COALESCE(canonical_ja,''),      COALESCE(canonical_ja_source,''),
       COALESCE(canonical_vi,''),      COALESCE(canonical_vi_source,''),
       COALESCE(canonical_id,''),      COALESCE(canonical_id_source,''),
       COALESCE(canonical_es,''),      COALESCE(canonical_es_source,''),
       COALESCE(canonical_pt_br,''),   COALESCE(canonical_pt_br_source,''),
       COALESCE(canonical_zh,''),      COALESCE(canonical_zh_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,'')
  FROM kwave_entities WHERE id=$1`, id).Scan(
		&r.ko, &r.entityType, &r.aliasesKo,
		&en, &enS, &ja, &jaS, &vi, &viS, &idl, &idS,
		&es, &esS, &pt, &ptS, &zh, &zhS, &zhh, &zhhS)
	if err != nil {
		return nil, false
	}
	r.localeVals["canonical_en"], r.localeSrc["canonical_en"] = en, enS
	r.localeVals["canonical_ja"], r.localeSrc["canonical_ja"] = ja, jaS
	r.localeVals["canonical_vi"], r.localeSrc["canonical_vi"] = vi, viS
	r.localeVals["canonical_id"], r.localeSrc["canonical_id"] = idl, idS
	r.localeVals["canonical_es"], r.localeSrc["canonical_es"] = es, esS
	r.localeVals["canonical_pt_br"], r.localeSrc["canonical_pt_br"] = pt, ptS
	r.localeVals["canonical_zh"], r.localeSrc["canonical_zh"] = zh, zhS
	r.localeVals["canonical_zh_hant"], r.localeSrc["canonical_zh_hant"] = zhh, zhhS

	r.isPerson = r.entityType == "person"
	if r.isPerson {
		_ = pool.QueryRow(ctx, `
SELECT COALESCE(primary_role::text,''), COALESCE(agency,''), COALESCE(gender,''),
       COALESCE(birth_year,0), COALESCE(groups,'{}'::text[]),
       COALESCE(notable_works,'{}'::text[]),
       COALESCE(secondary_roles::text[],'{}'::text[])
  FROM kwave_entity_person_details WHERE entity_id=$1`, id).Scan(
			&r.primaryRole, &r.agency, &r.gender, &r.birthYear, &r.groups, &r.works, &r.secondary)
	}
	return r, true
}

// gaps returns the fillable EMPTY fields of r, EXCLUDING fields already marked
// source-exhausted in the ledger (so the loop converges).
func (a *Agent) gaps(ctx context.Context, pool *pgxpool.Pool, r *record) []string {
	exhausted := a.exhaustedFields(ctx, pool, r.id)
	var out []string
	for _, f := range localeFields {
		if exhausted[f] {
			continue
		}
		if f == "aliases_ko" {
			if len(trimNonEmpty(r.aliasesKo)) == 0 {
				out = append(out, f)
			}
			continue
		}
		if r.localeVals[f] == "" {
			out = append(out, f)
		}
	}
	if r.isPerson {
		for _, f := range personFields {
			if exhausted[f] {
				continue
			}
			if personFieldEmpty(r, f) {
				out = append(out, f)
			}
		}
	}
	return out
}

func personFieldEmpty(r *record, f string) bool {
	switch f {
	case "agency":
		return strings.TrimSpace(r.agency) == ""
	case "birth_year":
		return r.birthYear == 0
	case "gender":
		return strings.TrimSpace(r.gender) == ""
	case "groups":
		return len(trimNonEmpty(r.groups)) == 0
	case "notable_works":
		return len(trimNonEmpty(r.works)) == 0
	case "secondary_roles":
		return len(r.secondary) == 0
	case "primary_role":
		return r.primaryRole == "" || r.primaryRole == "other"
	}
	return false
}

// GapCount counts fillable empty fields (ignoring exhaustion) — the before/after
// success metric. Exported for tests.
func GapCount(r *record) int {
	n := 0
	for _, f := range localeFields {
		if f == "aliases_ko" {
			if len(trimNonEmpty(r.aliasesKo)) == 0 {
				n++
			}
			continue
		}
		if r.localeVals[f] == "" {
			n++
		}
	}
	if r.isPerson {
		for _, f := range personFields {
			if personFieldEmpty(r, f) {
				n++
			}
		}
	}
	return n
}

func (a *Agent) exhaustedFields(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) map[string]bool {
	out := map[string]bool{}
	if pool == nil {
		return out
	}
	rows, err := pool.Query(ctx, `
SELECT field FROM kwave_kdb_enrich_attempts
 WHERE entity_id=$1 AND exhausted=true AND last_attempt_at > now() - interval '7 days'`, id)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var f string
		if rows.Scan(&f) == nil {
			out[f] = true
		}
	}
	return out
}

// enrichOne runs the cascade for one entity's still-empty fields and records
// per-field attempts. Returns the ItemResult (one accounted Action).
func (a *Agent) enrichOne(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) agents.ItemResult {
	r, ok := a.loadRecord(ctx, pool, id)
	if !ok {
		return agents.ItemResult{ID: id, Action: agents.ActionErrored, Source: "heuristic",
			Reason: "record not found at run time"}
	}
	missing := a.gaps(ctx, pool, r)
	if len(missing) == 0 {
		return agents.ItemResult{ID: id, Action: agents.ActionNoop, Source: "heuristic",
			Reason: "no fillable gap (filled or source-exhausted)"}
	}

	localeMiss, personMiss := splitFields(missing)
	filledFields := map[string]string{} // field → source
	tried := map[string]string{}        // field → last source tried

	if len(localeMiss) > 0 {
		a.cascadeLocales(ctx, pool, r, localeMiss, filledFields, tried)
	}
	if r.isPerson && len(personMiss) > 0 {
		a.cascadePerson(ctx, pool, r, personMiss, filledFields, tried)
	}

	for _, f := range missing {
		_, satisfied := filledFields[f]
		a.recordAttempt(ctx, pool, id, f, tried[f], satisfied)
	}

	if len(filledFields) > 0 {
		return agents.ItemResult{ID: id, Action: agents.ActionFilled, Source: "cascade",
			Reason: "filled " + strings.Join(sortStrings(keys(filledFields)), ",")}
	}
	return agents.ItemResult{ID: id, Action: agents.ActionSkipped, Source: "cascade",
		Reason: "no source produced a value; attempts advanced"}
}

// recordAttempt upserts the per-field ledger. satisfied=true → drop the row.
// Otherwise attempts++ and mark exhausted once attempts reaches maxAttempts.
func (a *Agent) recordAttempt(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID, field, lastSource string, satisfied bool) {
	if pool == nil {
		return
	}
	if satisfied {
		_, _ = pool.Exec(ctx, `DELETE FROM kwave_kdb_enrich_attempts WHERE entity_id=$1 AND field=$2`, id, field)
		return
	}
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, exhausted, last_source, last_attempt_at)
VALUES ($1, $2, 1, false, $3, now())
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts = kwave_kdb_enrich_attempts.attempts + 1,
       last_source = $3,
       last_attempt_at = now(),
       exhausted = (kwave_kdb_enrich_attempts.attempts + 1) >= $4`, id, field, lastSource, maxAttempts)
}

func splitFields(fields []string) (locale, person []string) {
	personSet := map[string]bool{}
	for _, f := range personFields {
		personSet[f] = true
	}
	for _, f := range fields {
		if personSet[f] {
			person = append(person, f)
		} else {
			locale = append(locale, f)
		}
	}
	return locale, person
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// known returns the already-filled locale spellings (romanization basis for L4).
func known(r *record) map[string]string {
	out := map[string]string{}
	for col, code := range localeToCode {
		if v := r.localeVals[col]; v != "" {
			out[code] = v
		}
	}
	return out
}

// makeFillInput builds the L4 locale fill request for the missing locale codes.
func makeFillInput(r *record, missingCodes []string, wd *aijudge.ClassifyWikidata, sitelinks map[string]string) *aijudge.FillInput {
	return &aijudge.FillInput{
		Ko:          r.ko,
		EntityType:  r.entityType,
		PrimaryRole: r.primaryRole,
		AliasesKo:   r.aliasesKo,
		Known:       known(r),
		Missing:     missingCodes,
		Wikidata:    wd,
		Sitelinks:   sitelinks,
	}
}

func sortStrings(s []string) []string { sort.Strings(s); return s }

func trimNonEmpty(xs []string) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

var _ = kdb.SourceCodexFallback
