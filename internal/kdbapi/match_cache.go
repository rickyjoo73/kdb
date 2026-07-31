package kdbapi

// match_cache — /v1/entities/match 응답의 TTL 캐시 + single-flight 합류.
//
// ★근거(2026-07-31 실측, 최근 7일 /v1/entities/match):
//   - 총 1,454콜 / 고유 쿼리 361건 → **중복 1,093건(75.2%)**
//   - 재호출 간격: **<10초 657건(60%)** · <1분 156 · <10분 212 · 1시간+ 68
//   - 지연: 평균 1,718ms · 최대 8,967ms (0~2초 구간에 1,133콜 집중, 8초대 꼬리 26콜)
//
// 소비자가 같은 기사 본문을 반복 전송한다(상위 1건은 79회). 특히 <10초 재호출 657건은
// 동시 요청이 겹친 것이라(실측: 같은 본문이 53ms 간격으로 2건) TTL 캐시만으로는 둘 다
// 미스가 나 이중 계산된다 → single-flight 로 뒤따라온 요청을 앞선 계산에 합류시킨다.
// 합류는 같은 결과를 나눠 갖는 것이라 **stale 위험이 0**이다.
//
// TTL 은 기본 5분. match 결과의 재료(엔티티 다국어 표기)는 분 단위로 바뀌지 않고,
// 바뀌더라도 "추측=빈칸" 원칙상 늦게 반영되는 값은 빈칸이지 틀린 값이 아니다.
// KDB_MATCH_CACHE_TTL_SECONDS=0 으로 끄면 캐시·합류 모두 우회한다.

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"time"
)

// matchCacheMaxEntries — 상한. 고유 쿼리가 7일에 361건이라 넉넉하다. 초과 시 만료분을
// 먼저 비우고, 그래도 넘치면 전체를 비운다(LRU 를 둘 만큼 규모가 크지 않다).
const matchCacheMaxEntries = 2048

type matchCacheEntry struct {
	ready   chan struct{} // 계산 완료 시 close — 합류자는 이걸 기다린다
	ents    []MatchedEntity
	err     error
	expires time.Time
}

type matchCache struct {
	mu  sync.Mutex
	ttl time.Duration
	m   map[string]*matchCacheEntry
}

func newMatchCache(ttl time.Duration) *matchCache {
	if ttl <= 0 {
		return nil // 비활성
	}
	return &matchCache{ttl: ttl, m: make(map[string]*matchCacheEntry)}
}

// matchCacheKey — 결과에 영향을 주는 모든 입력을 담는다. 본문은 길어서 해시한다.
func matchCacheKey(req MatchEntitiesRequest) string {
	sum := sha256.Sum256([]byte(req.SourceText))
	var b strings.Builder
	b.WriteString(hex.EncodeToString(sum[:]))
	b.WriteByte('|')
	b.WriteString(req.Locale)
	b.WriteByte('|')
	b.WriteString(strings.TrimSpace(req.Status))
	b.WriteByte('|')
	b.WriteString(strconv.Itoa(req.Limit))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(req.Disambiguate))
	b.WriteByte('|')
	b.WriteString(strconv.FormatFloat(req.MinConfidence, 'f', 3, 64))
	b.WriteByte('|')
	b.WriteString(strconv.FormatBool(req.VerifiedOnly))
	return b.String()
}

// do — 캐시 히트면 그대로, 미스면 compute 를 1회만 실행하고 동시 요청은 합류시킨다.
// 반환의 두 번째 값은 캐시/합류로 계산을 건너뛰었는지(계측용).
func (c *matchCache) do(key string, compute func() ([]MatchedEntity, error)) ([]MatchedEntity, error, bool) {
	if c == nil {
		ents, err := compute()
		return ents, err, false
	}
	now := time.Now()

	c.mu.Lock()
	if e, ok := c.m[key]; ok {
		select {
		case <-e.ready:
			// 계산 완료분 — 만료 전이면 그대로 쓴다.
			if now.Before(e.expires) {
				c.mu.Unlock()
				return e.ents, e.err, true
			}
			delete(c.m, key) // 만료 → 아래에서 새로 계산
		default:
			// 계산 진행중 — 합류한다(stale 위험 0).
			c.mu.Unlock()
			<-e.ready
			return e.ents, e.err, true
		}
	}
	if len(c.m) >= matchCacheMaxEntries {
		c.evictLocked(now)
	}
	e := &matchCacheEntry{ready: make(chan struct{})}
	c.m[key] = e
	c.mu.Unlock()

	e.ents, e.err = compute()
	e.expires = time.Now().Add(c.ttl)
	close(e.ready)

	// 실패는 캐시하지 않는다 — 일시 오류를 TTL 동안 굳히지 않기 위해.
	if e.err != nil {
		c.mu.Lock()
		if cur, ok := c.m[key]; ok && cur == e {
			delete(c.m, key)
		}
		c.mu.Unlock()
	}
	return e.ents, e.err, false
}

// evictLocked — 만료분 제거. 그래도 상한이면 전체 비움. 호출자가 mu 를 쥐고 있어야 한다.
func (c *matchCache) evictLocked(now time.Time) {
	for k, e := range c.m {
		select {
		case <-e.ready:
			if !now.Before(e.expires) {
				delete(c.m, k)
			}
		default:
			// 진행중 항목은 합류자가 기다리고 있을 수 있으므로 건드리지 않는다.
		}
	}
	if len(c.m) >= matchCacheMaxEntries {
		for k, e := range c.m {
			select {
			case <-e.ready:
				delete(c.m, k)
			default:
			}
		}
	}
}
