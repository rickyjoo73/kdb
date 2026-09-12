package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrPolicy = errors.New("operation blocked by current identity, lock or retry policy")

// Retry is for authenticated operator actions, not automatic loop resets. The
// caller supplies its verified actor identity; consumers have no retry route.
func (s *Store) Retry(ctx context.Context, owner, actor string, id uuid.UUID, revision int64, ordinal int, locale, reason string) error {
	if strings.TrimSpace(actor) == "" || strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return ErrPolicy
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var current int64
	var status, policy string
	err = tx.QueryRow(ctx, `SELECT revision,status,policy_version FROM kentity_preparations WHERE id=$1 AND owner_key=$2 FOR UPDATE`, id, owner).Scan(&current, &status, &policy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != revision {
		return ErrRevision
	}
	if status == "cancelled" {
		return ErrPolicy
	}
	jobPolicy := PolicyVersion
	if policy == CommonPolicyVersion {
		if !s.CommonEnabled || !s.CommonFillEnabled {
			return ErrPolicy
		}
		jobPolicy = CommonFillPolicy
	} else if policy != PolicyVersion {
		return ErrPolicy
	}
	var entityID, jobID uuid.UUID
	var fingerprint, jobState string
	var previous *time.Time
	var retries int
	err = tx.QueryRow(ctx, `SELECT j.id,j.entity_id,j.input_fingerprint,j.state,j.manual_retries,j.last_manual_retry_at
 FROM kentity_locale_readiness l JOIN kentity_preparation_items i USING(preparation_id,ordinal)
 JOIN kentity_locale_fill_jobs j ON j.entity_id=i.resolved_entity_id AND j.locale=l.locale AND j.input_fingerprint=l.input_fingerprint AND j.policy_version=$4
 WHERE l.preparation_id=$1 AND l.ordinal=$2 AND l.locale=$3 FOR UPDATE OF j`, id, ordinal, locale, jobPolicy).Scan(&jobID, &entityID, &fingerprint, &jobState, &retries, &previous)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrPolicy
	}
	if err != nil {
		return err
	}
	if jobState != "failed" && jobState != "no_evidence" {
		return ErrPolicy
	}
	if previous != nil && time.Since(*previous) < 5*time.Minute {
		return ErrPolicy
	}
	if previous == nil || time.Since(*previous) > 24*time.Hour {
		retries = 0
	}
	if retries >= 3 {
		return ErrPolicy
	}
	if policy == CommonPolicyVersion {
		snap, err := readLockedCommonSnapshot(ctx, tx, entityID)
		if err != nil {
			return err
		}
		_, fp, ok := commonFillEligible(snap, locale)
		if !ok || fp != fingerprint {
			return ErrPolicy
		}
	} else {
		snap, err := readSnapshot(ctx, tx, entityID, true)
		if err != nil {
			return err
		}
		l := evaluate(snap, locale)
		if l.State != "pending" || l.Fingerprint != fingerprint {
			return ErrPolicy
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state='pending',attempts=0,next_retry_at=now(),
 manual_retries=$2,last_manual_retry_at=now(),last_reason=$3,updated_at=now() WHERE id=$1`, jobID, retries+1, "operator retry: "+reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_locale_readiness SET state='pending',reason='operator requested bounded retry',observed_at=now() WHERE preparation_id=$1 AND ordinal=$2 AND locale=$3`, id, ordinal, locale); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_preparations SET revision=revision+1,next_check_at=now(),updated_at=now() WHERE id=$1`, id); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"actor": actor, "job_id": jobID, "maximum_source_calls": 4, "previous_revision": revision})
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_readiness_events(preparation_id,ordinal,locale,state,reason,snapshot) VALUES($1,$2,$3,'retry_requested',$4,$5)`, id, ordinal, locale, reason, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
