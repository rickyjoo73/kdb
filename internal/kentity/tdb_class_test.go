package kentity

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestTDBSourceClassConflictDoesNotUseNameSubstrings(t *testing.T) {
	for _, typ := range []string{"restaurant", "shopping", "accommodation"} {
		for _, classes := range [][]string{{"Q928830"}, {"Q22808403"}} {
			if !TDBSourceClassConflict(typ, classes) {
				t.Fatal(typ, classes)
			}
		}
	}
	for _, typ := range []string{"transport", "transit", "tourist_spot", "heritage", "education"} {
		if TDBSourceClassConflict(typ, []string{"Q928830"}) {
			t.Fatal("legitimate geographic case blocked", typ)
		}
	}
	if TDBSourceClassConflict("restaurant", []string{"Q11707"}) || TDBSourceClassConflict("person", []string{"Q5"}) {
		t.Fatal("supported source class blocked")
	}
	if !TDBSourceClassConflict("education", []string{"Q5"}) {
		t.Fatal("school is not an individual")
	}
}
func TestTDBClassConflictBlocksPreviouslyCollectedCandidateAndDecision(t *testing.T) {
	s, b, m, id := registeredTDB(t)
	ctx := context.Background()
	if err := s.DecideTDBMapping(ctx, "fixture", tdbDecision(m, id)); err != nil {
		t.Fatal(err)
	}
	// Simulate a source category conflict discovered in an existing observation,
	// including an old approved mapping. No worker refresh is needed to stop use.
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET source_type='restaurant',result=jsonb_set(result,'{instance_of}','["Q928830","Q22808403"]'::jsonb) WHERE id=$1`, m.Shadow.ID); err != nil {
		t.Fatal(err)
	}
	current, err := s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || current.Current || current.Fresh || !current.ClassConflict {
		t.Fatal(current, err)
	}
	if _, err = s.RegisterTDBCandidate(ctx, "another-fixture", tdbRegistration(current)); !errors.Is(err, ErrProtected) {
		t.Fatal("class-conflicting candidate accepted", err)
	}
	d := tdbDecision(current, id)
	d.Revision = current.Revision
	if err = s.DecideTDBMapping(ctx, "fixture", d); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	rows, err := s.TDBShadows(ctx, "review", 10)
	if err != nil || len(rows) != 1 || !rows[0].ClassConflict {
		t.Fatal(rows, err)
	}
	b.ObservedAt = time.Now()
	b.Bindings[0].Type = "restaurant"
	if _, err = s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	src := researchSource()
	src.Entities["Q123"].SourceLabels["ko"] = "합성 전철역"
	src.Entities["Q123"].InstanceOf = []string{"Q928830", "Q22808403"}
	if ok, err := (&TDBShadowWorker{Store: s, Source: src}).ProcessOne(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	current, err = s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || current.Shadow.State != "blocked" || current.Shadow.Proposal == nil || !current.ClassConflict {
		t.Fatal(current, err)
	}
}
