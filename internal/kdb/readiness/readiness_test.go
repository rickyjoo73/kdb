package readiness

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

func fixture(t *testing.T) (*Store, uuid.UUID) {
	t.Helper()
	pool := testdb.New(t)
	b, err := os.ReadFile("../../../migrations/0115_kentity_readiness.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	if _, err = pool.Exec(context.Background(), `INSERT INTO kwave_entities(id,canonical_ko,canonical_en,canonical_en_source) VALUES($1,'시험인물','Test Person','wikidata-label')`, id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(context.Background(), `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123456')`, id); err != nil {
		t.Fatal(err)
	}
	return &Store{Pool: pool}, id
}

func input(locales ...string) Input {
	return Input{Terms: []Term{{KO: "시험인물", Type: "person"}}, Locales: locales, ArticleID: "article-1", ArticleVersion: "v1"}
}
func mustCreate(t *testing.T, s *Store, key string, in Input) *Preparation {
	t.Helper()
	p, err := s.Create(context.Background(), "consumer:A", key, in)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func localeOf(t *testing.T, p *Preparation, loc string) Locale {
	t.Helper()
	for _, l := range p.Items[0].Locales {
		if l.Locale == loc {
			return l
		}
	}
	t.Fatalf("locale %s missing", loc)
	return Locale{}
}
func count(t *testing.T, pool *pgxpool.Pool, q string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), q).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestNormalizeAndEvaluate(t *testing.T) {
	in, err := Normalize(input("zh-Hans", "ZH_hant", "en", "en"))
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Locales) != 3 {
		t.Fatal(in)
	}
	if _, err := Normalize(input("xx")); err == nil {
		t.Fatal("unknown locale accepted")
	}
	in, err = Normalize(input())
	if err != nil || len(in.Locales) != 1 || in.Locales[0] != "en" {
		t.Fatal(in, err)
	}
	s := Snapshot{Status: "active", QID: "Q123", Values: map[string]string{"en": "Test"}, Sources: map[string]string{"en": "wikidata-label"}}
	l := evaluate(s, "ja")
	if l.State == "ready" || l.Value != "" || l.FallbackValue != "Test" {
		t.Fatal(l)
	}
	s.Values["ja"] = "한글오염"
	s.Sources["ja"] = "wikidata-label"
	if l = evaluate(s, "ja"); l.State == "ready" {
		t.Fatal("invalid script counted ready", l)
	}
	s.Values["ja"] = "テスト"
	s.Sources["ja"] = "codex-fallback"
	if l = evaluate(s, "ja"); l.State != "unverified" {
		t.Fatal(l)
	}
	s.Locked = true
	s.Sources["ja"] = ""
	if l = evaluate(s, "ja"); l.State == "ready" {
		t.Fatal("lock was treated as locale approval", l)
	}
}

func TestCreateIdempotencyOwnershipAndRequestedLocales(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "same-key", input("en"))
	if p.Status != "ready" || p.Items[0].EntityID == nil || len(p.Items[0].Locales) != 1 {
		t.Fatal(p)
	}
	l := localeOf(t, p, "en")
	if l.ReadyAt == nil || l.FirstReadyAt == nil || l.ObservedAt.Before(p.CreatedAt) {
		t.Fatal(l)
	}
	q := mustCreate(t, s, "same-key", input("en"))
	if p.ID != q.ID || p.Revision != q.Revision {
		t.Fatal("idempotency failed")
	}
	if _, err := s.Create(ctx, "consumer:A", "same-key", input("ja")); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "consumer:B", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, "consumer:B", p.ID, p.Revision, "unauthorized"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	other, err := s.Create(ctx, "consumer:B", "same-key", input("ja"))
	if err != nil || other.ID == p.ID {
		t.Fatal(other, err)
	}
}

func TestConcurrentIdempotentCreate(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	in := input("en")
	var wg sync.WaitGroup
	results := make(chan uuid.UUID, 4)
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := s.Create(ctx, "consumer:A", "concurrent", in)
			if err != nil {
				errs <- err
				return
			}
			results <- p.ID
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var first uuid.UUID
	for id := range results {
		if first == uuid.Nil {
			first = id
		}
		if id != first {
			t.Fatal("multiple IDs")
		}
	}
	if n := count(t, s.Pool, `SELECT count(*) FROM kentity_preparations`); n != 1 {
		t.Fatal(n)
	}
}

func TestFallbackNotNativeAndJobDeduplication(t *testing.T) {
	s, _ := fixture(t)
	p := mustCreate(t, s, "ja-1", input("ja"))
	q := mustCreate(t, s, "ja-2", input("ja"))
	for _, p := range []*Preparation{p, q} {
		l := localeOf(t, p, "ja")
		if p.Status == "ready" || l.State != "pending" || l.FallbackValue != "Test Person" || l.ReadyAt != nil {
			t.Fatal(p)
		}
	}
	if n := count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs`); n != 1 {
		t.Fatal(n)
	}
	n := count(t, s.Pool, `SELECT count(*) FROM kentity_readiness_events`)
	if err := s.Refresh(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_readiness_events`) != n {
		t.Fatal("unchanged refresh generated an event")
	}
}

func TestHomonymsAndExplicitEntityIdentity(t *testing.T) {
	s, id := fixture(t)
	ctx := context.Background()
	other := uuid.New()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,canonical_en,canonical_en_source) VALUES($1,'시험인물','Another Person','operator')`, other); err != nil {
		t.Fatal(err)
	}
	p := mustCreate(t, s, "ambiguous", input("en"))
	if p.Status != "review" || p.Items[0].EntityID != nil || len(p.Items[0].CandidateIDs) != 2 || localeOf(t, p, "en").State != "ambiguous" {
		t.Fatal(p)
	}
	in := input("en")
	in.Terms[0].EntityID = id.String()
	p = mustCreate(t, s, "explicit", in)
	if p.Status != "ready" || *p.Items[0].EntityID != id {
		t.Fatal(p)
	}
}

type fakeSource struct {
	Ent  *wikidata.Entity
	Err  error
	Hook func()
}

func (f fakeSource) Fetch(context.Context, string) (*wikidata.Entity, error) {
	if f.Hook != nil {
		f.Hook()
	}
	return f.Ent, f.Err
}
func source() fakeSource {
	return fakeSource{Ent: &wikidata.Entity{QID: "Q123456", Labels: map[string]string{"ko": "시험인물", "ja": "テスト人物", "zh": "测试人物"}, InstanceOf: []string{"Q5"}}}
}

func TestFillLocalesIndependentlyAndTrackWithdrawal(t *testing.T) {
	s, id := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "two-locales", input("ja", "zh"))
	w := &Worker{Store: s, Source: source()}
	for i := 0; i < 2; i++ {
		ok, err := w.ProcessOne(ctx)
		if err != nil || !ok {
			t.Fatal(ok, err)
		}
	}
	if err := s.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil || p.Status != "ready" {
		t.Fatal(p, err)
	}
	first := localeOf(t, p, "ja").FirstReadyAt
	if n := count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs`); n != 2 {
		t.Fatal("unrelated locale fill reopened job", n)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET canonical_ja_source='codex-fallback' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if err = s.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l := localeOf(t, p, "ja")
	if p.Status == "ready" || l.ReadyAt != nil || l.FirstReadyAt == nil || !first.Equal(*l.FirstReadyAt) {
		t.Fatal(p)
	}
}

func TestMissingEvidenceAndSourceFailureAreDifferent(t *testing.T) {
	for _, mode := range []string{"missing", "error", "wrong-anchor", "wrong-name", "name-element"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := fixture(t)
			ctx := context.Background()
			p := mustCreate(t, s, mode, input("ja"))
			f := source()
			want := "policy_blocked"
			switch mode {
			case "missing":
				delete(f.Ent.Labels, "ja")
				want = "no_evidence"
			case "error":
				f.Err = errors.New("fixture timeout")
				want = "failed"
			case "wrong-anchor":
				f.Ent.QID = "Q999"
			case "wrong-name":
				f.Ent.Labels["ko"] = "다른사람"
			case "name-element":
				f.Ent.InstanceOf = []string{"Q4167410"}
			}
			if _, err := (&Worker{Store: s, Source: f}).ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			if err := s.Refresh(ctx, p.ID); err != nil {
				t.Fatal(err)
			}
			p, err := s.Get(ctx, "consumer:A", p.ID)
			if err != nil {
				t.Fatal(err)
			}
			if l := localeOf(t, p, "ja"); l.State != want || l.Value != "" {
				t.Fatal(l)
			}
			if n := count(t, s.Pool, `SELECT count(*) FROM kwave_entities WHERE COALESCE(canonical_ja,'')<>''`); n != 0 {
				t.Fatal(n)
			}
		})
	}
}

func TestFillRechecksIdentityAndLockAfterFetch(t *testing.T) {
	for _, mode := range []string{"locked", "qid", "operator-value", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			s, id := fixture(t)
			ctx := context.Background()
			p := mustCreate(t, s, mode, input("ja"))
			f := source()
			f.Hook = func() {
				var err error
				switch mode {
				case "locked":
					_, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET operator_locked=true WHERE id=$1`, id)
				case "qid":
					_, err = s.Pool.Exec(ctx, `UPDATE kwave_entity_external_refs SET external_id='Q777' WHERE entity_id=$1`, id)
				case "operator-value":
					_, err = s.Pool.Exec(ctx, `UPDATE kwave_entities SET canonical_ja='管理者表記',canonical_ja_source='operator' WHERE id=$1`, id)
				case "cancel":
					err = s.Cancel(ctx, "consumer:A", p.ID, p.Revision, "article cancelled")
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := (&Worker{Store: s, Source: f}).ProcessOne(ctx); err != nil {
				t.Fatal(err)
			}
			var value string
			if err := s.Pool.QueryRow(ctx, `SELECT COALESCE(canonical_ja,'') FROM kwave_entities WHERE id=$1`, id).Scan(&value); err != nil {
				t.Fatal(err)
			}
			if mode == "operator-value" {
				if value != "管理者表記" {
					t.Fatal(value)
				}
			} else if value != "" {
				t.Fatal(value)
			}
		})
	}
}

func TestFillTransactionRollbackAndLeaseFencing(t *testing.T) {
	s, id := fixture(t)
	ctx := context.Background()
	_ = mustCreate(t, s, "lease", input("ja"))
	w := &Worker{Store: s, Source: source()}
	j, err := w.claim(ctx)
	if err != nil || j == nil {
		t.Fatal(j, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET lease_until=now()-interval '1 second' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	k, err := w.claim(ctx)
	if err != nil || k == nil || k.Generation <= j.Generation {
		t.Fatal(k, err)
	}
	if err = w.finish(ctx, *j, source().Ent, "", ""); err == nil {
		t.Fatal("stale lease committed")
	}
	if _, err = s.Pool.Exec(ctx, `ALTER TABLE kwave_kdb_evidence_refs ADD CONSTRAINT fixture_no_source CHECK(provider<>'wikidata')`); err != nil {
		t.Fatal(err)
	}
	if err = w.finish(ctx, *k, source().Ent, "", ""); err == nil {
		t.Fatal("partial fill committed")
	}
	var v string
	if err = s.Pool.QueryRow(ctx, `SELECT COALESCE(canonical_ja,'') FROM kwave_entities WHERE id=$1`, id).Scan(&v); err != nil || v != "" {
		t.Fatal(v, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET attempts=4,lease_until=now()-interval '1 second' WHERE id=$1`, j.ID); err != nil {
		t.Fatal(err)
	}
	if k, err = w.claim(ctx); err != nil || k != nil {
		t.Fatal(k, err)
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs WHERE state='running'`) != 0 {
		t.Fatal("last-attempt crash left running job")
	}
}

func TestCancelRevisionAndNoFurtherFill(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "cancel", input("ja"))
	if err := s.Cancel(ctx, "consumer:A", p.ID, p.Revision-1, "stale"); !errors.Is(err, ErrRevision) {
		t.Fatal(err)
	}
	if err := s.Cancel(ctx, "consumer:A", p.ID, p.Revision, "article superseded"); err != nil {
		t.Fatal(err)
	}
	if err := s.Refresh(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil || p.Status != "cancelled" || localeOf(t, p, "ja").State != "cancelled" {
		t.Fatal(p, err)
	}
	if ok, err := (&Worker{Store: s, Source: source()}).ProcessOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
}

func TestRetryBackoffAndUnchangedInputDoesNotResetBudget(t *testing.T) {
	s, _ := fixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "retry", input("ja"))
	w := &Worker{Store: s, Source: fakeSource{Err: errors.New("temporary failure")}}
	if _, err := w.ProcessOne(ctx); err != nil {
		t.Fatal(err)
	}
	var attempts int
	var next time.Time
	if err := s.Pool.QueryRow(ctx, `SELECT attempts,next_retry_at FROM kentity_locale_fill_jobs`).Scan(&attempts, &next); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || !next.After(time.Now()) {
		t.Fatal(attempts, next)
	}
	for i := 0; i < 3; i++ {
		if err := s.Refresh(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := w.ProcessOne(ctx); err != nil || ok {
		t.Fatal("backoff bypassed", ok, err)
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs`) != 1 {
		t.Fatal("identical input requeued")
	}
}
