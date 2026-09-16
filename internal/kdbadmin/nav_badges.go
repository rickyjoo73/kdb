package kdbadmin

// nav_badges — 메뉴 옆 숫자. **어느 화면에 일이 있는지 한눈에 보이게 한다.**
//
// ★왜 이것이 "메뉴 정리"인가 (2026-09-16).
//
//	메뉴가 30개인데 어느 것을 봐야 하는지 알 길이 없었다. 줄이자는 이야기가
//	먼저 나왔지만, 이 저장소는 이미 그 실수를 했다 — 공통 원장 7화면이 **링크가
//	하나도 없어서** URL 을 아는 사람만 들어갔고, 흡수분 537,841건이 화면에서
//	통째로 안 보였다(router.go 주석). 숨기면 그 일이 없어지는 게 아니라 안 보일 뿐이다.
//
//	그래서 숨기지 않고 **비어 있음을 보이게** 한다. "동일인 판정 대기열 0" 이라고
//	적혀 있으면 운영자는 누르지 않는다. 그게 30개를 18개로 줄이는 것보다 정확하다.
//
// ★그리고 NavItem.BadgeCount 는 **이미 있었다.** 템플릿도 그 값을 그릴 줄 안다
//	(partials.html 이 10 이상이면 빨강, 미만이면 주황). 아무도 채우지 않았을 뿐이다.
//	오늘만 다섯 번째다 — TTL 시계 · 앵커 표 · 관리 화면 유형 목록 · catchall-retype ·
//	그리고 이것.
//
// ★숫자는 **그 화면이 세는 것과 같은 조건**이어야 한다. 배지와 화면이 다르면
//	배지가 없느니만 못하다 — 눌러 보고 다르면 그 뒤로 아무도 안 믿는다.
//	아래 각 항목은 어느 핸들러를 베꼈는지 적어 둔다.
//
// ★나브는 **모든 관리 화면에서** 그려지므로 비싸면 안 된다. 한 번의 쿼리로
//	전부 세고 60초 캐시한다. 캐시가 비어도 화면은 그대로 뜬다(0 은 "없음"이
//	아니라 "아직 안 셌음"이므로 배지를 안 그린다 — 템플릿이 >0 만 그린다).

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// navBadgeTTL — 나브가 매 페이지마다 그려지므로 짧게 캐시한다.
const navBadgeTTL = 60 * time.Second

var (
	navBadgeMu   sync.Mutex
	navBadgeAt   time.Time
	navBadgeVals map[string]int
)

// navBadgeCounts — 메뉴 경로 → 대기 건수. 못 세면 빈 맵을 돌려준다.
//
// **못 센 것을 0 으로 적지 않는다.** 0 은 "일이 없다"는 뜻이고 빈 맵은 "모른다"는
// 뜻인데, 템플릿이 >0 만 그리므로 모르면 배지가 안 뜬다. 이 저장소는 조용한 0 에
// 여러 번 데였다.
func navBadgeCounts(ctx context.Context, pool *pgxpool.Pool) map[string]int {
	if pool == nil {
		return nil
	}
	// ★실패도 캐시한다 (2026-09-16). 안 그러면 DB 가 느릴 때 **모든 관리 화면이**
	//   매번 3초를 기다린다 — 배지 하나 때문에 콘솔 전체가 느려지는 것은
	//   맞바꿀 만한 거래가 아니다. navBadgeAt 은 성공·실패 모두에 찍는다.
	navBadgeMu.Lock()
	if !navBadgeAt.IsZero() && time.Since(navBadgeAt) < navBadgeTTL {
		v := navBadgeVals
		navBadgeMu.Unlock()
		return v // 실패였으면 nil — "모른다"가 그대로 전달된다
	}
	navBadgeMu.Unlock()

	qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	var inbox, queue, conflicts, anchors, corrections, tierUnknown, localeGaps int
	// 한 번에 센다. 각 줄은 해당 화면의 조건을 그대로 베낀 것이다.
	err := pool.QueryRow(qctx, `
SELECT
  -- 신규 후보(Inbox): handlers_inbox.go — status='candidate'
  (SELECT count(*) FROM kwave_entities WHERE status='candidate'),
  -- 발굴 큐: handlers_ondemand.go — 큐 status='pending'
  (SELECT count(*) FROM kwave_entity_research_queue WHERE status='pending'),
  -- 충돌·동명이인: handlers_entities.go — 활성 canonical_ko 중복(의도적 분리는 제외)
  (SELECT count(*) FROM (
     SELECT canonical_ko FROM kwave_entities
      WHERE status='active' AND COALESCE(disambig,'')=''
      GROUP BY canonical_ko HAVING count(*) > 1) d),
  -- 앵커 검수: handlers_anchor_review.go — 활성 행에 대해 판정이 남아 있는 것
  (SELECT count(*) FROM kwave_kdb_anchor_audit a
    WHERE COALESCE(a.verdict,'') <> ''
      AND EXISTS (SELECT 1 FROM kwave_entities e WHERE e.id=a.entity_id AND e.status='active')),
  -- 교정요청 심사: corrections.ListPending — status IN ('pending','proposed')
  (SELECT count(*) FROM kwave_kdb_corrections WHERE status IN ('pending','proposed')),
  -- 검증 tier: handlers_quality.go — 등급이 안 매겨진 활성
  (SELECT count(*) FROM kwave_entities
    WHERE status='active' AND (verification_tier IS NULL OR verification_tier='')),
  -- 언어별 누락: 활성인데 주요 locale 에 빈칸이 있는 것
  (SELECT count(*) FROM kwave_entities
    WHERE status='active' AND (
      COALESCE(canonical_en,'')='' OR COALESCE(canonical_ja,'')='' OR
      COALESCE(canonical_zh,'')='' OR COALESCE(canonical_vi,'')=''))`).
		Scan(&inbox, &queue, &conflicts, &anchors, &corrections, &tierUnknown, &localeGaps)
	if err != nil {
		navBadgeMu.Lock()
		navBadgeVals, navBadgeAt = nil, time.Now() // 다음 60초는 다시 안 묻는다
		navBadgeMu.Unlock()
		return nil // 못 셌다. 0 으로 적지 않는다.
	}

	out := map[string]int{
		"/admin/kdb/inbox":            inbox,
		"/admin/ondemand/queue":       queue,
		"/admin/entities/conflicts":   conflicts,
		"/admin/entities/anchors":     anchors,
		"/admin/corrections":          corrections,
		"/admin/quality/verification": tierUnknown,
		"/admin/entities/locale-gaps": localeGaps,
	}
	navBadgeMu.Lock()
	navBadgeVals, navBadgeAt = out, time.Now()
	navBadgeMu.Unlock()
	return out
}

// applyNavBadges — 센 것을 메뉴에 붙인다. 없는 경로는 그대로 둔다.
func applyNavBadges(items []NavItem, counts map[string]int) []NavItem {
	if len(counts) == 0 {
		return items
	}
	for i := range items {
		if items[i].Section {
			continue
		}
		if n, ok := counts[items[i].Path]; ok {
			items[i].BadgeCount = n
		}
	}
	return items
}
