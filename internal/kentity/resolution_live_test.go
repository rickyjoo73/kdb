package kentity

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

// Explicitly opt-in public-source reads; all writes stay in the disposable
// kdb_workflow_test schema. This does not approve or seed production entities.
func TestResolverLiveCrossDomain(t *testing.T) {
	if os.Getenv("KDB_LIVE_ENTITY_RESEARCH") != "1" {
		t.Skip("explicit live-source opt-in required")
	}
	s, _ := fixture(t)
	b, err := os.ReadFile("../../migrations/0117_kentity_resolution.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	s.AutoResearch = true
	worker := &Resolver{Store: s, Source: wikidata.New()}
	for _, in := range []CandidateInput{{KO: "김대중", Type: "person", Domains: []string{"politics"}, Reason: "공개 원천 정치 분야 격리 검증"}, {KO: "정주영", Type: "person", Domains: []string{"economy"}, Reason: "공개 원천 경제 분야 격리 검증"}, {KO: "양용은", Type: "person", Domains: []string{"sports"}, Reason: "공개 원천 스포츠 분야 격리 검증"}, {KO: "대한적십자사", Type: "organization", Domains: []string{"society"}, Reason: "공개 원천 사회 분야 격리 검증"}} {
		t.Run(in.Domains[0], func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
			defer cancel()
			e, err := s.CreateCandidate(ctx, "live-fixture", in.KO, in)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = worker.ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			j, err := s.Resolution(ctx, e.ID)
			if err != nil || j.State != "review" || len(j.Proposals) == 0 {
				t.Fatal("no reviewable source proposal", j, err)
			}
			names := 0
			for _, p := range j.Proposals {
				names += len(p.Names)
			}
			if names == 0 {
				t.Fatal("source raw labels unavailable")
			}
			t.Logf("domain=%s proposals=%d recorded_names=%d approved_names=0", in.Domains[0], len(j.Proposals), names)
		})
	}
}
