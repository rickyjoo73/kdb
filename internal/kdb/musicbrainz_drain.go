package kdb

// MusicBrainz candidate drain.
//
// The current client searches MusicBrainz's /artist resource. Therefore this
// drain is deliberately group-only. A song/album title must never be promoted
// by an artist hit, even when the search score is high. Song and album support
// belongs in separate recording/release-group connectors with an artist, ISRC,
// or release anchor.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
)

const musicBrainzArtistResource = "artist"

// musicBrainzResourceForEntityType is the provider contract enforced before
// any external reference is written. The current connector supports only KDB
// groups through MusicBrainz artists. In particular, song_album is false.
func musicBrainzResourceForEntityType(entityType string) (string, bool) {
	switch strings.TrimSpace(entityType) {
	case "group":
		return musicBrainzArtistResource, true
	default:
		return "", false
	}
}

func isMusicBrainzGroupMatch(term string, artist musicbrainz.Artist) bool {
	return artist.Score >= 90 && strings.TrimSpace(artist.ID) != "" &&
		strings.EqualFold(strings.TrimSpace(artist.Type), "Group") &&
		musicbrainz.NameMatches(term, artist.Name)
}

// DrainMusicBrainzCandidates rechecks candidate groups against the
// MusicBrainz artist endpoint and promotes only an exact normalized-name,
// country-scoped, high-score match. It returns (promoted, provider calls).
func DrainMusicBrainzCandidates(ctx context.Context, pool *pgxpool.Pool, cl *musicbrainz.Client, limit int) (promoted, checked int) {
	if pool == nil || cl == nil || limit <= 0 {
		return 0, 0
	}

	// Record the resource-contract rejection explicitly without calling the
	// provider. This makes the blocked song/album lane observable while keeping
	// the selector below strictly group-only.
	recordMusicBrainzTypeMismatches(ctx, pool, limit)

	rows, err := pool.Query(ctx, `
SELECT id::text, entity_type::text,
       COALESCE(NULLIF(canonical_en,''), canonical_ko) AS term
  FROM kwave_entities
 WHERE status='candidate' AND entity_type='group'
   AND operator_locked=false
   AND (
     EXISTS(SELECT 1 FROM kwave_entity_research_queue q
            WHERE q.intake_normalized_key=lower(regexp_replace(btrim(kwave_entities.canonical_ko), '[[:space:][:punct:]]+', '', 'g'))
              AND q.precheck_status IN ('pass','approved'))
     OR EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts g
               WHERE g.entity_id=kwave_entities.id AND g.field='candidate-gate' AND g.last_source='kept')
   )
   AND char_length(canonical_ko) BETWEEN 2 AND 40
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=kwave_entities.id AND r.provider='musicbrainz')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a
                  WHERE a.entity_id=kwave_entities.id AND a.field='musicbrainz'
                    AND a.last_attempt_at > now() - interval '30 days')
   AND NOT EXISTS(
       SELECT 1 FROM kwave_entity_resolution_attempts a
        WHERE a.entity_id=kwave_entities.id AND a.provider='musicbrainz'
          AND ((a.status IN ('applied','no_match','type_mismatch')
                AND a.attempted_at > now() - interval '30 days')
            OR (a.status='transient'
                AND a.attempted_at > now() - interval '1 hour')))
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0
	}
	type row struct{ id, entityType, term string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.entityType, &r.term) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		resource, compatible := musicBrainzResourceForEntityType(it.entityType)
		if !compatible || resource != musicBrainzArtistResource {
			recordMusicBrainzOutcome(ctx, pool, it.id, "type_mismatch", 0, 0,
				fmt.Sprintf("entity_type=%s cannot use MusicBrainz /artist", it.entityType), time.Now())
			continue
		}
		if strings.TrimSpace(it.term) == "" {
			continue
		}

		checked++
		started := time.Now()
		artists, searchErr := cl.SearchGroups(ctx, it.term)
		if searchErr != nil {
			// Caller cancellation is not a provider failure and must not open a
			// provider cooldown. Other errors receive a short transient backoff.
			if ctx.Err() != nil {
				auditCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				recordMusicBrainzOutcome(auditCtx, pool, it.id, "canceled", 0, 0, searchErr.Error(), started)
				cancel()
				continue
			}
			recordMusicBrainzOutcome(ctx, pool, it.id, "transient", 0, 0, searchErr.Error(), started)
			continue
		}

		var best *musicbrainz.Artist
		for i := range artists {
			candidate := &artists[i]
			if isMusicBrainzGroupMatch(it.term, *candidate) {
				best = candidate
				break
			}
		}
		if best == nil {
			recordMusicBrainzOutcome(ctx, pool, it.id, "no_match", len(artists), 0,
				"no exact normalized-name Group artist match with score >= 90", started)
			recordMusicBrainzCooldown(ctx, pool, it.id, "no_match")
			continue
		}

		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			recordMusicBrainzOutcome(ctx, pool, it.id, "transient", len(artists), best.Score,
				txErr.Error(), started)
			continue
		}
		insertTag, insertErr := tx.Exec(ctx, `
INSERT INTO kwave_entity_external_refs
  (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'musicbrainz',$2,$3,0.75,$4,now())
ON CONFLICT DO NOTHING`, it.id, best.ID,
			"https://musicbrainz.org/artist/"+best.ID,
			fmt.Sprintf(`{"resource_type":"artist","name":%q,"country":%q,"disambiguation":%q,"score":%d}`,
				best.Name, best.Country, best.Disambiguation, best.Score))
		if insertErr != nil || insertTag.RowsAffected() != 1 {
			_ = tx.Rollback(ctx)
			msg := "external reference was not inserted"
			if insertErr != nil {
				msg = insertErr.Error()
			}
			recordMusicBrainzOutcome(ctx, pool, it.id, "transient", len(artists), best.Score, msg, started)
			continue
		}

		updateTag, updateErr := tx.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.75),
	   notes = COALESCE(NULLIF(notes,'') || ' · ','') ||
	           'musicbrainz artist KR exact-name 확정('||$2||') 승급',
	   updated_at=now()
 WHERE id=$1 AND status='candidate' AND entity_type='group'
   AND operator_locked=false`, it.id, best.ID)
		if updateErr != nil || updateTag.RowsAffected() != 1 {
			_ = tx.Rollback(ctx)
			msg := "candidate was not eligible at update time"
			if updateErr != nil {
				msg = updateErr.Error()
			}
			recordMusicBrainzOutcome(ctx, pool, it.id, "transient", len(artists), best.Score, msg, started)
			continue
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			recordMusicBrainzOutcome(ctx, pool, it.id, "transient", len(artists), best.Score, commitErr.Error(), started)
			continue
		}

		promoted++
		recordMusicBrainzCooldown(ctx, pool, it.id, "applied")
		recordMusicBrainzOutcome(ctx, pool, it.id, "applied", len(artists), best.Score, "artist exact-name promotion", started)
	}
	return promoted, checked
}

func recordMusicBrainzTypeMismatches(ctx context.Context, pool *pgxpool.Pool, limit int) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_resolution_attempts
  (entity_id, provider, status, candidate_count, error_text, duration_ms, attempted_at)
SELECT e.id, 'musicbrainz', 'type_mismatch', 0,
       'entity_type=song_album cannot use MusicBrainz /artist', 0, now()
  FROM kwave_entities e
 WHERE e.status='candidate' AND e.entity_type='song_album'
   AND e.operator_locked=false
   AND NOT EXISTS (
       SELECT 1 FROM kwave_entity_resolution_attempts a
        WHERE a.entity_id=e.id AND a.provider='musicbrainz'
          AND a.status='type_mismatch'
          AND a.attempted_at > now() - interval '30 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
}

func recordMusicBrainzCooldown(ctx context.Context, pool *pgxpool.Pool, id, outcome string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts
  (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'musicbrainz',1,now(),$2)
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1,
    last_attempt_at=now(), last_source=EXCLUDED.last_source`, id, outcome)
}

func recordMusicBrainzOutcome(ctx context.Context, pool *pgxpool.Pool, id, outcome string, candidates, score int, detail string, started time.Time) {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_entity_resolution_attempts
  (entity_id, provider, status, candidate_count, match_score, error_text, duration_ms, attempted_at)
VALUES ($1,'musicbrainz',$2,$3,$4::numeric/100.0,NULLIF($5,''),$6,now())`,
		id, outcome, candidates, score, detail, int(time.Since(started).Milliseconds()))
}
