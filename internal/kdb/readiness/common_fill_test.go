package readiness

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"os"
	"strings"
	"testing"
	"time"
)

func commonFillFixture(t *testing.T) (*Store, uuid.UUID, uuid.UUID) {
	t.Helper()
	s, id, _ := commonFixture(t)
	ctx := context.Background()
	b, err := os.ReadFile("../../../migrations/0121_kentity_common_fill.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, string(b)); err != nil {
		t.Fatal(err)
	}
	anchor := uuid.New()
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,license_code,export_allowed,status,verified_by,verified_at) VALUES($1,$2,'wikidata','Q9000121','https://www.wikidata.org/wiki/Q9000121','identity','CC0-1.0',true,'verified','synthetic-identity-review',now())`, anchor, id); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_external_ids(entity_id,provider,external_id,status,evidence_id) VALUES($1,'wikidata','Q9000121','verified',$2)`, id, anchor); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `INSERT INTO kentity_id_reservations(provider,external_id,native_owner) VALUES('wikidata','Q9000121',$1)`, id); err != nil {
		t.Fatal(err)
	}
	s.CommonFillEnabled = true
	return s, id, anchor
}
func commonFillSource() fakeSource {
	return fakeSource{Ent: &wikidata.Entity{QID: "Q9000121", SourceLabels: map[string]string{"ko": "공통동명", "ja": "テスト人物", "zh": "共同人物", "zh-hans": "共同人物", "pt": "Pessoa comum"}, InstanceOf: []string{"Q5"}}}
}
func TestCommonFillExactLocalesAndIndependentBudgets(t *testing.T) {
	s, id, anchor := commonFillFixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "common-fill-exact", commonInput(id, "en", "ja", "zh-Hans", "pt-BR"))
	oldReady := localeOf(t, p, "en").ReadyAt
	if localeOf(t, p, "ja").State != "pending" || count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs WHERE policy_version='common-anchored-fill-v1'`) != 3 {
		t.Fatal(p)
	}
	worker := &Worker{Store: s, Source: commonFillSource()}
	if ok, err := worker.ProcessOne(ctx); err != nil || ok {
		t.Fatal("legacy worker claimed common job", ok, err)
	}
	for i := 0; i < 3; i++ {
		if ok, err := worker.ProcessCommonOne(ctx); err != nil || !ok {
			t.Fatal(i, ok, err)
		}
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"ja", "zh-Hans"} {
		l := localeOf(t, p, locale)
		if l.State != "ready" || l.Reason != "common_auto_recorded_name" || !strings.Contains(string(l.Proof), "policy:common-anchored-fill-v1") {
			t.Fatal(l)
		}
		if strings.Contains(string(l.Proof), "verified_by") || strings.Contains(string(l.Proof), "synthetic-identity-review") {
			t.Fatal("reviewer identifier leaked in consumer proof")
		}
	}
	if l := localeOf(t, p, "pt-BR"); l.State != "no_evidence" || l.Value != "" {
		t.Fatal("generic source locale relabelled", l)
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs`) != 3 || !localeOf(t, p, "en").ReadyAt.Equal(*oldReady) {
		t.Fatal("unrelated language reset job budget or ready clock")
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_evidence_dependencies`) != 2 {
		t.Fatal("identity dependencies missing")
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_evidence SET export_allowed=false WHERE id=$1`, anchor); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, locale := range []string{"ja", "zh-Hans"} {
		l := localeOf(t, p, locale)
		if l.State == "ready" || l.Value != "" || string(l.Proof) != "{}" {
			t.Fatal("dependent name survived withdrawn identity", l)
		}
	}
	if localeOf(t, p, "en").State != "ready" {
		t.Fatal("unrelated reviewed evidence incorrectly withdrawn")
	}
	if count(t, s.Pool, `SELECT count(*) FROM kentity_evidence WHERE verified_by='policy:common-anchored-fill-v1' AND status='blocked' AND NOT export_allowed`) != 2 {
		t.Fatal("dependency withdrawal missing")
	}
}
func TestCommonFillRechecksCancellationLocksIdentityAndExistingNames(t *testing.T) {
	for _, scenario := range []string{"cancel", "lock", "withdraw", "existing_name", "lease", "wrong_qid", "wrong_name", "wrong_type", "name_element", "gate"} {
		t.Run(scenario, func(t *testing.T) {
			s, id, anchor := commonFillFixture(t)
			ctx := context.Background()
			p := mustCreate(t, s, scenario, commonInput(id, "ja"))
			src := commonFillSource()
			switch scenario {
			case "wrong_qid":
				src.Ent.QID = "Q9999"
			case "wrong_name":
				src.Ent.SourceLabels["ko"] = "다른 사람"
			case "wrong_type":
				src.Ent.InstanceOf = []string{"Q43229"}
			case "name_element":
				src.Ent.InstanceOf = []string{"Q4167410"}
			}
			src.Hook = func() {
				var err error
				switch scenario {
				case "cancel":
					err = s.Cancel(ctx, "consumer:A", p.ID, p.Revision, "synthetic cancellation")
				case "lock":
					_, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET operator_locked=true WHERE id=$1`, id)
				case "withdraw":
					_, err = s.Pool.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE id=$1`, anchor)
				case "existing_name":
					_, err = s.Pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,source_code) VALUES($1,'ja','既存記録','canonical','unknown','synthetic-existing')`, id)
				case "lease":
					_, err = s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET lease_token=gen_random_uuid()`)
				case "gate":
					s.CommonFillEnabled = false
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			_, err := (&Worker{Store: s, Source: src}).ProcessCommonOne(ctx)
			if scenario == "lease" {
				if !errors.Is(err, ErrRevision) {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if count(t, s.Pool, `SELECT count(*) FROM kentity_names WHERE source_code='wikidata-label' AND locale='ja'`) != 0 {
				t.Fatal("unsafe name installed", scenario)
			}
		})
	}
}
func TestCommonFillGateUnanchoredAndStoredFormsNeverQueue(t *testing.T) {
	for _, scenario := range []string{"gate", "anchor", "reservation", "stored", "candidate", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			s, id, anchor := commonFillFixture(t)
			ctx := context.Background()
			var err error
			switch scenario {
			case "gate":
				s.CommonFillEnabled = false
			case "anchor":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_external_ids SET status='unverified' WHERE entity_id=$1`, id)
			case "reservation":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_id_reservations SET native_owner=NULL WHERE native_owner=$1`, id)
			case "stored":
				_, err = s.Pool.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,source_code) VALUES($1,'ja','推定名前','alias','generated','fixture')`, id)
			case "candidate":
				_, err = s.Pool.Exec(ctx, `UPDATE kentity_entities SET status='candidate' WHERE id=$1`, id)
			case "legacy":
				_ = anchor
				id = uuid.New()
				_, err = s.Pool.Exec(ctx, `INSERT INTO kwave_entities(id,canonical_ko,entity_type,status) VALUES($1,'공통동명','person','active')`, id)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := mustCreate(t, s, scenario, commonInput(id, "ja"))
			if localeOf(t, p, "ja").State == "pending" || count(t, s.Pool, `SELECT count(*) FROM kentity_locale_fill_jobs`) != 0 {
				t.Fatal("protected state queued", scenario, p)
			}
		})
	}
}
func TestCommonFillRetryBudgetAndOperatorRecheck(t *testing.T) {
	s, id, _ := commonFillFixture(t)
	ctx := context.Background()
	p := mustCreate(t, s, "retry", commonInput(id, "ja"))
	worker := &Worker{Store: s, Source: fakeSource{Err: errors.New("synthetic upstream")}}
	for i := 1; i <= 4; i++ {
		if ok, err := worker.ProcessCommonOne(ctx); err != nil || !ok {
			t.Fatal(i, ok, err)
		}
		if i < 4 {
			if _, err := s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET next_retry_at=now()`); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ok, err := worker.ProcessCommonOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	p, err := s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if localeOf(t, p, "ja").State != "failed" {
		t.Fatal(p)
	}
	if err = s.Retry(ctx, "consumer:B", "fixture", p.ID, p.Revision, 0, "ja", "bounded source recovery"); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant retry leak", err)
	}
	if err = s.Retry(ctx, "consumer:A", "fixture", p.ID, p.Revision, 0, "ja", "bounded source recovery"); err != nil {
		t.Fatal(err)
	}
	p, err = s.Get(ctx, "consumer:A", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state='failed'`); err != nil {
		t.Fatal(err)
	}
	if err = s.Retry(ctx, "consumer:A", "fixture", p.ID, p.Revision, 0, "ja", "too soon recovery"); !errors.Is(err, ErrPolicy) {
		t.Fatal("cooldown bypass", err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state='running',attempts=4,lease_until=now()-interval '1 minute',lease_token=gen_random_uuid()`); err != nil {
		t.Fatal(err)
	}
	if ok, err := worker.ProcessCommonOne(ctx); err != nil || ok {
		t.Fatal(ok, err)
	}
	var next *time.Time
	if err = s.Pool.QueryRow(ctx, `SELECT next_retry_at FROM kentity_locale_fill_jobs`).Scan(&next); err != nil || next != nil {
		t.Fatal("final lease was not terminal", next, err)
	}
}
