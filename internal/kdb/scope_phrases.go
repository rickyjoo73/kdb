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
//
// ★여섯 번째 표현 `대중문화` (2026-09-20). 판정기가 가장 많이 쓰는 말인데 여기 없었다
//
//	— "한국 대중문화(K-콘텐츠) 엔티티가 아닌 유통업체"(롯데마트). 어제 만든 레인은
//	이 낱말을 자기 파일에 **인라인으로** 적어 일곱 번째 사본을 만들었다. 그 사본이
//	이 파일의 존재 이유를 그대로 반복했으므로, 표현은 여기로 올리고 사본은 지웠다.
const ScopeRejectionNotePattern = `비-?K|범위 ?밖|K-엔터|K-콘텐츠|비-?엔터|비연예|대중문화`

// ForeignSubjectNotePattern — 한국 대상이 아니라고 말하는 표현. 범위가 넓어져도
// 해외 대상은 그대로 범위 밖이므로, 이 표시가 있으면 옛 기각이 그대로 유효하다.
const ForeignSubjectNotePattern = `해외|외국|일본|중국|미국|영국|글로벌|Japan|Global`

// DeadOccupationScopeNotePattern — **여섯 번째 표현**이자, 앞의 다섯과 성질이 다르다.
//
// 앞의 다섯은 «사유 문구»라 ScopeRejectionNotePattern 에 모을 수 있었다. 이것은
// `[revert-term:reject]` 라는 **표시**를 달고 들어와서, 표시가 문구를 이긴다.
// NotATombstoneSQL 이 표시를 먼저 보기 때문이다.
//
//	notes: `[revert-term:reject] wikidata Q211650 직업이 비-엔터: "South Korean
//	        association football player"`
//
// ★그 표시의 명제는 "이 레코드에 붙은 QID 가 비-K 다"인데, **같은 줄이 그 QID 를
//
//	"South Korean …" 이라고 적고 있다.** 기록이 스스로를 반박한다. 여기서 죽은 것은
//	대상이 아니라 «직업이 연예가 아니다»라는 옛 범위 명제다(0143 으로 소멸).
//
// 실측(2026-09-20): 236행이 이 모양으로 묻혀 있었다 — candidate 164 · rejected 69 ·
// active 3. 전부 위키데이터 앵커가 있고, 164 중 161 이 앵커를 달고 있으며 120 에
// 중국어 표기가 이미 차 있다. 직업 상위는 politician 26 · footballer 32 · writer 9 ·
// baseball player 9 — 번역 소비자가 매일 묻는 바로 그 사람들이다(이재명 · 정의선 · 류현진).
//
// ★"South Korean" 으로 **시작**하는 것만 본다. 느슨하게 'Korean' 을 허용하면
//
//	`North Korean table tennis player`(김정)가 같이 들어온다 — 그건 범위가 넓어져도
//	범위 밖이다. 조선시대 인물(이순신 "Korean (Joseon) naval commander")도 여기서는
//	빼 둔다. 12건뿐이고, 되살릴지는 별도 판단이다.
const DeadOccupationScopeNotePattern = `직업이 비-?엔터: "South Korean`

var (
	scopeRejectionRe      = regexp.MustCompile(ScopeRejectionNotePattern)
	foreignSubjectRe      = regexp.MustCompile(ForeignSubjectNotePattern)
	deadOccupationScopeRe = regexp.MustCompile(DeadOccupationScopeNotePattern)
)

// IsDeadOccupationScopeNote — 이 노트가 **스스로를 반박하는 revert-term 기각**인가.
// 해석이 아니다 — 같은 줄에 적힌 위키데이터 설명을 그대로 읽는다.
func IsDeadOccupationScopeNote(notes string) bool {
	return deadOccupationScopeRe.MatchString(notes)
}

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
// ★그리고 **표시가 자기 사유와 어긋나는** 한 계열이 더 있다(2026-09-20).
//
//	`[revert-term:reject]` 를 달았는데 사유가 `직업이 비-엔터: "South Korean …"` 이면
//	그 표시의 명제("QID 가 비-K")가 같은 줄에서 부정된다. 표시보다 사유가 먼저다 —
//	그래서 이 조건은 DeadOccupationScopeNotePattern 을 **면제로 먼저** 본다.
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
	return "(" + col + " ~ '" + DeadOccupationScopeNotePattern + "'\n    OR (" +
		col + " NOT LIKE '%[revert-term:reject]%'\n   AND " +
		col + " NOT LIKE '%[ttl-expire:reject]%'\n   AND " +
		col + " !~ '" + ScopeRejectionNotePattern + "'))"
}

// notATombstoneE — `kwave_entities e` 별칭용 미리 만든 조건. 원시 문자열 SQL 안에
// 끼워 넣으려면 값이 필요하다.
var notATombstoneE = NotATombstoneSQL("e")

// ─── 살아 있는 명제 ─────────────────────────────────────────────────────────
//
// ★위의 다섯 표현은 **죽은** 명제다. 아래 둘은 범위가 넓어져도 **그대로 살아 있다.**
//
//	일반명사다   무지개(기상 현상) · 왠지(부사) · 명의(소유권) · 헤엄
//	해외 대상이다 블리즈컨(Blizzard) · 오가와 마코토(일본)
//
// ★왜 여기 같이 두나 (2026-09-20). 판정기는 **살아 있는 명제를 죽은 명제의 낱말로
//
//	쓴다.** 실제 원장:
//
//	  무지개: "…일반 명사로서의 '무지개'를 다루고 있으며 K-콘텐츠(song_album)와 무관"
//
//	그래서 죽은 명제를 되살리는 레인이 낱말만 보고 396건을 걷었고 그중 118건이
//	일반명사, 24건이 해외였다. 43건은 이미 «걷힘 → 재판정 → 같은 결론» 왕복을 마쳤다.
//	죽은 것을 고를 때는 **살아 있는 것을 같이 빼야** 한다 — 두 패턴이 한 자리에 있어야
//	그 짝을 빠뜨리지 않는다.
const CommonNounNotePattern = `일반 ?명사|일반 ?단어|일반어|일상어|보통명사|부사\(|부사 |범주어|장르어|관용(구|어)`

var commonNounRe = regexp.MustCompile(CommonNounNotePattern)

// AssertsLiveExclusion — 이 사유가 **지금도 유효한 범위 밖 판정**을 말하는가.
// 참이면 범위가 넓어져도 되살리면 안 된다.
func AssertsLiveExclusion(reason string) bool {
	return commonNounRe.MatchString(reason) || foreignSubjectRe.MatchString(reason)
}

// ─── 노트가 스스로 말하는 «무엇인가» ────────────────────────────────────────
//
// ★rejected 더미에는 되살릴 근거가 노트 안에 이미 적혀 있다 (2026-09-20 실측).
//
//	SK하이닉스  "SK하이닉스는 한국의 주요 반도체 제조 **기업**임" → 비-K(범위밖)로 기각
//	국민의힘    "국민의힘은 한국의 **정당**명"                    → 비-K(범위밖)로 기각
//	두산 베어스 "KBO **프로야구단**"                              → 엔터 카테고리 아님
//	서울대학교  "일반 교육기관(**대학교**)"                       → gpt 일반어
//
//	`[revert-term:reject]` 가 오세훈에게 한 것과 **같은 자기반박**이다 — 기각 사유가
//	그 대상을 0143 범위 안의 종류로 적고 있다. 해석이 아니라 그 줄을 읽는 것이다.
//
// ★여기서 «일반어»라는 낱말은 근거가 되지 못한다. 옛 판정기는 «연예가 아니다»를
//
//	전부 «일반어»라고 적었다 — 서울대학교의 사유가 정확히 "gpt 일반어 — 비-K(범위밖):
//	일반 교육기관(대학교)" 이다. 구체적인 종류(대학교)가 적혀 있으면 그것이 이긴다.
//	구체적 종류 없이 «일반명사»만 있으면 닫힌 채로 둔다.
//
// ★이 표는 **미상 칸(term·unknown)의 재유형화**에도 쓴다. 관측된 낱말 → 유형이라
// LLM 추측이 아니고 dataqa_log 로 되돌릴 수 있다.
// ★`\b` 를 쓰지 않는다. PostgreSQL 정규식에서 `\b` 는 **낱말 경계가 아니라 백스페이스**
//
//	다(경계는 `\y`). Go 에는 `\y` 가 없다. 두 엔진에서 같은 뜻인 문법만 쓴다는 이
//	파일의 규칙이 그래서 있고, 시험이 그것을 지킨다.
var InScopeSubjectKinds = []struct {
	Pattern string // PostgreSQL·Go 양쪽에서 같은 뜻인 문법만
	Type    string // 그 종류가 들어갈 entity_type
}{
	{`대학교|대학원|대학|학교|교육기관`, "school"},
	{`정당`, "political_party"},
	{`구단|프로야구|프로축구|야구단|축구단|배구단|농구단|국가대표팀`, "sports_team"},
	{`부처|정부기관|중앙행정|공공기관|지방자치|시청|군청|도청|교육청|공사|공단`, "government_body"},
	{`협회|재단|학회|노동조합|노조|연합회|조합`, "organization"},
	{`학술지|잡지|간행물|일간지|신문사`, "publication"},
	{`기업|회사|법인|제조사|유통업체|은행|증권사`, "company"},
}

// InScopeSubjectNotePattern — 위 표 전체의 합집합(선정 조건용).
var InScopeSubjectNotePattern = func() string {
	parts := make([]string, 0, len(InScopeSubjectKinds))
	for _, k := range InScopeSubjectKinds {
		parts = append(parts, k.Pattern)
	}
	return strings.Join(parts, "|")
}()

var inScopeSubjectRes = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(InScopeSubjectKinds))
	for i, k := range InScopeSubjectKinds {
		out[i] = regexp.MustCompile(k.Pattern)
	}
	return out
}()

// InScopeSubjectKind — 노트가 이름 붙인 0143 범위 종류. 없으면 ("", false).
// 위에서부터 먼저 걸리는 것을 쓴다 — 표의 순서가 곧 우선순위다(학교가 기업보다 앞).
func InScopeSubjectKind(notes string) (string, bool) {
	for i, re := range inScopeSubjectRes {
		if re.MatchString(notes) {
			return InScopeSubjectKinds[i].Type, true
		}
	}
	return "", false
}

// ─── 사유의 종류를 표시로 적는다 ────────────────────────────────────────────
//
// ★낱말로 가르는 일을 여기서 끝낸다 (2026-09-20). 위 두 패턴은 **이미 찍힌 노트**를
// 읽기 위한 것이다. 앞으로 찍는 표시에는 종류를 기계가 읽을 수 있게 같이 적는다 —
// 그러면 다음 레인은 문장을 해석하지 않고 표시만 본다.
//
//	[cand-evidence:review] 의심=기상 현상 / … [rk:common-noun]
//
// 이 한 조각이 없어서 오늘 왕복이 생겼다. 표시를 만들 때 **걷는 조건도 같이 정한다**
// 는 원칙의 실행부다.
const (
	ReasonKindScope        = "scope"         // 연예가 아니다 — 0143 으로 죽은 명제
	ReasonKindCommonNoun   = "common-noun"   // 일반명사·일상어·범주어 — 살아 있다
	ReasonKindForeign      = "foreign"       // 해외 대상 — 살아 있다
	ReasonKindTypeMismatch = "type-mismatch" // 대상은 실존하나 저장 종류가 틀렸다
	ReasonKindProduct      = "product"       // 제품·상품명 — 살아 있다
)

var reasonKindRe = regexp.MustCompile(`\[rk:([a-z-]+)\]`)

// ReasonKindTag — 노트에 붙일 표시.
func ReasonKindTag(kind string) string { return "[rk:" + kind + "]" }

// ReasonKindOf — 노트에 적힌 사유 종류. 없으면 빈 문자열(옛 행).
func ReasonKindOf(notes string) string {
	if m := reasonKindRe.FindStringSubmatch(notes); m != nil {
		return m[1]
	}
	return ""
}

// DeadScopeInSegmentSQL — **그 표시가 붙인 사유 구간에서만** 죽은 범위 문구를 찾는다.
//
// ★notes 전체를 보면 안 된다 (2026-09-20 실측). 한 행의 notes 에는 레인 대여섯 개가
//
//	남긴 말이 쌓여 있다. 무지개의 경우 cand-evidence 의 사유는 "기상 현상" 인데,
//	같은 notes 의 다른 자리에 gatekeeper 가 "K-엔터테인먼트 고유명사가 아닌" 이라고
//	적어 두었다. 전체를 보면 그 말에 걸려 **옳게 거른 것이 되살아난다.**
//
// marker 뒤 구간에 죽은 문구가 있고, 살아 있는 명제(일반명사·해외)는 없어야 한다.
func DeadScopeInSegmentSQL(notesExpr, marker string) string {
	seg := "split_part(" + notesExpr + ", '" + marker + "', 2)"
	return "(" + seg + " ~ '" + ScopeRejectionNotePattern + "'\n" +
		"        AND " + seg + " !~ '" + ForeignSubjectNotePattern + "'\n" +
		"        AND " + seg + " !~ '" + CommonNounNotePattern + "')"
}
