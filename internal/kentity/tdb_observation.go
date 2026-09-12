package kentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"sort"
	"time"
)

const TDBObservationPolicy = "tdb-id-observation-v1"

type TDBObservation struct {
	ShadowID  uuid.UUID  `json:"shadow_id"`
	Binding   TDBBinding `json:"binding"`
	Available bool       `json:"available"`
	Reason    string     `json:"reason"`
}
type TDBObservationBatch struct {
	Policy     string           `json:"policy"`
	ObservedAt time.Time        `json:"observed_at"`
	Records    []TDBObservation `json:"records"`
}
type TDBObservationReport struct {
	DryRun                                                      bool `json:"dry_run"`
	Total, Available, Unavailable, Changed, Unchanged, Requeued int
}
type TDBObservationTarget struct {
	ShadowID uuid.UUID `json:"shadow_id"`
	TDBID    uuid.UUID `json:"tdb_id"`
	QID      string    `json:"qid"`
}
type TDBObservationHealth struct {
	Total, Fresh, Stale, Blocked int
	Oldest                       *time.Time
}

func (s *Store) TDBObservationHealth(ctx context.Context) (TDBObservationHealth, error) {
	var h TDBObservationHealth
	err := s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE source_observed_at>=now()-interval '15 minutes'),count(*) FILTER(WHERE source_observed_at<now()-interval '15 minutes'),count(*) FILTER(WHERE state='blocked'),min(source_observed_at) FROM kentity_tdb_shadows`).Scan(&h.Total, &h.Fresh, &h.Stale, &h.Blocked, &h.Oldest)
	return h, err
}
func (s *Store) TDBObservationTargets(ctx context.Context) ([]TDBObservationTarget, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,tdb_id,qid FROM kentity_tdb_shadows ORDER BY source_observed_at,id LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TDBObservationTarget{}
	for rows.Next() {
		var r TDBObservationTarget
		if err = rows.Scan(&r.ShadowID, &r.TDBID, &r.QID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func validateTDBObservations(in TDBObservationBatch) error {
	if in.Policy != TDBObservationPolicy || in.ObservedAt.IsZero() || time.Since(in.ObservedAt) > 5*time.Minute || time.Until(in.ObservedAt) > time.Minute || len(in.Records) < 1 || len(in.Records) > 100 {
		return ErrInvalid
	}
	seen := map[uuid.UUID]bool{}
	for _, r := range in.Records {
		if r.ShadowID == uuid.Nil || r.Binding.ID == uuid.Nil || !researchQID.MatchString(r.Binding.QID) || seen[r.ShadowID] {
			return ErrInvalid
		}
		seen[r.ShadowID] = true
		if r.Available {
			if r.Reason != "" {
				return ErrInvalid
			}
			b := TDBBindingBatch{Policy: TDBShadowPolicy, Source: "wikidata", License: "CC0", State: "live", Enabled: true, ObservedAt: in.ObservedAt, Bindings: []TDBBinding{r.Binding}}
			if r.Binding.Type == "" || validateTDBBatch(b) != nil {
				return ErrInvalid
			}
		} else {
			switch r.Reason {
			case "source_policy_blocked", "place_inactive", "link_missing", "record_deleted", "type_unsupported":
			default:
				return ErrInvalid
			}
		}
	}
	return nil
}

// Each source binding commits independently. A partially failed batch may be
// replayed without repeating work; absence is accepted only from a complete,
// successful ID-only source query, never inferred from a timeout.
func (s *Store) ObserveTDBBindings(ctx context.Context, in TDBObservationBatch, apply bool) (TDBObservationReport, error) {
	report := TDBObservationReport{DryRun: !apply, Total: len(in.Records)}
	if err := validateTDBObservations(in); err != nil {
		return report, err
	}
	records := append([]TDBObservation(nil), in.Records...)
	sort.Slice(records, func(i, j int) bool { return records[i].ShadowID.String() < records[j].ShadowID.String() })
	// Validate the complete target set before any per-binding transaction.
	for _, r := range records {
		var valid bool
		if err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kentity_tdb_shadows WHERE id=$1 AND tdb_id=$2 AND qid=$3 AND source_observed_at<=$4)`, r.ShadowID, r.Binding.ID, r.Binding.QID, in.ObservedAt).Scan(&valid); err != nil {
			return report, err
		}
		if !valid {
			return report, ErrProtected
		}
	}
	for _, r := range records {
		if r.Available {
			report.Available++
			b := TDBBindingBatch{Policy: TDBShadowPolicy, Source: "wikidata", License: "CC0", State: "live", Enabled: true, ObservedAt: in.ObservedAt, Bindings: []TDBBinding{r.Binding}}
			result, err := s.ImportTDBBindings(ctx, "kdb:tdb-id-observer", b, apply)
			if err != nil {
				return report, err
			}
			report.Changed += result.Changed
			report.Unchanged += result.Unchanged
		} else {
			report.Unavailable++
			changed, err := s.observeTDBUnavailable(ctx, r, in.ObservedAt, apply)
			if err != nil {
				return report, err
			}
			if changed {
				report.Changed++
			} else {
				report.Unchanged++
			}
		}
		if apply && r.Available && !r.Binding.Locked {
			requeued, err := s.refreshTDBSourceIfExpired(ctx, r.ShadowID)
			if err != nil {
				return report, err
			}
			if requeued {
				report.Requeued++
			}
		}
	}
	return report, nil
}
func (s *Store) observeTDBUnavailable(ctx context.Context, in TDBObservation, observed time.Time, apply bool) (bool, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s';SET LOCAL statement_timeout='10s'`); err != nil {
		return false, err
	}
	sh, err := readTDBShadow(ctx, tx, in.ShadowID, true)
	if err != nil {
		return false, err
	}
	if sh.TDBID != in.Binding.ID || sh.QID != in.Binding.QID || observed.Before(sh.ObservedAt) {
		return false, ErrProtected
	}
	sum := sha256.Sum256(mustJSON(struct {
		Policy, Reason string
		ID             uuid.UUID
		QID            string
	}{TDBObservationPolicy, in.Reason, in.Binding.ID, in.Binding.QID}))
	fp := hex.EncodeToString(sum[:])
	changed := sh.Fingerprint != fp
	if !apply {
		return changed, nil
	}
	if changed {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,before_value,after_value) SELECT id,'kdb:tdb-id-observer','source_unavailable',jsonb_build_object('fingerprint',source_fingerprint,'state',state,'result',result),jsonb_build_object('reason',$2::text) FROM kentity_tdb_shadows WHERE id=$1`, sh.ID, in.Reason); err != nil {
			return false, err
		}
		if _, err = tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='blocked',source_fingerprint=$2,source_observed_at=$3,generation=generation+1,lease_token=NULL,lease_until=NULL,next_attempt_at=NULL,result='{}',reason=$4,last_seen_at=now(),updated_at=now() WHERE id=$1`, sh.ID, fp, observed, in.Reason); err != nil {
			return false, err
		}
	} else {
		if _, err = tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET source_observed_at=$2,last_seen_at=now() WHERE id=$1`, sh.ID, observed); err != nil {
			return false, err
		}
	}
	return changed, tx.Commit(ctx)
}
func (s *Store) refreshTDBSourceIfExpired(ctx context.Context, id uuid.UUID) (bool, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return false, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s';SET LOCAL statement_timeout='10s'`); err != nil {
		return false, err
	}
	sh, err := readTDBShadow(ctx, tx, id, true)
	if err != nil {
		return false, err
	}
	if sh.Locked || time.Since(sh.ObservedAt) > 15*time.Minute || sh.State == "pending" || sh.State == "running" {
		return false, nil
	}
	expired := sh.State == "review" && sh.Proposal != nil && time.Since(sh.Proposal.ObservedAt) >= 24*time.Hour
	// Exhausted, no-evidence and failed observations get at most one fresh
	// bounded generation per 24h; unchanged source metadata cannot reset it.
	exhausted := (sh.State == "failed" || sh.State == "blocked") && time.Since(sh.UpdatedAt) >= 24*time.Hour
	if !expired && !exhausted {
		return false, nil
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,before_value,after_value) SELECT id,'kdb:tdb-id-observer','source_refresh_scheduled',jsonb_build_object('generation',generation,'result',result),jsonb_build_object('reason','24시간 독립 원천 갱신','maximum_calls',4) FROM kentity_tdb_shadows WHERE id=$1`, id); err != nil {
		return false, err
	}
	tag, err := tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='pending',attempts=0,generation=generation+1,lease_token=NULL,lease_until=NULL,next_attempt_at=now(),result='{}',reason='scheduled bounded source refresh',updated_at=now() WHERE id=$1`, id)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() != 1 {
		return false, errors.New("source refresh row disappeared")
	}
	return true, tx.Commit(ctx)
}
