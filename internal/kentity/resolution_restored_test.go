package kentity

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func TestResolverApprovalAgainstRestoredSchema(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &Store{Pool: pool, AutoResearch: true}
	e, err := s.CreateCandidate(ctx, "restored-resolution-fixture", uuid.NewString(), CandidateInput{KO: "시험동명", Type: "person", Domains: []string{"politics", "sports"}, Reason: "실제 스키마 격리 DB 조사 승인 검증"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_id_reservations WHERE entity_id=$1`, e.ID); err != nil {
			t.Error(err)
		}
		for _, table := range []string{"kentity_resolution_jobs", "kentity_candidate_requests", "kentity_audit_events", "kentity_names", "kentity_external_ids", "kentity_evidence", "kentity_entity_domains"} {
			if _, err := pool.Exec(ctx, "DELETE FROM "+table+" WHERE entity_id=$1", e.ID); err != nil {
				t.Error(err)
			}
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kentity_entities WHERE id=$1`, e.ID); err != nil {
			t.Error(err)
		}
	})
	qid := "Q9999999990117"
	src := researchSource()
	source := src.Entities["Q123"]
	source.QID = qid
	src.Entities = map[string]*wikidata.Entity{qid: source}
	src.Candidates = []wikidata.Candidate{{QID: qid}}
	if _, err = (&Resolver{Store: s, Source: src}).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := s.Resolution(ctx, e.ID)
	if err != nil || j.State != "review" {
		t.Fatal(j, err)
	}
	err = s.ApproveResearch(ctx, "restored-fixture", ResearchApproval{EntityID: e.ID, JobID: j.ID, Generation: j.Generation, QID: qid, Locales: []string{"en", "ja"}, Reason: "격리 DB 원천 표기 승인 검증", IdentityFacts: "원천 식별자와 동명 후보를 구분하는 가상의 테스트 근거입니다. 운영 데이터 승인이 아닙니다.", Attested: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE entity_id=$1`, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(ctx, e.ID)
	if err != nil || got.Status != "candidate" {
		t.Fatal(got, err)
	}
	for _, n := range got.Names {
		if n.Status == "verified" {
			t.Fatal("withdrawn evidence retained verification")
		}
	}
}
