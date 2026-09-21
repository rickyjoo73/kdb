package kdb

// discogs_drain — song_album 의 잔존 codex ja/zh/zh_hant 셀을 Discogs release 제목으로 confirm
// 한다(iTunes 폴백). iTunes 가 이미 confirm 한 셀은 source=itunes 라 자동 제외 → Discogs 는
// iTunes 미수록/미매칭 곡을 보강. ★값 불변, source codex→discogs(권위 검증등급) + release-ID/
// artist 를 external_ref(discogs)로 앵커 저장. 실측상 Discogs 제목은 라틴 위주(ja/zh 번역 ≈0)라
// 영어/로마자 제목 곡 corroborate 가 주효과. confirm-only(동명곡 오매칭 방지), 45d 쿨다운,
// 2.5s pacing, [scope:review] 제외. 곡당 1회 검색으로 매칭되는 모든 codex locale 셀을 승급.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rickyjoo73/kdb/internal/kdb/discogs"
)

// DrainDiscogsSongs — confirm-only Discogs 승급 + 아티스트/릴리스 앵커 저장. 45d 쿨다운.
// 반환=(confirm 승급 셀 수, 앵커 신규 저장 엔티티 수).
func DrainDiscogsSongs(ctx context.Context, pool *pgxpool.Pool, cl *discogs.Client, limit int) (confirmed, anchored int) {
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
   -- ★itunes_drain 과 같은 목록으로 넓힌다(2026-09-15) — 둘이 다른 것을 고르면
   --   한쪽이 본 것을 다른 쪽이 못 보는 사각이 생긴다. 값은 안 바뀐다(글자 일치 확인).
   AND ARRAY[canonical_ja_source,canonical_zh_source,canonical_zh_hant_source] && $2::text[]
   AND COALESCE(notes,'') NOT LIKE '%[scope:review]%'
   AND NOT EXISTS(SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=kwave_entities.id
                  AND a.field='discogs' AND a.last_attempt_at > now() - interval '45 days')
 ORDER BY updated_at DESC
 LIMIT $1`, limit, MachineFilledSourcesWeakerThan(SourceDiscogs))
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

	// changedRows — 원장이 실제로 바뀐 **행** 수. confirmed 와 anchored 가 같은 행에서
	// 둘 다 일어날 수 있으므로 합치지 않고 행으로 센다.
	changedRows := 0
	for _, it := range items {
		if strings.TrimSpace(it.term) == "" {
			continue
		}
		rowChanged := false
		res, serr := cl.Search(ctx, it.term, "", 6)
		time.Sleep(2500 * time.Millisecond) // Discogs 예의(무토큰 25/min)
		if serr != nil || len(res) == 0 {
			// 쿨다운 기록 후 다음(재시도 폭주 방지)
			markDiscogsAttempt(ctx, pool, it.id)
			continue
		}
		// release 제목 정규화 집합 + 매칭 release 의 artist/id 추출용 맵.
		type rel struct {
			artist string
			id     int64
		}
		titleMap := map[string]rel{}
		for _, r := range res {
			a, t := discogs.SplitTitle(r.Title)
			n := itunesNormTitle(t)
			if n != "" {
				if _, ok := titleMap[n]; !ok {
					titleMap[n] = rel{artist: a, id: r.ID}
				}
			}
		}
		var anchorArtist string
		var anchorID int64
		cells := []struct{ loc, val, src string }{
			{"ja", it.ja, it.jaS}, {"zh", it.zh, it.zhS}, {"zh_hant", it.zht, it.zhtS},
		}
		for _, c := range cells {
			if !isWeakerThan(c.src, SourceDiscogs) || strings.TrimSpace(c.val) == "" {
				continue
			}
			m, ok := titleMap[itunesNormTitle(c.val)]
			if !ok {
				continue
			}
			col := "canonical_" + c.loc
			srcc := col + "_source"
			tag, _ := pool.Exec(ctx, `UPDATE kwave_entities SET `+srcc+`='discogs', updated_at=now()
			     WHERE id=$1 AND COALESCE(`+srcc+`,'') = ANY($2::text[])`, it.id, MachineFilledSourcesWeakerThan(SourceDiscogs))
			if tag.RowsAffected() > 0 {
				confirmed++
				rowChanged = true
			}
			if anchorArtist == "" && strings.TrimSpace(m.artist) != "" {
				anchorArtist = m.artist
				anchorID = m.id
			}
		}
		if anchorArtist != "" {
			tag, _ := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, raw_payload, fetched_at)
VALUES ($1,'discogs',$2,$3,0.7,$4,now())
ON CONFLICT DO NOTHING`, it.id, fmt.Sprintf("%d", anchorID),
				fmt.Sprintf("https://www.discogs.com/release/%d", anchorID),
				fmt.Sprintf(`{"artist":%q}`, anchorArtist))
			if tag.RowsAffected() > 0 {
				anchored++
				rowChanged = true
			}
		}
		markDiscogsAttempt(ctx, pool, it.id)
		if rowChanged {
			changedRows++
		}
	}
	// 레인 성과 원장(0151). scanned=뽑은 행 수, applied=원장이 바뀐 행 수.
	//
	// ★2026-09-21 39회차에 고쳤다. 배선할 때 둘 다 `confirmed+anchored` 를 넣었다 —
	//	 즉 **scanned 와 applied 가 항상 같았다.** 그러면 `scanned>0 AND applied=0` 이
	//	 영영 성립하지 않아 **이 레인만 신호에서 빠져 있었다.** 못 찾는 계측기는
	//	 거짓 경보보다 나쁘다. 거짓 경보는 시끄럽기라도 하지만 이건 조용하다.
	RecordCounts(ctx, pool, "discogs-songs", false, len(items), changedRows, map[string]int{
		"못 찾음": len(items) - changedRows,
	})
	return confirmed, anchored
}

func markDiscogsAttempt(ctx context.Context, pool *pgxpool.Pool, id string) {
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, attempts, last_attempt_at, last_source)
VALUES ($1,'discogs',1,now(),'discogs')
ON CONFLICT (entity_id, field) DO UPDATE SET attempts=kwave_kdb_enrich_attempts.attempts+1, last_attempt_at=now()`, id)
}
