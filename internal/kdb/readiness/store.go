package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	Pool          *pgxpool.Pool
	CommonEnabled bool
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (s *Store) Create(ctx context.Context, owner, key string, in Input) (*Preparation, error) {
	in, err := Normalize(in)
	if err != nil {
		return nil, err
	}
	policy := PolicyVersion
	if in.Catalog == "common" {
		if !s.CommonEnabled {
			return nil, ErrPolicy
		}
		policy = CommonPolicyVersion
	}
	if owner == "" || len(owner) > 150 {
		return nil, errors.New("authenticated owner required")
	}
	if key == "" {
		key = uuid.NewString()
	}
	if len(key) > 200 {
		return nil, errors.New("idempotency key too long")
	}
	fingerprint := hash(struct {
		Policy string
		Input  Input
	}{policy, in})
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	var id uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO kentity_preparations(owner_key,idempotency_key,payload_hash,policy_version,requested_locales,source_url,article_id,article_version)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(owner_key,idempotency_key) DO NOTHING RETURNING id`, owner, key, fingerprint, policy, in.Locales, in.SourceURL, in.ArticleID, in.ArticleVersion).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		var old string
		if err = tx.QueryRow(ctx, `SELECT id,payload_hash FROM kentity_preparations WHERE owner_key=$1 AND idempotency_key=$2`, owner, key).Scan(&id, &old); err != nil {
			return nil, err
		}
		if old != fingerprint {
			return nil, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return s.Get(ctx, owner, id)
	}
	if err != nil {
		return nil, err
	}
	batch := &pgx.Batch{}
	for i, t := range in.Terms {
		var supplied *uuid.UUID
		if t.EntityID != "" {
			parsed, _ := uuid.Parse(t.EntityID)
			supplied = &parsed
		}
		batch.Queue(`INSERT INTO kentity_preparation_items(preparation_id,ordinal,term,entity_type,context_hint,supplied_entity_id) VALUES($1,$2,$3,$4,$5,$6)`, id, i, t.KO, t.Type, t.Context, supplied)
		batch.Queue(`INSERT INTO kentity_locale_readiness(preparation_id,ordinal,locale) SELECT $1,$2,unnest($3::text[])`, id, i, in.Locales)
	}
	if err = tx.SendBatch(ctx, batch).Close(); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_readiness_events(preparation_id,state,reason,snapshot) VALUES($1,'created','request accepted; no historical readiness inferred',$2)`, id, []byte(`{"policy_version":"`+policy+`"}`)); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	// Resolution is local DB work only. External fills are durable worker jobs.
	if err = s.Refresh(ctx, id); err != nil {
		return nil, err
	}
	return s.Get(ctx, owner, id)
}

func (s *Store) Get(ctx context.Context, owner string, id uuid.UUID) (*Preparation, error) {
	p, err := s.read(ctx, owner, id)
	if err != nil {
		return nil, err
	}
	if p.PolicyVersion == CommonPolicyVersion {
		if !s.CommonEnabled {
			return nil, ErrPolicy
		}
		// Common values are never served from an unchecked historical ready cache.
		return s.refreshCommon(ctx, owner, id)
	}
	return p, nil
}

func (s *Store) read(ctx context.Context, owner string, id uuid.UUID) (*Preparation, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	p, err := readPreparation(ctx, tx, owner, id)
	if err != nil {
		return nil, err
	}
	return p, tx.Commit(ctx)
}

func readPreparation(ctx context.Context, tx pgx.Tx, owner string, id uuid.UUID) (*Preparation, error) {
	p := &Preparation{Items: []Item{}}
	err := tx.QueryRow(ctx, `SELECT id,owner_key,policy_version,status,revision,requested_locales,source_url,article_id,article_version,created_at,updated_at
 FROM kentity_preparations WHERE id=$1 AND owner_key=$2`, id, owner).Scan(&p.ID, &p.Owner, &p.PolicyVersion, &p.Status, &p.Revision, &p.RequestedLocales, &p.SourceURL, &p.ArticleID, &p.ArticleVersion, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT ordinal,term,entity_type,context_hint,supplied_entity_id,resolved_entity_id,bound_entity_id,candidate_ids,identity_state FROM kentity_preparation_items WHERE preparation_id=$1 ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var it Item
		if err = rows.Scan(&it.Ordinal, &it.Term, &it.Type, &it.Context, &it.SuppliedEntityID, &it.EntityID, &it.BoundEntityID, &it.CandidateIDs, &it.IdentityState); err != nil {
			rows.Close()
			return nil, err
		}
		it.Locales = []Locale{}
		p.Items = append(p.Items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for i := range p.Items {
		rows, err = tx.Query(ctx, `SELECT locale,state,value,source,reason,fallback_value,input_fingerprint,observed_at,first_ready_at,ready_at,COALESCE(to_jsonb(l)->'proof','{}'::jsonb)
 FROM kentity_locale_readiness l WHERE preparation_id=$1 AND ordinal=$2 ORDER BY locale`, id, p.Items[i].Ordinal)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var l Locale
			if err = rows.Scan(&l.Locale, &l.State, &l.Value, &l.Source, &l.Reason, &l.FallbackValue, &l.Fingerprint, &l.ObservedAt, &l.FirstReadyAt, &l.ReadyAt, &l.Proof); err != nil {
				rows.Close()
				return nil, err
			}
			p.Items[i].Locales = append(p.Items[i].Locales, l)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (s *Store) Refresh(ctx context.Context, id uuid.UUID) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var owner, status string
	err = tx.QueryRow(ctx, `SELECT owner_key,status FROM kentity_preparations WHERE id=$1 FOR UPDATE SKIP LOCKED`, id).Scan(&owner, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if status == "cancelled" {
		return nil
	}
	p, err := readPreparation(ctx, tx, owner, id)
	if err != nil {
		return err
	}
	if p.PolicyVersion == CommonPolicyVersion {
		rollback(tx)
		if !s.CommonEnabled {
			return nil
		}
		_, err = s.refreshCommon(ctx, owner, id)
		return err
	}
	changed, allReady, review := false, true, false
	for _, item := range p.Items {
		snap, candidates, identity, err := resolve(ctx, tx, item)
		if err != nil {
			return err
		}
		var resolved *uuid.UUID
		if snap != nil {
			resolved = &snap.ID
		}
		if _, err = tx.Exec(ctx, `UPDATE kentity_preparation_items SET resolved_entity_id=$3,bound_entity_id=COALESCE(bound_entity_id,$3),candidate_ids=$4,identity_state=$5 WHERE preparation_id=$1 AND ordinal=$2`, id, item.Ordinal, resolved, candidates, identity); err != nil {
			return err
		}
		for _, old := range item.Locales {
			next := Locale{Locale: old.Locale, State: identity, Reason: "entity identity is not resolved", Fingerprint: hash(candidates)}
			if snap != nil {
				next = evaluate(*snap, old.Locale)
			}
			if next.State == "pending" && snap != nil {
				if _, err = tx.Exec(ctx, `INSERT INTO kentity_locale_fill_jobs(entity_id,locale,input_fingerprint,policy_version,qid) VALUES($1,$2,$3,$4,$5)
 ON CONFLICT(entity_id,locale,input_fingerprint,policy_version) DO UPDATE
 SET state=CASE WHEN kentity_locale_fill_jobs.attempts<4 THEN 'pending' ELSE 'failed' END,
 next_retry_at=CASE WHEN kentity_locale_fill_jobs.attempts<4 THEN now() ELSE NULL END,
 last_reason='renewed request interest; existing retry budget retained',updated_at=now()
 WHERE kentity_locale_fill_jobs.state='stale'`, snap.ID, old.Locale, next.Fingerprint, PolicyVersion, snap.QID); err != nil {
					return err
				}
				var jobState, why string
				if err = tx.QueryRow(ctx, `SELECT state,last_reason FROM kentity_locale_fill_jobs WHERE entity_id=$1 AND locale=$2 AND input_fingerprint=$3 AND policy_version=$4`, snap.ID, old.Locale, next.Fingerprint, PolicyVersion).Scan(&jobState, &why); err != nil {
					return err
				}
				if jobState == "no_evidence" || jobState == "policy_blocked" || jobState == "failed" {
					next.State = jobState
					next.Reason = why
				}
				if jobState == "complete" {
					next.State = "policy_blocked"
					next.Reason = "previously installed value was withdrawn; explicit evidence review required"
				}
			}
			if next.State != "ready" {
				allReady = false
			}
			if next.State == "ambiguous" || next.State == "policy_blocked" || next.State == "unverified" || next.State == "no_evidence" {
				review = true
			}
			if equalState(old, next) {
				continue
			}
			changed = true
			if err = writeLocale(ctx, tx, id, item.Ordinal, old, next); err != nil {
				return err
			}
		}
	}
	newStatus := "preparing"
	if allReady {
		newStatus = "ready"
	} else if review {
		newStatus = "review"
	}
	_, err = tx.Exec(ctx, `UPDATE kentity_preparations SET status=$2,revision=revision+CASE WHEN $3 OR status<>$2 THEN 1 ELSE 0 END,
 updated_at=CASE WHEN $3 OR status<>$2 THEN now() ELSE updated_at END,
 next_check_at=now()+CASE WHEN $2='ready' THEN interval '5 minutes' ELSE interval '30 seconds' END WHERE id=$1`, id, newStatus, changed)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func equalState(a, b Locale) bool {
	return a.State == b.State && a.Value == b.Value && a.Source == b.Source && a.Reason == b.Reason && a.FallbackValue == b.FallbackValue && a.Fingerprint == b.Fingerprint
}

func writeLocale(ctx context.Context, tx pgx.Tx, id uuid.UUID, ordinal int, old, next Locale) error {
	err := tx.QueryRow(ctx, `UPDATE kentity_locale_readiness SET state=$4,value=$5,source=$6,reason=$7,fallback_value=$8,input_fingerprint=$9,
 observed_at=now(), first_ready_at=CASE WHEN $4='ready' THEN COALESCE(first_ready_at,now()) ELSE first_ready_at END,
 ready_at=CASE WHEN $4='ready' THEN CASE WHEN input_fingerprint=$9 AND value=$5 THEN COALESCE(ready_at,now()) ELSE now() END ELSE NULL END
 WHERE preparation_id=$1 AND ordinal=$2 AND locale=$3 RETURNING observed_at,first_ready_at,ready_at`, id, ordinal, next.Locale, next.State, next.Value, next.Source, next.Reason, next.FallbackValue, next.Fingerprint).Scan(&next.ObservedAt, &next.FirstReadyAt, &next.ReadyAt)
	if err != nil {
		return err
	}
	if len(next.Proof) > 0 {
		if _, err = tx.Exec(ctx, `UPDATE kentity_locale_readiness SET proof=$4 WHERE preparation_id=$1 AND ordinal=$2 AND locale=$3`, id, ordinal, next.Locale, next.Proof); err != nil {
			return err
		}
	}
	b, err := json.Marshal(struct {
		Before Locale `json:"before"`
		After  Locale `json:"after"`
	}{old, next})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO kentity_readiness_events(preparation_id,ordinal,locale,state,reason,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, id, ordinal, next.Locale, next.State, next.Reason, b)
	return err
}

func resolve(ctx context.Context, tx pgx.Tx, it Item) (*Snapshot, []uuid.UUID, string, error) {
	ids := []uuid.UUID{}
	if it.SuppliedEntityID != nil {
		ids = append(ids, *it.SuppliedEntityID)
	} else {
		rows, err := tx.Query(ctx, `SELECT id FROM kwave_entities WHERE status='active' AND (canonical_ko=$1 OR $1=ANY(aliases_ko))
 AND ($2='' OR entity_type::text=$2) ORDER BY id LIMIT 51`, it.Term, it.Type)
		if err != nil {
			return nil, nil, "", err
		}
		for rows.Next() {
			var id uuid.UUID
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, "", err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, nil, "", err
		}
	}
	// Once bound, a request cannot silently switch to a different same-name
	// entity. New ambiguity is held for review; a different identity needs a new
	// explicit request/version, preserving the meaning of historical ready times.
	if it.BoundEntityID != nil && (len(ids) != 1 || ids[0] != *it.BoundEntityID) {
		snap, err := readSnapshot(ctx, tx, *it.BoundEntityID, false)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ids, "no_evidence", nil
		}
		if err != nil {
			return nil, nil, "", err
		}
		snap.Ambiguous = true
		return &snap, ids, "ambiguous", nil
	}
	if len(ids) == 0 {
		return nil, ids, "no_evidence", nil
	}
	if len(ids) != 1 {
		return nil, ids, "ambiguous", nil
	}
	snap, err := readSnapshot(ctx, tx, ids[0], false)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ids, "no_evidence", nil
	}
	if err != nil {
		return nil, nil, "", err
	}
	if it.Type != "" && snap.Type != it.Type {
		return nil, ids, "ambiguous", nil
	}
	return &snap, ids, "resolved", nil
}

func readSnapshot(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (Snapshot, error) {
	if lock {
		// A parent row lock alone does not protect updates to existing child
		// identity rows. Hold the actual external-ID/profile rows until commit.
		if _, err := tx.Exec(ctx, `SELECT id FROM kwave_entities WHERE id=$1 FOR UPDATE`, id); err != nil {
			return Snapshot{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT provider FROM kwave_entity_external_refs WHERE entity_id=$1 ORDER BY provider FOR SHARE`, id); err != nil {
			return Snapshot{}, err
		}
		if _, err := tx.Exec(ctx, `SELECT entity_id FROM kwave_entity_person_details WHERE entity_id=$1 FOR SHARE`, id); err != nil {
			return Snapshot{}, err
		}
	}
	q := `SELECT to_jsonb(e), COALESCE((SELECT to_jsonb(d)-'updated_at'-'created_at' FROM kwave_entity_person_details d WHERE d.entity_id=e.id),'{}'::jsonb),
 COALESCE((SELECT external_id FROM kwave_entity_external_refs r WHERE r.entity_id=e.id AND r.provider='wikidata'),'')
 FROM kwave_entities e WHERE e.id=$1`
	if lock {
		q += " FOR UPDATE OF e"
	}
	var b, details []byte
	var qid string
	if err := tx.QueryRow(ctx, q, id).Scan(&b, &details, &qid); err != nil {
		return Snapshot{}, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return Snapshot{}, err
	}
	str := func(key string) string { var v string; _ = json.Unmarshal(raw[key], &v); return v }
	snap := Snapshot{ID: id, KO: str("canonical_ko"), Type: str("entity_type"), Status: str("status"), QID: qid, Values: map[string]string{}, Sources: map[string]string{}}
	_ = json.Unmarshal(raw["operator_locked"], &snap.Locked)
	_ = json.Unmarshal(raw["needs_disambig"], &snap.Ambiguous)
	_ = json.Unmarshal(raw["aliases_ko"], &snap.Aliases)
	for _, l := range []string{"en", "ja", "zh", "zh_hant", "vi", "es", "id", "pt_br"} {
		snap.Values[l] = str("canonical_" + l)
		snap.Sources[l] = str("canonical_" + l + "_source")
	}
	// Exclude timestamps/notes: worker bookkeeping must not recreate fill jobs.
	snap.Fingerprint = hash(struct {
		ID                                        uuid.UUID
		KO, Type, Status, QID, Disambig, Category string
		Locked, Ambiguous                         bool
		Aliases                                   []string
		Details                                   json.RawMessage
	}{id, snap.KO, snap.Type, snap.Status, snap.QID, str("disambig"), str("category_hint"), snap.Locked, snap.Ambiguous, snap.Aliases, details})
	var ownerColumn bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid=to_regclass('kentity_entities') AND attname='write_owner' AND NOT attisdropped)`).Scan(&ownerColumn); err != nil {
		return Snapshot{}, err
	}
	if ownerColumn {
		var owner string
		if err := tx.QueryRow(ctx, `SELECT write_owner FROM kentity_entities WHERE id=$1`, id).Scan(&owner); err != nil {
			return Snapshot{}, err
		}
		if owner != "kdb" {
			snap.Status = "common_writer"
			snap.Values = map[string]string{}
			snap.Sources = map[string]string{}
			snap.Fingerprint = hash([]string{snap.Fingerprint, owner})
		}
	}
	return snap, nil
}

func (s *Store) Cancel(ctx context.Context, owner string, id uuid.UUID, revision int64, reason string) error {
	if strings.TrimSpace(reason) == "" || len(reason) > 1000 {
		return errors.New("a cancellation reason is required")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var current int64
	var state, policy string
	err = tx.QueryRow(ctx, `SELECT revision,status,policy_version FROM kentity_preparations WHERE id=$1 AND owner_key=$2 FOR UPDATE`, id, owner).Scan(&current, &state, &policy)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != revision {
		return ErrRevision
	}
	if state == "cancelled" {
		return nil
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_preparations SET status='cancelled',cancelled_at=now(),updated_at=now(),revision=revision+1 WHERE id=$1`, id); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_locale_readiness SET state='cancelled',value='',fallback_value='',reason=$2,ready_at=NULL,observed_at=now() WHERE preparation_id=$1`, id, reason); err != nil {
		return err
	}
	if policy == CommonPolicyVersion {
		if _, err = tx.Exec(ctx, `UPDATE kentity_locale_readiness SET proof='{}' WHERE preparation_id=$1`, id); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(map[string]any{"actor": owner, "revision": revision})
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_readiness_events(preparation_id,state,reason,snapshot) VALUES($1,'cancelled',$2,$3)`, id, reason, b); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) Reconcile(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		limit = 25
	}
	rows, err := s.Pool.Query(ctx, `SELECT id FROM kentity_preparations WHERE cancelled_at IS NULL AND next_check_at<=now() AND (policy_version=$2 OR ($3 AND policy_version=$4)) ORDER BY next_check_at,id LIMIT $1`, limit, PolicyVersion, s.CommonEnabled, CommonPolicyVersion)
	if err != nil {
		return 0, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for i, id := range ids {
		if err = s.Refresh(ctx, id); err != nil {
			return i, fmt.Errorf("refresh preparation %s: %w", id, err)
		}
	}
	return len(ids), nil
}
