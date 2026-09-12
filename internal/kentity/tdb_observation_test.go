package kentity

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"
)

func observationBatch(b TDBBindingBatch, m *TDBMapping) TDBObservationBatch {
	return TDBObservationBatch{Policy: TDBObservationPolicy, ObservedAt: time.Now(), Records: []TDBObservation{{ShadowID: m.Shadow.ID, Binding: b.Bindings[0], Available: true}}}
}
func TestTDBObservationRefreshDoesNotResetWorkOrApproval(t *testing.T) {
	s, b, m, id := registeredTDB(t)
	ctx := context.Background()
	if err := s.DecideTDBMapping(ctx, "fixture", tdbDecision(m, id)); err != nil {
		t.Fatal(err)
	}
	before, err := s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := observationBatch(b, m)
	r, err := s.ObserveTDBBindings(ctx, input, false)
	if err != nil || !r.DryRun || r.Unchanged != 1 {
		t.Fatal(r, err)
	}
	unchanged, err := s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || !unchanged.Shadow.ObservedAt.Equal(before.Shadow.ObservedAt) {
		t.Fatal("dry run changed observation", err)
	}
	r, err = s.ObserveTDBBindings(ctx, input, true)
	if err != nil || r.Requeued != 0 || r.Unchanged != 1 {
		t.Fatal(r, err)
	}
	after, err := s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || !after.Current || after.Shadow.Generation != before.Shadow.Generation || after.Shadow.Attempts != before.Shadow.Attempts || after.Revision != before.Revision {
		t.Fatal(after, err)
	}
	h, err := s.TDBObservationHealth(ctx)
	if err != nil || h.Total != 1 || h.Fresh != 1 || h.Stale != 0 {
		t.Fatal(h, err)
	}
}
func TestTDBObservationNegativeEvidenceRevokesMappingButNotMasters(t *testing.T) {
	for _, reason := range []string{"source_policy_blocked", "place_inactive", "link_missing", "record_deleted", "type_unsupported"} {
		t.Run(reason, func(t *testing.T) {
			s, b, m, id := registeredTDB(t)
			ctx := context.Background()
			if err := s.DecideTDBMapping(ctx, "fixture", tdbDecision(m, id)); err != nil {
				t.Fatal(err)
			}
			input := observationBatch(b, m)
			input.Records[0].Available = false
			input.Records[0].Reason = reason
			r, err := s.ObserveTDBBindings(ctx, input, false)
			if err != nil || r.Changed != 1 {
				t.Fatal(r, err)
			}
			before, err := s.TDBMapping(ctx, m.Shadow.ID)
			if err != nil || !before.Current {
				t.Fatal(before, err)
			}
			r, err = s.ObserveTDBBindings(ctx, input, true)
			if err != nil || r.Unavailable != 1 || r.Changed != 1 {
				t.Fatal(r, err)
			}
			after, err := s.TDBMapping(ctx, m.Shadow.ID)
			if err != nil || after.Current || after.Status != "conflict" || after.Shadow.State != "blocked" || after.Shadow.Proposal != nil {
				t.Fatal(after, err)
			}
			input.ObservedAt = time.Now()
			r, err = s.ObserveTDBBindings(ctx, input, true)
			if err != nil || r.Unchanged != 1 {
				t.Fatal(r, err)
			}
			again, err := s.TDBMapping(ctx, m.Shadow.ID)
			if err != nil || again.Revision != after.Revision || again.Shadow.Generation != after.Shadow.Generation {
				t.Fatal("negative replay reset work", again, err)
			}
			input = observationBatch(b, m)
			r, err = s.ObserveTDBBindings(ctx, input, true)
			if err != nil || r.Changed != 1 {
				t.Fatal(r, err)
			}
			restored, err := s.TDBMapping(ctx, m.Shadow.ID)
			if err != nil || restored.Current || restored.Shadow.State != "pending" {
				t.Fatal("source recovery auto approved link", restored, err)
			}
			if _, err = s.Get(ctx, id); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestTDBObservationOldOrIncompleteTargetsCannotOverwrite(t *testing.T) {
	s, b, m := tdbMappingFixture(t)
	ctx := context.Background()
	input := observationBatch(b, m)
	input.Records[0].Available = false
	input.Records[0].Reason = "place_inactive"
	input.ObservedAt = m.Shadow.ObservedAt.Add(-time.Second)
	if _, err := s.ObserveTDBBindings(ctx, input, true); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	input.ObservedAt = time.Now()
	extra := input.Records[0]
	extra.ShadowID = uuid.New()
	input.Records = append(input.Records, extra)
	if _, err := s.ObserveTDBBindings(ctx, input, true); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
	latest, err := s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || latest.Shadow.State != "review" {
		t.Fatal("incomplete target set changed earlier record", latest, err)
	}
}
func TestTDBObservationRefreshesExpiredSourceWithBoundedBudget(t *testing.T) {
	for _, scenario := range []string{"expired", "exhausted", "recent_failure", "locked"} {
		t.Run(scenario, func(t *testing.T) {
			s, b, m := tdbMappingFixture(t)
			ctx := context.Background()
			switch scenario {
			case "expired":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET result=jsonb_set(result,'{observed_at}',to_jsonb(now()-interval '25 hours'))`)
			case "exhausted":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='failed',attempts=4,result='{}',updated_at=now()-interval '25 hours'`)
			case "recent_failure":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='failed',attempts=4,result='{}',updated_at=now()`)
			case "locked":
				b.Bindings[0].Locked = true
			}
			r, err := s.ObserveTDBBindings(ctx, observationBatch(b, m), true)
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if scenario == "expired" || scenario == "exhausted" {
				want = 1
			}
			if r.Requeued != want {
				t.Fatal(scenario, r)
			}
			r, err = s.ObserveTDBBindings(ctx, observationBatch(b, m), true)
			if err != nil || r.Requeued != 0 || r.Changed != 0 {
				t.Fatal("refresh loop reset generation", r, err)
			}
		})
	}
}
func TestTDBObservationValidationAndTargets(t *testing.T) {
	s, b, m := tdbMappingFixture(t)
	ctx := context.Background()
	targets, err := s.TDBObservationTargets(ctx)
	if err != nil || len(targets) != 1 || targets[0].ShadowID != m.Shadow.ID {
		t.Fatal(targets, err)
	}
	for _, scenario := range []string{"stale", "future", "unknown_reason", "empty_type", "extra_reason", "duplicate"} {
		input := observationBatch(b, m)
		switch scenario {
		case "stale":
			input.ObservedAt = time.Now().Add(-6 * time.Minute)
		case "future":
			input.ObservedAt = time.Now().Add(2 * time.Minute)
		case "unknown_reason":
			input.Records[0].Available = false
			input.Records[0].Reason = "timeout"
		case "empty_type":
			input.Records[0].Binding.Type = ""
		case "extra_reason":
			input.Records[0].Reason = "place_inactive"
		case "duplicate":
			input.Records = append(input.Records, input.Records[0])
		}
		if _, err = s.ObserveTDBBindings(ctx, input, true); !errors.Is(err, ErrInvalid) {
			t.Fatal(scenario, err)
		}
	}
}
