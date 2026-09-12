package kentity

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func fixture(t *testing.T) (*Store, uuid.UUID) {
	t.Helper()
	pool := testdb.New(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text;
 INSERT INTO kwave_persons(name_ko) VALUES('김동명')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,canonical_en) VALUES($1,'김동명','Kim Test')`, id); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("../../migrations/0116_kentity_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	return &Store{Pool: pool}, id
}

func TestLegacyProjectionPreservesUUIDAndSingleWriter(t *testing.T) {
	s, id := fixture(t)
	ctx := context.Background()
	e, err := s.Get(ctx, id)
	if err != nil || e.Origin != "kdb" || e.ID != id || len(e.Names) != 2 {
		t.Fatal(e, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET canonical_ko='잘못된직접쓰기' WHERE id=$1`, id); err == nil {
		t.Fatal("legacy writer bypass")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET canonical_ko='김개명',canonical_en='Updated Name' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, id)
	if err != nil || e.KO != "김개명" || e.Revision != 2 {
		t.Fatal(e, err)
	}
	found := false
	for _, n := range e.Names {
		if n.Value == "Updated Name" && n.Status == "legacy" {
			found = true
		}
	}
	if !found {
		t.Fatal("stale mirrored locale or invented verification")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET updated_at=now() WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, id)
	if err != nil || e.Revision != 2 {
		t.Fatal("bookkeeping changed identity revision", e, err)
	}
	var confirmed int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_crosswalks WHERE status='confirmed'`).Scan(&confirmed); err != nil || confirmed != 0 {
		t.Fatal("name-only legacy match confirmed", confirmed, err)
	}
}

func TestCrossDomainHomonymsAndIdempotentCandidateRegistration(t *testing.T) {
	s, oldID := fixture(t)
	ctx := context.Background()
	in := CandidateInput{KO: "김동명", Type: "person", Domains: []string{"sports", "politics"}, Reason: "독립 인물 여부 검수"}
	p, err := s.CreateCandidate(ctx, "operator@test", "first", in)
	if err != nil {
		t.Fatal(err)
	}
	q, err := s.CreateCandidate(ctx, "operator@test", "first", in)
	if err != nil || p.ID != q.ID {
		t.Fatal(q, err)
	}
	q, err = s.CreateCandidate(ctx, "operator@test", "second", in)
	if err != nil || p.ID == q.ID || p.ID == oldID || p.Status != "candidate" || len(p.Domains) != 2 {
		t.Fatal(p, q, err)
	}
	in.Reason = "다른 입력"
	if _, err = s.CreateCandidate(ctx, "operator@test", "first", in); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	es, err := s.Search(ctx, "김동명", "person", "sports", 50)
	if err != nil || len(es) != 2 {
		t.Fatal(es, err)
	}
	var leaked int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities WHERE id=ANY($1)`, []uuid.UUID{p.ID, q.ID}).Scan(&leaked); err != nil || leaked != 0 {
		t.Fatal("native politician/athlete leaked into legacy worker", leaked, err)
	}
}

func TestEvidenceCannotCrossEntityAndTemporalRelationRejectsInvalidRange(t *testing.T) {
	s, id := fixture(t)
	ctx := context.Background()
	e, err := s.CreateCandidate(ctx, "operator@test", "new", CandidateInput{KO: "테스트팀", Type: "team", Domains: []string{"sports"}, Reason: "검수"})
	if err != nil {
		t.Fatal(err)
	}
	evidence := uuid.New()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_url) VALUES($1,$2,'fixture','https://example.test/source')`, evidence, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,'en','Test Team','canonical','recorded','verified',$2,'fixture')`, e.ID, evidence); err == nil {
		t.Fatal("another entity's evidence used")
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_relations(subject_id,predicate,object_id,valid_from,valid_until) VALUES($1,'member_of',$2,'2026-09-12','2025-01-01')`, id, e.ID); err == nil {
		t.Fatal("backwards membership period")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET status='active' WHERE id=$1`, e.ID); err == nil {
		t.Fatal("unverified native entity activated")
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,source_code) VALUES($1,'en','Overwrite','canonical','recorded','operator')`, id); err == nil {
		t.Fatal("second legacy name writer allowed")
	}
}
