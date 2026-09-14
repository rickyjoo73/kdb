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

	// ★세는 조건은 동기화가 **실제로 고칠 수 있는 범위**와 같아야 한다.
	//   처음엔 넓게 셌다가, 고칠 수 없는 행까지 세고 "왜 안 고쳤냐"로 실패했다.
	//   (canonical_en 이 라틴 원제꼴이 아니거나 MR 로마자면 드레인이 일부러 건너뛴다.)
	//   그래서 romanize.go 의 가드를 그대로 쓴다 — 사본을 두면 또 갈라진다.
	stale := func() int {
		var n int
		if err := pool.QueryRow(ctx, `
SELECT count(*) FROM (
  SELECT id, canonical_vi    v, canonical_vi_source    s, 'vi'    loc, canonical_en, canonical_en_source, operator_locked, status, entity_type FROM kwave_entities
  UNION ALL SELECT id, canonical_es,    canonical_es_source,    'es',    canonical_en, canonical_en_source, operator_locked, status, entity_type FROM kwave_entities
  UNION ALL SELECT id, canonical_id,    canonical_id_source,    'id',    canonical_en, canonical_en_source, operator_locked, status, entity_type FROM kwave_entities
  UNION ALL SELECT id, canonical_pt_br, canonical_pt_br_source, 'pt_br', canonical_en, canonical_en_source, operator_locked, status, entity_type FROM kwave_entities
) t
 WHERE status='active' AND NOT operator_locked
   AND entity_type NOT IN ('unknown','term')
   AND canonical_en <> '' AND canonical_en ~ $1`+latinPropagateSQLGuard+`
   AND COALESCE(canonical_en_source,'') NOT IN ('codex-fallback','')
   AND COALESCE(s,'')='romanization'
   AND COALESCE(v,'')<>'' AND v <> canonical_en
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_dataqa_log d
        WHERE d.entity_id = t.id AND d.locale = t.loc
          AND d.verdict='contaminated' AND d.reverted_at IS NULL)`, latinOriginPattern).Scan(&n); err != nil {
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
	// 고칠 수 있는 범위로만 셌으니 **끝나면 0 이어야 한다.**
	if after != 0 {
		t.Errorf("동기화 뒤에도 낡은 파생본이 %d칸 남았다 (전 %d칸)", after, before)
	}
	t.Logf("낡은 파생본 %d → %d", before, after)
}
