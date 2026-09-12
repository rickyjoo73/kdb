package kentity

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"log"
	"strings"
	"time"
)

type TDBShadowFetcher interface {
	Fetch(context.Context, string) (*wikidata.Entity, error)
}
type TDBShadowWorker struct {
	Store  *Store
	Source TDBShadowFetcher
}

func (w *TDBShadowWorker) Run(ctx context.Context) {
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		func() {
			defer func() {
				if recover() != nil {
					log.Print("tdb shadow: panic recovered; lease will expire")
				}
			}()
			work, cancel := context.WithTimeout(ctx, 25*time.Second)
			defer cancel()
			if _, err := w.ProcessOne(work); err != nil {
				log.Print("tdb shadow: bounded observation failed")
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func (w *TDBShadowWorker) claim(ctx context.Context) (*TDBShadow, error) {
	if _, err := w.Store.Pool.Exec(ctx, `UPDATE kentity_tdb_shadows SET state='failed',reason='lease_expired_budget',lease_token=NULL,lease_until=NULL,next_attempt_at=NULL,updated_at=now() WHERE state='running' AND attempts>=4 AND lease_until<now()`); err != nil {
		return nil, err
	}
	j := &TDBShadow{Token: uuid.New()}
	err := w.Store.Pool.QueryRow(ctx, `WITH due AS(SELECT id FROM kentity_tdb_shadows WHERE policy_version=$1 AND attempts<4 AND ((state IN ('pending','failed') AND next_attempt_at<=now()) OR (state='running' AND lease_until<now())) ORDER BY next_attempt_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) UPDATE kentity_tdb_shadows j SET state='running',attempts=attempts+1,generation=generation+1,lease_token=$2,lease_until=now()+interval '60 seconds',next_attempt_at=NULL,updated_at=now() FROM due WHERE j.id=due.id RETURNING j.id,j.tdb_id,j.qid,j.source_fingerprint,j.generation,j.source_locked,j.source_observed_at,j.attempts,j.source_type`, TDBShadowPolicy, j.Token).Scan(&j.ID, &j.TDBID, &j.QID, &j.Fingerprint, &j.Generation, &j.Locked, &j.ObservedAt, &j.Attempts, &j.SourceType)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}
func (w *TDBShadowWorker) ProcessOne(ctx context.Context) (bool, error) {
	if w.Source == nil {
		return false, errors.New("TDB shadow source missing")
	}
	j, err := w.claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	if j.Locked || time.Since(j.ObservedAt) > 24*time.Hour {
		return true, w.finish(ctx, *j, nil, "blocked", "source binding expired or protected")
	}
	fetch, cancel := context.WithTimeout(ctx, 12*time.Second)
	e, err := w.Source.Fetch(fetch, j.QID)
	cancel()
	if err != nil {
		return true, w.finish(ctx, *j, nil, "failed", "source_error")
	}
	if e == nil || e.QID != j.QID || strings.TrimSpace(e.SourceLabels["ko"]) == "" {
		return true, w.finish(ctx, *j, nil, "blocked", "missing exact source identity or Korean label")
	}
	if bad, _ := e.IsNameElement(); bad {
		return true, w.finish(ctx, *j, nil, "blocked", "name element is not an Entity identity")
	}
	if typ, ok := TDBTypeFor(j.SourceType); ok && typ.EntityType != "unknown" && (typ.EntityType == "person") != containsString(e.InstanceOf, "Q5") {
		return true, w.finish(ctx, *j, nil, "blocked", "TDB category and independent source person type disagree")
	}
	p := &Proposal{ObservedAt: time.Now().UTC(), QID: e.QID, KO: e.SourceLabels["ko"], Description: e.Descriptions["ko"], SourceURL: "https://www.wikidata.org/wiki/" + e.QID, License: "CC0-1.0", Names: []Name{}, ExistingIDs: []uuid.UUID{}, Reason: "TDB binding and common identity require independent review"}
	p.InstanceOf = append([]string(nil), e.InstanceOf...)
	if len(p.InstanceOf) > 20 {
		p.InstanceOf = p.InstanceOf[:20]
	}
	if len([]rune(p.KO)) > 300 {
		return true, w.finish(ctx, *j, nil, "blocked", "source label exceeds bound")
	}
	if len([]rune(p.Description)) > 2000 {
		p.Description = string([]rune(p.Description)[:2000])
	}
	for _, lang := range []string{"ko", "en", "ja", "zh", "zh-hans", "zh-hant", "zh-tw", "vi", "id", "es", "pt", "pt-br"} {
		value := strings.TrimSpace(e.SourceLabels[lang])
		if value == "" || len([]rune(value)) > 300 {
			continue
		}
		locale, check := lang, lang
		switch lang {
		case "zh-hans":
			locale, check = "zh-Hans", "zh"
		case "zh-hant":
			locale, check = "zh-Hant", "zh_hant"
		case "zh-tw":
			locale, check = "zh-TW", "zh_hant"
		case "pt-br":
			locale, check = "pt-BR", "pt_br"
		case "pt":
			check = "pt_br"
		}
		if !kdb.IsValidSpellingForLocale(check, value) {
			continue
		}
		p.Names = append(p.Names, Name{Locale: locale, Value: value, Kind: "canonical", Form: "recorded", Status: "unverified", Source: "wikidata-label", Owner: "tdb-shadow"})
	}
	return true, w.finish(ctx, *j, p, "review", "independent source observed; TDB link is not identity approval")
}
func (w *TDBShadowWorker) finish(ctx context.Context, j TDBShadow, p *Proposal, state, reason string) error {
	tx, err := w.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	var valid bool
	var sourceLocked bool
	if err = tx.QueryRow(ctx, `SELECT state='running' AND generation=$2 AND lease_token=$3 AND source_fingerprint=$4 AND lease_until>now(),source_locked FROM kentity_tdb_shadows WHERE id=$1 FOR UPDATE`, j.ID, j.Generation, j.Token, j.Fingerprint).Scan(&valid, &sourceLocked); err != nil {
		return err
	}
	if !valid {
		return ErrProtected
	}
	if sourceLocked {
		p = nil
		state = "blocked"
		reason = "source binding protected"
	}
	if p != nil {
		rows, err := tx.Query(ctx, `SELECT DISTINCT entity_id FROM (SELECT entity_id FROM kwave_entity_external_refs WHERE provider='wikidata' AND external_id=$1 UNION SELECT entity_id FROM kentity_external_ids WHERE provider='wikidata' AND external_id=$1 AND status IN ('verified','unverified','conflict')) x ORDER BY entity_id LIMIT 51`, j.QID)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id uuid.UUID
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			p.ExistingIDs = append(p.ExistingIDs, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
	}
	raw := []byte(`{}`)
	if p != nil {
		raw, err = json.Marshal(p)
		if err != nil {
			return err
		}
	}
	var retry *time.Time
	if state == "failed" && j.Attempts < 4 {
		next := time.Now().Add(time.Duration(1<<uint(j.Attempts-1)) * time.Minute)
		retry = &next
	}
	if _, err = tx.Exec(ctx, `UPDATE kentity_tdb_shadows SET state=$2,reason=$3,result=$4,lease_token=NULL,lease_until=NULL,next_attempt_at=$5,updated_at=now() WHERE id=$1`, j.ID, state, reason, raw, retry); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,after_value) VALUES($1,'independent-wikidata-observer',$2,jsonb_build_object('result',$3::jsonb,'reason',$4::text,'generation',$5::bigint))`, j.ID, state, raw, reason, j.Generation); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
