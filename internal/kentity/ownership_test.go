package kentity

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func ownershipFixture(t *testing.T) (*Store, OwnershipInput) {
	t.Helper()
	s, id := fixture(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE kwave_entities SET canonical_ko='시험동명',status='rejected',notes='연예 분야가 아니라 기존 용도에서 기각' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0117_kentity_resolution.sql", "0118_kentity_write_ownership.sql"} {
		b, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, string(b)); err != nil {
			t.Fatal(name, err)
		}
	}
	s.AutoResearch = true
	e, err := s.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	o, err := s.LegacyOwnership(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return s, OwnershipInput{ID: id, Revision: e.Revision, Fingerprint: o.Fingerprint, Type: "person", Domains: []string{"politics", "society"}, SourceURL: "https://example.test/scope", Reason: "연예 전용 용도 제한과 일반 공통 Entity 정체성을 분리해 검수하기 위한 합성 요청", Attested: true}
}
func TestOwnershipTransitionPreservesUUIDAndLegacyPolicy(t *testing.T) {
	s, in := ownershipFixture(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_entities SET write_owner='native' WHERE id=$1`, in.ID); err == nil {
		t.Fatal("unaudited writer transition")
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, in.ID); err != nil {
		t.Fatal(err)
	}
	// The legacy parent fingerprint may include trigger-maintained metadata in
	// production; fetch it again after attaching the source fixture.
	o, err := s.LegacyOwnership(ctx, in.ID)
	if err != nil {
		t.Fatal(err)
	}
	in.Fingerprint = o.Fingerprint
	if err = s.AdoptLegacy(ctx, "fixture", in); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, in.ID)
	if err != nil || e.ID != in.ID || e.Origin != "kdb" || e.WriteOwner != "native" || e.Status != "candidate" || len(e.Names) != 1 || e.Names[0].Status != "unverified" {
		t.Fatal(e, err)
	}
	var legacyStatus string
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM kwave_entities WHERE id=$1`, in.ID).Scan(&legacyStatus); err != nil || legacyStatus != "rejected" {
		t.Fatal("legacy serving policy changed", legacyStatus, err)
	}
	if _, err = (&Resolver{Store: s, Source: researchSource()}).ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	j, err := s.Resolution(ctx, in.ID)
	if err != nil || j.State != "review" || len(j.Proposals[0].ExistingIDs) != 0 {
		t.Fatal(j, err)
	}
	if err = s.ApproveResearch(ctx, "fixture", ResearchApproval{EntityID: in.ID, JobID: j.ID, Generation: j.Generation, QID: "Q123", Locales: []string{"en", "ja"}, Reason: "범위 전환 후 새 정체성과 기록 표기 검수", IdentityFacts: "기존 UUID와 같은 외부 식별자 및 프로필을 대조하는 합성 검증 근거입니다.", Attested: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET canonical_en='Old Writer Overwrite',status='rejected' WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, in.ID)
	if err != nil || e.Status != "active" {
		t.Fatal("legacy rejection overwrote common state", e, err)
	}
	for _, n := range e.Names {
		if n.Value == "Old Writer Overwrite" {
			t.Fatal("two name writers")
		}
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entity_external_refs SET external_id='Q456' WHERE entity_id=$1 AND provider='wikidata'`, in.ID); err == nil {
		t.Fatal("legacy replaced common anchor")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE entity_id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, in.ID)
	if err != nil || e.Status != "candidate" {
		t.Fatal("legacy-origin evidence invalidation missed", e, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, in.ID); err != nil {
		t.Fatal(err)
	}
	e, err = s.Get(ctx, in.ID)
	if err != nil || !e.Locked {
		t.Fatal("legacy lock ignored", e, err)
	}
}
func TestOwnershipTransitionRejectsChangedInputLocksActiveRowsAndWrongScope(t *testing.T) {
	for _, scenario := range []string{"changed", "locked", "active", "bad_domain", "not_attested"} {
		t.Run(scenario, func(t *testing.T) {
			s, in := ownershipFixture(t)
			ctx := context.Background()
			expected := ErrProtected
			switch scenario {
			case "changed":
				_, _ = s.Pool.Exec(ctx, `UPDATE kwave_entities SET notes='changed' WHERE id=$1`, in.ID)
			case "locked":
				_, _ = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, in.ID)
			case "active":
				_, _ = s.Pool.Exec(ctx, `UPDATE kwave_entities SET status='active' WHERE id=$1`, in.ID)
			case "bad_domain":
				in.Domains = []string{"not-a-domain"}
				expected = ErrInvalid
			case "not_attested":
				in.Attested = false
				expected = ErrInvalid
			}
			if err := s.AdoptLegacy(ctx, "fixture", in); !errors.Is(err, expected) {
				t.Fatal(err)
			}
			var n int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_ownership_decisions`).Scan(&n); err != nil || n != 0 {
				t.Fatal("partial ownership change", n, err)
			}
		})
	}
}
func TestConcurrentOwnershipTransitionsOnlyCommitOnce(t *testing.T) {
	s, in := ownershipFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.AdoptLegacy(context.Background(), "fixture", in) }()
	}
	wg.Wait()
	close(errs)
	n := 0
	for err := range errs {
		if err == nil {
			n++
		}
	}
	if n != 1 {
		t.Fatal(n)
	}
}
func TestNewLegacyInsertsKeepLegacyWriterAndNativeUUIDsCannotBeReused(t *testing.T) {
	s, _ := ownershipFixture(t)
	ctx := context.Background()
	id := uuid.New()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'새 legacy')`, id); err != nil {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, id)
	if err != nil || e.WriteOwner != "kdb" {
		t.Fatal(e, err)
	}
	p, err := s.CreateCandidate(ctx, "fixture", "native", CandidateInput{KO: "별도 native", Type: "company", Domains: []string{"economy"}, Reason: "UUID 소유권 보호 테스트"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'UUID 가로채기')`, p.ID); err == nil {
		t.Fatal("legacy stole native UUID")
	}
}
