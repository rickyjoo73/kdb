package kentity

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

type fakeResearch struct {
	Candidates []wikidata.Candidate
	Entities   map[string]*wikidata.Entity
	Err        error
	SearchHook func()
	FetchHook  func()
}

func (s fakeResearch) Search(ctx context.Context, q, locale string, limit int, filter bool) ([]wikidata.Candidate, error) {
	if filter {
		return nil, errors.New("entertainment filter must be off")
	}
	if s.SearchHook != nil {
		s.SearchHook()
	}
	return s.Candidates, s.Err
}
func (s fakeResearch) Fetch(ctx context.Context, qid string) (*wikidata.Entity, error) {
	if s.FetchHook != nil {
		s.FetchHook()
	}
	return s.Entities[qid], s.Err
}
func researchFixture(t *testing.T) (*Store, *Entity) {
	t.Helper()
	s, _ := fixture(t)
	b, err := os.ReadFile("../../migrations/0117_kentity_resolution.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(context.Background(), string(b)); err != nil {
		t.Fatal(err)
	}
	s.AutoResearch = true
	e, err := s.CreateCandidate(context.Background(), "fixture", "research", CandidateInput{KO: "시험동명", Type: "person", Domains: []string{"sports", "politics"}, Reason: "분야별 누락 조사 합성 검증"})
	if err != nil {
		t.Fatal(err)
	}
	return s, e
}
func researchSource() fakeResearch {
	return fakeResearch{Candidates: []wikidata.Candidate{{QID: "Q123"}, {QID: "Q456"}}, Entities: map[string]*wikidata.Entity{
		"Q123": {QID: "Q123", Labels: map[string]string{"ko": "시험동명", "en": "Test Person", "ja": "テスト人物"}, SourceLabels: map[string]string{"ko": "시험동명", "en": "Test Person (politician)", "ja": "テスト人物", "zh": "測試人物", "pt": "Test Person"}, InstanceOf: []string{"Q5"}},
		"Q456": {QID: "Q456", Labels: map[string]string{"ko": "시험동명", "en": "Other Person"}, SourceLabels: map[string]string{"ko": "시험동명", "en": "Other Person"}, InstanceOf: []string{"Q5"}},
	}}
}
func TestCrossDomainResearchCollectsHomonymsWithoutPromotingOrCopying(t *testing.T) {
	s, e := researchFixture(t)
	ctx := context.Background()
	src := researchSource()
	old := uuid.New()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'기존 외부 ID')`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, old); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&Resolver{Store: s, Source: src}).ProcessOne(ctx); err != nil || !ok {
		t.Fatal(ok, err)
	}
	j, err := s.Resolution(ctx, e.ID)
	if err != nil || j.State != "review" || len(j.Proposals) != 2 || len(j.Proposals[0].ExistingIDs) != 1 {
		t.Fatal(j, err)
	}
	seen := map[string]string{}
	for _, n := range j.Proposals[0].Names {
		seen[n.Locale] = n.Value
	}
	if seen["en"] != "Test Person (politician)" || seen["zh"] != "測試人物" || seen["pt"] != "Test Person" || seen["zh-Hans"] != "" || seen["pt-BR"] != "" {
		t.Fatal("source label/locale was silently rewritten", seen)
	}
	current, err := s.Get(ctx, e.ID)
	if err != nil || current.Status != "candidate" || len(current.Names) != 1 {
		t.Fatal("source guesses published", current, err)
	}
	var anchors int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_external_ids WHERE entity_id=$1`, e.ID).Scan(&anchors); err != nil || anchors != 0 {
		t.Fatal("source guess became identity anchor", anchors, err)
	}
	if err = s.RequestResearch(ctx, "fixture", e.ID, 1, "동일 요청을 다시 전송한 경우"); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&Resolver{Store: s, Source: src}).ProcessOne(ctx); err != nil || ok {
		t.Fatal("completed research repeated", ok, err)
	}
}
func TestResearchRejectsNameElementsWrongIdentityAndWrongPersonType(t *testing.T) {
	for _, scenario := range []string{"name_element", "wrong_qid", "wrong_name", "not_person"} {
		t.Run(scenario, func(t *testing.T) {
			s, e := researchFixture(t)
			src := researchSource()
			src.Candidates = src.Candidates[:1]
			p := src.Entities["Q123"]
			switch scenario {
			case "name_element":
				p.InstanceOf = []string{"Q4167410"}
			case "wrong_qid":
				p.QID = "Q999"
			case "wrong_name":
				p.Labels["ko"] = "다른 사람"
			case "not_person":
				p.InstanceOf = []string{"Q43229"}
			}
			if _, err := (&Resolver{Store: s, Source: src}).ProcessOne(context.Background()); err != nil {
				t.Fatal(err)
			}
			j, err := s.Resolution(context.Background(), e.ID)
			if err != nil || j.State != "no_match" || len(j.Proposals) != 0 {
				t.Fatal(j, err)
			}
		})
	}
}
func TestResearchRechecksLockCancellationAndLeaseAtCommit(t *testing.T) {
	for _, scenario := range []string{"locked", "cancelled", "stale_lease"} {
		t.Run(scenario, func(t *testing.T) {
			s, e := researchFixture(t)
			ctx := context.Background()
			src := researchSource()
			src.Candidates = src.Candidates[:1]
			src.FetchHook = func() {
				switch scenario {
				case "locked":
					_, _ = s.Pool.Exec(ctx, `UPDATE kentity_entities SET operator_locked=true WHERE id=$1`, e.ID)
				case "cancelled":
					j, err := s.Resolution(ctx, e.ID)
					if err != nil {
						t.Fatal(err)
					}
					if err = s.CancelResearch(ctx, "fixture", e.ID, j.ID, j.Generation, "진행 중 기사 조사 취소 요청"); err != nil {
						t.Fatal(err)
					}
				case "stale_lease":
					_, _ = s.Pool.Exec(ctx, `UPDATE kentity_resolution_jobs SET lease_token=gen_random_uuid() WHERE entity_id=$1`, e.ID)
				}
			}
			_, err := (&Resolver{Store: s, Source: src}).ProcessOne(ctx)
			if scenario != "locked" && !errors.Is(err, ErrProtected) {
				t.Fatal(err)
			}
			if scenario == "locked" && err != nil {
				t.Fatal(err)
			}
			j, err := s.Resolution(ctx, e.ID)
			if err != nil || len(j.Proposals) != 0 {
				t.Fatal("stale output committed", j, err)
			}
		})
	}
}
func TestResearchRetriesAreBoundedAndFinalLeaseExpiryIsTerminal(t *testing.T) {
	s, e := researchFixture(t)
	ctx := context.Background()
	w := &Resolver{Store: s, Source: fakeResearch{Err: errors.New("synthetic upstream failure")}}
	for attempt := 1; attempt <= 4; attempt++ {
		if _, err := w.ProcessOne(ctx); err != nil {
			t.Fatal(err)
		}
		j, err := s.Resolution(ctx, e.ID)
		if err != nil || j.Attempts != attempt || j.State != "failed" {
			t.Fatal(j, err)
		}
		if attempt < 4 {
			if j.NextAttempt == nil {
				t.Fatal("retry missing")
			}
			_, _ = s.Pool.Exec(ctx, `UPDATE kentity_resolution_jobs SET next_attempt_at=now() WHERE entity_id=$1`, e.ID)
		} else if j.NextAttempt != nil {
			t.Fatal("unbounded retry")
		}
	}
	if ok, err := w.ProcessOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	_, err := s.Pool.Exec(ctx, `UPDATE kentity_resolution_jobs SET state='running',lease_token=gen_random_uuid(),lease_until=now()-interval '1 minute' WHERE entity_id=$1`, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := w.ProcessOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	j, err := s.Resolution(ctx, e.ID)
	if err != nil || j.State != "failed" || j.Reason != "lease_expired_budget" {
		t.Fatal(j, err)
	}
}
func TestResearchQueueClaimIsExclusive(t *testing.T) {
	s, _ := researchFixture(t)
	w := &Resolver{Store: s}
	var wg sync.WaitGroup
	jobs := make(chan *Resolution, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			j, err := w.claim(context.Background())
			if err != nil {
				t.Error(err)
			}
			jobs <- j
		}()
	}
	wg.Wait()
	close(jobs)
	n := 0
	for j := range jobs {
		if j != nil {
			n++
		}
	}
	if n != 1 {
		t.Fatal("duplicate lease", n)
	}
}
