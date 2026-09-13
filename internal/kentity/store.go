// Package kentity provides the normalized cross-domain catalog in the KDB DB.
// Legacy rows keep their original writer; this API does not loosen old gates.
package kentity

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var ErrInvalid = errors.New("invalid entity input")
var ErrNotFound = errors.New("entity not found")
var ErrProtected = errors.New("legacy ownership, revision or operator lock prevents change")
var ErrConflict = errors.New("request key already used with different input")

type Store struct {
	Pool         *pgxpool.Pool
	AutoResearch bool
}
type Entity struct {
	ID         uuid.UUID `json:"id"`
	Type       string    `json:"type"`
	Subtype    string    `json:"subtype"`
	KO         string    `json:"ko"`
	Origin     string    `json:"origin"`
	WriteOwner string    `json:"write_owner"`
	Status     string    `json:"status"`
	Locked     bool      `json:"locked"`
	Revision   int64     `json:"revision"`
	Domains    []string  `json:"domains"`
	Names      []Name    `json:"names,omitempty"`
}
type Name struct {
	Locale       string     `json:"locale"`
	Value        string     `json:"value"`
	Kind         string     `json:"kind"`
	Form         string     `json:"form"`
	Status       string     `json:"status"`
	Source       string     `json:"source"`
	Owner        string     `json:"write_owner"`
	Verification string     `json:"verification_method,omitempty"`
	EvidenceID   *uuid.UUID `json:"evidence_id,omitempty"`
	SourceURL    string     `json:"source_url,omitempty"`
}
type CandidateInput struct {
	KO      string   `json:"ko"`
	Type    string   `json:"type"`
	Subtype string   `json:"subtype"`
	Domains []string `json:"domains"`
	Reason  string   `json:"reason"`
}

var supportedTypes = map[string]bool{"person": true, "organization": true, "company": true, "location": true, "work": true, "team": true, "league": true, "event": true, "product": true, "concept": true, "unknown": true}
var supportedDomains = map[string]bool{"politics": true, "government": true, "economy": true, "society": true, "entertainment": true, "sports": true, "travel": true, "culture": true}

func (s *Store) Search(ctx context.Context, q, typ, domain string, limit int) ([]Entity, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if len([]rune(q)) > 200 || (typ != "" && !supportedTypes[typ]) || (domain != "" && !supportedDomains[domain]) {
		return nil, ErrInvalid
	}
	rows, err := s.Pool.Query(ctx, `SELECT e.id,e.entity_type,COALESCE(e.subtype,''),e.canonical_ko,e.origin_system,e.status,e.operator_locked,e.revision,COALESCE(to_jsonb(e)->>'write_owner',e.origin_system),
 ARRAY(SELECT d.domain FROM kentity_entity_domains d WHERE d.entity_id=e.id ORDER BY d.domain)
 FROM kentity_entities e WHERE ($1='' OR strpos(lower(e.canonical_ko),lower($1))>0)
 AND ($2='' OR e.entity_type=$2) AND ($3='' OR EXISTS(SELECT 1 FROM kentity_entity_domains d WHERE d.entity_id=e.id AND d.domain=$3))
 ORDER BY e.updated_at DESC,e.id LIMIT $4`, strings.TrimSpace(q), typ, domain, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entity{}
	for rows.Next() {
		var e Entity
		if err = rows.Scan(&e.ID, &e.Type, &e.Subtype, &e.KO, &e.Origin, &e.Status, &e.Locked, &e.Revision, &e.WriteOwner, &e.Domains); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Get(ctx context.Context, id uuid.UUID) (*Entity, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	e := &Entity{}
	err = tx.QueryRow(ctx, `SELECT e.id,e.entity_type,COALESCE(e.subtype,''),e.canonical_ko,e.origin_system,e.status,e.operator_locked,e.revision,COALESCE(to_jsonb(e)->>'write_owner',e.origin_system),
 ARRAY(SELECT d.domain FROM kentity_entity_domains d WHERE d.entity_id=e.id ORDER BY d.domain) FROM kentity_entities e WHERE e.id=$1`, id).Scan(&e.ID, &e.Type, &e.Subtype, &e.KO, &e.Origin, &e.Status, &e.Locked, &e.Revision, &e.WriteOwner, &e.Domains)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT n.locale,n.value,n.kind,n.form,n.verification_status,COALESCE(n.source_code,''),n.write_owner,n.evidence_id,COALESCE(v.source_url,''),COALESCE(v.verified_by,'') FROM kentity_name_catalog n LEFT JOIN kentity_evidence v ON v.id=n.evidence_id AND v.entity_id=n.entity_id WHERE n.entity_id=$1 ORDER BY n.locale,n.kind,n.value LIMIT 500`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var n Name
		var reviewer string
		if err = rows.Scan(&n.Locale, &n.Value, &n.Kind, &n.Form, &n.Status, &n.Source, &n.Owner, &n.EvidenceID, &n.SourceURL, &reviewer); err != nil {
			rows.Close()
			return nil, err
		}
		n.Verification = "unreviewed"
		if n.Status == "verified" {
			n.Verification = "operator_review"
			if strings.HasPrefix(reviewer, "policy:") {
				n.Verification = reviewer
			}
		}
		e.Names = append(e.Names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return e, tx.Commit(ctx)
}

// CreateCandidate is an authenticated operator action, not automatic promotion.
// Same-name candidates deliberately remain distinct UUIDs for identity review.
func (s *Store) CreateCandidate(ctx context.Context, actor, key string, in CandidateInput) (*Entity, error) {
	in.KO = strings.TrimSpace(in.KO)
	in.Reason = strings.TrimSpace(in.Reason)
	if actor == "" || key == "" || len(key) > 200 || in.KO == "" || len([]rune(in.KO)) > 200 || len(in.Subtype) > 100 || in.Reason == "" || len(in.Reason) > 2000 || !supportedTypes[in.Type] || len(in.Domains) > 8 {
		return nil, ErrInvalid
	}
	domains := map[string]bool{}
	for _, d := range in.Domains {
		if !supportedDomains[d] {
			return nil, fmt.Errorf("%w: domain", ErrInvalid)
		}
		domains[d] = true
	}
	in.Domains = nil
	for d := range domains {
		in.Domains = append(in.Domains, d)
	}
	sort.Strings(in.Domains)
	b, _ := json.Marshal(in)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(b))
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	id := uuid.New()
	tag, err := tx.Exec(ctx, `INSERT INTO kentity_candidate_requests(owner_key,request_key,payload_hash,entity_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, actor, key, fingerprint, id)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		var old string
		if err = tx.QueryRow(ctx, `SELECT entity_id,payload_hash FROM kentity_candidate_requests WHERE owner_key=$1 AND request_key=$2`, actor, key).Scan(&id, &old); err != nil {
			return nil, err
		}
		if old != fingerprint {
			return nil, ErrConflict
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return s.Get(ctx, id)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,subtype,canonical_ko,origin_system,status) VALUES($1,$2,NULLIF($3,''),$4,'native','candidate')`, id, in.Type, in.Subtype, in.KO); err != nil {
		return nil, err
	}
	for d := range domains {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_entity_domains(entity_id,domain,assigned_by,reason,policy_version) VALUES($1,$2,$3,$4,'kentity-writer-v1')`, id, d, actor, in.Reason); err != nil {
			return nil, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,source_code) VALUES($1,'ko',$2,'canonical','unknown','operator-candidate')`, id, in.KO); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value) VALUES($1,$2,'candidate_created',$3,$4)`, id, actor, in.Reason, b); err != nil {
		return nil, err
	}
	if s.AutoResearch {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_resolution_jobs(entity_id,entity_revision,query_ko,entity_type,requested_by) VALUES($1,1,$2,$3,$4)`, id, in.KO, in.Type, actor); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.Get(ctx, id)
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}
