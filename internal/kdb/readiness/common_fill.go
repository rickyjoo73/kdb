package readiness

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"strings"
	"time"
)

const CommonFillPolicy = "common-anchored-fill-v1"

type commonAnchor struct {
	QID, URL, License, VerifiedBy string
	EvidenceID                    uuid.UUID
	ObservedAt, VerifiedAt        time.Time
	Reserved                      bool
}

func readCommonAnchors(ctx context.Context, tx pgx.Tx, id uuid.UUID) ([]commonAnchor, error) {
	rows, err := tx.Query(ctx, `SELECT x.external_id,v.id,v.source_url,v.license_code,v.observed_at,v.verified_at,v.verified_by,
 EXISTS(SELECT 1 FROM kentity_id_reservations r WHERE r.provider='wikidata' AND r.external_id=x.external_id AND r.entity_id=x.entity_id)
 AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs l WHERE l.provider='wikidata' AND l.external_id=x.external_id AND l.entity_id<>x.entity_id)
 FROM kentity_external_ids x JOIN kentity_evidence v ON v.id=x.evidence_id AND v.entity_id=x.entity_id
 WHERE x.entity_id=$1 AND x.provider='wikidata' AND x.status='verified' AND v.provider='wikidata' AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed
 ORDER BY x.external_id,v.id LIMIT 3`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []commonAnchor{}
	for rows.Next() {
		var a commonAnchor
		if err = rows.Scan(&a.QID, &a.EvidenceID, &a.URL, &a.License, &a.ObservedAt, &a.VerifiedAt, &a.VerifiedBy, &a.Reserved); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Other-language fills must not reset a missing locale's retry budget or ready
// clock. Korean alias changes still invalidate source identity matching.
func commonInputFingerprint(s *commonSnapshot, locale string) string {
	copy := *s
	copy.Names = []commonName{}
	for _, n := range s.Names {
		if n.Locale == locale || n.Locale == "ko" {
			copy.Names = append(copy.Names, n)
		}
	}
	return hash(struct {
		Policy, Locale string
		Snapshot       commonSnapshot
	}{CommonPolicyVersion, locale, copy})
}
func commonFillEligible(s *commonSnapshot, locale string) (commonAnchor, string, bool) {
	if s == nil || s.Owner != "native" || s.Status != "active" || s.Locked || len(s.IdentityEvidence) == 0 || len(s.Anchors) != 1 {
		return commonAnchor{}, "", false
	}
	if canonical, ok := commonLocales[strings.ToLower(locale)]; !ok || canonical != locale {
		return commonAnchor{}, "", false
	}
	for _, n := range s.Names {
		if n.Locale == locale {
			return commonAnchor{}, "", false
		}
	} // Never overwrite any stored form, even blocked/expired.
	a := s.Anchors[0]
	if !a.Reserved || !validQID.MatchString(a.QID) || a.License != "CC0-1.0" || a.URL != "https://www.wikidata.org/wiki/"+a.QID || a.VerifiedBy == "" || a.ObservedAt.IsZero() || a.VerifiedAt.IsZero() {
		return commonAnchor{}, "", false
	}
	return a, hash([]string{CommonFillPolicy, commonInputFingerprint(s, locale)}), true
}
func readLockedCommonSnapshot(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*commonSnapshot, error) {
	for _, q := range []string{`SELECT id FROM kentity_entities WHERE id=$1 FOR UPDATE`, `SELECT id FROM kentity_evidence WHERE entity_id=$1 ORDER BY id FOR SHARE`, `SELECT external_id FROM kentity_external_ids WHERE entity_id=$1 ORDER BY provider,external_id FOR SHARE`, `SELECT id FROM kentity_names WHERE entity_id=$1 ORDER BY id FOR SHARE`} {
		if _, err := tx.Exec(ctx, q, id); err != nil {
			return nil, err
		}
	}
	return readCommonSnapshot(ctx, tx, id)
}
func queueCommonFill(ctx context.Context, tx pgx.Tx, s *commonSnapshot, l *Locale) error {
	a, fp, ok := commonFillEligible(s, l.Locale)
	if !ok {
		return nil
	}
	l.State, l.Reason, l.Fingerprint = "pending", "common_auto_fill_queued", fp
	if _, err := tx.Exec(ctx, `INSERT INTO kentity_locale_fill_jobs(entity_id,locale,input_fingerprint,policy_version,qid) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(entity_id,locale,input_fingerprint,policy_version) DO UPDATE SET state=CASE WHEN kentity_locale_fill_jobs.attempts<4 THEN 'pending' ELSE 'failed' END,
 next_retry_at=CASE WHEN kentity_locale_fill_jobs.attempts<4 THEN now() ELSE NULL END,last_reason='renewed request interest; existing retry budget retained',updated_at=now()
 WHERE kentity_locale_fill_jobs.state='stale'`, s.ID, l.Locale, fp, CommonFillPolicy, a.QID); err != nil {
		return err
	}
	var state, reason string
	if err := tx.QueryRow(ctx, `SELECT state,last_reason FROM kentity_locale_fill_jobs WHERE entity_id=$1 AND locale=$2 AND input_fingerprint=$3 AND policy_version=$4`, s.ID, l.Locale, fp, CommonFillPolicy).Scan(&state, &reason); err != nil {
		return err
	}
	switch state {
	case "failed", "no_evidence", "policy_blocked":
		l.State, l.Reason = state, reason
	case "complete":
		l.State, l.Reason = "policy_blocked", "previously installed value was withdrawn; explicit evidence review required"
	}
	return nil
}
func (w *Worker) claimCommon(ctx context.Context) (*job, error) {
	if !w.Store.CommonEnabled || !w.Store.CommonFillEnabled {
		return nil, nil
	}
	if _, err := w.Store.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state='failed',last_reason='worker lease expired after retry budget',lease_token=NULL,lease_until=NULL,next_retry_at=NULL,updated_at=now() WHERE policy_version=$1 AND state='running' AND lease_until<now() AND attempts>=4`, CommonFillPolicy); err != nil {
		return nil, err
	}
	j := &job{Token: uuid.New()}
	err := w.Store.Pool.QueryRow(ctx, `WITH due AS(SELECT j.id FROM kentity_locale_fill_jobs j WHERE j.policy_version=$1 AND j.attempts<4
 AND ((j.state IN ('pending','failed','no_evidence') AND j.next_retry_at<=now()) OR (j.state='running' AND j.lease_until<now()))
 AND EXISTS(SELECT 1 FROM kentity_preparations p JOIN kentity_preparation_items i ON i.preparation_id=p.id JOIN kentity_locale_readiness l USING(preparation_id,ordinal)
 WHERE p.policy_version=$3 AND p.cancelled_at IS NULL AND i.resolved_entity_id=j.entity_id AND l.locale=j.locale AND l.input_fingerprint=j.input_fingerprint AND l.state IN ('pending','failed','no_evidence'))
 ORDER BY j.next_retry_at,j.created_at,j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED)
 UPDATE kentity_locale_fill_jobs j SET state='running',attempts=attempts+1,generation=generation+1,lease_token=$2,lease_until=now()+interval '60 seconds',updated_at=now()
 FROM due WHERE j.id=due.id RETURNING j.id,j.entity_id,j.locale,j.input_fingerprint,j.qid,j.generation,j.attempts`, CommonFillPolicy, j.Token, CommonPolicyVersion).Scan(&j.ID, &j.EntityID, &j.Locale, &j.Fingerprint, &j.QID, &j.Generation, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}
func (w *Worker) ProcessCommonOne(ctx context.Context) (bool, error) {
	if w.Source == nil {
		return false, errors.New("common fill source missing")
	}
	j, err := w.claimCommon(ctx)
	if err != nil || j == nil {
		return false, err
	}
	// Avoid network calls when identity/lock changed since intake.
	tx, err := w.Store.Pool.Begin(ctx)
	if err != nil {
		return true, err
	}
	s, err := readCommonSnapshot(ctx, tx, j.EntityID)
	rollback(tx)
	if err != nil {
		return true, err
	}
	a, fp, ok := commonFillEligible(s, j.Locale)
	if !ok || fp != j.Fingerprint || a.QID != j.QID {
		return true, w.finishCommon(ctx, *j, nil, "stale", "common_auto_input_changed")
	}
	lookup, cancel := context.WithTimeout(ctx, 12*time.Second)
	e, err := w.Source.Fetch(lookup, j.QID)
	cancel()
	if err != nil {
		return true, w.finishCommon(ctx, *j, nil, "failed", "source_error")
	}
	return true, w.finishCommon(ctx, *j, e, "", "")
}
func (w *Worker) finishCommon(ctx context.Context, j job, e *wikidata.Entity, state, reason string) error {
	tx, err := w.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s';SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var interested uuid.UUID
	err = tx.QueryRow(ctx, `SELECT p.id FROM kentity_preparations p WHERE p.policy_version=$4 AND p.cancelled_at IS NULL AND EXISTS(
 SELECT 1 FROM kentity_preparation_items i JOIN kentity_locale_readiness l USING(preparation_id,ordinal) WHERE i.preparation_id=p.id AND i.resolved_entity_id=$1 AND l.locale=$2 AND l.input_fingerprint=$3 AND l.state IN ('pending','failed','no_evidence'))
 ORDER BY p.id LIMIT 1 FOR SHARE OF p`, j.EntityID, j.Locale, j.Fingerprint, CommonPolicyVersion).Scan(&interested)
	if errors.Is(err, pgx.ErrNoRows) {
		state, reason = "stale", "all requests cancelled or replaced before fill completion"
	} else if err != nil {
		return err
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT state='running' AND generation=$2 AND lease_token=$3 AND lease_until>now() AND policy_version=$4 FROM kentity_locale_fill_jobs WHERE id=$1 FOR UPDATE`, j.ID, j.Generation, j.Token, CommonFillPolicy).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrRevision
	}
	if !w.Store.CommonEnabled || !w.Store.CommonFillEnabled {
		state, reason = "policy_blocked", "common_auto_gate_off"
	}
	snap, err := readLockedCommonSnapshot(ctx, tx, j.EntityID)
	if err != nil {
		return err
	}
	anchor, fp, eligible := commonFillEligible(snap, j.Locale)
	if !eligible || fp != j.Fingerprint || anchor.QID != j.QID {
		state, reason = "stale", "common_auto_input_changed"
	}
	if state == "" {
		state, reason = "no_evidence", "common_auto_exact_locale_absent"
		if e == nil || e.QID != j.QID {
			state, reason = "policy_blocked", "common_auto_wrong_identity"
		} else if bad, _ := e.IsNameElement(); bad {
			state, reason = "policy_blocked", "common_auto_wrong_identity"
		} else if !commonSourceMatches(snap, e) {
			state, reason = "policy_blocked", "common_auto_wrong_identity"
		} else {
			value := strings.TrimSpace(e.SourceLabels[strings.ToLower(j.Locale)])
			check := map[string]string{"zh-Hans": "zh", "zh-Hant": "zh_hant", "zh-TW": "zh_hant", "pt": "pt_br", "pt-BR": "pt_br"}[j.Locale]
			if check == "" {
				check = j.Locale
			}
			if value != "" && len([]rune(value)) <= 300 && kdb.IsValidSpellingForLocale(check, value) {
				evidence := uuid.New()
				actor := "policy:" + CommonFillPolicy
				if _, err = tx.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,license_code,export_allowed,status,verified_by,verified_at,summary,observed_at) VALUES($1,$2,'wikidata',$3,$4,'name','CC0-1.0',true,'verified',$5,now(),$6,now())`, evidence, j.EntityID, j.QID, anchor.URL, actor, "Exact recorded label checked by anchored-fill policy; no human locale review or official-name assertion: "+j.Locale); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO kentity_evidence_dependencies(evidence_id,entity_id,depends_on_id) VALUES($1,$2,$3)`, evidence, j.EntityID, anchor.EvidenceID); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,$2,$3,'canonical','recorded','verified',$4,'wikidata-label')`, j.EntityID, j.Locale, value, evidence); err != nil {
					return err
				}
				if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value) VALUES($1,$2,'common_requested_locale_installed','Exact source label for reviewed stable identity; no existing name overwritten',jsonb_build_object('job_id',$3::uuid,'name_evidence_id',$4::uuid,'identity_evidence_id',$5::uuid,'locale',$6::text,'value',$7::text,'human_locale_review',false,'official_name_asserted',false))`, j.EntityID, actor, j.ID, evidence, anchor.EvidenceID, j.Locale, value); err != nil {
					return err
				}
				state, reason = "complete", "common_auto_recorded_name"
			}
		}
	}
	var retry *time.Time
	if j.Attempts < 4 {
		if state == "failed" {
			next := time.Now().Add(time.Duration(1<<uint(j.Attempts-1)) * time.Minute)
			retry = &next
		}
		if state == "no_evidence" {
			next := time.Now().Add(7 * 24 * time.Hour)
			retry = &next
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state=$2,last_reason=$3,next_retry_at=$4,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, j.ID, state, reason, retry); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func commonSourceMatches(s *commonSnapshot, e *wikidata.Entity) bool {
	if (s.Type == "person") != contains(e.InstanceOf, "Q5") {
		return false
	}
	n := func(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), "")) }
	names := []string{s.KO}
	for _, v := range s.Names {
		if v.Locale == "ko" && v.Form == "recorded" && v.Status == "verified" && v.Current && v.EvidenceStatus == "verified" && v.Export {
			names = append(names, v.Value)
		}
	}
	source := append([]string{e.SourceLabels["ko"]}, e.Aliases["ko"]...)
	for _, a := range names {
		for _, b := range source {
			if n(a) != "" && n(a) == n(b) {
				return true
			}
		}
	}
	return false
}
func contains(v []string, want string) bool {
	for _, s := range v {
		if s == want {
			return true
		}
	}
	return false
}
