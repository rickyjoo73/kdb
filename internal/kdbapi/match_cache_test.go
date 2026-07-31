package kdbapi

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// 동시 요청 합류 — 실측상 재호출의 60%가 10초 이내(같은 본문이 53ms 간격으로 도착)라
// TTL 히트 이전에 겹치는 요청을 하나로 묶는 것이 이 캐시의 핵심 효과다.
func TestMatchCacheSingleFlight(t *testing.T) {
	c := newMatchCache(time.Minute)
	var calls int32
	release := make(chan struct{})

	const n = 20
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			ents, err, _ := c.do("k", func() ([]MatchedEntity, error) {
				atomic.AddInt32(&calls, 1)
				<-release // 모든 고루틴이 합류할 시간을 준다
				return []MatchedEntity{{KO: "아이유"}}, nil
			})
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if len(ents) != 1 || ents[0].KO != "아이유" {
				t.Errorf("bad result: %+v", ents)
			}
		}()
	}
	// 합류가 성립하려면 첫 요청이 계산에 진입해 있어야 한다.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("compute 가 %d회 실행됨 — single-flight 실패(1회여야 함)", got)
	}
}

func TestMatchCacheHitAndExpiry(t *testing.T) {
	c := newMatchCache(80 * time.Millisecond)
	var calls int32
	compute := func() ([]MatchedEntity, error) {
		atomic.AddInt32(&calls, 1)
		return []MatchedEntity{{KO: "BTS"}}, nil
	}

	if _, _, cached := c.do("k", compute); cached {
		t.Fatal("첫 호출은 미스여야 한다")
	}
	if _, _, cached := c.do("k", compute); !cached {
		t.Fatal("두 번째 호출은 캐시 히트여야 한다")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("compute %d회 — 캐시가 안 먹었다", got)
	}

	time.Sleep(120 * time.Millisecond) // TTL 경과
	if _, _, cached := c.do("k", compute); cached {
		t.Fatal("만료 후에는 다시 계산해야 한다")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("compute %d회 — 만료 후 재계산 실패", got)
	}
}

// 실패는 캐시하지 않는다 — 일시 오류를 TTL 동안 굳히면 소비자가 그 시간만큼 막힌다.
func TestMatchCacheDoesNotCacheErrors(t *testing.T) {
	c := newMatchCache(time.Minute)
	var calls int32
	boom := errors.New("query failed")

	for i := 0; i < 3; i++ {
		_, err, _ := c.do("k", func() ([]MatchedEntity, error) {
			atomic.AddInt32(&calls, 1)
			return nil, boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("에러가 전달돼야 한다: %v", err)
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("compute %d회 — 실패가 캐시됐다(3회여야 함)", got)
	}
}

// TTL=0 이면 캐시·합류를 완전히 우회한다(끌 수 있어야 한다).
func TestMatchCacheDisabled(t *testing.T) {
	c := newMatchCache(0)
	if c != nil {
		t.Fatal("TTL 0 이면 nil(비활성)이어야 한다")
	}
	var calls int32
	for i := 0; i < 3; i++ {
		_, _, cached := c.do("k", func() ([]MatchedEntity, error) {
			atomic.AddInt32(&calls, 1)
			return nil, nil
		})
		if cached {
			t.Fatal("비활성 캐시가 히트를 보고했다")
		}
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Fatalf("compute %d회 — 비활성인데 우회가 안 됐다", got)
	}
}

// 키가 본문뿐 아니라 결과에 영향을 주는 모든 파라미터를 반영하는지.
func TestMatchCacheKeyDistinguishesParams(t *testing.T) {
	base := MatchEntitiesRequest{SourceText: "본문", Locale: "ja", Limit: 5}
	seen := map[string]string{}
	variants := map[string]MatchEntitiesRequest{
		"base":       base,
		"locale":     {SourceText: "본문", Locale: "en", Limit: 5},
		"text":       {SourceText: "다른 본문", Locale: "ja", Limit: 5},
		"limit":      {SourceText: "본문", Locale: "ja", Limit: 9},
		"status":     {SourceText: "본문", Locale: "ja", Limit: 5, Status: "candidate"},
		"disambig":   {SourceText: "본문", Locale: "ja", Limit: 5, Disambiguate: true},
		"minconf":    {SourceText: "본문", Locale: "ja", Limit: 5, MinConfidence: 0.9},
		"verifiedon": {SourceText: "본문", Locale: "ja", Limit: 5, VerifiedOnly: true},
	}
	for name, req := range variants {
		k := matchCacheKey(req)
		if prev, dup := seen[k]; dup {
			t.Fatalf("%s 와 %s 의 캐시 키가 충돌 — 서로 다른 응답이 섞인다", name, prev)
		}
		seen[k] = name
	}
}
