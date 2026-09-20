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
