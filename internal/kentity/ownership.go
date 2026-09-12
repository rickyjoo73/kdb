package kentity

import (
	"context"
	"errors"
	"net/url"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LegacyOwnership struct {
	Status, Reason, Fingerprint string
	Locked                      bool
}

func (s *Store) LegacyOwnership(ctx context.Context, id uuid.UUID) (*LegacyOwnership, error) {
	o := &LegacyOwnership{}
	err := s.Pool.QueryRow(ctx, `SELECT status,COALESCE(notes,''),md5(to_jsonb(e)::text),operator_locked FROM kwave_entities e WHERE id=$1`, id).Scan(&o.Status, &o.Reason, &o.Fingerprint, &o.Locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return o, err
}

type OwnershipInput struct {
	ID                                   uuid.UUID
	Revision                             int64
	Fingerprint, Type, Reason, SourceURL string
	Domains                              []string
	Attested                             bool
}

func (s *Store) AdoptLegacy(ctx context.Context, actor string, in OwnershipInput) error {
	u, err := url.Parse(in.SourceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || actor == "" || in.ID == uuid.Nil || in.Revision < 1 || in.Fingerprint == "" || !supportedTypes[in.Type] || len([]rune(strings.TrimSpace(in.Reason))) < 20 || len(in.Reason) > 2000 || len(in.SourceURL) > 2000 || !in.Attested || len(in.Domains) < 1 || len(in.Domains) > 8 {
		return ErrInvalid
	}
	domains := map[string]bool{}
	for _, d := range in.Domains {
		if !supportedDomains[d] {
			return ErrInvalid
		}
		domains[d] = true
	}
	in.Domains = nil
	for d := range domains {
		in.Domains = append(in.Domains, d)
	}
	sort.Strings(in.Domains)
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var sourceState, sourceFingerprint string
	var sourceLocked bool
	err = tx.QueryRow(ctx, `SELECT status,operator_locked,md5(to_jsonb(e)::text) FROM kwave_entities e WHERE id=$1 FOR UPDATE`, in.ID).Scan(&sourceState, &sourceLocked, &sourceFingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	var revision int64
	var owner string
	var locked bool
	err = tx.QueryRow(ctx, `SELECT revision,write_owner,operator_locked FROM kentity_entities WHERE id=$1 FOR UPDATE`, in.ID).Scan(&revision, &owner, &locked)
	if err != nil {
		return err
	}
	if sourceState != "rejected" || sourceLocked || locked || owner != "kdb" || revision != in.Revision || sourceFingerprint != in.Fingerprint {
		return ErrProtected
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_ownership_decisions(entity_id,expected_revision,source_fingerprint,selected_type,domains,actor,reason,source_url) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, in.ID, in.Revision, in.Fingerprint, in.Type, in.Domains, actor, in.Reason, in.SourceURL); err != nil {
		return err
	}
	if s.AutoResearch {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_resolution_jobs(entity_id,entity_revision,query_ko,entity_type,requested_by) SELECT id,revision,canonical_ko,entity_type,$2 FROM kentity_entities WHERE id=$1`, in.ID, actor); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
