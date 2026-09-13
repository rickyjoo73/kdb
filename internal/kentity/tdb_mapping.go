package kentity

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"net/url"
	"sort"
	"strings"
	"time"
)

type TDBMapping struct {
	Shadow                          TDBShadow
	Status, Reason                  string
	Revision, SourceGeneration      int64
	EntityID, EvidenceID, BindingID *uuid.UUID
	Candidates                      []Entity
	Type                            TDBType
	Fresh, Current                  bool
	CanRecheck                      bool
	ClassConflict                   bool
	Events                          []TDBMappingEvent
}
type TDBMappingEvent struct {
	Actor, Action, Reason string
	At                    time.Time
}
type TDBRegistration struct {
	ShadowID                  uuid.UUID
	Generation                int64
	Fingerprint, Reason, Type string
	Domains                   []string
}
type TDBMappingDecision struct {
	ShadowID, EntityID                                        uuid.UUID
	Generation, Revision, EntityRevision                      int64
	Fingerprint, Decision, Reason, IdentityFacts, EvidenceURL string
	Attested                                                  bool
}

func readTDBShadow(ctx context.Context, tx pgx.Tx, id uuid.UUID, lock bool) (TDBShadow, error) {
	var s TDBShadow
	var raw []byte
	q := `SELECT id,tdb_id,qid,link_method,link_score,state,reason,source_locked,attempts,generation,source_observed_at,updated_at,result,source_type,source_fingerprint FROM kentity_tdb_shadows WHERE id=$1`
	if lock {
		q += " FOR UPDATE"
	}
	err := tx.QueryRow(ctx, q, id).Scan(&s.ID, &s.TDBID, &s.QID, &s.Method, &s.Score, &s.State, &s.Reason, &s.Locked, &s.Attempts, &s.Generation, &s.ObservedAt, &s.UpdatedAt, &raw, &s.SourceType, &s.Fingerprint)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, ErrNotFound
	}
	if err != nil {
		return s, err
	}
	if string(raw) != "{}" {
		s.Proposal = &Proposal{}
		if err = json.Unmarshal(raw, s.Proposal); err != nil {
			return s, err
		}
	}
	return s, nil
}
func freshTDB(s TDBShadow) bool {
	if s.Proposal != nil && TDBSourceClassConflict(s.SourceType, s.Proposal.InstanceOf) {
		return false
	}
	return !s.Locked && s.State == "review" && s.Proposal != nil && time.Since(s.ObservedAt) <= 15*time.Minute && time.Until(s.ObservedAt) < time.Minute && !s.Proposal.ObservedAt.IsZero() && time.Since(s.Proposal.ObservedAt) < 24*time.Hour && time.Until(s.Proposal.ObservedAt) < time.Minute && s.Proposal.QID == s.QID && s.Proposal.SourceURL == "https://www.wikidata.org/wiki/"+s.QID && s.Proposal.License == "CC0-1.0"
}
func (s *Store) TDBMapping(ctx context.Context, id uuid.UUID) (*TDBMapping, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	m := &TDBMapping{Status: "unmapped", Candidates: []Entity{}}
	m.Shadow, err = readTDBShadow(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	m.Type, _ = TDBTypeFor(m.Shadow.SourceType)
	m.Fresh = freshTDB(m.Shadow)
	m.ClassConflict = m.Shadow.Proposal != nil && TDBSourceClassConflict(m.Shadow.SourceType, m.Shadow.Proposal.InstanceOf)
	m.CanRecheck = !m.Shadow.Locked && m.Shadow.State != "pending" && m.Shadow.State != "running" && time.Since(m.Shadow.ObservedAt) < 15*time.Minute && time.Until(m.Shadow.ObservedAt) < time.Minute
	err = tx.QueryRow(ctx, `SELECT status,reason,revision,entity_id,evidence_id,source_binding_id,source_generation FROM kentity_crosswalks WHERE source_system='tdb' AND source_id=$1`, m.Shadow.TDBID.String()).Scan(&m.Status, &m.Reason, &m.Revision, &m.EntityID, &m.EvidenceID, &m.BindingID, &m.SourceGeneration)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.id,e.entity_type,COALESCE(e.subtype,''),e.canonical_ko,e.origin_system,e.write_owner,e.status,e.operator_locked,e.revision,
 ARRAY(SELECT domain FROM kentity_entity_domains WHERE entity_id=e.id ORDER BY domain) FROM kentity_entities e WHERE e.id=$2 OR e.id IN(
 SELECT entity_id FROM kwave_entity_external_refs WHERE provider='wikidata' AND external_id=$1 UNION SELECT entity_id FROM kentity_external_ids WHERE provider='wikidata' AND external_id=$1 AND status<>'withdrawn') ORDER BY e.id LIMIT 51`, m.Shadow.QID, m.EntityID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var e Entity
		if err = rows.Scan(&e.ID, &e.Type, &e.Subtype, &e.KO, &e.Origin, &e.WriteOwner, &e.Status, &e.Locked, &e.Revision, &e.Domains); err != nil {
			rows.Close()
			return nil, err
		}
		m.Candidates = append(m.Candidates, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	if m.Status == "confirmed" && m.Fresh && m.BindingID != nil && *m.BindingID == id && m.SourceGeneration == m.Shadow.Generation && m.EntityID != nil && m.EvidenceID != nil {
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kentity_entities e JOIN kentity_evidence v ON v.entity_id=e.id JOIN kentity_crosswalks c ON c.source_system='tdb' AND c.source_id=$3 WHERE e.id=$1 AND NOT e.operator_locked AND e.status IN ('active','candidate') AND e.revision=c.target_revision AND v.id=$2 AND v.status='verified' AND
 (EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE entity_id=e.id AND provider='wikidata' AND external_id=$4) OR EXISTS(SELECT 1 FROM kentity_external_ids WHERE entity_id=e.id AND provider='wikidata' AND external_id=$4 AND status IN ('verified','unverified'))))`, m.EntityID, m.EvidenceID, m.Shadow.TDBID.String(), m.Shadow.QID).Scan(&m.Current); err != nil {
			return nil, err
		}
	}
	rows, err = tx.Query(ctx, `SELECT actor,action,COALESCE(after_value->>'reason',''),created_at FROM kentity_tdb_shadow_events WHERE shadow_id=$1 ORDER BY id DESC LIMIT 30`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var event TDBMappingEvent
		if err = rows.Scan(&event.Actor, &event.Action, &event.Reason, &event.At); err != nil {
			rows.Close()
			return nil, err
		}
		m.Events = append(m.Events, event)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	return m, tx.Commit(ctx)
}
func validTDBAction(actor, reason string, id uuid.UUID, generation int64, fp string) bool {
	return actor != "" && len(actor) <= 200 && len(strings.TrimSpace(reason)) >= 10 && len(reason) <= 2000 && id != uuid.Nil && generation >= 1 && len(fp) == 64
}

// RegisterTDBCandidate copies only independently fetched CC0 source labels.
// It creates a review link, not an approved TDB mapping or a serving identity.
func (s *Store) RegisterTDBCandidate(ctx context.Context, actor string, in TDBRegistration) (uuid.UUID, error) {
	if !validTDBAction(actor, in.Reason, in.ShadowID, in.Generation, in.Fingerprint) || len(in.Domains) < 1 || len(in.Domains) > 8 {
		return uuid.Nil, ErrInvalid
	}
	domains := map[string]bool{}
	for _, d := range in.Domains {
		if !supportedDomains[d] {
			return uuid.Nil, ErrInvalid
		}
		domains[d] = true
	}
	canonical := in
	canonical.Domains = []string{}
	for d := range domains {
		canonical.Domains = append(canonical.Domains, d)
	}
	sort.Strings(canonical.Domains)
	rawInput, _ := json.Marshal(canonical)
	sum := sha256.Sum256(rawInput)
	inputHash := hex.EncodeToString(sum[:])
	requestOwner := "tdb-registration:" + actor
	requestKey := in.ShadowID.String() + ":" + in.Fingerprint
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return uuid.Nil, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s';SET LOCAL statement_timeout='10s'`); err != nil {
		return uuid.Nil, err
	}
	sh, err := readTDBShadow(ctx, tx, in.ShadowID, true)
	if err != nil {
		return uuid.Nil, err
	}
	typ, ok := TDBTypeFor(sh.SourceType)
	if !ok || !freshTDB(sh) || sh.Generation != in.Generation || sh.Fingerprint != in.Fingerprint {
		return uuid.Nil, ErrProtected
	}
	selectedType := typ.EntityType
	if selectedType == "unknown" {
		selectedType = in.Type
		if !supportedTypes[selectedType] || selectedType == "unknown" {
			return uuid.Nil, ErrInvalid
		}
	} else if in.Type != "" && in.Type != selectedType {
		return uuid.Nil, ErrInvalid
	}
	if (selectedType == "person") != containsString(sh.Proposal.InstanceOf, "Q5") {
		return uuid.Nil, ErrProtected
	}
	if !tdbTypeCompatible(sh.SourceType, selectedType) {
		return uuid.Nil, ErrProtected
	}
	var priorID uuid.UUID
	var priorHash string
	err = tx.QueryRow(ctx, `SELECT entity_id,payload_hash FROM kentity_candidate_requests WHERE owner_key=$1 AND request_key=$2`, requestOwner, requestKey).Scan(&priorID, &priorHash)
	if err == nil {
		if priorHash != inputHash {
			return uuid.Nil, ErrConflict
		}
		return priorID, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_crosswalks(source_system,source_table,source_id,status,reason,source_binding_id,source_fingerprint,source_generation,mapping_policy_version) VALUES('tdb','tdb_places',$1,'review','TDB identity comparison pending',$2,$3,$4,'tdb-shadow-v1') ON CONFLICT DO NOTHING`, sh.TDBID.String(), sh.ID, sh.Fingerprint, sh.Generation); err != nil {
		return uuid.Nil, err
	}
	var mapped *uuid.UUID
	var binding uuid.UUID
	var status string
	if err = tx.QueryRow(ctx, `SELECT entity_id,source_binding_id,status FROM kentity_crosswalks WHERE source_system='tdb' AND source_id=$1 FOR UPDATE`, sh.TDBID.String()).Scan(&mapped, &binding, &status); err != nil {
		return uuid.Nil, err
	}
	if mapped != nil {
		return uuid.Nil, ErrProtected
	}
	if binding != sh.ID || status != "review" {
		return uuid.Nil, ErrProtected
	}
	var owner *uuid.UUID
	if err = tx.QueryRow(ctx, `INSERT INTO kentity_id_reservations(provider,external_id) VALUES('wikidata',$1) ON CONFLICT(provider,external_id) DO UPDATE SET external_id=EXCLUDED.external_id RETURNING entity_id`, sh.QID).Scan(&owner); err != nil {
		return uuid.Nil, err
	}
	var claimed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE provider='wikidata' AND external_id=$1) OR EXISTS(SELECT 1 FROM kentity_external_ids WHERE provider='wikidata' AND external_id=$1 AND status<>'withdrawn')`, sh.QID).Scan(&claimed); err != nil {
		return uuid.Nil, err
	}
	if owner != nil || claimed {
		return uuid.Nil, ErrProtected
	}
	id, evidence := uuid.New(), uuid.New()
	p := sh.Proposal
	if strings.TrimSpace(p.KO) == "" || len([]rune(p.KO)) > 300 {
		return uuid.Nil, ErrInvalid
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,write_owner,status) VALUES($1,$2,$3,'tdb','native','candidate')`, id, selectedType, p.KO); err != nil {
		return uuid.Nil, err
	}
	// Reserve the source ID against competing legacy and common writers. This
	// holds a candidate claim; it does not approve identity or a language name.
	if _, err = tx.Exec(ctx, `UPDATE kentity_id_reservations SET entity_id=$2 WHERE provider='wikidata' AND external_id=$1`, sh.QID, id); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_candidate_requests(owner_key,request_key,payload_hash,entity_id) VALUES($1,$2,$3,$4)`, requestOwner, requestKey, inputHash, id); err != nil {
		return uuid.Nil, err
	}
	for d := range domains {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_entity_domains(entity_id,domain,assigned_by,reason,policy_version) VALUES($1,$2,$3,$4,'kentity-writer-v1')`, id, d, actor, in.Reason); err != nil {
			return uuid.Nil, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,license_code,export_allowed,status,observed_at,summary) VALUES($1,$2,'wikidata',$3,$4,'identity','CC0-1.0',true,'unverified',$5,'Independently observed source; TDB identity link and entity approval remain unverified')`, evidence, id, sh.QID, p.SourceURL, p.ObservedAt); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_external_ids(entity_id,provider,external_id,status,evidence_id,policy_version) VALUES($1,'wikidata',$2,'unverified',$3,'kentity-writer-v1')`, id, sh.QID, evidence); err != nil {
		return uuid.Nil, err
	}
	for _, n := range p.Names {
		if n.Value == "" || n.Form != "recorded" || n.Source != "wikidata-label" {
			return uuid.Nil, ErrInvalid
		}
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_names(entity_id,locale,value,kind,form,status,evidence_id,source_code) VALUES($1,$2,$3,'canonical','recorded','unverified',$4,'wikidata-label') ON CONFLICT DO NOTHING`, id, n.Locale, n.Value, evidence); err != nil {
			return uuid.Nil, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_crosswalks SET entity_id=$2,candidate_ids=ARRAY[$2::uuid],source_fingerprint=$3,source_generation=$4,revision=revision+1,reason=$5,updated_at=now() WHERE source_system='tdb' AND source_id=$1`, sh.TDBID.String(), id, sh.Fingerprint, sh.Generation, in.Reason); err != nil {
		return uuid.Nil, err
	}
	if s.AutoResearch {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_resolution_jobs(entity_id,entity_revision,query_ko,entity_type,requested_by) VALUES($1,1,$2,$3,$4)`, id, p.KO, selectedType, actor); err != nil {
			return uuid.Nil, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value) VALUES($1,$2,'tdb_candidate_registered',$3,jsonb_build_object('shadow_id',$4::uuid,'tdb_id',$5::uuid,'qid',$6::text,'source_type',$7::text,'mapping_confirmed',false,'names_verified',false,'tdb_fields_copied',false))`, id, actor, in.Reason, sh.ID, sh.TDBID, sh.QID, sh.SourceType); err != nil {
		return uuid.Nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,after_value) VALUES($1,$2,'candidate_registered',jsonb_build_object('entity_id',$3::uuid,'reason',$4::text))`, sh.ID, actor, id, in.Reason); err != nil {
		return uuid.Nil, err
	}
	return id, tx.Commit(ctx)
}

func (s *Store) DecideTDBMapping(ctx context.Context, actor string, in TDBMappingDecision) error {
	if !validTDBAction(actor, in.Reason, in.ShadowID, in.Generation, in.Fingerprint) || in.Revision < 0 {
		return ErrInvalid
	}
	if in.Decision != "confirmed" && in.Decision != "rejected" && in.Decision != "review" {
		return ErrInvalid
	}
	if in.Decision == "confirmed" {
		u, err := url.Parse(in.EvidenceURL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(in.EvidenceURL) > 2000 || !in.Attested || len([]rune(strings.TrimSpace(in.IdentityFacts))) < 20 || len(in.IdentityFacts) > 4000 || in.EntityID == uuid.Nil || in.EntityRevision < 1 {
			return ErrInvalid
		}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s';SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	sh, err := readTDBShadow(ctx, tx, in.ShadowID, true)
	if err != nil {
		return err
	}
	if sh.Generation != in.Generation || sh.Fingerprint != in.Fingerprint {
		return ErrProtected
	}
	if in.Decision == "confirmed" && !freshTDB(sh) {
		return ErrProtected
	}
	if in.Revision == 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_crosswalks(source_system,source_table,source_id,status,reason,source_binding_id,source_fingerprint,source_generation,mapping_policy_version) VALUES('tdb','tdb_places',$1,'review','TDB identity comparison pending',$2,$3,$4,'tdb-shadow-v1') ON CONFLICT DO NOTHING`, sh.TDBID.String(), sh.ID, sh.Fingerprint, sh.Generation); err != nil {
			return err
		}
	}
	var revision int64
	var oldState string
	var oldEntity, oldEvidence *uuid.UUID
	if err = tx.QueryRow(ctx, `SELECT revision,status,entity_id,evidence_id FROM kentity_crosswalks WHERE source_system='tdb' AND source_id=$1 FOR UPDATE`, sh.TDBID.String()).Scan(&revision, &oldState, &oldEntity, &oldEvidence); err != nil {
		return err
	}
	if (in.Revision != revision && !(in.Revision == 0 && revision == 1 && oldEntity == nil)) || (in.Decision == "confirmed" && oldState == "confirmed") {
		return ErrProtected
	}
	target := oldEntity
	var evidence *uuid.UUID
	var targetIdentityRevision int64
	if in.Decision == "confirmed" {
		// Preserve legacy writer lock order (legacy root -> common root).
		if _, err = tx.Exec(ctx, `SELECT id FROM kwave_entities WHERE id=$1 FOR UPDATE`, in.EntityID); err != nil {
			return err
		}
		var rev int64
		var locked bool
		var state, typ string
		// confirmed 인 crosswalk 는 대상의 현재 정체성 버전을 고정해야 한다(§10.1).
		if err = tx.QueryRow(ctx, `SELECT revision,operator_locked,status,entity_type,identity_revision FROM kentity_entities WHERE id=$1 FOR UPDATE`, in.EntityID).Scan(&rev, &locked, &state, &typ, &targetIdentityRevision); err != nil {
			return err
		}
		if rev != in.EntityRevision || locked || (state != "active" && state != "candidate") || typ == "unknown" || (typ == "person") != containsString(sh.Proposal.InstanceOf, "Q5") {
			return ErrProtected
		}
		if !tdbTypeCompatible(sh.SourceType, typ) {
			return ErrProtected
		}
		if _, err = tx.Exec(ctx, `SELECT entity_id FROM kwave_entity_external_refs WHERE entity_id=$1 AND provider='wikidata' FOR SHARE`, in.EntityID); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `SELECT entity_id FROM kentity_external_ids WHERE entity_id=$1 AND provider='wikidata' FOR SHARE`, in.EntityID); err != nil {
			return err
		}
		var linked bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM kwave_entity_external_refs WHERE entity_id=$1 AND provider='wikidata' AND external_id=$2) OR EXISTS(SELECT 1 FROM kentity_external_ids WHERE entity_id=$1 AND provider='wikidata' AND external_id=$2 AND status IN ('verified','unverified'))`, in.EntityID, sh.QID).Scan(&linked); err != nil {
			return err
		}
		if !linked {
			return ErrProtected
		}
		ev := uuid.New()
		evidence = &ev
		target = &in.EntityID
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_evidence(id,entity_id,provider,source_record_id,source_url,claim_type,status,verified_by,verified_at,summary) VALUES($1,$2,'operator-tdb-link-review',$3,$4,'identity','verified',$5,now(),$6)`, ev, in.EntityID, sh.TDBID.String(), in.EvidenceURL, actor, in.IdentityFacts); err != nil {
			return err
		}
	}
	if oldEvidence != nil {
		if _, err = tx.Exec(ctx, `UPDATE kentity_evidence SET status='withdrawn' WHERE id=$1 AND status='verified'`, oldEvidence); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_crosswalks SET status=$2,entity_id=$3,evidence_id=$4,source_binding_id=$5,source_fingerprint=$6,source_generation=$7,target_identity_revision=$11,revision=revision+1,reason=$8,decided_by=$9,decided_at=now(),updated_at=now(),target_revision=$10 WHERE source_system='tdb' AND source_id=$1`, sh.TDBID.String(), in.Decision, target, evidence, sh.ID, sh.Fingerprint, sh.Generation, in.Reason, actor, in.EntityRevision, targetIdentityRevision); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value) VALUES($1,$2,'tdb_mapping_decision',$3,jsonb_build_object('status',$4::text,'revision',$5::bigint,'entity_id',$6::uuid),jsonb_build_object('status',$7::text,'entity_id',$1::uuid,'source_id',$8::uuid,'shadow_id',$9::uuid,'tdb_data_export_approved',false,'names_changed',false))`, target, actor, in.Reason, oldState, revision, oldEntity, in.Decision, sh.TDBID, sh.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,after_value) VALUES($1,$2,'mapping_decision',jsonb_build_object('status',$3::text,'reason',$4::text,'entity_id',$5::uuid))`, sh.ID, actor, in.Decision, in.Reason, target); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) RecheckTDB(ctx context.Context, actor string, id uuid.UUID, generation int64, reason string) error {
	if actor == "" || len(actor) > 200 || id == uuid.Nil || generation < 1 || len(strings.TrimSpace(reason)) < 10 || len(reason) > 2000 {
		return ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	sh, err := readTDBShadow(ctx, tx, id, true)
	if err != nil {
		return err
	}
	if sh.Locked || sh.Generation != generation || sh.State == "running" || sh.State == "pending" || time.Since(sh.ObservedAt) > 15*time.Minute || time.Until(sh.ObservedAt) > time.Minute {
		return ErrProtected
	}
	var last *time.Time
	var n int
	if err = tx.QueryRow(ctx, `SELECT last_manual_recheck_at,manual_rechecks FROM kentity_tdb_shadows WHERE id=$1`, id).Scan(&last, &n); err != nil {
		return err
	}
	if last != nil && time.Since(*last) < 5*time.Minute {
		return ErrProtected
	}
	if last == nil || time.Since(*last) > 24*time.Hour {
		n = 0
	}
	if n >= 3 {
		return ErrProtected
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,before_value,after_value) SELECT id,$2,'source_recheck_requested',jsonb_build_object('generation',generation,'result',result,'state',state),jsonb_build_object('reason',$3::text,'maximum_calls',4) FROM kentity_tdb_shadows WHERE id=$1`, id, actor, reason); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='pending',attempts=0,generation=generation+1,lease_token=NULL,lease_until=NULL,next_attempt_at=now(),result='{}',manual_rechecks=$2,last_manual_recheck_at=now(),reason='operator requested bounded source recheck',updated_at=now() WHERE id=$1`, id, n+1); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
