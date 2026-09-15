package kdb

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/rickyjoo73/kdb/internal/testdb"
)

// ★합의는 "서로 다른 곳이 같은 말을 했다"는 뜻이다 (2026-09-15).
//
//	종전 질의는 매체 수만 셌다 — COUNT(DISTINCT source_domain). 검색이 `site:` 를
//	안 지켜 밖의 페이지를 주는데 우리가 요청한 도메인을 출처로 적은 탓에, 한 URL 이
//	여러 매체로 적히면 그 수만큼 셌다. 실측으로 최근 90일 합의 통과 560건 중
//	145건(26%)이 그렇게 만들어진 것이었다:
//
//	  이지혁  zh-hant "jh_lee_actor"  ← 인스타그램 아이디
//	  강수    zh-hant "降水"           ← 강수량을 다룬 위키백과
//	  쉿(Shhh) ja    "shhh!"          ← 스팀 상점 페이지
//
//	이 시험은 **한 페이지는 아무리 여러 이름으로 적혀도 한 곳**임을 고정한다.
func TestRestoredConsensusCountsPagesNotJustDomainLabels(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	s := &ObservationStore{Pool: pool}
	id := uuid.New()

	t.Cleanup(func() {
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_media_observations WHERE entity_id=$1`, id); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entity_resolution_attempts WHERE entity_id=$1`, id); err != nil {
			t.Error(err)
		}
		if _, err := pool.Exec(ctx, `DELETE FROM kwave_entities WHERE id=$1`, id); err != nil {
			t.Error(err)
		}
	})
	if _, err := pool.Exec(ctx, `
INSERT INTO kwave_entities(id, canonical_ko, entity_type, status)
VALUES ($1, '합의시험대상', 'person', 'candidate')`, id); err != nil {
		t.Fatal(err)
	}

	obs := func(domain, url string) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
INSERT INTO kwave_media_observations
  (entity_id, locale, spelling, spelling_normalized, source_domain, source_url, observed_at, confidence)
VALUES ($1, 'ja', 'ゴウイシケン', 'ごういしけん', $2, $3, now(), 0.9)`, id, domain, url); err != nil {
			t.Fatal(err)
		}
	}

	// ① 같은 페이지 하나가 매체 셋으로 적혔다 — 합의가 아니다.
	obs("koari.net", "https://www.youtube.com/watch?v=x")
	obs("daebak.tokyo", "https://www.youtube.com/watch?v=x")
	obs("danmee.jp", "https://www.youtube.com/watch?v=x")
	if _, ok, err := s.EvaluateConsensus(ctx, id, "ja"); err != nil || ok {
		t.Fatalf("한 페이지가 합의로 승격됐다 (ok=%v err=%v)", ok, err)
	}

	// ② 서로 다른 페이지가 붙으면 그때가 합의다.
	obs("channelk.jp", "https://channelk.jp/news/1")
	if _, ok, err := s.EvaluateConsensus(ctx, id, "ja"); err != nil || !ok {
		t.Fatalf("서로 다른 페이지인데 합의가 안 났다 (ok=%v err=%v)", ok, err)
	}
}
