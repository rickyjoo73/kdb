package enricher

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
)

// cascadeLocales fills empty locale canonicals + aliases_ko for the given
// missing columns via L2 MusicBrainz → L3 Wikidata → L4 gpt-5.5. Each layer
// only targets columns still empty; persistence never overwrites a non-empty
// value. filledFields/tried are updated in place.
func (a *Agent) cascadeLocales(ctx context.Context, pool *pgxpool.Pool, r *record, missing []string, filledFields, tried map[string]string) {
	remaining := func() []string {
		var out []string
		for _, f := range missing {
			if _, done := filledFields[f]; !done {
				out = append(out, f)
			}
		}
		return out
	}

	// L2 MusicBrainz — group / singer-type artists. Provides locale aliases +
	// Korean aliases.
	if a.src.mb != nil && len(remaining()) > 0 && (r.entityType == "group" || r.entityType == "person") {
		if m := a.musicbrainzAliases(ctx, r.ko); len(m) > 0 {
			a.applyLocaleMap(ctx, pool, r, remaining(), m, kdb.SourceMusicBrainz, filledFields, tried, "musicbrainz")
		}
	}

	// L3 Wikidata — labels for all locales + Korean aliases (from also-known-as).
	var wd *aijudge.ClassifyWikidata
	var sitelinks map[string]string
	if a.src.wd != nil && len(remaining()) > 0 {
		ent, cand, err := a.src.wd.SearchAndFetch(ctx, r.ko)
		if err == nil && ent != nil {
			wd = &aijudge.ClassifyWikidata{QID: ent.QID}
			if cand != nil {
				wd.Description = cand.Description
			}
			sitelinks = ent.Sitelinks
			m := map[string][]string{}
			for code, v := range ent.Labels {
				if strings.TrimSpace(v) != "" {
					m[code] = []string{v}
				}
			}
			a.applyLocaleMap(ctx, pool, r, remaining(), m, kdb.SourceWikidataLabel, filledFields, tried, "wikidata")
		}
	}

	// L4 gpt-5.5 — synthesize the locale spellings still missing. aliases_ko is
	// not a locale code the fill prompt handles, so it is left to L2/L3 only.
	rem := remaining()
	var missCodes []string
	for _, f := range rem {
		if code, ok := localeToCode[f]; ok {
			missCodes = append(missCodes, code)
		}
	}
	for _, f := range rem {
		tried[f] = "gpt-5.5"
	}
	if len(missCodes) > 0 && a.localeBase != nil {
		in := makeFillInput(r, missCodes, wd, sitelinks)
		var res aijudge.FillResult
		if err := a.localeBase.CallJSON(ctx, in, &res); err == nil {
			for _, sp := range res.Spellings {
				col := "canonical_" + sp.Locale
				if !contains(rem, col) || strings.TrimSpace(sp.Value) == "" {
					continue
				}
				if a.writeLocale(ctx, pool, r, col, sp.Value, string(kdb.SourceCodexFallback)) {
					filledFields[col] = "gpt-5.5"
				}
			}
		}
	}
}

// applyLocaleMap writes external-source locale values (priority-aware, empty
// only) for the requested columns + aliases_ko, recording fills.
func (a *Agent) applyLocaleMap(ctx context.Context, pool *pgxpool.Pool, r *record, want []string, m map[string][]string, src kdb.Source, filledFields, tried map[string]string, label string) {
	wantSet := map[string]bool{}
	for _, f := range want {
		wantSet[f] = true
		tried[f] = label
	}
	for code, vals := range m {
		vals = trimNonEmpty(vals)
		if len(vals) == 0 {
			continue
		}
		if code == "ko" {
			if wantSet["aliases_ko"] {
				if a.appendAliasesKo(ctx, pool, r, vals) {
					filledFields["aliases_ko"] = label
				}
			}
			continue
		}
		col := "canonical_" + code
		if !wantSet[col] {
			continue
		}
		if a.writeLocale(ctx, pool, r, col, vals[0], string(src)) {
			filledFields[col] = label
		}
	}
}

// writeLocale sets a canonical_<loc> column ONLY when it is currently empty
// (no-overwrite). Returns true if a value was written.
func (a *Agent) writeLocale(ctx context.Context, pool *pgxpool.Pool, r *record, col, val, source string) bool {
	if pool == nil || strings.TrimSpace(val) == "" {
		return false
	}
	srcCol := col + "_source"
	tag, err := pool.Exec(ctx,
		`UPDATE kwave_entities SET `+col+`=$2, `+srcCol+`=$3, updated_at=now()
		  WHERE id=$1 AND (`+col+` IS NULL OR `+col+`='')`, r.id, val, source)
	if err == nil && tag.RowsAffected() > 0 {
		r.localeVals[col] = val
		return true
	}
	return false
}

// appendAliasesKo de-dupes new Korean aliases into aliases_ko, excluding the
// canonical itself. Returns true if at least one new alias was added.
func (a *Agent) appendAliasesKo(ctx context.Context, pool *pgxpool.Pool, r *record, vals []string) bool {
	if pool == nil {
		return false
	}
	add := make([]string, 0, len(vals))
	have := map[string]bool{r.ko: true}
	for _, x := range r.aliasesKo {
		have[strings.TrimSpace(x)] = true
	}
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" && !have[v] {
			add = append(add, v)
			have[v] = true
		}
	}
	if len(add) == 0 {
		return false
	}
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entities
   SET aliases_ko = (SELECT ARRAY(SELECT DISTINCT x FROM unnest(COALESCE(aliases_ko,'{}'::text[]) || $2::text[]) x WHERE x <> '')),
       updated_at = now()
 WHERE id = $1`, r.id, add)
	if err == nil && tag.RowsAffected() > 0 {
		r.aliasesKo = append(r.aliasesKo, add...)
		return true
	}
	return false
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// musicbrainzAliases searches MusicBrainz for the artist and returns its
// locale-keyed aliases (the L2 source). Empty map on no match / error.
func (a *Agent) musicbrainzAliases(ctx context.Context, ko string) map[string][]string {
	if a.src.mb == nil {
		return nil
	}
	// FindAliases: 반환 artist 가 ko 와 정규화 일치할 때만 alias 반환 (오매칭 가드).
	aliases, err := a.src.mb.FindAliases(ctx, ko)
	if err != nil || len(aliases) == 0 {
		return nil
	}
	return map[string][]string(aliases)
}
