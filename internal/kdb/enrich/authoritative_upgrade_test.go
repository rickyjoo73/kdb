package enrich

import (
	"context"
	"strings"
	"testing"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 기계값 목록이 **우선순위표와 어긋나면 안 된다.**
//
// 이 목록의 뜻은 "권위값이 오면 양보해야 하는 칸"이다. 우선순위표에서 wikidata-label(5)
// 보다 낮은(=숫자가 큰) 등급만 여기 있어야 한다. 누군가 표의 등급을 올리고 이 목록을
// 안 고치면, 드레인이 **권위값을 기계값으로 착각해 덮으러 간다**.
func TestMachineSourcesAreAllWeakerThanWikidata(t *testing.T) {
	wd := kdb.Priority(kdb.SourceWikidataLabel)
	for _, name := range machineFilledSources() {
		p := kdb.Priority(kdb.Source(name))
		if p <= wd {
			t.Fatalf("%s 의 등급이 %d 로 wikidata-label(%d) 이상이다 — 기계값 목록에서 빼야 한다", name, p, wd)
		}
	}
	if len(machineFilledSources()) == 0 {
		t.Fatal("기계값 목록이 비었다 — 드레인이 아무것도 안 고른다")
	}
}

// 실측으로 확인한 주력 기계 출처가 목록에 **반드시** 들어 있어야 한다.
//
// ★2026-09-15 실측. 서빙 칸의 출처 분포는 romanization 24.4% · gtranslate 22.0% ·
// opencc 5.5% · codex-fallback 5.4% 였다. 그런데 두 드레인은 codex-fallback 하나만
// 보고 있었다 — 앵커 보유 4,536건 중 524건(11.5%)만 사정거리에 있었다.
func TestTheBigMachineSourcesAreCovered(t *testing.T) {
	have := map[string]bool{}
	for _, s := range machineFilledSources() {
		have[s] = true
	}
	for _, want := range []kdb.Source{
		kdb.SourceRomanization, kdb.SourceGTranslate,
		kdb.SourceOpenCC, kdb.SourceCodexFallback, kdb.SourceKanaRule,
	} {
		if !have[string(want)] {
			t.Fatalf("%s 가 기계값 목록에 없다 — 이 출처로 채워진 칸은 권위값이 있어도 영영 안 바뀐다", want)
		}
	}
}

// 두 드레인이 **같은 목록**을 봐야 한다. 서로 다르면 한쪽이 본 것을 다른 쪽이 못 본다.
func TestBothDrainsSelectOnTheSameSourceColumns(t *testing.T) {
	for _, col := range []string{
		"canonical_en_source", "canonical_ja_source", "canonical_vi_source",
		"canonical_id_source", "canonical_es_source", "canonical_pt_br_source",
		"canonical_zh_source", "canonical_zh_hant_source",
	} {
		if !strings.Contains(localeSourceCols, col) {
			t.Fatalf("localeSourceCols 에 %s 가 없다", col)
		}
		if !strings.Contains(machineCellCount, col) {
			t.Fatalf("machineCellCount 에 %s 가 없다 — 정렬이 그 칸을 못 센다", col)
		}
	}
}

// 두 드레인의 질의가 **실제 스키마에서 돈다.** 문법이 깨지면 Query 가 err 를 돌려주는데
// 두 함수 모두 err 일 때 조용히 (0,0) 을 돌려준다 — 안 도는 드레인이 "할 일 없음"과
// 구별이 안 된다. 이 저장소가 `|| true` 로 이미 한 번 데인 계열이다.
func TestRestoredUpgradeDrainQueriesActuallyRun(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	o := New(pool)

	// 넓힌 조건이 codex 만 보던 때보다 **더 많이** 골라야 한다. 같거나 적으면 넓히지 못한 것이다.
	var narrow, wide int
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities e
 WHERE e.status='active'
   AND EXISTS(SELECT 1 FROM kwave_entity_external_refs x
              WHERE x.entity_id=e.id AND x.provider='wikidata' AND x.external_id<>'')
   AND 'codex-fallback' = ANY(`+localeSourceCols+`)`).Scan(&narrow); err != nil {
		t.Fatalf("종전 조건이 안 돈다: %v", err)
	}
	if err := pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entities e
 WHERE e.status='active'
   AND EXISTS(SELECT 1 FROM kwave_entity_external_refs x
              WHERE x.entity_id=e.id AND x.provider='wikidata' AND x.external_id<>'')
   AND `+localeSourceCols+` && $1::text[]`, machineFilledSources()).Scan(&wide); err != nil {
		t.Fatalf("넓힌 조건이 안 돈다: %v", err)
	}
	t.Logf("앵커 보유 업그레이드 대상: 종전(codex 만) %d → 넓힌 뒤 %d", narrow, wide)
	if wide < narrow {
		t.Fatalf("넓혔는데 대상이 줄었다 (%d < %d)", wide, narrow)
	}

	// 정렬식이 도는지: 기계값 칸 수가 0~8 범위여야 한다.
	var lo, hi int
	if err := pool.QueryRow(ctx, `
SELECT COALESCE(min(c),0), COALESCE(max(c),0) FROM (
  SELECT `+machineCellCount+` AS c FROM kwave_entities e WHERE e.status='active' LIMIT 500) z`,
		machineFilledSources()).Scan(&lo, &hi); err != nil {
		t.Fatalf("정렬식이 안 돈다 — 이게 깨지면 드레인이 통째로 조용히 죽는다: %v", err)
	}
	if lo < 0 || hi > 8 {
		t.Fatalf("기계값 칸 수가 범위를 벗어났다: %d~%d", lo, hi)
	}

	// 드레인 본체를 n=0 으로 불러 조기반환 경로가 살아 있는지 확인(외부호출 없음).
	if p, u := o.DrainAnchoredRefill(ctx, 0); p != 0 || u != 0 {
		t.Fatalf("n=0 인데 일했다: processed=%d upgraded=%d", p, u)
	}
	if p, u := o.DrainLanglinkUpgrade(ctx, 0); p != 0 || u != 0 {
		t.Fatalf("n=0 인데 일했다: processed=%d upgraded=%d", p, u)
	}
}
