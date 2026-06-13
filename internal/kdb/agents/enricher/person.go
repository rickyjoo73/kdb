package enricher

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// cascadePerson fills empty person-detail fields via L3 Wikidata claims
// (agency/birth_year/notable_works) then L4 gpt-5.5 (all requested fields).
// Persistence never overwrites a non-empty value. NOTE (request#2 fix): unlike
// the legacy orchestrator, this runs regardless of global presence — Korean-only
// persons are simply lower selection priority, not skipped.
func (a *Agent) cascadePerson(ctx context.Context, pool *pgxpool.Pool, r *record, missing []string, filledFields, tried map[string]string, failed map[string]bool) {
	want := map[string]bool{}
	for _, f := range missing {
		want[f] = true
	}

	// Ensure a details row exists so writes land.
	if pool != nil {
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_person_details (entity_id, primary_role)
VALUES ($1, 'other'::person_role) ON CONFLICT (entity_id) DO NOTHING`, r.id)
	}

	// L3 Wikidata claims for agency/birth_year/notable_works.
	if a.src.wd != nil && (want["agency"] || want["birth_year"] || want["notable_works"]) {
		qid := a.lookupWikidataQID(ctx, pool, r.ko, r.id)
		for _, f := range []string{"agency", "birth_year", "notable_works"} {
			if want[f] {
				tried[f] = "wikidata"
			}
		}
		if qid != "" {
			if claims, err := a.src.wd.LookupClaims(ctx, qid); err == nil && claims != nil {
				if want["agency"] && strings.TrimSpace(claims.Agency) != "" {
					if a.writePersonText(ctx, pool, r, "agency", claims.Agency) {
						filledFields["agency"] = "wikidata"
					}
				}
				if want["birth_year"] && claims.BirthYear != 0 {
					if a.writeBirthYear(ctx, pool, r, claims.BirthYear) {
						filledFields["birth_year"] = "wikidata"
					}
				}
				if want["notable_works"] && len(trimNonEmpty(claims.NotableWorks)) > 0 {
					if a.appendPersonArray(ctx, pool, r, "notable_works", claims.NotableWorks) {
						filledFields["notable_works"] = "wikidata"
					}
				}
			}
		}
	}

	// L4 gpt-5.5 person-detail fill for whatever is still missing.
	var stillMissing []string
	for _, f := range missing {
		if _, done := filledFields[f]; !done {
			stillMissing = append(stillMissing, f)
		}
	}
	if len(stillMissing) == 0 || a.personBase == nil {
		return
	}
	var res personFillResult
	in := personFillInput{
		Ko: r.ko, PrimaryRole: r.primaryRole, Agency: r.agency,
		Groups: r.groups, Works: r.works, Missing: stillMissing,
	}
	if err := a.personBase.CallJSON(ctx, in, &res); err != nil {
		// transport 실패 — 시도로 치지 않는다(attempts 미소진, 다음 cycle 재시도).
		if failed != nil {
			for _, f := range stillMissing {
				failed[f] = true
			}
		}
		return
	}
	for _, f := range stillMissing {
		tried[f] = "gpt-5.5"
	}
	a.applyPersonFill(ctx, pool, r, want, res, filledFields)
}

func (a *Agent) applyPersonFill(ctx context.Context, pool *pgxpool.Pool, r *record, want map[string]bool, res personFillResult, filledFields map[string]string) {
	f := res.Fields
	if want["agency"] && f.Agency != nil && strings.TrimSpace(*f.Agency) != "" {
		if a.writePersonText(ctx, pool, r, "agency", *f.Agency) {
			filledFields["agency"] = "gpt-5.5"
		}
	}
	if want["gender"] && f.Gender != nil {
		g := strings.TrimSpace(*f.Gender)
		if g == "M" || g == "F" {
			if a.writePersonText(ctx, pool, r, "gender", g) {
				filledFields["gender"] = "gpt-5.5"
			}
		}
	}
	if want["birth_year"] && f.BirthYear != nil && *f.BirthYear >= 1900 && *f.BirthYear <= 2025 {
		if a.writeBirthYear(ctx, pool, r, *f.BirthYear) {
			filledFields["birth_year"] = "gpt-5.5"
		}
	}
	if want["primary_role"] && f.PrimaryRole != nil {
		role := strings.TrimSpace(*f.PrimaryRole)
		// person_role enum 에 없는 값(LLM 환각/오타)은 ::person_role cast 가 DB 에서
		// 실패해 조용히 미기록됐다 → Go 에서 미리 검증해 손실/무의미 쿼리 방지.
		if role != "" && role != "other" && validPersonRole(role) {
			if a.writePrimaryRole(ctx, pool, r, role) {
				filledFields["primary_role"] = "gpt-5.5"
			}
		}
	}
	if want["secondary_roles"] && len(f.SecondaryRoles) > 0 {
		if roles := filterPersonRoles(f.SecondaryRoles); len(roles) > 0 {
			if a.appendRoleArray(ctx, pool, r, "secondary_roles", roles) {
				filledFields["secondary_roles"] = "gpt-5.5"
			}
		}
	}
	if want["groups"] && len(trimNonEmpty(f.Groups)) > 0 {
		if a.appendPersonArray(ctx, pool, r, "groups", f.Groups) {
			filledFields["groups"] = "gpt-5.5"
		}
	}
	if want["notable_works"] && len(trimNonEmpty(f.NotableWorks)) > 0 {
		if a.appendPersonArray(ctx, pool, r, "notable_works", f.NotableWorks) {
			filledFields["notable_works"] = "gpt-5.5"
		}
	}
}

// personRoles — DB person_role enum 과 1:1 (migration 정의값). LLM 산출 role 을
// DB cast 전에 검증해 무효값(환각/오타)이 조용히 버려지지 않게 한다.
var personRoles = map[string]bool{
	"idol": true, "singer": true, "rapper": true, "actor": true,
	"broadcaster": true, "comedian": true, "director": true, "producer": true,
	"model": true, "creator": true, "athlete": true, "politician": true,
	"businessperson": true, "journalist": true, "fictional": true, "other": true,
}

func validPersonRole(s string) bool { return personRoles[strings.TrimSpace(s)] }

// filterPersonRoles — enum 에 있는 role 만 남긴다 (빈 값/무효값 제거).
func filterPersonRoles(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		if v = strings.TrimSpace(v); validPersonRole(v) {
			out = append(out, v)
		}
	}
	return out
}

// lookupWikidataQID finds an existing external ref, else searches Wikidata.
func (a *Agent) lookupWikidataQID(ctx context.Context, pool *pgxpool.Pool, ko string, id uuid.UUID) string {
	qid := ""
	if pool != nil {
		_ = pool.QueryRow(ctx,
			`SELECT external_id FROM kwave_entity_external_refs
			  WHERE entity_id=$1 AND provider='wikidata' LIMIT 1`, id).Scan(&qid)
	}
	if qid != "" {
		return qid
	}
	if a.src.wd == nil {
		return ""
	}
	if ent, _, err := a.src.wd.SearchAndFetch(ctx, ko); err == nil && ent != nil {
		return ent.QID
	}
	return ""
}

// --- no-overwrite person-detail writers ----------------------------------

func (a *Agent) writePersonText(ctx context.Context, pool *pgxpool.Pool, r *record, col, val string) bool {
	if pool == nil || strings.TrimSpace(val) == "" {
		return false
	}
	tag, err := pool.Exec(ctx,
		`UPDATE kwave_entity_person_details SET `+col+`=$2
		  WHERE entity_id=$1 AND (`+col+` IS NULL OR `+col+`='')`, r.id, val)
	return err == nil && tag.RowsAffected() > 0
}

func (a *Agent) writeBirthYear(ctx context.Context, pool *pgxpool.Pool, r *record, y int) bool {
	if pool == nil || y == 0 {
		return false
	}
	tag, err := pool.Exec(ctx,
		`UPDATE kwave_entity_person_details SET birth_year=$2
		  WHERE entity_id=$1 AND birth_year IS NULL`, r.id, y)
	return err == nil && tag.RowsAffected() > 0
}

func (a *Agent) writePrimaryRole(ctx context.Context, pool *pgxpool.Pool, r *record, role string) bool {
	if pool == nil {
		return false
	}
	tag, err := pool.Exec(ctx,
		`UPDATE kwave_entity_person_details SET primary_role=$2::person_role
		  WHERE entity_id=$1 AND (primary_role IS NULL OR primary_role='other')`, r.id, role)
	return err == nil && tag.RowsAffected() > 0
}

// appendPersonArray de-dupes new values into a text[] detail column ONLY when
// it is currently empty (no-overwrite of an existing curated list).
func (a *Agent) appendPersonArray(ctx context.Context, pool *pgxpool.Pool, r *record, col string, vals []string) bool {
	vals = trimNonEmpty(vals)
	if pool == nil || len(vals) == 0 {
		return false
	}
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entity_person_details
   SET `+col+` = (SELECT ARRAY(SELECT DISTINCT x FROM unnest($2::text[]) x WHERE x <> ''))
 WHERE entity_id = $1 AND array_length(`+col+`,1) IS NULL`, r.id, vals)
	return err == nil && tag.RowsAffected() > 0
}

// appendRoleArray is appendPersonArray for the person_role[] column.
func (a *Agent) appendRoleArray(ctx context.Context, pool *pgxpool.Pool, r *record, col string, vals []string) bool {
	vals = trimNonEmpty(vals)
	if pool == nil || len(vals) == 0 {
		return false
	}
	tag, err := pool.Exec(ctx, `
UPDATE kwave_entity_person_details
   SET `+col+` = (SELECT ARRAY(SELECT DISTINCT x FROM unnest($2::text[]) x WHERE x <> '')::person_role[])
 WHERE entity_id = $1 AND array_length(`+col+`,1) IS NULL`, r.id, vals)
	return err == nil && tag.RowsAffected() > 0
}
