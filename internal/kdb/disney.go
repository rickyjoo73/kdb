package kdb

// Disney+ 현지제목 그라운딩 — 넷플릭스 OTT(ott.go)의 자매 provider. Disney+ 지역 페이지에서
// 작품(drama/show/movie)의 공식 현지제목을 확보한다. 공통 드레인(drainOTT)을 재사용하고
// Disney 고유의 URL·지역코드·ID 해석만 정의한다.
//
// ★구조(넷플릭스와 동형, 실측 검증): site:disneyplus.com {ko} → /browse/entity-{uuid}(지역 공통 ID).
// site:disneyplus.com/{ja-jp|es-es|zh-tw}/browse/{entity-id} → 그 지역 스니펫에 현지제목 노출
// (예: 무빙 ja-jp="ムービングを配信で見る | Disney+" → gemma 추출 "ムービング").
// es 는 영어로 서빙되는 경우 많음(Moving) → non-ASCII 가드가 거부(넷플릭스와 동일).
//
// ★IP 차단 방지: 매 조회 사이 disneyInterval(기본 10초) pacing. 절대 벌크 금지.
// source='disney'(prio 4, 권위 distributor). vi 는 Disney+ 미서비스 지역이라 제외.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// disneyLocalePath — KDB locale → Disney+ URL 지역코드(lang-COUNTRY). non-ASCII 가드가
// 영어leak 을 거를 수 있는 ja/es/zh_hant 만(넷플릭스와 동일 근거). vi 는 Disney+ 베트남
// 미서비스, pt_br/id 는 ASCII 언어라 영어 구분 불가 → 제외.
var disneyLocalePath = map[string]string{
	"ja": "ja-jp", "es": "es-es", "zh_hant": "zh-tw",
}

// disneyEntityRe — Disney+ 콘텐츠 URL 의 entity ID(/browse/entity-{uuid}). page-/search 는 제외.
var disneyEntityRe = regexp.MustCompile(`/browse/(entity-[0-9a-fA-F-]+)`)

// disneyInterval — Disney+ 조회 간 최소 간격(IP 차단 방지). 기본 10초.
func disneyInterval() time.Duration {
	if v := os.Getenv("KDB_DISNEY_MIN_INTERVAL_MS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return time.Duration(n) * time.Millisecond
		}
	}
	return 10 * time.Second
}

// resolveDisneyID — site:disneyplus.com {ko} → entity ID. ★오매칭 차단: 스니펫 제목이 ko 를
// 포함하는 entity 만 채택(Disney 검색은 page-/타작품 노이즈가 많아 넷플릭스보다 엄격히 앵커).
func resolveDisneyID(ctx context.Context, ko string) (string, bool) {
	res := searxngDefault(ctx, "site:disneyplus.com "+ko)
	koNorm := strings.ReplaceAll(strings.TrimSpace(ko), " ", "")
	for _, r := range res {
		m := disneyEntityRe.FindStringSubmatch(r.URL)
		if m == nil {
			continue
		}
		if strings.Contains(strings.ReplaceAll(r.Title, " ", ""), koNorm) {
			return m[1], true // 제목 앵커 통과 = 올바른 작품
		}
	}
	return "", false // ko 제목 매칭 entity 없음 → 빈칸 유지(fabrication 안 함)
}

// disneyProvider — Disney+. site:disneyplus.com/{지역}/browse/entity-{uuid}.
var disneyProvider = ottProvider{
	name:          "disney",
	brand:         "Disney",
	cooldownField: "disneyfill",
	localePath:    disneyLocalePath,
	interval:      disneyInterval,
	resolveID:     resolveDisneyID,
	localeQuery: func(loc, id string) (string, string) {
		path := disneyLocalePath[loc]
		return fmt.Sprintf("site:disneyplus.com/%s/browse/%s", path, id),
			fmt.Sprintf("/%s/browse/%s", path, id)
	},
}

// DrainDisneyWorks — Disney+ 지역 공식제목으로 작품 locale 채움(drainOTT 래퍼).
func DrainDisneyWorks(ctx context.Context, pool *pgxpool.Pool, n int, koFilter string) (processed, filled int) {
	return drainOTT(ctx, pool, disneyProvider, n, koFilter)
}
