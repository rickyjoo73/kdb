package kdb

// scope_phrases — **옛 범위(K-엔터테인먼트)로 내린 기각**을 알아보는 패턴을 한 곳에 둔다.
//
// ★왜 상수인가 (2026-09-16). 같은 명제를 원장에 쓴 사람마다 다른 말로 적었다:
//
//	비-K(범위밖) · K-엔터테인먼트 · 비연예 · 비-엔터 · K-콘텐츠
//
//	이재명   "한국의 실존 정치인"        → 기각 "K-엔터 인물이 아님"
//	차범근   "한국의 전설적인 축구선수"  → 5회 기각 · 83일 미결
//	FC서울   "K리그 소속 프로 축구단"    → "K-콘텐츠가 아닌 프로축구 구단"
//	오세훈   "South Korean politician"   → "직업이 비-엔터"
//
// 전부 **같은 명제**다 — "연예가 아니다"이지 "그 이름의 대상이 없다"가 아니다.
// 범위가 한국의 인물·작품·조직·기관으로 넓어지면서(0143) 그 명제 자체가 죽었다.
//
// ★문구를 사이트마다 따로 적다가 다섯 번 새로 알았고, 그러고도 한 곳을 빠뜨렸다.
//
//	api.go Tombstoned      → 다섯 표현 다 모았다
//	scope_reopen.go        → 다섯 표현 다 모았다
//	intake_autoverify.go   → **두 표현만 있었다** ('비-K(범위밖)|K-엔터테인먼트')
//
//	그래서 오세훈은 scope-reopen 이 되살린 그 날 밤에 다시 `out_of_scope` 가 됐다.
//	되살아난 행을 revert-term 드레인이 옛 규칙으로 다시 죽였고, 새 요청은 좁은
//	패턴에 안 걸린 그 기각 행에 막혔다. 문구가 흩어져 있으면 여섯 번째가 또 나온다.
//
// 이 파일이 그 하나의 자리다. 새 표현을 발견하면 **여기만** 고친다.

import (
	"regexp"
	"strings"
	"time"
)

// ScopeRejectionNotePattern — 원장 notes 가 옛 범위 사유로 기각됐다고 말하는 표현.
// PostgreSQL 정규식(`~`)과 Go regexp 양쪽에서 같은 뜻으로 동작하는 문법만 쓴다.
const ScopeRejectionNotePattern = `비-?K|범위 ?밖|K-엔터|K-콘텐츠|비-?엔터|비연예`

// ForeignSubjectNotePattern — 한국 대상이 아니라고 말하는 표현. 범위가 넓어져도
// 해외 대상은 그대로 범위 밖이므로, 이 표시가 있으면 옛 기각이 그대로 유효하다.
const ForeignSubjectNotePattern = `해외|외국|일본|중국|미국|영국|글로벌|Japan|Global`

var (
	scopeRejectionRe = regexp.MustCompile(ScopeRejectionNotePattern)
	foreignSubjectRe = regexp.MustCompile(ForeignSubjectNotePattern)
)

// IsDeadScopeRejection — 이 노트의 기각 사유가 **범위 확대로 죽었는가**.
// 옛 범위 표현이 있고, 해외 대상 표시가 없을 때만 참이다.
func IsDeadScopeRejection(notes string) bool {
	return scopeRejectionRe.MatchString(notes) && !foreignSubjectRe.MatchString(notes)
}

// ─── 되살림 표시 ────────────────────────────────────────────────────────────
//
// ★왜 여기 있나 (2026-09-16). candidate_ttl 은 notes 의 `[reopened:YYYY-MM-DD]` 를
// 시계의 새 출발점으로 읽도록 **이미 만들어져 있었다.** 그런데 그 표시를 쓰는 곳이
// 시험 하나뿐이었다 — 되살리는 세 레인(scope_reopen · demand_evidence · rejudge)이
// 전부 자기 문구만 적었다.
//
//	09-16 실측: scope-reopen 이 오세훈을 09:39 에 candidate 로 되살렸고,
//	            10:04 에 TTL 이 "103일 미결"로 다시 기각했다. 25분이다.
//	            그 103일 중 대부분은 **기각돼 있던 기간**이고 기각은 미결이 아니다.
//
// 장치가 있는데 아무도 안 켜는 것은 장치가 없는 것과 같다 — 오히려 나쁘다.
// 있다고 믿게 하기 때문이다. 그래서 표시를 만드는 일을 한 함수로 모으고,
// 되살리는 코드가 그것을 안 쓰면 시험이 실패한다.

// ReopenedMarker — TTL 시계를 다시 시작시키는 표시. 되살리는 모든 경로가 붙인다.
func ReopenedMarker(now time.Time) string {
	return "[reopened:" + now.Format("2006-01-02") + "]"
}

// ReopenNote — 되살림 사유에 시계 표시를 붙인 노트.
func ReopenNote(now time.Time, reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ReopenedMarker(now)
	}
	return ReopenedMarker(now) + " " + reason
}

// ─── 기각의 무게 ────────────────────────────────────────────────────────────

// NotATombstoneSQL — **이름을 묻어 버리지 못하는 기각**을 걸러내는 SQL 조건.
//
// 기각의 명제가 "이 이름의 대상이 없다"가 아니면 그것은 tombstone 이 아니다.
// 세 계열이 그렇다 — 셋 다 명제가 "이 레코드가 아직 실증되지 않았다" 거나
// "지금 범위 밖이다" 일 뿐이다.
//
//	[revert-term:reject]  이 레코드에 붙은 QID 가 비-K 다
//	[ttl-expire:reject]   기한 내 실증되지 않았다 (설계 의도가 '재요청 시 재발굴')
//	옛 범위 기각            연예가 아니다 (범위 확대로 명제 자체가 죽었다)
//
// ★한 자리에 둔다. 같은 판단을 세 곳이 따로 적고 있었고 — Tombstoned ·
// CloseResolvedBacklog · rejectedTwinStillExists — 그중 둘이 TTL 을 안 빼서
// **TTL 기각이 existing_rejected_entity 로 세탁돼 영구 차단**이 됐다.
// alias 는 호출부가 정한다(e · 없으면 빈 문자열).
func NotATombstoneSQL(alias string) string {
	col := "COALESCE(notes,'')"
	if alias != "" {
		col = "COALESCE(" + alias + ".notes,'')"
	}
	return col + " NOT LIKE '%[revert-term:reject]%'\n   AND " +
		col + " NOT LIKE '%[ttl-expire:reject]%'\n   AND " +
		col + " !~ '" + ScopeRejectionNotePattern + "'"
}

// notATombstoneE — `kwave_entities e` 별칭용 미리 만든 조건. 원시 문자열 SQL 안에
// 끼워 넣으려면 값이 필요하다.
var notATombstoneE = NotATombstoneSQL("e")
