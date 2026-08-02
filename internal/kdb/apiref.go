package kdb

// apiref — 권위 API 식별자를 external_refs 에 남기는 단일 창구.
//
// ★왜 함수 하나로 모으나(2026-08-03). 종전엔 ref 적재 SQL 이 최소 4곳에 손으로
// 복제돼 있었고, 그 결과 각자 다르게 틀렸다:
//   - enrich/orchestrator runMusicBrainz : 아예 안 남김            (484건)
//   - enrich/orchestrator runVideoAPIs   : kofic 만 안 남김        ( 85건)
//   - agents/enricher musicbrainzAliases : 아예 안 남김
//   - kofic_drain                        : 남기되 external_id 에 **영어 제목**(30건)
//
// 앞의 셋은 불변식 api-source-no-ref 가 잡았지만 네 번째는 못 잡았다 — 행이 있으니
// 통과했다. 식별자 자리에 식별자가 아닌 걸 넣는 실수는 "안 넣는" 실수보다 나쁘다.
// 그래서 여기서 external_id 를 강제 검사하고, 비면 아무것도 쓰지 않는다.

import (
	"context"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RecordAPIRef — provider 의 식별자를 적재한다. entityID 는 uuid.UUID 또는 uuid 문자열.
// externalID 가 비면 no-op(false) — 장식 ref 금지.
//
// ★이미 다른 식별자가 적혀 있으면 **덮지 않고 경고만 남긴다.** provider 당 writer 가
// 여러 개고 엄격도가 다르기 때문이다 — 예로 musicbrainz 는 DrainMusicBrainzCandidates
// (SearchGroups + 타입/스코어 게이트)가 FindAliases(이름 정규화 일치)보다 엄격하다.
// 뒤에 온 느슨한 판정이 앞의 엄격한 판정을 조용히 갈아치우면, 앵커가 바뀐 사실 자체가
// 기록에 안 남는다. 이건 이 커밋이 고치고 있는 결함과 정확히 같은 모양이다.
// 불일치는 해소 대상이 아니라 **드러낼 대상**이다.
func RecordAPIRef(ctx context.Context, pool *pgxpool.Pool, entityID any, provider, externalID, url string, confidence float64) bool {
	if pool == nil {
		return false
	}
	provider = strings.TrimSpace(provider)
	externalID = strings.TrimSpace(externalID)
	if provider == "" || externalID == "" {
		return false
	}
	// DO UPDATE 는 external_id 를 건드리지 않는다 — 기존 행이 있으면 그 값이 그대로
	// RETURNING 된다. 우리가 넣으려던 값과 다르면 그게 곧 불일치 신호다.
	var stored string
	err := pool.QueryRow(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1::uuid, $2, $3, NULLIF($4,''), $5, now())
ON CONFLICT (entity_id, provider) DO UPDATE
   SET url        = COALESCE(NULLIF(EXCLUDED.url,''), kwave_entity_external_refs.url),
       fetched_at = now()
RETURNING external_id`, entityID, provider, externalID, url, confidence).Scan(&stored)
	if err != nil {
		log.Printf("kdb.apiref: %s ref 적재 실패 entity=%v: %v", provider, entityID, err)
		return false
	}
	if stored != externalID {
		log.Printf("kdb.apiref: ⚠ %s 앵커 불일치 entity=%v 기존=%s 신규=%s — 기존 유지(덮지 않음)",
			provider, entityID, stored, externalID)
		return false
	}
	return true
}

// ReplaceAPIRef — 기존 식별자를 **의도적으로** 교체한다. 장식 ref 교정처럼
// "기존 값이 식별자가 아님"이 확정된 경우에만 쓴다. 무엇을 무엇으로 바꿨는지
// 반드시 로그에 남는다 — 앵커 교체는 조용히 일어나면 안 되는 사건이다.
func ReplaceAPIRef(ctx context.Context, pool *pgxpool.Pool, entityID any, provider, externalID, url string, confidence float64, why string) bool {
	if pool == nil {
		return false
	}
	provider = strings.TrimSpace(provider)
	externalID = strings.TrimSpace(externalID)
	if provider == "" || externalID == "" {
		return false
	}
	var prev string
	err := pool.QueryRow(ctx, `
SELECT COALESCE(external_id,'') FROM kwave_entity_external_refs
 WHERE entity_id=$1::uuid AND provider=$2`, entityID, provider).Scan(&prev)
	if err != nil {
		prev = "(없음)"
	}
	_, err = pool.Exec(ctx, `
INSERT INTO kwave_entity_external_refs (entity_id, provider, external_id, url, confidence, fetched_at)
VALUES ($1::uuid, $2, $3, NULLIF($4,''), $5, now())
ON CONFLICT (entity_id, provider) DO UPDATE
   SET external_id = EXCLUDED.external_id,
       url         = COALESCE(NULLIF(EXCLUDED.url,''), kwave_entity_external_refs.url),
       fetched_at  = now()`, entityID, provider, externalID, url, confidence)
	if err != nil {
		log.Printf("kdb.apiref: %s ref 교체 실패 entity=%v: %v", provider, entityID, err)
		return false
	}
	if prev != externalID {
		log.Printf("kdb.apiref: %s 앵커 교체 entity=%v %s → %s (%s)", provider, entityID, prev, externalID, why)
	}
	return true
}
