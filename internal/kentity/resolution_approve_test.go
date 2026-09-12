package kentity

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func approvalFixture(t *testing.T) (*Store, ResearchApproval) {
	t.Helper()
	s, e := researchFixture(t)
	ctx := context.Background()
	src := researchSource()
	src.Candidates = src.Candidates[:1]
	if _, err := (&Resolver{Store: s, Source: src}).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := s.Resolution(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, ResearchApproval{EntityID: e.ID, JobID: j.ID, Generation: j.Generation, QID: "Q123", Locales: []string{"en", "ja"}, Reason: "격리 환경에서 선택 기록 표기 승인 검증", IdentityFacts: "독립 식별자와 활동 이력을 비교하여 다른 동명 후보와 구분한 합성 검수입니다.", Attested: true}
}
func TestResearchApprovalInstallsOnlySelectedRecordedLocales(t *testing.T) {
	s, in := approvalFixture(t)
	ctx := context.Background()
	if err := s.ApproveResearch(ctx, "fixture-reviewer", in); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, in.EntityID)
	if err != nil || e.Status != "active" || e.Revision != 2 {
		t.Fatal(e, err)
	}
	verified := map[string]string{}
	for _, n := range e.Names {
		if n.Status == "verified" {
			verified[n.Locale] = n.Value
		}
	}
	if len(verified) != 2 || verified["en"] != "Test Person (politician)" || verified["ja"] != "テスト人物" {
		t.Fatal(verified)
	}
	j, err := s.Resolution(ctx, in.EntityID)
	if err != nil || j.State != "approved" {
		t.Fatal(j, err)
	}
	if err = s.ApproveResearch(ctx, "fixture-reviewer", in); !errors.Is(err, ErrProtected) {
		t.Fatal("duplicate approval", err)
	}
	legacy := uuid.New()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'경합 legacy')`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, legacy); err == nil {
		t.Fatal("legacy writer stole approved common ID")
	}
}
func TestResearchApprovalRechecksIdentityAndInput(t *testing.T) {
	for _, scenario := range []string{"existing_legacy_claim", "locked", "stale_generation", "unknown_proposal", "unknown_locale", "no_attestation"} {
		t.Run(scenario, func(t *testing.T) {
			s, in := approvalFixture(t)
			ctx := context.Background()
			expected := ErrProtected
			switch scenario {
			case "existing_legacy_claim":
				id := uuid.New()
				if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'늦은 기존 ID')`, id); err != nil {
					t.Fatal(err)
				}
				if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, id); err != nil {
					t.Fatal(err)
				}
			case "locked":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_entities SET operator_locked=true WHERE id=$1`, in.EntityID)
			case "stale_generation":
				in.Generation++
			case "unknown_proposal":
				in.QID = "Q999"
				expected = ErrInvalid
			case "unknown_locale":
				in.Locales = []string{"pt-BR"}
				expected = ErrInvalid
			case "no_attestation":
				in.Attested = false
				expected = ErrInvalid
			}
			if err := s.ApproveResearch(ctx, "fixture", in); !errors.Is(err, expected) {
				t.Fatal(err)
			}
			var n int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_evidence WHERE entity_id=$1`, in.EntityID).Scan(&n); err != nil || n != 0 {
				t.Fatal("partial approval", n, err)
			}
		})
	}
}
func TestNativeApprovalAndLegacyInsertionCannotBothOwnSameID(t *testing.T) {
	s, in := approvalFixture(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'동시 기존 기록')`, id); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	start := make(chan struct{})
	wg.Add(2)
	go func() { defer wg.Done(); <-start; results <- s.ApproveResearch(ctx, "fixture", in) }()
	go func() {
		defer wg.Done()
		<-start
		_, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, id)
		results <- err
	}()
	close(start)
	wg.Wait()
	close(results)
	n := 0
	for err := range results {
		if err == nil {
			n++
		}
	}
	if n != 1 {
		t.Fatal("ownership race: expected one committed writer", n)
	}
}
func TestEvidenceWithdrawalBlocksNamesAndSupportsNewObservation(t *testing.T) {
	s, in := approvalFixture(t)
	ctx := context.Background()
	if err := s.ApproveResearch(ctx, "fixture", in); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_evidence SET source_url='https://example.test/changed' WHERE entity_id=$1`, in.EntityID); err == nil {
		t.Fatal("approved evidence rewritten")
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE entity_id=$1`, in.EntityID); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, in.EntityID)
	if err != nil || e.Status != "candidate" {
		t.Fatal(e, err)
	}
	for _, n := range e.Names {
		if n.Status == "verified" {
			t.Fatal("withdrawn proof still served verified")
		}
	}
	if err = s.RequestResearch(ctx, "fixture", e.ID, e.Revision, "철회 후 새로운 원천 관찰 재검수"); err != nil {
		t.Fatal(err)
	}
	if _, err = (&Resolver{Store: s, Source: researchSource()}).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := s.Resolution(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.JobID = j.ID
	in.Generation = j.Generation
	if err = s.ApproveResearch(ctx, "fixture", in); err != nil {
		t.Fatal("new observation approval", err)
	}
	var n int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_evidence WHERE entity_id=$1`, e.ID).Scan(&n); err != nil || n != 2 {
		t.Fatal("evidence history lost", n, err)
	}
}
