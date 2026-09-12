package kentity

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type ResearchApproval struct {
	EntityID, JobID            uuid.UUID
	Generation                 int64
	QID, Reason, IdentityFacts string
	Locales                    []string
	Attested                   bool
}

// ApproveResearch records a human identity/locale review of a stored snapshot.
// Nothing from client-submitted label text, confidence or source URLs is trusted.
// Approval means reviewed recorded usage, never a claim of official naming.
func (s *Store) ApproveResearch(ctx context.Context, actor string, in ResearchApproval) error {
	if actor == "" || in.EntityID == uuid.Nil || in.JobID == uuid.Nil || in.Generation < 1 || !researchQID.MatchString(in.QID) || !in.Attested || len([]rune(strings.TrimSpace(in.IdentityFacts))) < 20 || len(in.IdentityFacts) > 4000 || len(strings.TrimSpace(in.Reason)) < 10 || len(in.Reason) > 2000 || len(in.Locales) < 1 || len(in.Locales) > 12 {
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
	var origin, state, ko, typ string
	var revision int64
	var locked bool
	err = tx.QueryRow(ctx, `SELECT COALESCE(to_jsonb(e)->>'write_owner',e.origin_system),status,canonical_ko,entity_type,revision,operator_locked FROM kentity_entities e WHERE id=$1 FOR UPDATE`, in.EntityID).Scan(&origin, &state, &ko, &typ, &revision, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if origin != "native" || state != "candidate" || locked {
		return ErrProtected
	}
	var raw []byte
	var valid bool
	err = tx.QueryRow(ctx, `SELECT state='review' AND generation=$3 AND entity_revision=$4 AND query_ko=$5 AND entity_type=$6,result FROM kentity_resolution_jobs WHERE id=$1 AND entity_id=$2 FOR UPDATE`, in.JobID, in.EntityID, in.Generation, revision, ko, typ).Scan(&valid, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !valid {
		return ErrProtected
	}
	var proposals []Proposal
	if err = json.Unmarshal(raw, &proposals); err != nil {
		return err
	}
	var proposal *Proposal
	for i := range proposals {
		if proposals[i].QID == in.QID {
			proposal = &proposals[i]
			break
		}
	}
	if proposal == nil || proposal.ObservedAt.IsZero() || proposal.License != "CC0-1.0" || proposal.SourceURL != "https://www.wikidata.org/wiki/"+in.QID {
		return ErrInvalid
	}
	names := map[string]Name{}
	for _, n := range proposal.Names {
		names[n.Locale] = n
	}
	selected := map[string]Name{}
	for _, locale := range in.Locales {
		n, ok := names[locale]
		if !ok || n.Value == "" || n.Form != "recorded" || n.Source != "wikidata-label" {
			return ErrInvalid
		}
		selected[locale] = n
	}
	// Both native approval and every legacy Wikidata claim touch this unique
	// reservation row, protecting against phantom inserts between check/commit.
	var owner *uuid.UUID
	err = tx.QueryRow(ctx, `INSERT INTO kentity_id_reservations(provider,external_id) VALUES('wikidata',$1) ON CONFLICT(provider,external_id) DO UPDATE SET external_id=EXCLUDED.external_id RETURNING native_owner`, in.QID).Scan(&owner)
	if err != nil {
		return err
	}
	if owner != nil && *owner != in.EntityID {
		return ErrProtected
	}
	var claimed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE provider='wikidata' AND external_id=$1 AND entity_id<>$2) OR EXISTS(SELECT 1 FROM kentity_external_ids WHERE provider='wikidata' AND external_id=$1 AND status='verified' AND entity_id<>$2)`, in.QID, in.EntityID).Scan(&claimed); err != nil {
		return err
	}
	if claimed {
		return ErrProtected
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_id_reservations SET native_owner=$2 WHERE provider='wikidata' AND external_id=$1`, in.QID, in.EntityID); err != nil {
		return err
	}
	evidence := uuid.New()
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,license_code,export_allowed,status,verified_by,verified_at,summary,observed_at)
 VALUES($1,$2,'wikidata',$3,$4,'identity','CC0-1.0',true,'verified',$5,now(),$6,$7)`, evidence, in.EntityID, in.QID, proposal.SourceURL, actor, in.IdentityFacts, proposal.ObservedAt); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_external_ids(entity_id,provider,external_id,status,evidence_id) VALUES($1,'wikidata',$2,'verified',$3)
 ON CONFLICT(entity_id,provider,external_id) DO UPDATE SET status='verified',evidence_id=EXCLUDED.evidence_id`, in.EntityID, in.QID, evidence); err != nil {
		return err
	}
	for _, n := range selected {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,$2,$3,'canonical','recorded','verified',$4,'wikidata-label')
 ON CONFLICT(entity_id,locale,value,kind,source_code) DO UPDATE SET status='verified',evidence_id=EXCLUDED.evidence_id,form='recorded',revision=kentity_names.revision+1,updated_at=now()`, in.EntityID, n.Locale, n.Value, evidence); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_entities SET status='active',revision=revision+1,updated_at=now() WHERE id=$1`, in.EntityID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_resolution_jobs SET state='approved',reason='operator_reviewed_recorded_usage',updated_at=now() WHERE id=$1`, in.JobID); err != nil {
		return err
	}
	detail, _ := json.Marshal(map[string]any{"job_id": in.JobID, "qid": in.QID, "locales": in.Locales, "evidence_id": evidence, "new_revision": revision + 1, "official_name_asserted": false})
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value) VALUES($1,$2,'candidate_identity_and_names_approved',$3,jsonb_build_object('status','candidate','revision',$4::bigint),$5)`, in.EntityID, actor, in.Reason, revision, detail); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
