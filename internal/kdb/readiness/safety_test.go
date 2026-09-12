package readiness

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestBoundRequestNeverSwitchesToAnotherHomonym(t *testing.T) {
	s, id := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "bound", input("en"))
	first := localeOf(t, p, "en").FirstReadyAt
	other := uuid.New()
	if _, err := s.Pool.Exec(ctx, `UPDATE kwave_entities SET canonical_ko='개명한인물' WHERE id=$1;`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,canonical_en,canonical_en_source) VALUES($1,'시험인물','Other Person','operator')`, other); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l := localeOf(t, p, "en")
	if p.Status != "review" || p.Items[0].BoundEntityID == nil || *p.Items[0].BoundEntityID != id || p.Items[0].EntityID == nil || *p.Items[0].EntityID != id || l.State != "ambiguous" || l.ReadyAt != nil || !first.Equal(*l.FirstReadyAt) {
		t.Fatalf("silent identity switch: %+v %+v", p.Items[0], l)
	}
}

func TestManualRetryPolicyRevisionAuditAndBudget(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "retry-manual", input("ja"))
	w := &Worker{Store: s, Source: fakeSource{Err: errors.New("fixture source failure")}}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	retry := func(owner, actor string, rev int64) error {
		return s.Retry(ctx, owner, actor, p.ID, rev, 0, "ja", "출처 장애 복구 확인")
	}
	if err = retry("consumer:B", "operator@test", p.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err = retry("consumer:A", "", p.Revision); !errors.Is(err, ErrPolicy) {
		t.Fatal(err)
	}
	if err = retry("consumer:A", "operator@test", p.Revision-1); !errors.Is(err, ErrRevision) {
		t.Fatal(err)
	}
	for n := 1; n <= 3; n++ {
		if err = retry("consumer:A", "operator@test", p.Revision); err != nil {
			t.Fatalf("retry %d: %v", n, err)
		}
		if count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs WHERE state='pending' AND attempts=0`) != 1 {
			t.Fatal("retry did not reset bounded job")
		}
		if count(t, s.Pool, `SELECT count(*) FROM kentity_readiness_events WHERE state='retry_requested' AND snapshot->>'actor'='operator@test'`) != n {
			t.Fatal("missing audit")
		}
		if _, err = w.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		p, err = s.Get(ctx, "consumer:A", p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = retry("consumer:A", "operator@test", p.Revision); !errors.Is(err, ErrPolicy) {
			t.Fatal("cooldown bypass", err)
		}
		if _, err = s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET last_manual_retry_at=now()-interval '6 minutes'`); err != nil {
			t.Fatal(err)
		}
	}
	if err = retry("consumer:A", "operator@test", p.Revision); !errors.Is(err, ErrPolicy) {
		t.Fatal("manual budget bypass", err)
	}
}

func TestManualRetryCannotBypassIdentityLockOrCancellation(t *testing.T) {
	for _, mode := range []string{"locked", "identity", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			s, id := fixture(t)
			ctx := context.Background()
			p := mustCreate(t, s, mode, input("ja"))
			if _, err := (&Worker{Store: s, Source: fakeSource{Err: errors.New("source failure")}}).ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			var err error
			switch mode {
			case "locked":
				_, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, id)
			case "identity":
				_, err = s.Pool.Exec(ctx, `UPDATE kwave_entity_external_refs SET external_id='Q999' WHERE entity_id=$1`, id)
			case "cancelled":
				err = s.Cancel(ctx, "consumer:A", p.ID, p.Revision, "기사 취소")
				p.Revision++
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = s.Retry(ctx, "consumer:A", "operator@test", p.ID, p.Revision, 0, "ja", "재시도"); !errors.Is(err, ErrPolicy) {
				t.Fatal(err)
			}
		})
	}
}

func TestSharedJobSurvivesOneRequestCancellation(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "first", input("ja"))
	q := mustCreate(t, s, "second", input("ja"))
	if err := s.Cancel(ctx, "consumer:A", p.ID, p.Revision, "첫 기사 취소"); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&Worker{Store: s, Source: source()}).ProcessOne(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.Refresh(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	q, err := s.Get(ctx, "consumer:A", q.ID)
	if err != nil || q.Status != "ready" {
		t.Fatal(q, err)
	}
	p, err = s.Get(ctx, "consumer:A", p.ID)
	if err != nil || p.Status != "cancelled" {
		t.Fatal(p, err)
	}
}

func TestSnapshotLocksChildIdentityRows(t *testing.T) {
	s, id := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(tx)
	if _, err = readSnapshot(ctx, tx, id, true); err != nil {
		t.Fatal(err)
	}
	other, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rollback(other)
	_, err = other.Exec(ctx, `SELECT external_id FROM kwave_entity_external_refs WHERE entity_id=$1 FOR UPDATE NOWAIT`, id)
	var pgerr *pgconn.PgError
	if !errors.As(err, &pgerr) || pgerr.Code != "55P03" {
		t.Fatal("external identity row is not protected", err)
	}
}

func TestNewRequestCanResumeCancelledFillWithoutResettingBudget(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "old-request", input("ja"))
	w := &Worker{Store: s, Source: source()}
	j, err := w.claim(ctx)
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	if err = s.Cancel(ctx, "consumer:A", p.ID, p.Revision, "취소"); err != nil {
		t.Fatal(err)
	}
	if err = w.finish(ctx, *j, source().Ent, "", ""); err != nil {
		t.Fatal(err)
	}
	q := mustCreate(t, s, "new-request", input("ja"))
	if ok, err := w.ProcessOne(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = s.Refresh(ctx, q.ID); err != nil {
		t.Fatal(err)
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs WHERE state='complete' AND attempts=2`) != 1 {
		t.Fatal("cancelled job stuck or retry budget reset")
	}
}
