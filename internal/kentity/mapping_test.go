package kentity

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func mappingFixture(t *testing.T) (*Store, MappingDecision) {
	t.Helper()
	s, _ := fixture(t)
	ctx := context.Background()
	ms, err := s.Mappings(ctx, "review", 50)
	if err != nil || len(ms) != 1 {
		t.Fatal(ms, err)
	}
	m, err := s.Mapping(ctx, ms[0].SourceID)
	if err != nil || len(m.Candidates) != 1 {
		t.Fatal(m, err)
	}
	p := m.Candidates[0]
	return s, MappingDecision{SourceID: m.SourceID, EntityID: p.ID, Revision: m.Revision, EntityRevision: p.Revision, Fingerprint: m.Fingerprint, EntityFingerprint: p.Fingerprint, Decision: "confirmed", Reason: "합성 테스트에서 동일인 검수 근거 확인", EvidenceURL: "https://example.test/identity", IdentityFacts: "독립 식별자와 소속 이력 및 활동 기록을 비교한 합성 검수 근거입니다.", Attested: true}
}
func TestMappingApprovalIsAuditedButNeverCopiesOrLicensesProfile(t *testing.T) {
	s, in := mappingFixture(t)
	ctx := context.Background()
	if err := s.DecideMapping(ctx, "reviewer-fixture", in); err != nil {
		t.Fatal(err)
	}
	m, err := s.Mapping(ctx, in.SourceID)
	if err != nil || m.Status != "confirmed" || m.EntityID == nil || *m.EntityID != in.EntityID || m.Revision != 2 {
		t.Fatal(m, err)
	}
	var evidence, audits, copies int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM kentity_evidence WHERE entity_id=$1 AND status='verified' AND NOT export_allowed AND license_code='unreviewed'),(SELECT count(*) FROM kentity_audit_events WHERE entity_id=$1 AND action='legacy_mapping_decision'),(SELECT count(*) FROM kwave_entity_person_details WHERE entity_id=$1)`, in.EntityID).Scan(&evidence, &audits, &copies)
	if err != nil || evidence != 1 || audits != 1 || copies != 0 {
		t.Fatal(evidence, audits, copies, err)
	}
	if err = s.DecideMapping(ctx, "reviewer-fixture", in); !errors.Is(err, ErrProtected) {
		t.Fatal("stale decision", err)
	}
}
func TestMappingRejectsStaleSourceProfileLockedEntityAndNameOnlyApproval(t *testing.T) {
	for _, scenario := range []string{"source_changed", "target_profile_changed", "locked", "no_facts", "no_attestation", "bad_url", "foreign_id"} {
		t.Run(scenario, func(t *testing.T) {
			s, in := mappingFixture(t)
			ctx := context.Background()
			expected := ErrProtected
			switch scenario {
			case "source_changed":
				_, _ = s.Pool.Exec(ctx, `UPDATE kwave_persons SET name_ko='수정된 이름' WHERE id=$1`, in.SourceID)
			case "target_profile_changed":
				_, _ = s.Pool.Exec(ctx, `INSERT INTO kwave_entity_person_details(entity_id,agency) VALUES($1,'수정된 소속')`, in.EntityID)
			case "locked":
				_, _ = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, in.EntityID)
			case "no_facts":
				in.IdentityFacts = "이름만 같음"
				expected = ErrInvalid
			case "no_attestation":
				in.Attested = false
				expected = ErrInvalid
			case "bad_url":
				in.EvidenceURL = "javascript:alert(1)"
				expected = ErrInvalid
			case "foreign_id":
				in.EntityID = uuid.New()
			}
			if err := s.DecideMapping(ctx, "fixture", in); !errors.Is(err, expected) {
				t.Fatal(err)
			}
			var n int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_evidence`).Scan(&n); err != nil || n != 0 {
				t.Fatal("partial write", n, err)
			}
		})
	}
}
func TestConcurrentMappingDecisionsCommitOnlyOnce(t *testing.T) {
	s, in := mappingFixture(t)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.DecideMapping(context.Background(), "fixture", in) }()
	}
	wg.Wait()
	close(errs)
	ok := 0
	for err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatal("expected exactly one decision", ok)
	}
}
