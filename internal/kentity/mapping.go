package kentity

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Mapping approval records an operator's identity decision, not a data license
// or permission to overwrite a profile. Those are separate write gates.
type Mapping struct {
	SourceID                    uuid.UUID
	Status, Reason, Fingerprint string
	Revision                    int64
	EntityID                    *uuid.UUID
	CandidateIDs                []uuid.UUID
	Source                      Profile
	Candidates                  []Profile
}
type Profile struct {
	ID                                           uuid.UUID
	KO, EN, Role, Agency, BirthYear, Fingerprint string
	URLs                                         []string
	Locked                                       bool
	Revision                                     int64
}
type MappingDecision struct {
	SourceID, EntityID                                                           uuid.UUID
	Revision, EntityRevision                                                     int64
	Fingerprint, EntityFingerprint, Decision, Reason, EvidenceURL, IdentityFacts string
	Attested                                                                     bool
}

func (s *Store) Mappings(ctx context.Context, status string, limit int) ([]Mapping, error) {
	if status == "" {
		status = "review"
	}
	if status != "review" && status != "conflict" && status != "confirmed" && status != "rejected" {
		return nil, ErrInvalid
	}
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.Pool.Query(ctx, `SELECT c.source_id::uuid,c.status,c.reason,c.revision,c.entity_id,c.candidate_ids,
 p.name_ko,COALESCE(to_jsonb(p)->>'primary_role',''),COALESCE(to_jsonb(p)->>'agency','')
 FROM kentity_crosswalks c JOIN kwave_persons p ON p.id::text=c.source_id
 WHERE c.source_system='legacy_person' AND c.status=$1 ORDER BY c.updated_at DESC,c.source_id LIMIT $2`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Mapping{}
	for rows.Next() {
		var m Mapping
		if err = rows.Scan(&m.SourceID, &m.Status, &m.Reason, &m.Revision, &m.EntityID, &m.CandidateIDs, &m.Source.KO, &m.Source.Role, &m.Source.Agency); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) Mapping(ctx context.Context, id uuid.UUID) (*Mapping, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	m := &Mapping{SourceID: id}
	err = tx.QueryRow(ctx, `SELECT status,reason,revision,entity_id,candidate_ids FROM kentity_crosswalks WHERE source_system='legacy_person' AND source_id=$1`, id.String()).Scan(&m.Status, &m.Reason, &m.Revision, &m.EntityID, &m.CandidateIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT to_jsonb(p),md5(to_jsonb(p)::text) FROM kwave_persons p WHERE id=$1`, id).Scan(&raw, &m.Fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p struct {
		KO        string   `json:"name_ko"`
		EN        string   `json:"name_en"`
		Role      string   `json:"primary_role"`
		Agency    string   `json:"agency"`
		BirthYear *int     `json:"birth_year"`
		URLs      []string `json:"source_urls"`
		Locked    bool     `json:"operator_locked"`
	}
	if err = json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	m.Source = Profile{ID: id, KO: p.KO, EN: p.EN, Role: p.Role, Agency: p.Agency, URLs: p.URLs, Locked: p.Locked}
	if p.BirthYear != nil {
		b, _ := json.Marshal(p.BirthYear)
		m.Source.BirthYear = string(b)
	}
	ids := append([]uuid.UUID{}, m.CandidateIDs...)
	if m.EntityID != nil {
		ids = append(ids, *m.EntityID)
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.canonical_ko,COALESCE(e.canonical_en,''),COALESCE(d.primary_role::text,''),COALESCE(d.agency,''),COALESCE(d.birth_year::text,''),COALESCE(e.source_urls,'{}'),e.operator_locked,c.revision,md5(to_jsonb(e)::text || COALESCE(to_jsonb(d)::text,''))
 FROM kwave_entities e JOIN kentity_entities c ON c.id=e.id LEFT JOIN kwave_entity_person_details d ON d.entity_id=e.id WHERE e.id=ANY($1) ORDER BY e.id`, ids)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var p Profile
		if err = rows.Scan(&p.ID, &p.KO, &p.EN, &p.Role, &p.Agency, &p.BirthYear, &p.URLs, &p.Locked, &p.Revision, &p.Fingerprint); err != nil {
			rows.Close()
			return nil, err
		}
		m.Candidates = append(m.Candidates, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return m, tx.Commit(ctx)
}

func (s *Store) DecideMapping(ctx context.Context, actor string, in MappingDecision) error {
	in.Reason = strings.TrimSpace(in.Reason)
	in.IdentityFacts = strings.TrimSpace(in.IdentityFacts)
	if actor == "" || in.SourceID == uuid.Nil || in.Revision < 1 || in.Fingerprint == "" || len(in.Reason) < 10 || len(in.Reason) > 2000 || len(in.IdentityFacts) > 4000 {
		return ErrInvalid
	}
	if in.Decision != "confirmed" && in.Decision != "rejected" {
		return ErrInvalid
	}
	if in.Decision == "confirmed" {
		u, err := url.Parse(in.EvidenceURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(in.EvidenceURL) > 2000 || len([]rune(in.IdentityFacts)) < 20 || !in.Attested || in.EntityID == uuid.Nil || in.EntityRevision < 1 || in.EntityFingerprint == "" {
			return ErrInvalid
		}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='5s'`); err != nil {
		return err
	}
	var status string
	var revision int64
	err = tx.QueryRow(ctx, `SELECT status,revision FROM kentity_crosswalks WHERE source_system='legacy_person' AND source_id=$1 FOR UPDATE`, in.SourceID.String()).Scan(&status, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if revision != in.Revision || (status != "review" && status != "conflict") {
		return ErrProtected
	}
	var fingerprint string
	var locked bool
	err = tx.QueryRow(ctx, `SELECT md5(to_jsonb(p)::text),COALESCE((to_jsonb(p)->>'operator_locked')::boolean,false) FROM kwave_persons p WHERE id=$1 FOR SHARE`, in.SourceID).Scan(&fingerprint, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrProtected
	}
	if err != nil {
		return err
	}
	if fingerprint != in.Fingerprint || locked {
		return ErrProtected
	}
	var evidence *uuid.UUID
	var target *uuid.UUID
	if in.Decision == "confirmed" {
		// Legacy parent first: child profile/hash triggers also lock this row.
		var typ, state string
		err = tx.QueryRow(ctx, `SELECT entity_type::text,status,operator_locked FROM kwave_entities WHERE id=$1 FOR UPDATE`, in.EntityID).Scan(&typ, &state, &locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrProtected
		}
		if err != nil {
			return err
		}
		if typ != "person" || (state != "active" && state != "candidate") || locked {
			return ErrProtected
		}
		var rev int64
		if err = tx.QueryRow(ctx, `SELECT revision FROM kentity_entities WHERE id=$1 AND origin_system='kdb' FOR UPDATE`, in.EntityID).Scan(&rev); err != nil {
			return err
		}
		if rev != in.EntityRevision {
			return ErrProtected
		}
		var targetFingerprint string
		if err = tx.QueryRow(ctx, `SELECT md5(to_jsonb(e)::text || COALESCE(to_jsonb(d)::text,'')) FROM kwave_entities e LEFT JOIN kwave_entity_person_details d ON d.entity_id=e.id WHERE e.id=$1`, in.EntityID).Scan(&targetFingerprint); err != nil {
			return err
		}
		if targetFingerprint != in.EntityFingerprint {
			return ErrProtected
		}
		id := uuid.New()
		evidence = &id
		target = &in.EntityID
		// The operator verifies only identity linkage. Source reuse stays unreviewed.
		_, err = tx.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,status,verified_by,verified_at,summary)
 VALUES($1,$2,'operator-identity-review',$3,$4,'identity','verified',$5,now(),$6)`, id, in.EntityID, in.SourceID.String(), in.EvidenceURL, actor, in.IdentityFacts)
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE kentity_crosswalks SET status=$2,entity_id=$3,evidence_id=$4,reason=$5,decided_by=$6,decided_at=now(),source_fingerprint=$7,revision=revision+1,updated_at=now() WHERE source_system='legacy_person' AND source_id=$1`, in.SourceID.String(), in.Decision, target, evidence, in.Reason, actor, fingerprint)
	if err != nil {
		return err
	}
	before, _ := json.Marshal(map[string]any{"source_id": in.SourceID, "status": status, "revision": revision})
	after, _ := json.Marshal(map[string]any{"source_id": in.SourceID, "status": in.Decision, "revision": revision + 1, "evidence_id": evidence, "entity_id": target, "profile_copied": false})
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value) VALUES($1,$2,'legacy_mapping_decision',$3,$4,$5)`, target, actor, in.Reason, before, after); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
