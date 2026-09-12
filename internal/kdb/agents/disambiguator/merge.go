package disambiguator

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/homonym"
)

// A refusal is an accounted policy/stale-input result, not a database failure.
type mergeRefusal struct {
	reason string
	review bool
}

func (r *mergeRefusal) Error() string { return r.reason }

func validateAssignments(asgs []memberResult, members map[string]member) error {
	byID := map[string]memberResult{}
	for _, asg := range asgs {
		if _, ok := members[asg.ID]; !ok {
			return errors.New("assignment ID outside cluster")
		}
		if _, exists := byID[asg.ID]; exists {
			return errors.New("duplicate assignment ID")
		}
		byID[asg.ID] = asg
	}
	for _, asg := range asgs {
		if asg.Decision != "merge" {
			continue
		}
		if asg.SameAs == nil {
			return errors.New("merge has no survivor ID")
		}
		winnerID := strings.TrimSpace(*asg.SameAs)
		if _, ok := members[winnerID]; !ok || winnerID == asg.ID {
			return errors.New("invalid survivor ID")
		}
		winner, ok := byID[winnerID]
		if !ok || winner.Decision != "distinct" {
			return errors.New("survivor must have an explicit distinct decision; chains/cycles forbidden")
		}
	}
	return nil
}

func beginMergeTransaction(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SET LOCAL lock_timeout = '2s'; SET LOCAL statement_timeout = '10s'`); err != nil {
		rollbackMerge(tx)
		return nil, err
	}
	return tx, nil
}

func rollbackMerge(tx pgx.Tx) {
	// A cancelled caller must not leave row locks held in the pool.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

// Lock parent rows in UUID order for all callers, including reverse merge
// requests. FOR UPDATE also conflicts with child FK checks. SERIALIZABLE and
// the existing child→parent input-hash triggers protect concurrent evidence
// edits; serialization/deadlock failures are surfaced, never silently retried.
func lockMergePair(ctx context.Context, tx pgx.Tx, loserID, winnerID uuid.UUID, repair bool) error {
	rows, err := tx.Query(ctx, `SELECT id, status, operator_locked FROM kwave_entities
 WHERE id=ANY($1) ORDER BY id FOR UPDATE`, []uuid.UUID{loserID, winnerID})
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id uuid.UUID
		var status string
		var locked bool
		if err := rows.Scan(&id, &status, &locked); err != nil {
			return err
		}
		n++
		writable := status == "active" || status == "candidate"
		if repair && id == loserID {
			writable = status == "rejected"
		}
		if locked || !writable {
			return &mergeRefusal{reason: "merge participant locked or no longer in expected status"}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if n != 2 {
		return &mergeRefusal{reason: "merge participant missing"}
	}
	return nil
}

func sameIdentityInput(a, b member) bool {
	return a.id == b.id && a.ko == b.ko && a.en == b.en && a.qid == b.qid &&
		a.entityType == b.entityType && a.status == b.status && a.role == b.role &&
		a.agency == b.agency && a.birthYear == b.birthYear && reflect.DeepEqual(a.works, b.works) &&
		a.inputHash == b.inputHash && a.externalRefs == b.externalRefs
}

var validMergeQID = regexp.MustCompile(`^Q[1-9][0-9]*$`)

// Names, shared profession, agency or birth year are not unique identities.
// For the initial automated path require the same persisted Wikidata identity,
// same type, and no conflicting evidence. Other providers need an explicit
// namespace/identity policy before they can authorize automatic merging.
func mergeEvidenceGate(ctx context.Context, tx pgx.Tx, loser, winner member) error {
	if winner.status != "active" {
		return &mergeRefusal{reason: "automatic merge survivor must already be active", review: true}
	}
	if loser.entityType != winner.entityType || loser.entityType == "unknown" || loser.entityType == "" {
		return &mergeRefusal{reason: "merge requires the same known entity type", review: true}
	}
	if homonym.Conflict(signals(loser), signals(winner)) {
		return &mergeRefusal{reason: "identity evidence conflicts; review before merge", review: true}
	}
	var conflicting bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
 SELECT 1 FROM kwave_entity_external_refs l JOIN kwave_entity_external_refs w USING(provider)
 WHERE l.entity_id=$1 AND w.entity_id=$2 AND l.external_id<>w.external_id
 AND COALESCE(l.external_id,'')<>'' AND COALESCE(w.external_id,'')<>'')`, loser.id, winner.id).Scan(&conflicting); err != nil {
		return err
	}
	if conflicting {
		return &mergeRefusal{reason: "external identity IDs conflict; review before merge", review: true}
	}
	if loser.qid != winner.qid || !validMergeQID.MatchString(loser.qid) {
		return &mergeRefusal{reason: "no shared stable identity ID; names/roles alone cannot authorize merge", review: true}
	}
	return nil
}

func mergeAtomically(ctx context.Context, pool *pgxpool.Pool, loser, winner member, asg memberResult) error {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	tx, err := beginMergeTransaction(ctx, pool)
	if err != nil {
		return err
	}
	defer rollbackMerge(tx)
	if err := lockMergePair(ctx, tx, loser.id, winner.id, false); err != nil {
		return err
	}
	current, err := readMembers(ctx, tx, []uuid.UUID{loser.id, winner.id})
	if err != nil {
		return err
	}
	byID := map[uuid.UUID]member{}
	for _, m := range current {
		byID[m.id] = m
	}
	if !sameIdentityInput(loser, byID[loser.id]) || !sameIdentityInput(winner, byID[winner.id]) {
		return &mergeRefusal{reason: "identity input changed since model snapshot; fresh review required"}
	}
	if err := mergeEvidenceGate(ctx, tx, loser, winner); err != nil {
		return err
	}
	if err := carryEvidence(ctx, tx, loser.id, winner.id); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE kwave_entities w
 SET aliases_ko=ARRAY(SELECT DISTINCT x FROM unnest(COALESCE(w.aliases_ko,'{}'::text[]) ||
 ARRAY[l.canonical_ko] || COALESCE(l.aliases_ko,'{}'::text[])) x WHERE x<>'' AND x<>w.canonical_ko),
 updated_at=now() FROM kwave_entities l WHERE w.id=$1 AND l.id=$2`, winner.id, loser.id)
	if err != nil {
		return fmt.Errorf("carry aliases: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("survivor alias update affected no row")
	}
	tag, err = tx.Exec(ctx, `UPDATE kwave_entities SET status='rejected', needs_disambig=false,
 disambig_reviewed_at=now(), updated_at=now(),
 notes=COALESCE(NULLIF(notes,'') || ' · ','') || 'disambiguator: merged into ' || $2 || ' [' || $3 || '] (' || $4 || ')'
 WHERE id=$1 AND operator_locked=false AND status IN ('active','candidate')`,
		loser.id, winner.ko, winner.id.String(), relationOf(asg))
	if err != nil {
		return fmt.Errorf("retire variant: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("variant retirement affected no row")
	}
	if _, err := tx.Exec(ctx, `UPDATE kwave_entity_person_details d SET entity_id=$2
 WHERE d.entity_id=$1 AND NOT EXISTS (SELECT 1 FROM kwave_entity_person_details w WHERE w.entity_id=$2)`, loser.id, winner.id); err != nil {
		return fmt.Errorf("carry person details: %w", err)
	}
	return tx.Commit(ctx)
}
