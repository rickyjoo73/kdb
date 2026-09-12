package kentity

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// SetLock follows the legacy parent -> common identity lock order. An old
// safety lock is never released indirectly by unlocking the common record.
func (s *Store) SetLock(ctx context.Context, actor string, id uuid.UUID, revision int64, locked bool, reason string) error {
	if actor == "" || id == uuid.Nil || revision < 1 || len([]rune(strings.TrimSpace(reason))) < 10 || len(reason) > 2000 {
		return ErrInvalid
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var legacyLocked bool
	err = tx.QueryRow(ctx, `SELECT operator_locked FROM kwave_entities WHERE id=$1 FOR SHARE`, id).Scan(&legacyLocked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var owner string
	var oldLocked bool
	var current int64
	err = tx.QueryRow(ctx, `SELECT write_owner,operator_locked,revision FROM kentity_entities WHERE id=$1 FOR UPDATE`, id).Scan(&owner, &oldLocked, &current)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if owner != "native" || revision != current || (!locked && legacyLocked) {
		return ErrProtected
	}
	if oldLocked == locked {
		return ErrProtected
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_entities SET operator_locked=$2,revision=revision+1,updated_at=now() WHERE id=$1`, id, locked); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value) VALUES($1,$2,'operator_lock_changed',$3,jsonb_build_object('locked',$4::boolean,'revision',$5::bigint),jsonb_build_object('locked',$6::boolean,'revision',$5::bigint+1))`, id, actor, reason, oldLocked, current, locked); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
