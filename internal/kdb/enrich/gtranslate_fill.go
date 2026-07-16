package enrich

// gtranslate_fill.go — 구글 번역 폴백 백필 배치(오너 방침 2026-07-16: "공식소스가 못
// 채우면 기계번역이라도 채워 서빙"). Enrich L5 와 동일 가드(FillEN 캐시/월한도 +
// applyEmptyOnly 문자셋·suppress·빈칸-only)를 공유하는 one-shot — cmd/kdb
// `translate-fill [N]`. 기존 active 엔티티의 canonical_en 빈칸을 일괄 채운다.

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
)

// loadGTranslateContext — 맥락 문장 생성용: 엔티티에 대해 이미 아는 정보(동명 구분·
// 역할·소속·그룹·대표작)를 로드한다. 실패해도 nil 이 아닌 빈 컨텍스트(EntityID 만) 반환
// — FillEN 이 엔티티 단위 캐시 키를 항상 갖게 한다.
func loadGTranslateContext(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) *kdb.GTranslateEntityContext {
	g := &kdb.GTranslateEntityContext{EntityID: id.String()}
	_ = pool.QueryRow(ctx, `
SELECT COALESCE(e.disambig,''), COALESCE(e.category_hint,''),
       COALESCE(d.primary_role::text,''), COALESCE(d.agency,''),
       COALESCE(d.groups,'{}'), COALESCE(d.notable_works,'{}')
FROM kwave_entities e
LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
WHERE e.id = $1`, id).Scan(&g.Disambig, &g.CategoryHint, &g.Role, &g.Agency, &g.Groups, &g.NotableWorks)
	return g
}

// TranslateFillBacklog — canonical_en 빈칸 active 엔티티를 구글 번역으로 채움.
// 반환: 채움/스킵 건수. 스킵 = 번역실패(캐시된 실패 포함)·가드 기각·비대상 타입.
func (o *Orchestrator) TranslateFillBacklog(ctx context.Context, limit int) (filled, skipped int) {
	g := kdb.NewGTranslator(o.Pool)
	if g == nil {
		log.Printf("kdb.translate-fill: KDB_GTRANSLATE_KEY 미설정 — 중단")
		return 0, 0
	}
	rows, err := o.Pool.Query(ctx, `
SELECT id::text FROM kwave_entities
 WHERE status='active' AND operator_locked = false
   AND COALESCE(canonical_en,'') = '' AND canonical_ko ~ '[가-힣]'
   AND entity_type NOT IN ('unknown','term')
 ORDER BY updated_at DESC
 LIMIT $1`, limit)
	if err != nil {
		log.Printf("kdb.translate-fill: query: %v", err)
		return 0, 0
	}
	var ids []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			ids = append(ids, s)
		}
	}
	rows.Close()

	for i, idStr := range ids {
		uid, err := uuid.Parse(idStr)
		if err != nil {
			skipped++
			continue
		}
		snap, err := loadSnapshot(ctx, o.Pool, uid)
		if err != nil || snap == nil || snap.Values["en"] != "" {
			skipped++
			continue
		}
		tr, fresh, err := g.FillEN(ctx, snap.Ko, snap.EntityType, loadGTranslateContext(ctx, o.Pool, uid))
		if err != nil {
			log.Printf("kdb.translate-fill: ko=%q: %v", snap.Ko, err) // 일시 장애 — 다음 회차 재시도
			skipped++
			continue
		}
		if tr == "" {
			skipped++
		} else if applied, _ := o.applyEmptyOnly(ctx, snap, map[string][]string{"en": {tr}}, kdb.SourceGTranslate); len(applied) > 0 {
			filled++
		} else {
			skipped++ // 문자셋/suppress 가드 기각 또는 동시 채움
		}
		if fresh {
			time.Sleep(120 * time.Millisecond) // 외부 API 예의(레이트 완충)
		}
		if (i+1)%50 == 0 {
			log.Printf("kdb.translate-fill: %d/%d 진행 (filled=%d)", i+1, len(ids), filled)
		}
	}
	log.Printf("kdb.translate-fill: 완료 total=%d filled=%d skipped=%d 당월사용=%d자",
		len(ids), filled, skipped, g.GTranslateMonthUsage(ctx))
	return filled, skipped
}
