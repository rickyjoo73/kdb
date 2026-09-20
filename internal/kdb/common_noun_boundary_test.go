package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestLivePropositionsAreNotDeadScope — **오늘 잘못 걷은 118건의 실제 문장**을 박아 둔다.
//
// 판정기는 «일반명사다»(살아 있는 명제)를 «K-콘텐츠가 아니다»(죽은 명제)와 같은
// 낱말로 쓴다. 죽은 것만 고르려면 살아 있는 것을 같이 빼야 한다.
func TestLivePropositionsAreNotDeadScope(t *testing.T) {
	// 원장에서 그대로 가져온 사유들(2026-09-20 걷힌 396건 중).
	live := []string{
		"의심=기상 현상 / 제시된 뉴스 스니펫들은 모두 기상 현상, 브랜드명, 또는 일반 명사로서의 '무지개'를 다루고 있으며 K-콘텐츠(song_album)와 무관",
		"의심=부사 (adverb) / '왠지'는 부사로 사용되었을 뿐, 노래나 앨범(song_album)이 아니다",
		"의심=일반 명사(꿀을 담는 단지) / 제시된 뉴스 스니펫들은 모두 일반 명사로서의 '꿀단지'를 의미하며, K-엔터테인먼트의 song_album 이 아님",
		"의심=스토킹 행위자(일반 명사) / 기사 맥락상 대중문화 엔티티(노래/앨범)가 아닌 범죄 행위자를 지칭하는 일반 명사로 사용됨",
		"의심=Bruno Mars의 월드 투어 / 기사 맥락상 해당 엔티티는 한국 대중문화(K-콘텐츠)가 아닌 해외 아티스트 브루노 마스의 공연 투어임",
	}
	for _, n := range live {
		if !AssertsLiveExclusion(n) {
			t.Errorf("지금도 유효한 판정인데 못 알아봤다 — 걷으면 옳게 거른 것이 되살아난다:\n  %q", n)
		}
	}
	// 반대쪽: 죽은 명제만 있는 사유는 걷어야 한다.
	dead := []string{
		"의심=반도체 제조 기업 / 한국 대중문화(K-콘텐츠) 엔티티가 아닌 유통업체",
		"비-K(범위밖): 정치 정당",
		"K리그 프로축구단 FC서울(스포츠), K-콘텐츠가 아님",
	}
	for _, n := range dead {
		if AssertsLiveExclusion(n) {
			t.Errorf("죽은 범위 명제인데 살아 있다고 봤다 — 영영 못 되살린다:\n  %q", n)
		}
	}
}

// TestDeadScopeFlagReadsOnlyItsOwnSegment — 노트 **전체**를 보면 안 된다.
//
// ★실제 사고 (2026-09-20). 무지개의 cand-evidence 사유는 "기상 현상" 인데, 같은
// notes 의 다른 자리에 gatekeeper 가 "K-엔터테인먼트 고유명사가 아닌" 이라고 적어
// 두었다. 전체를 보면 그 말에 걸려 옳게 거른 것이 열린다.
func TestDeadScopeFlagReadsOnlyItsOwnSegment(t *testing.T) {
	cond := DeadScopeInSegmentSQL("COALESCE(notes,'')", "[cand-evidence:review]")
	if !strings.Contains(cond, "split_part") {
		t.Error("사유 구간을 잘라 보지 않는다 — notes 전체를 보면 다른 레인의 말에 걸린다")
	}
	if !strings.Contains(cond, CommonNounNotePattern) {
		t.Error("일반명사 문구를 빼지 않는다")
	}
	if !strings.Contains(cond, ForeignSubjectNotePattern) {
		t.Error("해외 문구를 빼지 않는다")
	}

	// 그리고 걷는 레인이 실제로 이 조건과 일반명사 구간을 함께 보는지.
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainDeadScopeFlags")
	if i < 0 {
		t.Fatal("DrainDeadScopeFlags 가 없다")
	}
	win := body[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	if !strings.Contains(win, "DeadScopeInSegmentSQL") {
		t.Error("구간 조건을 쓰지 않는다 — 낱말로 가르려 하면 오늘 왕복이 반복된다")
	}
	if !strings.Contains(win, "commonnoun.NotListedSQL") {
		t.Error("일반명사 구간을 보지 않는다")
	}
	if strings.Contains(win, "대중문화|K-콘텐츠") {
		t.Error("범위 문구를 또 인라인으로 적었다 — ScopeRejectionNotePattern 한 자리를 쓴다")
	}
	if strings.Contains(win, "SET status=") || strings.Contains(win, "status='active'") {
		t.Error("표시만 걷어야 하는데 status 를 바꾼다 — 판정을 대신하고 있다")
	}
}

// TestInScopeSubjectKindReadsWhatTheNoteSays — 기각 사유가 스스로 적은 종류.
//
// ★서울대학교의 실제 사유가 "gpt 일반어 — 비-K(범위밖): 일반 교육기관(대학교)" 이다.
// «일반어»라는 낱말은 옛 판정기가 «연예가 아니다»에 붙인 이름표라 근거가 못 되고,
// 같은 줄의 «대학교» 가 그보다 특정적이다.
func TestInScopeSubjectKindReadsWhatTheNoteSays(t *testing.T) {
	cases := map[string]string{
		"autopilot: gpt 일반어 — 비-K(범위밖): 일반 교육기관(대학교)":               "school",
		"resolve-unknowns: gpt brand_place — 국민의힘은 한국의 정당명으로":       "political_party",
		"[contam:review] 오염/정크 의심: 비-K(범위밖): 반도체 제조 기업":             "company",
		"[adjudicated:claude reject] 두산 베어스는 KBO 프로야구단으로 스포츠 브랜드이며": "sports_team",
		"비-K(범위밖): 문화체육관광부는 정부 부처":                                  "government_body",
		"K-엔터테인먼트 조직이 아닌 사회복지 협회":                                   "organization",
		"학술지 또는 전문 잡지 명칭으로 보이며":                                     "publication",
	}
	for note, want := range cases {
		got, ok := InScopeSubjectKind(note)
		if !ok || got != want {
			t.Errorf("InScopeSubjectKind(%q) = %q,%v — 기대 %q", note, got, ok, want)
		}
	}
	// 구체적인 종류가 없으면 열지 않는다.
	for _, note := range []string{
		"의심=기상 현상 / 노래나 앨범이 아닌 기상 현상으로 설명됨",
		"gpt 일반어 — 비-K(범위밖)",
		"비실체/일반어 — term reject",
	} {
		if kind, ok := InScopeSubjectKind(note); ok {
			t.Errorf("구체적 종류가 없는데 %q 로 열려 한다: %q", kind, note)
		}
	}
}

// TestRejectedRecoveryDoesNotPromote — 되살림은 candidate 까지다.
//
// 범위가 넓어졌다는 것이 «근거 없이 서빙해도 된다»는 뜻은 아니다. 판정은 고쳐진
// 판정기가 뉴스 근거로 한다.
func TestRejectedRecoveryDoesNotPromote(t *testing.T) {
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainScopeRejectedTyped")
	if i < 0 {
		t.Fatal("DrainScopeRejectedTyped 가 없다 — 기각 더미에 닿는 레인이 없다")
	}
	win := body[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	if strings.Contains(win, "status='active'") {
		t.Error("기각 더미에서 바로 active 로 올린다 — 근거 없는 서빙이다")
	}
	if !strings.Contains(win, "status='candidate'") {
		t.Error("candidate 로 되돌리지 않는다")
	}
	if !strings.Contains(win, "commonnoun.NotListedSQL") {
		t.Error("일반명사 구간을 보지 않는다 — 진짜 일반어가 되살아난다")
	}
	if !strings.Contains(win, "ReopenNote") {
		t.Error("되살림 시계 표시가 없다 — TTL 이 몇 분 만에 다시 기각한다(오세훈 25분)")
	}
	// 한 번 연 것을 또 열지 않는다(왕복 금지).
	if !strings.Contains(win, "[scope-rejected-reopen]") {
		t.Error("재열림 방지 표시가 없다 — 같은 행을 매 시간 다시 연다")
	}
	// 미상 칸은 재유형화해야 판정기가 본다(cand-evidence 가 term 을 제외하므로).
	if !strings.Contains(win, "scope-rejected-retype") {
		t.Error("미상 칸 재유형화 기록이 없다 — 되살려도 term 은 아무도 판정하지 않는다")
	}
}

// TestPatternsWorkInBothEngines — 같은 정규식을 Go 와 PostgreSQL 이 같이 읽는가.
//
// ★`\b` 는 Go 에서 낱말 경계지만 **PostgreSQL 에서는 백스페이스**다(경계는 `\y`).
// 한 자리에 두고 양쪽에 내려보내는 패턴에 그런 문법이 섞이면, Go 시험은 통과하는데
// SQL 조건만 조용히 아무것도 못 고른다 — 가장 찾기 어려운 종류의 결함이다.
func TestPatternsWorkInBothEngines(t *testing.T) {
	pats := map[string]string{
		"ScopeRejectionNotePattern":  ScopeRejectionNotePattern,
		"ForeignSubjectNotePattern":  ForeignSubjectNotePattern,
		"CommonNounNotePattern":      CommonNounNotePattern,
		"InScopeSubjectNotePattern":  InScopeSubjectNotePattern,
		"DeadOccupationScopeNotePat": DeadOccupationScopeNotePattern,
	}
	bad := []string{`\b`, `\y`, `\d`, `(?i)`, `(?=`, `(?!`}
	for name, p := range pats {
		for _, b := range bad {
			if strings.Contains(p, b) {
				t.Errorf("%s 에 %q 가 있다 — 두 엔진에서 뜻이 다르거나 없는 문법이다", name, b)
			}
		}
	}
}
