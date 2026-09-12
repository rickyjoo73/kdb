package kentity

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func tdbMappingFixture(t *testing.T) (*Store, TDBBindingBatch, *TDBMapping) {
	t.Helper()
	s, b := shadowFixture(t)
	ctx := context.Background()
	for _, name := range []string{"0117_kentity_resolution.sql", "0118_kentity_write_ownership.sql"} {
		raw, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	s.AutoResearch = true
	b.Bindings[0].Type = "person"
	if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&TDBShadowWorker{Store: s, Source: researchSource()}).ProcessOne(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	rows, err := s.TDBShadows(ctx, "review", 10)
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	m, err := s.TDBMapping(ctx, rows[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, b, m
}
func tdbRegistration(m *TDBMapping) TDBRegistration {
	return TDBRegistration{ShadowID: m.Shadow.ID, Generation: m.Shadow.Generation, Fingerprint: m.Shadow.Fingerprint, Domains: []string{"society", "sports"}, Reason: "합성 독립 원천 기반 후보 등록 검증"}
}
func tdbDecision(m *TDBMapping, id uuid.UUID) TDBMappingDecision {
	return TDBMappingDecision{ShadowID: m.Shadow.ID, Generation: m.Shadow.Generation, Fingerprint: m.Shadow.Fingerprint, Revision: m.Revision, EntityID: id, EntityRevision: 1, Decision: "confirmed", Reason: "합성 동일 대상 연결 검수 테스트", IdentityFacts: "동일 외부 ID와 활동 분야 및 소속을 대조한 합성 식별 사실 검증입니다.", EvidenceURL: "https://example.test/identity", Attested: true}
}
func registeredTDB(t *testing.T) (*Store, TDBBindingBatch, *TDBMapping, uuid.UUID) {
	t.Helper()
	s, b, m := tdbMappingFixture(t)
	id, err := s.RegisterTDBCandidate(context.Background(), "fixture", tdbRegistration(m))
	if err != nil {
		t.Fatal(err)
	}
	m, err = s.TDBMapping(context.Background(), m.Shadow.ID)
	if err != nil {
		t.Fatal(err)
	}
	return s, b, m, id
}

func TestTDBMappingCandidateIdempotenceAndSingleWriter(t *testing.T) {
	s, b, m := tdbMappingFixture(t)
	ctx := context.Background()
	in := tdbRegistration(m)
	id, err := s.RegisterTDBCandidate(ctx, "fixture", in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.RegisterTDBCandidate(ctx, "fixture", in)
	if err != nil || again != id {
		t.Fatal(again, err)
	}
	in.Reason = "입력 내용이 바뀐 동일 후보 등록 재요청"
	if _, err = s.RegisterTDBCandidate(ctx, "fixture", in); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	e, err := s.Get(ctx, id)
	if err != nil || e.ID == b.Bindings[0].ID || e.Origin != "tdb" || e.WriteOwner != "native" || e.Status != "candidate" || e.Type != "person" || e.Subtype != "tdb:person" {
		t.Fatal(e, err)
	}
	if len(e.Names) < 3 {
		t.Fatal(e.Names)
	}
	for _, n := range e.Names {
		if n.Status != "unverified" || n.Source != "wikidata-label" {
			t.Fatal("invented approval or original TDB name", n)
		}
	}
	m, err = s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || m.Status != "review" || m.Current || m.EntityID == nil || *m.EntityID != id || len(m.Candidates) != 1 || len(m.Events) < 3 {
		t.Fatal(m, err)
	}
	var jobs, legacy int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_resolution_jobs WHERE entity_id=$1`, id).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatal(jobs, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities WHERE id=$1`, id).Scan(&legacy); err != nil || legacy != 0 {
		t.Fatal(legacy, err)
	}
	other := uuid.New()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko) VALUES($1,'합성 경쟁 쓰기')`, other); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entity_external_refs(entity_id,provider,external_id) VALUES($1,'wikidata','Q123')`, other); err == nil {
		t.Fatal("legacy stole candidate source ID")
	}
}
func TestTDBMappingApprovalDoesNotApproveNamesOrReuse(t *testing.T) {
	s, _, m, id := registeredTDB(t)
	ctx := context.Background()
	d := tdbDecision(m, id)
	if err := s.DecideTDBMapping(ctx, "fixture-reviewer", d); err != nil {
		t.Fatal(err)
	}
	m, err := s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || !m.Current || m.Status != "confirmed" {
		t.Fatal(m, err)
	}
	var license, status string
	var export bool
	if err = s.Pool.QueryRow(ctx, `SELECT license_code,export_allowed,status FROM kentity_evidence WHERE id=$1`, m.EvidenceID).Scan(&license, &export, &status); err != nil || export || license != "unreviewed" || status != "verified" {
		t.Fatal(license, export, status, err)
	}
	e, err := s.Get(ctx, id)
	if err != nil || e.Status != "candidate" {
		t.Fatal(e, err)
	}
	for _, n := range e.Names {
		if n.Status != "unverified" {
			t.Fatal(n)
		}
	}
	d = tdbDecision(m, id)
	d.Decision = "review"
	if err = s.DecideTDBMapping(ctx, "fixture-reviewer", d); err != nil {
		t.Fatal(err)
	}
	m, err = s.TDBMapping(ctx, m.Shadow.ID)
	if err != nil || m.Current || m.Status != "review" {
		t.Fatal(m, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM kentity_evidence WHERE provider='operator-tdb-link-review' AND entity_id=$1`, id).Scan(&status); err != nil || status != "withdrawn" {
		t.Fatal(status, err)
	}
}
func TestTDBMappingSourceChangesInvalidateApproval(t *testing.T) {
	for _, scenario := range []string{"source_lock", "source_type", "source_score", "recheck", "target_lock", "target_revision", "claim_withdrawn", "evidence_withdrawn", "source_expired", "proposal_expired"} {
		t.Run(scenario, func(t *testing.T) {
			s, b, m, id := registeredTDB(t)
			ctx := context.Background()
			if err := s.DecideTDBMapping(ctx, "fixture", tdbDecision(m, id)); err != nil {
				t.Fatal(err)
			}
			m, err := s.TDBMapping(ctx, m.Shadow.ID)
			if err != nil || !m.Current {
				t.Fatal(m, err)
			}
			switch scenario {
			case "source_lock", "source_type", "source_score":
				if scenario == "source_lock" {
					b.Bindings[0].Locked = true
				}
				if scenario == "source_type" {
					b.Bindings[0].Type = "organization"
				}
				if scenario == "source_score" {
					b.Bindings[0].Score = 0.7
				}
				b.ObservedAt = time.Now()
				_, err = s.ImportTDBBindings(ctx, "fixture", b, true)
			case "recheck":
				err = s.RecheckTDB(ctx, "fixture", m.Shadow.ID, m.Shadow.Generation, "독립 원천 재확인 합성 검증 요청")
			case "target_lock":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET operator_locked=true WHERE id=$1`, id)
			case "target_revision":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET revision=revision+1 WHERE id=$1`, id)
			case "claim_withdrawn":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_external_ids SET status='withdrawn' WHERE entity_id=$1`, id)
			case "evidence_withdrawn":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE id=$1`, m.EvidenceID)
			case "source_expired":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET source_observed_at=now()-interval '16 minutes' WHERE id=$1`, m.Shadow.ID)
			case "proposal_expired":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET result=jsonb_set(result,'{observed_at}',to_jsonb(now()-interval '25 hours')) WHERE id=$1`, m.Shadow.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			latest, err := s.TDBMapping(ctx, m.Shadow.ID)
			if err != nil || latest.Current {
				t.Fatal("stale mapping is current", latest, err)
			}
			if scenario == "source_lock" || scenario == "source_type" || scenario == "source_score" || scenario == "recheck" {
				if latest.Status != "conflict" {
					t.Fatal(latest)
				}
				var status string
				if err = s.Pool.QueryRow(ctx, `SELECT status FROM kentity_evidence WHERE id=$1`, m.EvidenceID).Scan(&status); err != nil || status != "withdrawn" {
					t.Fatal(status, err)
				}
			}
			if _, err = s.Get(ctx, id); err != nil {
				t.Fatal("invalidation deleted root", err)
			}
		})
	}
}
func TestTDBMappingRejectsStaleUnattestedAndWrongTarget(t *testing.T) {
	for _, scenario := range []string{"attestation", "revision", "generation", "fingerprint", "target_revision", "wrong_qid", "locked", "type"} {
		t.Run(scenario, func(t *testing.T) {
			s, _, m, id := registeredTDB(t)
			ctx := context.Background()
			d := tdbDecision(m, id)
			switch scenario {
			case "attestation":
				d.Attested = false
			case "revision":
				d.Revision++
			case "generation":
				d.Generation++
			case "fingerprint":
				d.Fingerprint = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			case "target_revision":
				d.EntityRevision++
			case "wrong_qid":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_external_ids SET external_id='Q999' WHERE entity_id=$1`, id)
			case "locked":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_entities SET operator_locked=true WHERE id=$1`, id)
			case "type":
				_, _ = s.Pool.Exec(ctx, `UPDATE kentity_entities SET entity_type='organization' WHERE id=$1`, id)
			}
			if err := s.DecideTDBMapping(ctx, "fixture", d); err == nil {
				t.Fatal("unsafe mapping accepted")
			}
			var n int
			if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_crosswalks WHERE status='confirmed'`).Scan(&n); err != nil || n != 0 {
				t.Fatal(n, err)
			}
		})
	}
}
func TestTDBMappingConcurrentDecisionCommitsOnce(t *testing.T) {
	s, _, m, id := registeredTDB(t)
	var committed atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if s.DecideTDBMapping(context.Background(), "fixture", tdbDecision(m, id)) == nil {
				committed.Add(1)
			}
		}()
	}
	wg.Wait()
	if committed.Load() != 1 {
		t.Fatal(committed.Load())
	}
}

func TestTDBMappingConcurrentSourceCandidatesReserveOneIdentity(t *testing.T) {
	s, b, m := tdbMappingFixture(t)
	ctx := context.Background()
	b.Bindings[0].ID = uuid.New()
	b.ObservedAt = time.Now()
	if _, err := s.ImportTDBBindings(ctx, "fixture", b, true); err != nil {
		t.Fatal(err)
	}
	if ok, err := (&TDBShadowWorker{Store: s, Source: researchSource()}).ProcessOne(ctx); !ok || err != nil {
		t.Fatal(ok, err)
	}
	rows, err := s.TDBShadows(ctx, "review", 10)
	if err != nil || len(rows) != 2 {
		t.Fatal(rows, err)
	}
	var wg sync.WaitGroup
	var created atomic.Int64
	for _, sh := range rows {
		wg.Add(1)
		go func(id uuid.UUID) {
			defer wg.Done()
			current, err := s.TDBMapping(ctx, id)
			if err != nil {
				return
			}
			if _, err = s.RegisterTDBCandidate(ctx, "fixture", tdbRegistration(current)); err == nil {
				created.Add(1)
			}
		}(sh.ID)
	}
	wg.Wait()
	if created.Load() != 1 {
		t.Fatal("duplicate or lost source identity", created.Load(), m.Shadow.ID)
	}
	var n int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_entities WHERE origin_system='tdb'`).Scan(&n); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestTDBMappingSourceTypeContract(t *testing.T) {
	if len(tdbTypes) != 22 {
		t.Fatal("source type inventory changed without mapping review")
	}
	for _, pair := range [][2]string{{"person", "person"}, {"organization", "company"}, {"organization", "team"}, {"education", "location"}, {"education", "organization"}, {"restaurant", "location"}} {
		if !tdbTypeCompatible(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
	for _, pair := range [][2]string{{"person", "organization"}, {"tourist_spot", "person"}, {"unrecognized", "location"}, {"education", "unknown"}} {
		if tdbTypeCompatible(pair[0], pair[1]) {
			t.Fatal(pair)
		}
	}
	s, b, m := tdbMappingFixture(t)
	b.Bindings[0].Type = ""
	b.ObservedAt = time.Now()
	if _, err := s.ImportTDBBindings(context.Background(), "fixture", b, true); !errors.Is(err, ErrProtected) {
		t.Fatal("source category silently erased", err)
	}
	if _, err := s.RegisterTDBCandidate(context.Background(), "fixture", TDBRegistration{ShadowID: m.Shadow.ID, Generation: m.Shadow.Generation, Fingerprint: m.Shadow.Fingerprint, Type: "organization", Domains: []string{"society"}, Reason: "합성 원천 유형 오염 보호 검증"}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
}
func TestTDBMappingRecheckIsBounded(t *testing.T) {
	s, _, m, _ := registeredTDB(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if i > 0 {
			if _, err := s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET last_manual_recheck_at=now()-interval '6 minutes' WHERE id=$1`, m.Shadow.ID); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.RecheckTDB(ctx, "fixture", m.Shadow.ID, m.Shadow.Generation, "합성 재확인 한도 검증 요청"); err != nil {
			t.Fatal(i, err)
		}
		if ok, err := (&TDBShadowWorker{Store: s, Source: researchSource()}).ProcessOne(ctx); !ok || err != nil {
			t.Fatal(ok, err)
		}
		var err error
		m, err = s.TDBMapping(ctx, m.Shadow.ID)
		if err != nil {
			t.Fatal(err)
		}
		if err = s.RecheckTDB(ctx, "fixture", m.Shadow.ID, m.Shadow.Generation, "즉시 반복 재확인 차단 검증"); !errors.Is(err, ErrProtected) {
			t.Fatal(err)
		}
	}
	_, _ = s.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET last_manual_recheck_at=now()-interval '6 minutes'`)
	if err := s.RecheckTDB(ctx, "fixture", m.Shadow.ID, m.Shadow.Generation, "최대 한도 초과 재확인 요청"); !errors.Is(err, ErrProtected) {
		t.Fatal(err)
	}
}
