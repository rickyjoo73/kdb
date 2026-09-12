package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"os"
	"testing"
	"time"
)

func commonFixture(t *testing.T) (*Store, uuid.UUID, uuid.UUID) {
	t.Helper()
	s, _ := fixture(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `ALTER TABLE kwave_persons ADD name_ko text`); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"0116_kentity_core.sql", "0117_kentity_resolution.sql", "0118_kentity_write_ownership.sql", "0119_kentity_common_readiness.sql"} {
		b, err := os.ReadFile("../../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, string(b)); err != nil {
			t.Fatal(name, err)
		}
	}
	id, evidence := uuid.New(), uuid.New()
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,status) VALUES($1,'person','공통동명','native','candidate');`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,license_code,export_allowed,status,verified_by,verified_at) VALUES($1,$2,'fixture','fixture-identity','https://example.test/identity','CC0-1.0',true,'verified','synthetic-fixture',now())`, evidence, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,'en','Common Person','canonical','recorded','verified',$2,'wikidata-label'),($1,'pt','Pessoa comum','canonical','recorded','verified',$2,'wikidata-label'),($1,'zh','共同人物','canonical','recorded','verified',$2,'wikidata-label');`, id, evidence); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_entities SET status='active',revision=revision+1 WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	s.CommonEnabled = true
	return s, id, evidence
}
func commonInput(id uuid.UUID, locales ...string) Input {
	return Input{Catalog: "common", Terms: []Term{{KO: "공통동명", Type: "person", EntityID: id.String()}}, Locales: locales, ArticleID: "common-fixture", ArticleVersion: "v1"}
}

func TestCommonReadinessExactLocalesAndEvidenceWithdrawal(t *testing.T) {
	s, id, evidence := commonFixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "common-exact", commonInput(id, "en", "pt", "pt-BR", "zh", "zh-Hans", "zh-Hant", "ja"))
	if p.PolicyVersion != CommonPolicyVersion {
		t.Fatal(p)
	}
	for _, l := range []string{"en", "pt", "zh"} {
		v := localeOf(t, p, l)
		if v.State != "ready" || len(v.Proof) < 50 {
			t.Fatal(l, v)
		}
	}
	for _, l := range []string{"pt-BR", "zh-Hans", "zh-Hant", "ja"} {
		v := localeOf(t, p, l)
		if v.State != "no_evidence" || v.Value != "" || v.FallbackValue != "Common Person" {
			t.Fatal("generic locale counted ready", l, v)
		}
	}
	var n int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM kentity_locale_fill_jobs`).Scan(&n); err != nil || n != 0 {
		t.Fatal("common requested legacy filler", n, err)
	}
	oldReady := localeOf(t, p, "en").FirstReadyAt
	if _, err := s.Pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE id=$1`, evidence); err != nil {
		t.Fatal(err)
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l := localeOf(t, p, "en")
	if l.State != "policy_blocked" || l.ReadyAt != nil || l.Value != "" || string(l.Proof) != "{}" || l.FirstReadyAt == nil || !l.FirstReadyAt.Equal(*oldReady) {
		t.Fatal("withdrawn cached ready", l)
	}
}

func TestCommonReadinessNameOnlyNeverMeansIdentityConfirmed(t *testing.T) {
	s, id, _ := commonFixture(t)
	ctx := context.Background()
	in := commonInput(id, "en")
	in.Terms[0].EntityID = ""
	p := mustCreate(t, s, "common-name", in)
	if p.Items[0].IdentityState != "ambiguous" || p.Items[0].EntityID != nil || len(p.Items[0].CandidateIDs) != 1 || localeOf(t, p, "en").State == "ready" {
		t.Fatal(p)
	}
	in.Terms[0].EntityID = id.String()
	in.Terms[0].KO = "다른 이름"
	p = mustCreate(t, s, "common-wrong-term", in)
	if localeOf(t, p, "en").State != "ambiguous" {
		t.Fatal(p)
	}
	if _, err := s.Get(ctx, "consumer:B", p.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant leak", err)
	}
}

func TestCommonReadinessBlocksUnreviewedGeneratedExpiredAndLocked(t *testing.T) {
	for _, scenario := range []string{"unverified", "generated", "expired", "future", "locked", "reuse_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			s, id, evidence := commonFixture(t)
			ctx := context.Background()
			var err error
			switch scenario {
			case "unverified":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_names SET status='unverified' WHERE entity_id=$1 AND locale='en'`, id)
			case "generated":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_names SET form='generated' WHERE entity_id=$1 AND locale='en'`, id)
			case "expired":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_names SET valid_until=CURRENT_DATE-1 WHERE entity_id=$1 AND locale='en'`, id)
			case "future":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_names SET valid_from=CURRENT_DATE+1 WHERE entity_id=$1 AND locale='en'`, id)
			case "locked":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET operator_locked=true WHERE id=$1`, id)
			case "reuse_revoked":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_evidence SET export_allowed=false WHERE id=$1`, evidence)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := mustCreate(t, s, "blocked", commonInput(id, "en"))
			if l := localeOf(t, p, "en"); l.State == "ready" || l.Value != "" {
				t.Fatal(l)
			}
		})
	}
}

func TestCommonReadinessCancellationFeatureGateAndObservationTime(t *testing.T) {
	s, id, _ := commonFixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "ready", commonInput(id, "en"))
	l := localeOf(t, p, "en")
	if l.FirstReadyAt == nil {
		t.Fatal(l)
	}
	var proof map[string]any
	if err := json.Unmarshal(l.Proof, &proof); err != nil || proof["entity_id"] != id.String() {
		t.Fatal(proof, err)
	}
	p2, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil || p2.Revision != p.Revision || !localeOf(t, p2, "en").FirstReadyAt.Equal(*l.FirstReadyAt) {
		t.Fatal(p2, err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_names SET value='Revised Person',revision=revision+1 WHERE entity_id=$1 AND locale='en'`, id); err != nil {
		t.Fatal(err)
	}
	p2, err = s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	l2 := localeOf(t, p2, "en")
	if l2.Value != "Revised Person" || !l2.ReadyAt.After(*l.ReadyAt) || !l2.FirstReadyAt.Equal(*l.FirstReadyAt) {
		t.Fatal(l, l2)
	}
	s.CommonEnabled = false
	if _, err = s.Get(ctx, "consumer:A", p.ID); !errors.Is(err, ErrPolicy) {
		t.Fatal("disabled cached ready", err)
	}
	if _, err = s.Create(ctx, "consumer:A", "disabled", commonInput(id, "en")); !errors.Is(err, ErrPolicy) {
		t.Fatal(err)
	}
	s.CommonEnabled = true
	if err = s.Cancel(ctx, "consumer:A", p.ID, p2.Revision, "기사 버전 취소 합성 검증"); err != nil {
		t.Fatal(err)
	}
	p2, err = s.Get(ctx, "consumer:A", p.ID)
	if err != nil || p2.Status != "cancelled" || localeOf(t, p2, "en").Value != "" || string(localeOf(t, p2, "en").Proof) != "{}" {
		t.Fatal(p2, err)
	}
	if time.Since(*l.FirstReadyAt) > time.Minute {
		t.Fatal("historical readiness inferred")
	}
}
