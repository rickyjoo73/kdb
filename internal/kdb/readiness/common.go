package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rickyjoo73/kdb/internal/kdb"
)

// Script/region-specific requests are not satisfied by generic language labels.
var commonLocales = map[string]string{"ko": "ko", "en": "en", "ja": "ja", "zh": "zh", "zh-hans": "zh-Hans", "zh-hant": "zh-Hant", "zh-tw": "zh-TW", "vi": "vi", "id": "id", "es": "es", "pt": "pt", "pt-br": "pt-BR"}

type commonName struct {
	ID                 uuid.UUID  `json:"name_id"`
	Locale             string     `json:"locale"`
	Value              string     `json:"value"`
	Kind               string     `json:"kind"`
	Form               string     `json:"form"`
	Status             string     `json:"status"`
	Revision           int64      `json:"name_revision"`
	Source             string     `json:"source"`
	EvidenceID         *uuid.UUID `json:"evidence_id"`
	EvidenceStatus     string     `json:"evidence_status"`
	Export             bool       `json:"export_allowed"`
	License            string     `json:"license"`
	SourceURL          string     `json:"source_url"`
	Current            bool       `json:"current"`
	ValidFrom          *time.Time `json:"valid_from"`
	ValidUntil         *time.Time `json:"valid_until"`
	ObservedAt         *time.Time `json:"source_observed_at"`
	VerifiedAt         *time.Time `json:"source_verified_at"`
	VerifiedBy         string     `json:"-"`
	VerificationMethod string     `json:"verification_method"`
	// 현재 승인된 원천 정책. NULL 이면 그 근거는 공급 근거로 쓸 수 없다(S03).
	PolicyID       *uuid.UUID `json:"source_policy_id"`
	PolicyRevision *int64     `json:"source_policy_revision"`
	PolicyExport   bool       `json:"policy_name_export_allowed"`
}
type commonSnapshot struct {
	ID                      uuid.UUID `json:"entity_id"`
	KO, Type, Status, Owner string
	Locked                  bool
	Revision                int64       `json:"entity_revision"`
	IdentityEvidence        []uuid.UUID `json:"identity_evidence_ids"`
	// 정체성 근거를 받치는 현재 승인 정책들과, 정책이 없는 근거의 수.
	IdentityPolicies  []byte `json:"-"`
	IdentityUnbacked  int    `json:"-"`
	Names                   []commonName
	Anchors                 []commonAnchor
}

func readCommonSnapshot(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*commonSnapshot, error) {
	s := &commonSnapshot{ID: id, IdentityEvidence: []uuid.UUID{}, Names: []commonName{}}
	// 정체성 근거와 함께, 그 근거를 받치는 **현재 승인된** 정책을 읽는다. 과거에 true 였던
	// 플래그가 아니라 지금의 status/valid_until 을 직접 확인한다(S03).
	err := tx.QueryRow(ctx, `SELECT canonical_ko,entity_type,status,write_owner,operator_locked,revision,
 ARRAY(SELECT v.id FROM kentity_evidence v WHERE v.entity_id=e.id AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed ORDER BY v.id),
 COALESCE((SELECT jsonb_agg(DISTINCT jsonb_build_object('policy_id',p.id,'revision',p.revision))
             FROM kentity_evidence v JOIN LATERAL (SELECT sp.id,sp.revision FROM kentity_source_policies sp
               WHERE sp.provider=v.provider AND sp.status='approved' AND (sp.valid_until IS NULL OR sp.valid_until>now())
               ORDER BY sp.created_at DESC LIMIT 1) p ON true
            WHERE v.entity_id=e.id AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed),'[]'::jsonb),
 (SELECT count(*) FROM kentity_evidence v WHERE v.entity_id=e.id AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed
    AND NOT EXISTS(SELECT 1 FROM kentity_source_policies sp WHERE sp.provider=v.provider AND sp.status='approved' AND (sp.valid_until IS NULL OR sp.valid_until>now())))
 FROM kentity_entities e WHERE e.id=$1`, id).Scan(&s.KO, &s.Type, &s.Status, &s.Owner, &s.Locked, &s.Revision, &s.IdentityEvidence, &s.IdentityPolicies, &s.IdentityUnbacked)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT n.id,n.locale,n.value,n.kind,n.form,n.status,n.revision,n.source_code,n.evidence_id,COALESCE(v.status,''),COALESCE(v.export_allowed,false),COALESCE(v.license_code,''),COALESCE(v.source_url,''),(n.valid_from IS NULL OR n.valid_from<=CURRENT_DATE) AND (n.valid_until IS NULL OR n.valid_until>=CURRENT_DATE),n.valid_from,n.valid_until,v.observed_at,v.verified_at,COALESCE(v.verified_by,''),pol.id,pol.revision,COALESCE(pol.name_export_allowed,false) FROM kentity_names n LEFT JOIN kentity_evidence v ON v.id=n.evidence_id AND v.entity_id=n.entity_id LEFT JOIN LATERAL (SELECT sp.id,sp.revision,sp.name_export_allowed FROM kentity_source_policies sp WHERE sp.provider=v.provider AND sp.status='approved' AND (sp.valid_until IS NULL OR sp.valid_until>now()) ORDER BY sp.created_at DESC LIMIT 1) pol ON true WHERE n.entity_id=$1 ORDER BY n.locale,n.id LIMIT 501`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var n commonName
		if err = rows.Scan(&n.ID, &n.Locale, &n.Value, &n.Kind, &n.Form, &n.Status, &n.Revision, &n.Source, &n.EvidenceID, &n.EvidenceStatus, &n.Export, &n.License, &n.SourceURL, &n.Current, &n.ValidFrom, &n.ValidUntil, &n.ObservedAt, &n.VerifiedAt, &n.VerifiedBy, &n.PolicyID, &n.PolicyRevision, &n.PolicyExport); err != nil {
			return nil, err
		}
		n.VerificationMethod = "unreviewed"
		if n.Status == "verified" && n.EvidenceStatus == "verified" {
			n.VerificationMethod = "operator_review"
			if strings.HasPrefix(n.VerifiedBy, "policy:") {
				n.VerificationMethod = n.VerifiedBy
			}
		}
		s.Names = append(s.Names, n)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(s.Names) > 500 {
		return nil, errors.New("common name count exceeds bounded readiness snapshot")
	}
	rows.Close()
	s.Anchors, err = readCommonAnchors(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	return s, nil
}

func commonCandidates(ctx context.Context, tx pgx.Tx, it Item) ([]uuid.UUID, error) {
	if it.SuppliedEntityID != nil {
		return []uuid.UUID{*it.SuppliedEntityID}, nil
	}
	rows, err := tx.Query(ctx, `SELECT e.id FROM kentity_entities e WHERE e.status NOT IN ('rejected','retired') AND ($2='' OR e.entity_type=$2) AND (e.canonical_ko=$1 OR EXISTS(SELECT 1 FROM kentity_names n JOIN kentity_evidence v ON v.id=n.evidence_id AND v.entity_id=n.entity_id WHERE n.entity_id=e.id AND n.locale='ko' AND n.value=$1 AND n.status='verified' AND v.status='verified' AND v.export_allowed AND (n.valid_from IS NULL OR n.valid_from<=CURRENT_DATE) AND (n.valid_until IS NULL OR n.valid_until>=CURRENT_DATE))) ORDER BY e.id LIMIT 51`, it.Term, it.Type)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func validCommonName(n commonName) bool {
	if n.Kind != "canonical" || n.Form != "recorded" || n.Status != "verified" || !n.Current || n.EvidenceID == nil || n.EvidenceStatus != "verified" || !n.Export || n.License == "" || n.License == "unreviewed" {
		return false
	}
	u, err := url.Parse(n.SourceURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return false
	}
	locale := map[string]string{"zh-Hans": "zh", "zh-Hant": "zh_hant", "zh-TW": "zh_hant", "pt-BR": "pt_br", "pt": "pt_br"}[n.Locale]
	if locale == "" {
		locale = n.Locale
	}
	return strings.TrimSpace(n.Value) != "" && kdb.IsValidSpellingForLocale(locale, n.Value)
}

// policyRef — S03 의 정책 증명 항목. 선택한 근거와 필수 의존의 (policy_id, revision) 다.
type policyRef struct {
	PolicyID string `json:"policy_id"`
	Revision int64  `json:"revision"`
}

// mergePolicyProof — 정체성 근거의 정책들과 선택한 이름 근거의 정책을 중복 없이 합친다.
// 빈 배열을 돌려주지 않는다. 호출측은 ready 일 때만 쓴다(비어 있으면 DB CHECK 가 막는다).
func mergePolicyProof(identity []byte, nameID *uuid.UUID, nameRev *int64) json.RawMessage {
	var refs []policyRef
	if len(identity) > 0 {
		_ = json.Unmarshal(identity, &refs)
	}
	if nameID != nil && nameRev != nil {
		refs = append(refs, policyRef{PolicyID: nameID.String(), Revision: *nameRev})
	}
	seen := map[string]bool{}
	out := make([]policyRef, 0, len(refs))
	for _, r := range refs {
		key := r.PolicyID + "/" + strconv.FormatInt(r.Revision, 10)
		if r.PolicyID == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PolicyID != out[j].PolicyID {
			return out[i].PolicyID < out[j].PolicyID
		}
		return out[i].Revision < out[j].Revision
	})
	b, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`[]`)
	}
	return b
}

func evaluateCommon(s *commonSnapshot, locale string) Locale {
	l := Locale{Locale: locale, State: "no_evidence", Reason: "no reviewed exact-locale canonical name", Proof: json.RawMessage(`{}`)}
	if s.Owner == "kdb" {
		l.State = "unverified"
		l.Reason = "legacy catalog values have not undergone common evidence review"
	} else if s.Status != "active" || len(s.IdentityEvidence) == 0 {
		l.State = "policy_blocked"
		l.Reason = "common identity is not active with reusable verified evidence"
	} else if s.Locked {
		l.State = "policy_blocked"
		l.Reason = "common operator lock blocks serving"
	} else if s.IdentityUnbacked > 0 {
		// S03: 과거에 export_allowed 였다는 사실이 아니라 지금 승인된 정책이 있어야 공급한다.
		l.State = "policy_blocked"
		l.Reason = "identity evidence has no currently approved source policy"
	} else {
		matches := []commonName{}
		hasStored := false
		for _, n := range s.Names {
			if n.Locale == locale {
				hasStored = true
				if validCommonName(n) {
					matches = append(matches, n)
				}
			}
		}
		if len(matches) == 1 && (matches[0].PolicyID == nil || !matches[0].PolicyExport) {
			// 이름 자체는 검수됐지만 그 근거의 원천 정책이 승인되지 않았거나 표기 공급을 허용하지 않는다.
			// 승인 없는 공급은 하지 않는다(S03, P0.09 §5.5 기본 차단).
			l.State = "policy_blocked"
			l.Reason = "source policy for the reviewed name is not approved for name export"
		} else if len(matches) == 1 {
			n := matches[0]
			l.State = "ready"
			l.Value = n.Value
			l.Source = n.Source
			l.Reason = "reviewed exact-locale recorded name; not a claim of official naming"
			if n.VerifiedBy == "policy:"+CommonFillPolicy {
				l.Reason = "common_auto_recorded_name"
			}
			l.PolicyProof = mergePolicyProof(s.IdentityPolicies, n.PolicyID, n.PolicyRevision)
			l.Proof, _ = json.Marshal(struct {
				Policy           string      `json:"policy_version"`
				EntityID         uuid.UUID   `json:"entity_id"`
				EntityRevision   int64       `json:"entity_revision"`
				IdentityEvidence []uuid.UUID `json:"identity_evidence_ids"`
				Name             commonName  `json:"name"`
			}{CommonPolicyVersion, s.ID, s.Revision, s.IdentityEvidence, n})
		} else if len(matches) > 1 {
			l.State = "ambiguous"
			l.Reason = "overlapping reviewed canonical names require policy review"
		} else if hasStored {
			l.State = "unverified"
			l.Reason = "stored exact-locale name is unreviewed, generated, expired or evidence-blocked"
		}
		if l.State != "ready" && locale != "en" {
			var en []commonName
			for _, n := range s.Names {
				if n.Locale == "en" && validCommonName(n) {
					en = append(en, n)
				}
			}
			if len(en) == 1 {
				l.FallbackValue = en[0].Value
			}
		}
	}
	l.Fingerprint = commonInputFingerprint(s, locale)
	return l
}

func commonTermMatches(s *commonSnapshot, term string) bool {
	if s.KO == term {
		return true
	}
	for _, n := range s.Names {
		if n.Locale == "ko" && n.Value == term && n.Form == "recorded" && n.Status == "verified" && n.Current && n.EvidenceStatus == "verified" && n.Export {
			return true
		}
	}
	return false
}

// Every common read re-observes master/evidence under one serializable snapshot.
// A successful read is an observation, not an eternal license/publication grant.
func (s *Store) refreshCommon(ctx context.Context, owner string, id uuid.UUID) (*Preparation, error) {
	if !s.CommonEnabled {
		return nil, ErrPolicy
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return nil, err
	}
	var lockedID uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM kentity_preparations WHERE id=$1 AND owner_key=$2 FOR UPDATE`, id, owner).Scan(&lockedID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	p, err := readPreparation(ctx, tx, owner, id)
	if err != nil {
		return nil, err
	}
	if p.PolicyVersion != CommonPolicyVersion {
		return nil, ErrPolicy
	}
	if p.Status == "cancelled" {
		return p, tx.Commit(ctx)
	}
	changed, ready, review := false, true, false
	for _, it := range p.Items {
		candidates, err := commonCandidates(ctx, tx, it)
		if err != nil {
			return nil, err
		}
		identity := "no_evidence"
		var resolved *uuid.UUID
		var snap *commonSnapshot
		if len(candidates) > 0 {
			identity = "ambiguous"
		}
		// Candidate matching is discovery only. Callers must explicitly bind an ID
		// after contextual identity review; one same-name hit is not that review.
		if it.SuppliedEntityID != nil {
			snap, err = readCommonSnapshot(ctx, tx, *it.SuppliedEntityID)
			if errors.Is(err, pgx.ErrNoRows) {
				snap = nil
				err = nil
				identity = "no_evidence"
			}
			if err != nil {
				return nil, err
			}
			if snap != nil {
				identity = "resolved"
				resolved = &snap.ID
				if !commonTermMatches(snap, it.Term) || (it.Type != "" && it.Type != snap.Type) || (it.BoundEntityID != nil && *it.BoundEntityID != snap.ID) {
					identity = "ambiguous"
					snap = nil
					resolved = nil
				}
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE kentity_preparation_items SET resolved_entity_id=$3,bound_entity_id=COALESCE(bound_entity_id,$3),bound_identity_revision=COALESCE(bound_identity_revision,(SELECT identity_revision FROM kentity_entities WHERE id=$3)),bound_entity_revision=COALESCE(bound_entity_revision,(SELECT revision FROM kentity_entities WHERE id=$3)),candidate_ids=$4,identity_state=$5 WHERE preparation_id=$1 AND ordinal=$2`, id, it.Ordinal, resolved, candidates, identity); err != nil {
			return nil, err
		}
		for _, old := range it.Locales {
			next := Locale{Locale: old.Locale, State: identity, Reason: "common identity needs an explicitly reviewed Entity UUID", Proof: json.RawMessage(`{}`), Fingerprint: hash(struct {
				Policy   string
				IDs      []uuid.UUID
				Identity string
			}{CommonPolicyVersion, candidates, identity})}
			if snap != nil {
				next = evaluateCommon(snap, old.Locale)
				if s.CommonFillEnabled && next.State == "no_evidence" {
					if err = queueCommonFill(ctx, tx, snap, &next); err != nil {
						return nil, err
					}
				}
			}
			if next.State != "ready" {
				ready = false
			}
			if next.State == "ambiguous" || next.State == "policy_blocked" || next.State == "unverified" || next.State == "no_evidence" {
				review = true
			}
			if !equalState(old, next) {
				changed = true
				if err = writeLocale(ctx, tx, id, it.Ordinal, old, next); err != nil {
					return nil, err
				}
			}
		}
	}
	status := "preparing"
	if review {
		status = "review"
	}
	if ready {
		status = "ready"
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_locale_readiness SET observed_at=now() WHERE preparation_id=$1`, id); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_preparations SET status=$2,revision=revision+CASE WHEN $3 OR status<>$2 THEN 1 ELSE 0 END,updated_at=now(),next_check_at=now()+interval '30 seconds' WHERE id=$1`, id, status, changed); err != nil {
		return nil, err
	}
	result, err := readPreparation(ctx, tx, owner, id)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
