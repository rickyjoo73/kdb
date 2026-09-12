package kentity

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

const ResolutionPolicy = "candidate-research-v1"

var researchQID = regexp.MustCompile(`^Q[1-9][0-9]*$`)

type ResearchSource interface {
	Search(context.Context, string, string, int, bool) ([]wikidata.Candidate, error)
	Fetch(context.Context, string) (*wikidata.Entity, error)
}
type Resolver struct {
	Store  *Store
	Source ResearchSource
}
type Proposal struct {
	ObservedAt  time.Time   `json:"observed_at"`
	QID         string      `json:"qid"`
	KO          string      `json:"ko"`
	Description string      `json:"description"`
	SourceURL   string      `json:"source_url"`
	License     string      `json:"license"`
	Names       []Name      `json:"names"`
	ExistingIDs []uuid.UUID `json:"existing_ids"`
	Reason      string      `json:"reason"`
}
type Resolution struct {
	ID, EntityID               uuid.UUID
	Revision, Generation       int64
	Query, Type, State, Reason string
	Attempts                   int
	Token                      uuid.UUID
	NextAttempt                *time.Time
	UpdatedAt                  time.Time
	Proposals                  []Proposal
}

func (s *Store) Resolution(ctx context.Context, id uuid.UUID) (*Resolution, error) {
	j := &Resolution{EntityID: id}
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT id,entity_revision,query_ko,entity_type,state,reason,attempts,next_attempt_at,updated_at,result,generation FROM kentity_resolution_jobs WHERE entity_id=$1 ORDER BY created_at DESC,id DESC LIMIT 1`, id).Scan(&j.ID, &j.Revision, &j.Query, &j.Type, &j.State, &j.Reason, &j.Attempts, &j.NextAttempt, &j.UpdatedAt, &raw, &j.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &j.Proposals); err != nil {
		return nil, err
	}
	return j, nil
}

func (s *Store) RequestResearch(ctx context.Context, actor string, id uuid.UUID, revision int64, reason string) error {
	if actor == "" || id == uuid.Nil || revision < 1 || len(strings.TrimSpace(reason)) < 10 || len(reason) > 2000 {
		return ErrInvalid
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	var query, typ, origin, state string
	var locked bool
	var rev int64
	err = tx.QueryRow(ctx, `SELECT canonical_ko,entity_type,origin_system,status,operator_locked,revision FROM kentity_entities WHERE id=$1 FOR UPDATE`, id).Scan(&query, &typ, &origin, &state, &locked, &rev)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if origin != "native" || state != "candidate" || locked || rev != revision {
		return ErrProtected
	}
	tag, err := tx.Exec(ctx, `INSERT INTO kentity_resolution_jobs(entity_id,entity_revision,query_ko,entity_type,requested_by) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, id, rev, query, typ, actor)
	if err != nil {
		return err
	}
	if tag.RowsAffected() > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason) VALUES($1,$2,'candidate_research_requested',$3)`, id, actor, reason); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *Resolver) Run(ctx context.Context) {
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	for {
		func() {
			defer func() {
				if recover() != nil {
					log.Print("kentity resolver: recovered panic; lease will expire without publishing result")
				}
			}()
			work, cancel := context.WithTimeout(ctx, 80*time.Second)
			defer cancel()
			_, err := w.ProcessOne(work)
			if err != nil && ctx.Err() == nil {
				log.Printf("kentity resolver: %v", err)
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (w *Resolver) claim(ctx context.Context) (*Resolution, error) {
	if _, err := w.Store.Pool.Exec(ctx, `UPDATE kentity_resolution_jobs SET state='failed',reason='lease_expired_budget',lease_token=NULL,lease_until=NULL,next_attempt_at=NULL,updated_at=now() WHERE state='running' AND lease_until<now() AND attempts>=4`); err != nil {
		return nil, err
	}
	j := &Resolution{Token: uuid.New()}
	err := w.Store.Pool.QueryRow(ctx, `WITH due AS (
 SELECT id FROM kentity_resolution_jobs WHERE policy_version=$1 AND attempts<4
 AND ((state IN ('pending','failed') AND next_attempt_at<=now()) OR (state='running' AND lease_until<now()))
 ORDER BY COALESCE(next_attempt_at,created_at),created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE kentity_resolution_jobs j SET state='running',attempts=attempts+1,generation=generation+1,lease_token=$2,lease_until=now()+interval '120 seconds',next_attempt_at=NULL,updated_at=now()
 FROM due WHERE j.id=due.id RETURNING j.id,j.entity_id,j.entity_revision,j.query_ko,j.entity_type,j.attempts,j.generation`, ResolutionPolicy, j.Token).Scan(&j.ID, &j.EntityID, &j.Revision, &j.Query, &j.Type, &j.Attempts, &j.Generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

func (w *Resolver) ProcessOne(ctx context.Context) (bool, error) {
	if w.Source == nil {
		return false, errors.New("research source not configured")
	}
	j, err := w.claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	// Eligibility before source access; checked again under lock before saving.
	var eligible bool
	err = w.Store.Pool.QueryRow(ctx, `SELECT origin_system='native' AND status='candidate' AND NOT operator_locked AND revision=$2 AND canonical_ko=$3 AND entity_type=$4 FROM kentity_entities WHERE id=$1`, j.EntityID, j.Revision, j.Query, j.Type).Scan(&eligible)
	if err != nil {
		return true, err
	}
	if !eligible {
		return true, w.finish(ctx, *j, nil, "blocked", "entity_changed_or_locked")
	}
	lookup, cancel := context.WithTimeout(ctx, 12*time.Second)
	candidates, err := w.Source.Search(lookup, j.Query, "ko", 5, false)
	cancel() // no entertainment-only filter
	if err != nil {
		return true, w.finish(ctx, *j, nil, "failed", "source_error")
	}
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	proposals := []Proposal{}
	seen := map[string]bool{}
	for _, c := range candidates {
		if !researchQID.MatchString(c.QID) || seen[c.QID] {
			continue
		}
		seen[c.QID] = true
		lookup, cancel = context.WithTimeout(ctx, 12*time.Second)
		e, fetchErr := w.Source.Fetch(lookup, c.QID)
		cancel()
		if fetchErr != nil {
			return true, w.finish(ctx, *j, nil, "failed", "source_error")
		}
		if e == nil || e.QID != c.QID {
			continue
		}
		if bad, _ := e.IsNameElement(); bad {
			continue
		}
		if !researchNameMatches(j.Query, e) {
			continue
		}
		if j.Type == "person" && !containsString(e.InstanceOf, "Q5") {
			continue
		}
		if j.Type != "person" && containsString(e.InstanceOf, "Q5") {
			continue
		}
		p := Proposal{ObservedAt: time.Now().UTC(), QID: c.QID, KO: e.Labels["ko"], Description: e.Descriptions["ko"], SourceURL: "https://www.wikidata.org/wiki/" + c.QID, License: "CC0-1.0", Reason: "identity_requires_review", Names: []Name{}, ExistingIDs: []uuid.UUID{}}
		if p.Description == "" {
			p.Description = e.Descriptions["en"]
		}
		if rs := []rune(p.Description); len(rs) > 2000 {
			p.Description = string(rs[:2000])
		}
		for _, loc := range []string{"ko", "en", "ja", "zh", "zh-hant", "zh-tw", "vi", "id", "es", "pt", "pt-br"} {
			v := strings.TrimSpace(e.SourceLabels[loc])
			if v == "" || len([]rune(v)) > 300 {
				continue
			}
			checkLocale := loc
			switch loc {
			case "zh-hant", "zh-tw":
				checkLocale = "zh_hant"
			case "pt", "pt-br":
				checkLocale = "pt_br"
			}
			if loc != "ko" && !kdb.IsValidSpellingForLocale(checkLocale, v) {
				continue
			}
			locale := loc
			switch loc {
			case "zh-hant":
				locale = "zh-Hant"
			case "zh-tw":
				locale = "zh-TW"
			case "pt-br":
				locale = "pt-BR"
			}
			p.Names = append(p.Names, Name{Locale: locale, Value: v, Kind: "canonical", Form: "recorded", Status: "unverified", Source: "wikidata-label", Owner: "candidate-proposal"})
		}
		proposals = append(proposals, p)
	}
	state, reason := "review", "source_candidates_require_identity_review"
	if len(proposals) == 0 {
		state, reason = "no_match", "no_admissible_candidate_in_bounded_search"
	}
	return true, w.finish(ctx, *j, proposals, state, reason)
}

func researchNameMatches(q string, e *wikidata.Entity) bool {
	normalize := func(s string) string { return strings.Join(strings.Fields(strings.ToLower(s)), "") }
	q = normalize(q)
	if normalize(e.Labels["ko"]) == q {
		return true
	}
	for _, alias := range e.Aliases["ko"] {
		if normalize(alias) == q {
			return true
		}
	}
	return false
}
func containsString(values []string, want string) bool {
	for _, s := range values {
		if s == want {
			return true
		}
	}
	return false
}

func (w *Resolver) finish(ctx context.Context, j Resolution, proposals []Proposal, state, reason string) error {
	tx, err := w.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var eligible bool
	if err = tx.QueryRow(ctx, `SELECT origin_system='native' AND status='candidate' AND NOT operator_locked AND revision=$2 AND canonical_ko=$3 AND entity_type=$4 FROM kentity_entities WHERE id=$1 FOR UPDATE`, j.EntityID, j.Revision, j.Query, j.Type).Scan(&eligible); err != nil {
		return err
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT state='running' AND lease_token=$2 AND generation=$3 AND lease_until>now() FROM kentity_resolution_jobs WHERE id=$1 FOR UPDATE`, j.ID, j.Token, j.Generation).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return ErrProtected
	}
	if !eligible {
		proposals = nil
		state, reason = "blocked", "entity_changed_or_locked"
	}
	for i := range proposals {
		rows, err := tx.Query(ctx, `SELECT entity_id FROM kwave_entity_external_refs WHERE provider='wikidata' AND external_id=$1 UNION SELECT entity_id FROM kentity_external_ids WHERE provider='wikidata' AND external_id=$1 AND status<>'withdrawn' ORDER BY entity_id LIMIT 20`, proposals[i].QID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			proposals[i].ExistingIDs = append(proposals[i].ExistingIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if len(proposals[i].ExistingIDs) > 0 {
			proposals[i].Reason = "existing_identity_claim_requires_link_review"
		}
	}
	if proposals == nil {
		proposals = []Proposal{}
	}
	raw, err := json.Marshal(proposals)
	if err != nil {
		return err
	}
	var next *time.Time
	if state == "failed" && j.Attempts < 4 {
		t := time.Now().Add(time.Duration(1<<uint(j.Attempts-1)) * time.Minute)
		next = &t
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_resolution_jobs SET state=$2,reason=$3,result=$4,next_attempt_at=$5,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, j.ID, state, reason, raw, next); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value) VALUES($1,'entity-resolver','candidate_research_finished',$2,jsonb_build_object('job_id',$3::text,'state',$4::text,'proposals',$5::int,'attempt',$6::int))`, j.EntityID, reason, j.ID.String(), state, len(proposals), j.Attempts); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
