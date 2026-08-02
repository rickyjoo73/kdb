package kdb

// apiref — 권위 API 식별자를 external_refs 에 남기는 단일 창구.
//
// ★왜 함수 하나로 모으나(2026-08-03). 종전엔 ref 적재 SQL 이 최소 4곳에 손으로
// 복제돼 있었고, 그 결과 각자 다르게 틀렸다:
//   - enrich/orchestrator runMusicBrainz : 아예 안 남김            (484건)
//   - enrich/orchestrator runVideoAPIs   : kofic 만 안 남김        ( 85건)
//   - agents/enricher musicbrainzAliases : 아예 안 남김
//   - kofic_drain                        : 남기되 external_id 에 **영어 제목**(30건)
// 앞의 셋은 불변식 api-source-no-ref 가 잡았지만 네 번째는 못 잡았다 — 행이 있으니
// 통과했다. 식별자 자리에 식별자가 아닌 걸 넣는 실수는 "안 넣는" 실수보다 나쁘다.
// 그래서 여기서 external_id 를 강제 검사하고, 비면 아무것도 쓰지 않는다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RecordAPIRef — provider 의 식별자를 external_refs 에 적재한다. entityID 는
// uuid.UUID 또는 uuid 문자열. externalID 가 비면 no-op(false) — 장식 ref 금지.
// 이미 있으면 식별자·URL 을 갱신한다(재확인이 최신 사실이다).
func RecordAPIRef(ctx context.Context, pool *pgxpool.Pool, entityID any, provider, externalID, url string, confidence float64) bool {
	if pool == nil {
		return false
	}
	provider = strings.TrimSpace(provider)
	externalID = strings.TrimSpace(externalID)
	if provider == "" || externalID == "" {
		return false
	}
	_, err := pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1::uuid, $2, $3, NULLIF($4,''), $5, now())
ON CONFLICT (entity_id, provider) DO UPDATE
   SET external_id = EXCLUDED.external_id,
       url         = COALESCE(EXCLUDED.url, kwave_entity_external_refs.url),
       fetched_at  = now()`, entityID, provider, externalID, url, confidence)
	if err != nil {
		log.Printf("kdb.apiref: %s ref 적재 실패 entity=%v: %v", provider, entityID, err)
		return false
	}
	return true
}
