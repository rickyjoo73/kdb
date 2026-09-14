package kdb

import (
	"context"
	"testing"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 실측 결함 고정 (2026-09-14, presslocale 신고에서 추적).
//
//	사랑이 온다  en=`Love on the Menu`(tmdb)  vi=`Love Is Coming`(romanization)
//	강수지      en=`Kang Su-ji`             vi=`Kang Susie`
//
// DrainRomanizeLatin 이 대상을 "빈칸 또는 codex-fallback" 으로만 잡아서, 한 번 복사한
// 뒤 canonical_en 이 승급돼도 복사본이 낡은 채 남았다. 509칸이 그랬다.
// 소비자에게는 "언어마다 다른 라틴 표기"로 보였다.
//
// 불변식: **source='romanization' 인 칸은 canonical_en 과 같아야 한다.** 파생본이니까.
func TestRestoredRomanizeDerivedStaysInSyncWithEN(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	stale := func() int {
		var n int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT canonical_vi    v, canonical_vi_source    s, canonical_en, operator_locked, status, entity_type FROM kwave_entities
  UNION ALL SELECT canonical_es,    canonical_es_source,    canonical_en, operator_locked, status, entity_type FROM kwave_entities
  UNION ALL SELECT canonical_id,    canonical_id_source,    canonical_en, operator_locked, status, entity_type FROM kwave_entities
  UNION ALL SELECT canonical_pt_br, canonical_pt_br_source, canonical_en, operator_locked, status, entity_type FROM kwave_entities
) t
 WHERE status='active' AND NOT operator_locked
   AND entity_type NOT IN ('unknown','term')
   AND COALESCE(s,'')='romanization'
   AND COALESCE(v,'')<>'' AND COALESCE(canonical_en,'')<>''
   AND v <> canonical_en`).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	before := stale()
	DrainRomanizeLatin(ctx, pool)
	after := stale()

	if after > before {
		t.Fatalf("동기화가 어긋남을 늘렸다: %d → %d", before, after)
	}
	// 오염표시(dataqa)로 일부러 남겨 두는 칸이 있을 수 있으니 0 을 요구하지 않는다.
	// 다만 **줄어들지 않으면** 동기화가 아무 일도 안 한 것이다.
	if before > 0 && after == before {
		t.Errorf("낡은 파생본이 %d칸 있는데 동기화가 하나도 안 고쳤다", before)
	}
	t.Logf("낡은 파생본 %d → %d", before, after)
}
