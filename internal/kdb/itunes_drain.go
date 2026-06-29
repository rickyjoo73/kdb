package kdb

// itunes_drain — song_album 의 codex ja/zh/zh_hant 셀을 iTunes 국가 스토어 제목으로 검증한다.
// ★보수적 confirm-only: 반환 trackName 이 현재값과 정규화 일치할 때만 source→itunes(권위 검증
// 등급)로 승급하고 값은 바꾸지 않는다 — 영어/로마자 제목 곡(주류)이 JP/CN/TW 스토어에 동일
// 제목으로 존재함을 corroborate. improve(다른 번역제목)는 동명곡 오매칭 위험이라 보류(빈칸>틀린값).
// 부수효과: 매칭 시 artistName 을 external_ref(itunes)로 저장 — song_album 엔티티에 없던
// 아티스트 앵커를 확보해 향후 MusicBrainz/iTunes improve 의 기반을 만든다(오너 지시: 발굴하며 소스 확장).

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
)

// itunesNormTitle — 제목 비교용 정규화(소문자 + 공백/구두점 제거).
func itunesNormTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch r {
		case ' ', '\t', '-', '.', '_', '\'', '"', ',', '(', ')', '!', '?', ':', '·', '・', '’':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DrainITunesSongs — confirm-only iTunes 승급 + 아티스트 앵커 저장. 30d 쿨다운.
// 반환=(confirm 으로 승급한 셀 수, 아티스트 앵커를 새로 저장한 엔티티 수).
func DrainITunesSongs(ctx context.Context, pool *pgxpool.Pool, cl *itunes.Client, limit int) (confirmed, anchored int) {
	if pool == nil || cl == nil || limit <= 0 {
		return 0, 0
	}
	rows, err := pool.Query(ctx, `
SELECT id::text, COALESCE(NULLIF(canonical_en,''), canonical_ko) AS term,
       canonical_ja, COALESCE(canonical_ja_source,''),
       canonical_zh, COALESCE(canonical_zh_source,''),
       canonical_zh_hant, COALESCE(canonical_zh_hant_source,'')
  FROM kwave_entities
 WHERE status='active' AND entity_type='song_album'
   AND (canonical_ja_source='codex-fallback' OR canonical_zh_source='codex-fallback'
        OR canonical_zh_hant_source='codex-fallback')
   AND COALESCE(notes,'') NOT LIKE '%[scope:review]%'
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=kwave_entities.id
                  AND a.field='itunes' AND a.last_attempt_at > now() - interval '30 days')
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		return 0, 0
	}
	type row struct {
		id, term                    string
		ja, jaS, zh, zhS, zht, zhtS string
	}
	var items []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.term, &r.ja, &r.jaS, &r.zh, &r.zhS, &r.zht, &r.zhtS) == nil {
			items = append(items, r)
		}
	}
	rows.Close()

	for _, it := range items {
		if strings.TrimSpace(it.term) == "" {
			continue
		}
		cells := []struct{ loc, val, src string }{
			{"ja", it.ja, it.jaS}, {"zh", it.zh, it.zhS}, {"zh_hant", it.zht, it.zhtS},
		}
		var firstArtist string
		var firstTrackID int64
		for _, c := range cells {
			if c.src != "codex-fallback" || strings.TrimSpace(c.val) == "" {
				continue
			}
			country := itunes.CountryFor(c.loc)
			if country == "" {
				continue
			}
			res, serr := cl.Search(ctx, it.term, country, 5)
			time.Sleep(2500 * time.Millisecond) // iTunes 예의(저볼륨)
			if serr != nil || len(res) == 0 {
				continue
			}
			want := itunesNormTitle(c.val)
			if want == "" {
				continue
			}
			for _, t := range res {
				if itunesNormTitle(t.TrackName) != want {
					continue
				}
				// confirm: 값 불변, source codex→itunes(권위 검증등급).
				col := "canonical_" + c.loc
				srcc := col + "_source"
				tag, _ := pool.Exec(ctx, `UPDATE kwave_entities SET `+srcc+`='itunes', updated_at=now()
				     WHERE id=$1 AND COALESCE(`+srcc+`,'')='codex-fallback'`, it.id)
				if tag.RowsAffected() > 0 {
					confirmed++
				}
				if firstArtist == "" && strings.TrimSpace(t.ArtistName) != "" {
					firstArtist = t.ArtistName
					firstTrackID = t.TrackID
				}
				break
			}
		}
		if firstArtist != "" {
			tag, _ := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'itunes',$2,$3,0.7,$4,now())
ON CONFLICT DO NOTHING`, it.id, fmt.Sprintf("%d", firstTrackID),
				fmt.Sprintf("https://music.apple.com/song/%d", firstTrackID),
				fmt.Sprintf(`{"artist":%q}`, firstArtist))
			if tag.RowsAffected() > 0 {
				anchored++
			}
		}
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'itunes',1,now(),'itunes')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, it.id)
	}
	return confirmed, anchored
}
