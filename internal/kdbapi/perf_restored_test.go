package kdbapi

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 조회 성능 — 복원본(실데이터)에서 잰다. 합성 축약 스키마에서는 행이 없어 무의미하다.
//
// 상한은 운영 요청 타임아웃(기본 10s)에서 역산한다. 소비자가 실제로 두드리는 경로는
// 실측 상 lookup/bulk 47,803 · prepare 21,137 · match 17,517 · corrections 7,479 이고,
// 그중 match 는 2026-08-04 에 p99 8.6s 를 찍어 판별 예산을 남은 시간 기준으로 바꾼 전력이 있다.
// 여기서는 회귀 감지가 목적이라 여유 있는 상한을 걸고 실측값을 로그로 남긴다.
func TestRestoredQueryPerformance(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	store := &Store{Pool: pool}

	var entities int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities`).Scan(&entities); err != nil {
		t.Fatal(err)
	}
	t.Logf("복원본 엔티티 %d건 기준", entities)

	article := strings.Repeat("한국 대중문화 기사 본문입니다. 배우와 가수가 함께 등장하고 소속사와 작품 이름이 섞여 있습니다. ", 12)

	cases := []struct {
		name  string
		limit time.Duration
		run   func() error
	}{
		{"match(기사 본문 1,000자급)", 3 * time.Second, func() error {
			_, err := store.MatchEntitiesForLocale(ctx, MatchEntitiesRequest{SourceText: article, Locale: "ja", Limit: 20})
			return err
		}},
		{"match(문맥 없는 이름)", 2 * time.Second, func() error {
			_, err := store.MatchEntitiesForLocale(ctx, MatchEntitiesRequest{SourceText: "아이유", Locale: "ja", Limit: 20})
			return err
		}},
		{"entities 목록 + updated_since", 2 * time.Second, func() error {
			_, err := store.ListEntities(ctx, EntityFilter{Limit: 50, UpdatedSince: time.Now().Add(-24 * time.Hour)})
			return err
		}},
		{"entities 부분검색", 2 * time.Second, func() error {
			_, err := store.ListEntities(ctx, EntityFilter{Query: "김", Status: "active", Limit: 5})
			return err
		}},
	}
	for _, c := range cases {
		start := time.Now()
		if err := c.run(); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		took := time.Since(start)
		if took > c.limit {
			t.Errorf("%s: %s — 상한 %s 초과", c.name, took, c.limit)
		}
		t.Logf("%-32s %s", c.name, took)
	}
}

// TestRestoredHomonymQueryPerformance — 동명 후보 화면은 canonical_ko 로 전체를 묶는다.
// 인덱스가 없으면 엔티티가 늘수록 선형으로 느려지고, 운영자가 가장 자주 여는 화면이 된다.
func TestRestoredHomonymQueryPerformance(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()
	start := time.Now()
	var groups int
	err := pool.QueryRow(ctx, `SELECT count(*) FROM (
  SELECT canonical_ko, entity_type FROM kwave_entities WHERE status <> 'rejected'
   GROUP BY 1,2 HAVING count(*) > 1) g`).Scan(&groups)
	if err != nil {
		t.Fatal(err)
	}
	took := time.Since(start)
	if took > 2*time.Second {
		t.Errorf("동명 그룹 집계 %s — 상한 2s 초과", took)
	}
	t.Log(fmt.Sprintf("동명 그룹 %d개 집계 %s", groups, took))
}
