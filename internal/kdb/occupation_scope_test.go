package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestDeadOccupationScopeNote — 표시가 자기 사유와 어긋나는 기각을 알아보는지.
//
// ★가려야 하는 것은 «한국인인가»지 «연예인인가»가 아니다. 범위는 0143 으로 한국의
// 인물·작품·조직·기관까지 넓어졌고, 직업이 정치·체육·학술이라는 것은 더 이상
// 기각 사유가 아니다. 반대로 North Korean 은 범위가 넓어져도 범위 밖이다.
func TestDeadOccupationScopeNote(t *testing.T) {
	dead := []struct{ note, why string }{
		{`[revert-term:reject] wikidata Q211650 직업이 비-엔터: "South Korean association football player"`, "박성호 — 축구선수"},
		{`[revert-term:reject] wikidata Q15660348 직업이 비-엔터: "South Korean writer"`, "김인숙 — 작가"},
		{`... 직업이 비엔터: "South Korean politician"`, "하이픈 없는 표기도 같은 명제"},
		{`[revert-term:reject] wikidata Q22974752 직업이 비-엔터: "South Korean Go player" · [adjudicated:claude 2026-07-25 keep]`, "이창석 — 바둑기사"},
	}
	for _, c := range dead {
		if !IsDeadOccupationScopeNote(c.note) {
			t.Errorf("죽은 명제를 못 알아봤다 (%s): %q", c.why, c.note)
		}
	}

	alive := []struct{ note, why string }{
		{`[revert-term:reject] wikidata Q492381 직업이 비-엔터: "North Korean table tennis player"`, "김정 — 북한은 범위 확대와 무관하게 범위 밖"},
		{`[revert-term:reject] wikidata Q501 직업이 비-엔터: "Korean (Joseon) naval commander (1545 – 1598)"`, "이순신 — South Korean 이 아니다(별도 판단)"},
		{`[revert-term:reject] wikidata Q9999 직업이 비-엔터: "Japanese singer"`, "일본 대상"},
		{`[revert-term:reject] wikidata Q1234 가 이 이름의 대상이 아님`, "진짜 tombstone — 사유가 직업이 아니다"},
		{`[ttl-expire:reject] 기한 내 실증 실패`, "다른 계열"},
		{``, "빈 노트"},
	}
	for _, c := range alive {
		if IsDeadOccupationScopeNote(c.note) {
			t.Errorf("살아 있는 기각을 죽었다고 봤다 (%s): %q", c.why, c.note)
		}
	}
}

// TestNotATombstoneExemptsDeadOccupationScope — 표시보다 사유가 먼저인지.
//
// ★이것이 없으면 `[revert-term:reject]` 문자열 하나로 **영구 차단**이다. 실측
// 2026-09-20: 236행이 그렇게 묻혀 있었고 그중 164 는 candidate, 120 에 중국어 표기가
// 이미 차 있었다. 요청은 계속 들어오는데 응답은 나가지 않았다 — 오거부다.
func TestNotATombstoneExemptsDeadOccupationScope(t *testing.T) {
	for _, alias := range []string{"", "e"} {
		sql := NotATombstoneSQL(alias)
		if !strings.Contains(sql, DeadOccupationScopeNotePattern) {
			t.Errorf("alias=%q: 면제 조건이 없다 — revert-term 표시가 사유를 이긴다:\n%s", alias, sql)
		}
		// 면제는 **먼저** 와야 한다. 뒤에 AND 로 붙으면 아무것도 면제하지 못한다.
		iEx := strings.Index(sql, DeadOccupationScopeNotePattern)
		iRv := strings.Index(sql, "[revert-term:reject]")
		if iEx < 0 || iRv < 0 || iEx > iRv {
			t.Errorf("alias=%q: 면제가 revert-term 검사 앞에 없다:\n%s", alias, sql)
		}
		if !strings.Contains(sql, " OR ") {
			t.Errorf("alias=%q: 면제가 OR 로 연결되지 않았다 — AND 면 면제가 아니다:\n%s", alias, sql)
		}
		// 괄호 균형. 호출부가 `AND ` + 이것 으로 끼워 넣으므로 깨지면 모든 질의가 죽는다.
		if strings.Count(sql, "(") != strings.Count(sql, ")") {
			t.Errorf("alias=%q: 괄호가 안 맞는다:\n%s", alias, sql)
		}
	}
}

// TestOccupationScopeRestoreIsReachable — 만들어 놓고 부르는 곳이 없는지.
//
// 이 저장소가 반복해 밟는 «장치는 있는데 아무도 안 켠» 을 막는다.
func TestOccupationScopeRestoreIsReachable(t *testing.T) {
	src, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "kdb.DrainOccupationScopeRestore(") {
		t.Error("DrainOccupationScopeRestore 를 부르는 곳이 없다 — 묻힌 236행이 그대로 남는다")
	}
	i := strings.Index(body, `os.Args[1] == "occup-scope-restore"`)
	if i < 0 {
		t.Fatal("occup-scope-restore 일회성 명령이 없다")
	}
	win := body[i:]
	if end := strings.Index(win, "DrainOccupationScopeRestore("); end > 0 {
		win = win[:end]
	}
	// 기본이 dry-run 이어야 한다. active 를 바꾸는 도구가 기본으로 쓰면 안 된다.
	if !strings.Contains(win, "dry := 500, true") {
		t.Error("occup-scope-restore 가 기본 dry-run 이 아니다 — 실수로 운영 데이터를 바꿀 수 있다")
	}
}

// TestFetchFailureIsNotCountedAsNoEvidence — 물어보지 못한 것을 «근거 없음»으로
// 세면 안 된다.
//
// ★첫 dry-run 이 20/20 «근거없음» 으로 나왔다(2026-09-20). 행이 나쁜 줄 알았는데
// 인증서 없는 이미지라 HTTPS 가 통째로 실패한 것이었다. 한 칸으로 세면 전송 실패가
// 판정으로 세탁되고, «고칠 것이 없다»고 잘못 보고하게 된다.
func TestFetchFailureIsNotCountedAsNoEvidence(t *testing.T) {
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainOccupationScopeRestore")
	if i < 0 {
		t.Fatal("DrainOccupationScopeRestore 가 없다")
	}
	win := body[i:]
	j := strings.Index(win, "cl.Fetch(ctx, it.qid)")
	if j < 0 {
		t.Fatal("위키데이터 조회 자리가 없다")
	}
	// 조회 실패 직후 200자 안에서 NoEvidence 를 올리면 안 된다.
	seg := win[j:]
	if len(seg) > 300 {
		seg = seg[:300]
	}
	if !strings.Contains(seg, "FetchFailed++") {
		t.Error("조회 실패를 FetchFailed 로 세지 않는다 — 전송 실패가 판정으로 세탁된다")
	}
	if strings.Contains(seg[:strings.Index(seg, "FetchFailed++")], "NoEvidence++") {
		t.Error("조회 실패를 NoEvidence 로 세고 있다")
	}
}

// TestTwinCheckUsesTheSameMatchingAsLookup — «같은 이름의 active 가 있나»를 조회와
// 같은 방식으로 묻는지.
//
// ★canonical_ko 만 비교하면 별칭으로 이미 서빙되는 대상을 못 본다(데이식스→DAY6).
// 그대로 되살리면 같은 이름이 둘이 되어 조회가 모호해진다.
func TestTwinCheckUsesTheSameMatchingAsLookup(t *testing.T) {
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainOccupationScopeRestore")
	if i < 0 {
		t.Fatal("DrainOccupationScopeRestore 가 없다")
	}
	win := body[i:]
	if end := strings.Index(win, "\n}\n"); end > 0 {
		win = win[:end]
	}
	if !strings.Contains(win, "aliases_ko") {
		t.Error("동명 active 검사가 별칭을 안 본다 — 별칭으로 서빙 중인 대상과 충돌한다")
	}
	if !strings.Contains(win, "[[:space:][:punct:]]+") {
		t.Error("동명 active 검사가 정규화 키를 안 쓴다 — 조회와 붙이는 방식이 다르다")
	}
}

// TestHomonymIsNeverPromotedStraightToActive — 실존 확인과 «그 이름으로 서빙해도
// 된다»는 다르다.
//
// ★이 계열의 시작이 2026-07-31 김은정(컬링)이었고 그 이름은 지금도 이 묶음 안에 있다.
// 되살리되, 어느 김은정인지는 동명이인 경로가 정한다.
func TestHomonymIsNeverPromotedStraightToActive(t *testing.T) {
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "toActive :=")
	if i < 0 {
		t.Fatal("승급 판정 자리가 없다")
	}
	line := body[i:]
	if end := strings.Index(line, "\n"); end > 0 {
		line = line[:end]
	}
	if !strings.Contains(line, "!it.homonym") {
		t.Errorf("동명이인 표시가 승급 판정에 없다: %s", line)
	}
	if !strings.Contains(body, "needs_disambig") {
		t.Error("needs_disambig 를 읽지 않는다 — 동명이인 표시를 알 수가 없다")
	}
}

// TestZhRepairRunsOnASchedule — 자체 수리가 주기로 도는지.
//
// ★일회성 명령만 있으면 «손으로 0 을 만든 그 순간»만 0 이다. 그 뒤 승급 경로로
// 들어오는 값은 아무도 안 본다. 2026-09-20 에 내가 직접 증명했다 —
// occup-scope-restore 가 155행을 active 로 올렸고 그중 16칸이 오염돼 있었다.
// QA 자체검사(qaCharsetOK)는 **값을 채울 때만** 본다. 이미 값을 가진 행이
// candidate 에서 올라오면 그 검사를 한 번도 안 거친다.
func TestZhRepairRunsOnASchedule(t *testing.T) {
	src, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "zhRepairTicker") {
		t.Error("자체 수리에 주기 ticker 가 없다 — 승급 경로로 들어온 오염을 아무도 안 본다")
	}
	i := strings.Index(body, "case <-zhRepairTicker.C:")
	if i < 0 {
		t.Fatal("zhRepairTicker 를 받는 case 가 없다 — ticker 만 만들고 안 읽으면 아무 일도 안 난다")
	}
	win := body[i:]
	if end := strings.Index(win, "\n\t\tcase <-"); end > 0 {
		win = win[:end]
	}
	if !strings.Contains(win, "RepairZhVariants(") {
		t.Error("주기 case 가 RepairZhVariants 를 부르지 않는다")
	}
	// 주기 실행은 **실제로 고쳐야** 한다. dry 로 돌면 로그만 남고 데이터는 그대로다.
	if !strings.Contains(win, "false, false)") {
		t.Errorf("주기 실행이 dry-run 이다 — 로그만 남고 오염은 그대로다:\n%s", win)
	}
}

// TestRestoreClearsTheDeadScopeReviewMark — 되살리면서 표시를 걷는지.
//
// ★레인을 끄는 것과 그 레인이 남긴 표시를 걷는 것은 다른 일이다.
// stepScopeReview 는 2026-09-15 에 껐는데, 그때 찍힌 `[scope:review]` 는 그대로 남아
// 지금도 세 레인이 그 행을 제외한다 — itunes_drain · discogs_drain ·
// enrich/orchestrator 가 전부 `NOT LIKE '%[scope:review]%'` 를 건다.
// 표시를 안 걷으면 status 만 active 가 되고 값은 영영 안 채워진다.
func TestRestoreClearsTheDeadScopeReviewMark(t *testing.T) {
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainOccupationScopeRestore")
	if i < 0 {
		t.Fatal("DrainOccupationScopeRestore 가 없다")
	}
	win := body[i:]
	if !strings.Contains(win, `'[scope:review]', '[scope:review-해제됨]'`) {
		t.Error("되살리면서 [scope:review] 를 안 걷는다 — itunes·discogs·enrich 가 계속 제외한다")
	}
	// 지우지 않고 이름을 바꾼다. 흔적 없이 지우면 왜 표시가 없는지 알 길이 없다.
	if strings.Contains(win, `'[scope:review]', ''`) {
		t.Error("표시를 흔적 없이 지운다 — 무엇이 걷혔는지 알 수 없게 된다")
	}
	// 두 UPDATE(승급·되살림) 양쪽에 있어야 한다. 한쪽만 걷으면 나머지가 그대로 막힌다.
	if n := strings.Count(win, "[scope:review-해제됨]"); n < 2 {
		t.Errorf("표시를 걷는 UPDATE 가 %d 곳뿐이다 — 승급·되살림 양쪽에 있어야 한다", n)
	}
}

// TestDeadScopeReviewResidueIsBoundedByTier — 표시를 걷는 근거가 «범위가 넓어졌다»가
// 아니라 «이 행의 근거는 이미 섰다» 인지.
//
// 범위 확대만으로 표시를 걷으면 근거 없는 행까지 같이 올라온다. 등급이
// authoritative 인 것만 본다 — 그 등급은 범위 판단과 무관하게 매겨진 것이다.
func TestDeadScopeReviewResidueIsBoundedByTier(t *testing.T) {
	src, err := os.ReadFile("scope_reopen.go")
	if err != nil {
		t.Fatalf("scope_reopen.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func DrainOccupationScopeRestore")
	if i < 0 {
		t.Fatal("DrainOccupationScopeRestore 가 없다")
	}
	win := body[i:]
	j := strings.Index(win, "ScopeRejectionNotePattern")
	if j < 0 {
		t.Fatal("죽은 범위 사유 갈래가 없다")
	}
	// 그 갈래와 같은 괄호 안에 등급 조건이 있어야 한다.
	seg := win[max0(j-400) : j+80]
	if !strings.Contains(seg, "verification_tier = 'authoritative'") {
		t.Errorf("죽은 범위 사유 갈래에 등급 조건이 없다 — 근거 없는 행까지 올라온다:\n%s", seg)
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}
