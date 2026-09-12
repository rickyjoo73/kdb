package kentity

import (
	"context"
	"strings"

	"github.com/google/uuid"
)

func (s *Store) CancelResearch(ctx context.Context, actor string, entityID, jobID uuid.UUID, generation int64, reason string) error {
	if actor == "" || entityID == uuid.Nil || jobID == uuid.Nil || generation < 0 || len(strings.TrimSpace(reason)) < 10 || len(reason) > 2000 {
		return ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='5s'`); err != nil {
		return err
	}
	var id uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT id FROM kentity_entities WHERE id=$1 FOR UPDATE`, entityID).Scan(&id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE kentity_resolution_jobs SET state='cancelled',reason='operator_cancelled',lease_token=NULL,lease_until=NULL,next_attempt_at=NULL,updated_at=now() WHERE id=$1 AND entity_id=$2 AND generation=$3 AND state IN ('pending','running','failed')`, jobID, entityID, generation)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrProtected
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value) VALUES($1,$2,'candidate_research_cancelled',$3,jsonb_build_object('job_id',$4::text,'generation',$5::bigint))`, entityID, actor, reason, jobID.String(), generation); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
