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

	// 2026-07-23 Phase1: song_album 은 더 이상 차단 대상이 아니다 —
	// DrainMusicBrainzSongs(recording/release-group, 아티스트 스코프)가 전담한다.

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

// DrainMusicBrainzSongs — song_album candidate 를 MusicBrainz recording/release-group
// 에 아티스트 스코프로 대조해 승급한다 (2026-07-23 Phase1, 오너 승인 개선안).
//
// 게이트(오염 방어 — 실측 근거: title-only 는 "Grenade"→Bruno Mars 를 잡는다):
//   ①아티스트 문맥 필수 — 소비자 기사 context_hint 에 공기(co-mention)하는 active
//     인물/그룹만 스코프로 사용. 문맥 없으면 시도 자체를 안 함(no_context 기록→P3).
//   ②score>=90 + 제목 정규화 정확일치 + 아티스트 크레딧 일치 전부 요구.
// recording 우선, 실패 시 release-group(앨범). 승급 시 external_ref(musicbrainz) +
// 빈칸일 때만 canonical_en(라틴 제목) 채움. 반환 (promoted, checked).
func DrainMusicBrainzSongs(ctx context.Context, pool *pgxpool.Pool, cl *musicbrainz.Client, limit int) (promoted, checked int) {
	if pool == nil || cl == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT e.id::text, e.canonical_ko, COALESCE(e.canonical_en,''),
       COALESCE((SELECT q.context_hint FROM kwave_entity_research_queue q
                  WHERE (q.entity_ko=e.canonical_ko OR q.entity_ko=ANY(e.aliases_ko))
                    AND COALESCE(q.context_hint,'')<>'' ORDER BY q.created_at DESC LIMIT 1),'')
  FROM kwave_entities e
 WHERE e.status='candidate' AND e.entity_type='song_album'
   AND e.operator_locked=false
   AND char_length(e.canonical_ko) BETWEEN 1 AND 60
   AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r
                  WHERE r.entity_id=e.id AND r.provider='musicbrainz')
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a
                  WHERE a.entity_id=e.id AND a.field='mb-song'
                    AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY e.updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0
	}
	type row struct{ id, ko, en, hint string }
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.ko, &r.en, &r.hint) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		artist := coMentionActiveArtist(ctx, pool, it.hint, it.ko)
		if artist == "" {
			recordMBSongCooldown(ctx, pool, it.id, "no_context")
			continue
		}
		checked++
		started := time.Now()

		// ★검색어 순서(2026-07-23 실측 수정): MB 는 K-곡 제목을 라틴(en)으로 저장한다
		// ("파이어워크"≠"FIREWORKS") — en 이 있으면 en 먼저, 그다음 ko. 제목 일치
		// 판정도 두 표기 모두 허용.
		terms := []string{}
		if strings.TrimSpace(it.en) != "" && isMostlyASCII(it.en) {
			terms = append(terms, strings.TrimSpace(it.en))
		}
		terms = append(terms, it.ko)
		titleMatch := func(recTitle string) bool {
			if musicbrainz.NameMatches(it.ko, recTitle) {
				return true
			}
			return it.en != "" && musicbrainz.NameMatches(it.en, recTitle)
		}

		best, resource := (*musicbrainz.Recording)(nil), "recording"
		var lastErr error
		for _, term := range terms {
			recs, serr := cl.SearchRecordings(ctx, term, artist)
			if serr != nil {
				lastErr = serr
				continue
			}
			if m := pickSongMatchFn(titleMatch, artist, recs); m != nil {
				best = m
				break
			}
			rgs, rerr := cl.SearchReleaseGroups(ctx, term, artist)
			if rerr != nil {
				lastErr = rerr
				continue
			}
			if m := pickSongMatchFn(titleMatch, artist, rgs); m != nil {
				best, resource = m, "release-group"
				break
			}
		}
		if best == nil && lastErr != nil {
			recordMusicBrainzOutcome(ctx, pool, it.id, "transient", 0, 0, lastErr.Error(), started)
			continue
		}
		if best == nil {
			recordMusicBrainzOutcome(ctx, pool, it.id, "no_match", 0, 0,
				"no artist-scoped exact title match (artist="+artist+")", started)
			recordMBSongCooldown(ctx, pool, it.id, "no_match")
			continue
		}

		tx, txErr := pool.Begin(ctx)
		if txErr != nil {
			continue
		}
		_, insErr := tx.Exec(ctx, `
INSERT INTO kwave_entity_external_refs
  (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'musicbrainz',$2,$3,0.8,$4,now())
ON CONFLICT DO NOTHING`, it.id, best.ID,
			"https://musicbrainz.org/"+resource+"/"+best.ID,
			fmt.Sprintf(`{"resource_type":%q,"title":%q,"artist":%q,"score":%d}`,
				resource, best.Title, artist, best.Score))
		if insErr != nil {
			_ = tx.Rollback(ctx)
			continue
		}
		// 빈칸일 때만 en 채움(라틴 제목 한정 — 비라틴 MB 제목을 en 에 넣지 않는다).
		if isMostlyASCII(best.Title) {
			_, _ = tx.Exec(ctx, `
UPDATE kwave_entities SET canonical_en=$2, canonical_en_source='musicbrainz'
 WHERE id=$1 AND COALESCE(canonical_en,'')=''`, it.id, strings.TrimSpace(best.Title))
		}
		tag, upErr := tx.Exec(ctx, `
UPDATE kwave_entities
   SET status='active', confidence=GREATEST(confidence,0.78),
       notes = COALESCE(NULLIF(notes,'') || ' · ','') ||
               'musicbrainz '||$2||' 아티스트('||$3||') 스코프 정확일치 승급',
       updated_at=now()
 WHERE id=$1 AND status='candidate' AND entity_type='song_album'
   AND operator_locked=false`, it.id, resource, artist)
		if upErr != nil || tag.RowsAffected() != 1 {
			_ = tx.Rollback(ctx)
			continue
		}
		if tx.Commit(ctx) != nil {
			continue
		}
		promoted++
		recordMBSongCooldown(ctx, pool, it.id, "applied")
		recordMusicBrainzOutcome(ctx, pool, it.id, "applied", 1, best.Score,
			resource+" artist-scoped promotion ("+artist+")", started)
	}
	return promoted, checked
}

// pickSongMatchFn — 제목 일치판정(ko/en 양표기) 필수 + ①score>=95(쿼리 자체가
// artist:"X" 스코프라 고점수=양 조건 충족) 또는 ②score>=90 AND 아티스트 크레딧
// 직접 일치.
//
// ★2026-07-23 수정(첫 실행 실측): 한글 스코프명("티아라")과 MB 라틴 크레딧("T‐ARA")
// 은 정규화 비교가 원리적으로 불일치 → ArtistMatches 단독 요구는 정상 매칭까지
// 전멸시킨다. artist 절이 이미 쿼리에 들어가 있으므로 고점수를 1차 게이트로 쓴다.
func pickSongMatchFn(titleMatch func(string) bool, artist string, recs []musicbrainz.Recording) *musicbrainz.Recording {
	for i := range recs {
		r := &recs[i]
		if !titleMatch(r.Title) {
			continue
		}
		if r.Score >= 95 {
			return r
		}
		if r.Score >= 90 && r.ArtistMatches(artist) {
			return r
		}
	}
	return nil
}

// coMentionActiveArtist — 소비자 기사 힌트에 공기하는 active 인물/그룹명(최장 일치 1건).
// ★alias 포함 탐지(2026-07-23: 힌트가 "T-ara"·"BTS" 같은 별칭으로 언급해도 잡히게)
// — 매칭은 별칭으로 하되 반환은 canonical_ko(검색 스코프 용어).
func coMentionActiveArtist(ctx context.Context, pool *pgxpool.Pool, hint, selfKo string) string {
	h := strings.TrimSpace(hint)
	if h == "" {
		return ""
	}
	var artist string
	err := pool.QueryRow(ctx, `
SELECT e.canonical_ko
  FROM kwave_entities e,
       LATERAL unnest(ARRAY[e.canonical_ko] || e.aliases_ko) AS n(nm)
 WHERE e.status='active' AND e.entity_type::text IN ('person','group')
   AND char_length(n.nm) BETWEEN 2 AND 20
   AND n.nm <> $2 AND e.canonical_ko <> $2
   AND position(n.nm IN $1) > 0
 ORDER BY char_length(n.nm) DESC
 LIMIT 1`, h, selfKo).Scan(&artist)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(artist)
}

func isMostlyASCII(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	for _, r := range s {
		if r > 0x24F {
			return false
		}
	}
	return true
}

func recordMBSongCooldown(ctx context.Context, pool *pgxpool.Pool, id, outcome string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts
  (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'mb-song',1,now(),$2)
ON CONFLICT (entity_id, field) DO UPDATE
SET attempts=kwave_kdb_enrich_attempts.attempts+1,
    last_attempt_at=now(), last_source=EXCLUDED.last_source`, id, outcome)
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
