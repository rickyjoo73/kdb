package readiness

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
)

type Fetcher interface {
	Fetch(context.Context, string) (*wikidata.Entity, error)
}
type Worker struct {
	Store  *Store
	Source Fetcher
}
type job struct {
	ID, EntityID, Token      uuid.UUID
	Locale, Fingerprint, QID string
	Generation               int64
	Attempts                 int
}

func (w *Worker) Run(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("readiness worker recovered: %v", r)
				}
			}()
			work, cancel := context.WithTimeout(ctx, 40*time.Second)
			defer cancel()
			if _, err := w.Store.Reconcile(work, 25); err != nil {
				log.Printf("readiness reconcile: %v", err)
				return
			}
			for i := 0; i < 2; i++ {
				ok, err := w.ProcessOne(work)
				if err != nil {
					log.Printf("readiness fill: %v", err)
				}
				if !ok {
					break
				}
			}
		}()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *Worker) claim(ctx context.Context) (*job, error) {
	// A crash on the last allowed attempt must not leave a permanent running job.
	if _, err := w.Store.Pool.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state='failed',last_reason='worker lease expired after retry budget',
 lease_token=NULL,lease_until=NULL,next_retry_at=NULL,updated_at=now() WHERE state='running' AND lease_until<now() AND attempts>=4`); err != nil {
		return nil, err
	}
	j := &job{Token: uuid.New()}
	err := w.Store.Pool.QueryRow(ctx, `WITH due AS (
 SELECT j.id FROM kentity_locale_fill_jobs j
 WHERE j.policy_version=$1 AND j.attempts<4
 AND ((j.state IN ('pending','failed','no_evidence') AND j.next_retry_at<=now()) OR (j.state='running' AND j.lease_until<now()))
 AND EXISTS (SELECT 1 FROM kentity_locale_readiness l JOIN kentity_preparation_items i USING(preparation_id,ordinal)
 JOIN kentity_preparations p ON p.id=i.preparation_id
 WHERE i.resolved_entity_id=j.entity_id AND l.locale=j.locale AND l.input_fingerprint=j.input_fingerprint
 AND p.cancelled_at IS NULL AND l.state IN ('pending','failed','no_evidence'))
 ORDER BY j.next_retry_at,j.created_at,j.id LIMIT 1 FOR UPDATE OF j SKIP LOCKED
 ) UPDATE kentity_locale_fill_jobs j SET state='running',attempts=attempts+1,generation=generation+1,
 lease_token=$2,lease_until=now()+interval '60 seconds',updated_at=now()
 FROM due WHERE j.id=due.id RETURNING j.id,j.entity_id,j.locale,j.input_fingerprint,j.qid,j.generation,j.attempts`, PolicyVersion, j.Token).
		Scan(&j.ID, &j.EntityID, &j.Locale, &j.Fingerprint, &j.QID, &j.Generation, &j.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return j, err
}

func (w *Worker) ProcessOne(ctx context.Context) (bool, error) {
	if w.Source == nil {
		return false, errors.New("readiness source not configured")
	}
	j, err := w.claim(ctx)
	if err != nil || j == nil {
		return false, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 12*time.Second)
	ent, fetchErr := w.Source.Fetch(fetchCtx, j.QID)
	cancel()
	if fetchErr != nil {
		return true, w.finish(ctx, *j, nil, "failed", "source_error: "+shortReason(fetchErr.Error()))
	}
	return true, w.finish(ctx, *j, ent, "", "")
}

func shortReason(s string) string {
	rs := []rune(s)
	if len(rs) > 300 {
		return string(rs[:300])
	}
	return s
}

func (w *Worker) finish(ctx context.Context, j job, ent *wikidata.Entity, state, reason string) error {
	tx, err := w.Store.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(ctx, `SET LOCAL lock_timeout='2s'; SET LOCAL statement_timeout='10s'`); err != nil {
		return err
	}
	// Lock one still-interested request before the job, matching Refresh/Retry's
	// request -> job -> entity order. Its cancellation cannot commit between our
	// eligibility check and the locale write. Other requests may share this job.
	var interested uuid.UUID
	err = tx.QueryRow(ctx, `SELECT p.id FROM kentity_preparations p
 WHERE p.cancelled_at IS NULL AND EXISTS (
 SELECT 1 FROM kentity_preparation_items i JOIN kentity_locale_readiness l USING(preparation_id,ordinal)
 WHERE i.preparation_id=p.id AND i.resolved_entity_id=$1 AND l.locale=$2
 AND l.input_fingerprint=$3 AND l.state IN ('pending','failed','no_evidence'))
 ORDER BY p.id LIMIT 1 FOR SHARE OF p`, j.EntityID, j.Locale, j.Fingerprint).Scan(&interested)
	if errors.Is(err, pgx.ErrNoRows) {
		state, reason = "stale", "all requests cancelled or replaced before fill completion"
	} else if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT lease_token=$2 AND generation=$3 AND state='running' AND lease_until>now()
 FROM kentity_locale_fill_jobs WHERE id=$1 FOR UPDATE`, j.ID, j.Token, j.Generation).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return errors.New("stale fill lease; result rejected")
	}
	snap, err := readSnapshot(ctx, tx, j.EntityID, true)
	if errors.Is(err, pgx.ErrNoRows) {
		state, reason = "stale", "entity no longer exists"
	} else if err != nil {
		return err
	} else {
		current := evaluate(snap, j.Locale)
		if current.Fingerprint != j.Fingerprint || snap.QID != j.QID {
			state, reason = "stale", "identity, policy or requested locale changed during fetch"
		} else if current.State != "pending" {
			state, reason = "policy_blocked", "entity no longer eligible for automatic fill"
		}
	}
	if state == "" {
		state, reason = "no_evidence", "anchored source has no admissible requested-locale label"
		if ent == nil || ent.QID != j.QID || !validQID.MatchString(j.QID) {
			state, reason = "policy_blocked", "source returned a different or invalid identity"
		} else if bad, _ := ent.IsNameElement(); bad {
			state, reason = "policy_blocked", "anchor is a name element or disambiguation page"
		} else if !anchorNameMatches(snap, ent) {
			state, reason = "policy_blocked", "source identity name does not match the resolved entity"
		} else {
			value := strings.TrimSpace(ent.Labels[j.Locale])
			if value != "" && kdb.IsValidSpellingForLocale(j.Locale, value) {
				col := "canonical_" + j.Locale
				// Locale is validated by intake and checked again before SQL interpolation.
				if _, ok := locales[strings.ReplaceAll(j.Locale, "_", "-")]; !ok {
					return errors.New("unsupported stored fill locale")
				}
				tag, e := tx.Exec(ctx, `UPDATE kwave_entities SET `+col+`=$2,`+col+`_source='wikidata-label',updated_at=now()
 WHERE id=$1 AND status='active' AND operator_locked=false AND needs_disambig=false AND COALESCE(`+col+`,'')=''`, j.EntityID, value)
				if e != nil {
					return e
				}
				if tag.RowsAffected() != 1 {
					return errors.New("locale write suppressed; fill not committed")
				}
				url := "https://www.wikidata.org/wiki/" + j.QID
				if _, err = tx.Exec(ctx, `INSERT INTO kwave_kdb_evidence_refs(entity_id,lane,url,title,provider,snippet)
 VALUES($1,'requested-locale',$2,$3,'wikidata',$4) ON CONFLICT(entity_id,url) DO NOTHING`, j.EntityID, url, snap.KO, j.Locale+": "+value); err != nil {
					return err
				}
				state, reason = "complete", "requested locale installed from matching stable identity"
			}
		}
	}
	var retry *time.Time
	if state == "failed" && j.Attempts < 4 {
		t := time.Now().Add(time.Duration(1<<uint(j.Attempts-1)) * time.Minute)
		retry = &t
	}
	if state == "no_evidence" && j.Attempts < 4 {
		t := time.Now().Add(7 * 24 * time.Hour)
		retry = &t
	}
	_, err = tx.Exec(ctx, `UPDATE kentity_locale_fill_jobs SET state=$2,last_reason=$3,next_retry_at=$4,lease_token=NULL,lease_until=NULL,updated_at=now() WHERE id=$1`, j.ID, state, reason, retry)
	if err != nil {
		return err
	}
	// Preparation polling observes the committed value. Do not update additional
	// request rows here: each operation acquires its request before its job.
	return tx.Commit(ctx)
}

func anchorNameMatches(s Snapshot, e *wikidata.Entity) bool {
	norm := func(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), "")) }
	names := append([]string{s.KO}, s.Aliases...)
	labels := append([]string{e.Labels["ko"]}, e.Aliases["ko"]...)
	for _, a := range names {
		for _, b := range labels {
			if norm(a) != "" && norm(a) == norm(b) {
				return true
			}
		}
	}
	return false
}

func (w *Worker) String() string {
	return fmt.Sprintf("requested-locale worker policy=%s", PolicyVersion)
}
