package kentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const TDBShadowPolicy = "tdb-wikidata-bindings-v1"

type TDBBinding struct {
	ID     uuid.UUID `json:"tdb_id"`
	QID    string    `json:"qid"`
	Method string    `json:"method"`
	Score  float64   `json:"score"`
	Locked bool      `json:"locked"`
	Type   string    `json:"type,omitempty"`
}
type TDBBindingBatch struct {
	Policy     string       `json:"policy"`
	Source     string       `json:"source"`
	License    string       `json:"license"`
	State      string       `json:"state"`
	Enabled    bool         `json:"enabled"`
	Generated  bool         `json:"generated"`
	ObservedAt time.Time    `json:"observed_at"`
	Bindings   []TDBBinding `json:"bindings"`
}
type TDBImportReport struct {
	DryRun    bool `json:"dry_run"`
	Total     int  `json:"total"`
	Created   int  `json:"created"`
	Changed   int  `json:"changed"`
	Unchanged int  `json:"unchanged"`
}
type TDBShadow struct {
	ID                                      uuid.UUID
	TDBID                                   uuid.UUID
	QID, Method, State, Reason, Fingerprint string
	SourceType                              string
	Score                                   float64
	Locked                                  bool
	ClassConflict                           bool
	Attempts                                int
	Generation                              int64
	ObservedAt, UpdatedAt                   time.Time
	Token                                   uuid.UUID
	Proposal                                *Proposal
}

func validateTDBBatch(in TDBBindingBatch) error {
	if in.Policy != TDBShadowPolicy || in.Source != "wikidata" || in.License != "CC0" || in.State != "live" || !in.Enabled || in.Generated || len(in.Bindings) < 1 || len(in.Bindings) > 100 || in.ObservedAt.IsZero() || time.Since(in.ObservedAt) > 24*time.Hour || time.Until(in.ObservedAt) > time.Minute {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, b := range in.Bindings {
		if b.Type != "" {
			if _, ok := tdbTypes[b.Type]; !ok {
				return ErrInvalid
			}
		}
		key := b.ID.String() + ":" + b.QID
		if b.ID == uuid.Nil || !researchQID.MatchString(b.QID) || b.Method == "" || len(b.Method) > 200 || math.IsNaN(b.Score) || math.IsInf(b.Score, 0) || b.Score < 0 || b.Score > 1 || seen[key] {
			return ErrInvalid
		}
		seen[key] = true
	}
	return nil
}
func bindingFingerprint(b TDBBinding) string {
	v, _ := json.Marshal(struct {
		Policy  string
		Binding TDBBinding
	}{TDBShadowPolicy, b})
	h := sha256.Sum256(v)
	return hex.EncodeToString(h[:])
}

// This accepts only internal ID claims, never a TDB name, address, payload or
// source configuration. Names will be independently fetched from Wikidata.
func (s *Store) ImportTDBBindings(ctx context.Context, actor string, in TDBBindingBatch, apply bool) (TDBImportReport, error) {
	report := TDBImportReport{DryRun: !apply, Total: len(in.Bindings)}
	if actor == "" || len(actor) > 200 {
		return report, ErrInvalid
	}
	if err := validateTDBBatch(in); err != nil {
		return report, err
	}
	bindings := append([]TDBBinding(nil), in.Bindings...)
	sort.Slice(bindings, func(i, j int) bool {
		return bindings[i].ID.String()+bindings[i].QID < bindings[j].ID.String()+bindings[j].QID
	})
	mode := pgx.ReadOnly
	if apply {
		mode = pgx.ReadWrite
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable, AccessMode: mode})
	if err != nil {
		return report, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return report, err
	}
	for _, b := range bindings {
		fp := bindingFingerprint(b)
		var id uuid.UUID
		var old string
		var oldType string
		var previousObservation time.Time
		q := `SELECT id,source_fingerprint,source_observed_at,source_type FROM kentity_tdb_shadows WHERE tdb_id=$1 AND qid=$2`
		if apply {
			q += " FOR UPDATE"
		}
		err = tx.QueryRow(ctx, q, b.ID, b.QID).Scan(&id, &old, &previousObservation, &oldType)
		missing := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !missing {
			return report, err
		}
		if !missing && in.ObservedAt.Before(previousObservation) {
			return report, ErrProtected
		}
		if !missing && oldType != "" && b.Type == "" {
			return report, ErrProtected
		}
		if missing {
			report.Created++
		} else if fp == old {
			report.Unchanged++
		} else {
			report.Changed++
		}
		if !apply {
			continue
		}
		if missing {
			state := "pending"
			if b.Locked {
				state = "blocked"
			}
			err = tx.QueryRow(ctx, `INSERT INTO kentity_tdb_shadows(tdb_id,qid,source_fingerprint,link_method,link_score,source_observed_at,source_locked,policy_version,created_by,state,reason,source_type) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'TDB ID claim only; identity review required',$11) RETURNING id`, b.ID, b.QID, fp, b.Method, b.Score, in.ObservedAt, b.Locked, TDBShadowPolicy, actor, state, b.Type).Scan(&id)
			if err != nil {
				return report, err
			}
			if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,after_value) VALUES($1,$2,'binding_staged',$3)`, id, actor, mustJSON(b)); err != nil {
				return report, err
			}
		} else if fp != old {
			if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,before_value,after_value) SELECT id,$2,'binding_changed',jsonb_build_object('fingerprint',source_fingerprint,'result',result,'state',state),$3 FROM kentity_tdb_shadows WHERE id=$1`, id, actor, mustJSON(b)); err != nil {
				return report, err
			}
			state := "pending"
			if b.Locked {
				state = "blocked"
			}
			if _, err = tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET source_fingerprint=$2,link_method=$3,link_score=$4,source_locked=$5,source_observed_at=$6,state=$7,attempts=0,generation=generation+1,lease_token=NULL,lease_until=NULL,next_attempt_at=now(),result='{}',reason='source binding changed; prior decision invalidated',last_seen_at=now(),updated_at=now(),source_type=$8 WHERE id=$1`, id, fp, b.Method, b.Score, b.Locked, in.ObservedAt, state, b.Type); err != nil {
				return report, err
			}
		} else {
			if _, err = tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET last_seen_at=now(),source_observed_at=$2 WHERE id=$1`, id, in.ObservedAt); err != nil {
				return report, err
			}
		}
	}
	return report, tx.Commit(ctx)
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func (s *Store) TDBShadows(ctx context.Context, state string, limit int) ([]TDBShadow, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	switch state {
	case "", "pending", "running", "review", "failed", "blocked":
	default:
		return nil, ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT id,tdb_id,qid,link_method,link_score,state,reason,source_locked,attempts,generation,source_observed_at,updated_at,result,source_type,source_fingerprint FROM kentity_tdb_shadows WHERE ($1='' OR state=$1) ORDER BY updated_at DESC,id LIMIT $2`, state, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TDBShadow{}
	for rows.Next() {
		var r TDBShadow
		var b []byte
		if err = rows.Scan(&r.ID, &r.TDBID, &r.QID, &r.Method, &r.Score, &r.State, &r.Reason, &r.Locked, &r.Attempts, &r.Generation, &r.ObservedAt, &r.UpdatedAt, &b, &r.SourceType, &r.Fingerprint); err != nil {
			return nil, err
		}
		if string(b) != "{}" {
			r.Proposal = &Proposal{}
			if err = json.Unmarshal(b, r.Proposal); err != nil {
				return nil, err
			}
			r.ClassConflict = TDBSourceClassConflict(r.SourceType, r.Proposal.InstanceOf)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
