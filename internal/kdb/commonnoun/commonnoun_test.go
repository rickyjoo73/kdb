package commonnoun

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestKeyMatchesTheSQLRule — Go 의 Key() 와 SQL 의 NormKeySQL 이 **같은 규칙**인가.
//
// ★둘이 갈라지면 Go 가 등재한 낱말을 SQL 조건이 못 찾는다. 그러면 «등재했는데도
// 되살아나는» 조용한 실패가 되고, 그건 이 구간을 만든 이유를 통째로 지운다.
// SQL 을 여기서 돌릴 수 없으니 **규칙의 모양**을 대조한다: 소문자화 + 공백/문장부호 제거.
func TestKeyMatchesTheSQLRule(t *testing.T) {
	sql := NormKeySQL("x")
	for _, want := range []string{"lower(", "regexp_replace(", "btrim(", "[[:space:][:punct:]]+"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL 규칙에 %q 가 없다: %s", want, sql)
		}
	}
	cases := map[string]string{
		"무지개":       "무지개",
		" 나 혼자 산다 ": "나혼자산다",
		"Love Cell": "lovecell",
		"f(x)":      "fx",
		"아이브(IVE)":  "아이브ive",
		"KBS 2TV":   "kbs2tv",
		"타이틀-곡":     "타이틀곡",
		"":          "",
		"   ":       "",
	}
	for in, want := range cases {
		if got := Key(in); got != want {
			t.Errorf("Key(%q) = %q, 기대 %q", in, got, want)
		}
	}
}

// TestNotListedSQLShapesAJoinFreeCondition — 다른 레인이 그대로 끼워 쓸 수 있는가.
// 서브쿼리여야 한다 — JOIN 이면 호출부의 쿼리 모양을 바꿔야 하고, 그러면 아무도 안 쓴다
// (DrainScopeReopen 이 위키데이터 JOIN 때문에 2,030행을 못 본 것과 같은 함정).
func TestNotListedSQLShapesAJoinFreeCondition(t *testing.T) {
	cond := NotListedSQL("e.canonical_ko")
	if !strings.HasPrefix(strings.TrimSpace(cond), "NOT EXISTS") {
		t.Errorf("NOT EXISTS 서브쿼리여야 한다: %s", cond)
	}
	if !strings.Contains(cond, "status='confirmed'") {
		t.Error("되돌린(revoked) 등재까지 배제하면 고유명사가 영영 못 돌아온다")
	}
	if !strings.Contains(cond, Table) {
		t.Errorf("테이블 이름이 없다: %s", cond)
	}
	if strings.Contains(cond, ";") {
		t.Error("조건 조각에 세미콜론이 있으면 호출부 SQL 이 깨진다")
	}
}

// TestOnlyLiveExclusionKindsAreValid — «연예가 아니다»는 이 구간에 들어올 수 없다.
//
// ★이 구간의 명제는 «범위가 어떻게 바뀌어도 밖이다» 이다. 0143 으로 죽은 명제를
// 여기 담으면, 죽은 판정이 원장에 영구히 박혀 회수 레인이 손댈 수 없게 된다 —
// 오늘 고친 결함을 더 나쁜 형태로 다시 만드는 셈이다.
func TestOnlyLiveExclusionKindsAreValid(t *testing.T) {
	for _, k := range []string{KindCommonNoun, KindCategory, KindProduct, KindForeignSubject} {
		if !ValidKind(k) {
			t.Errorf("%q 가 유효 종류가 아니다", k)
		}
	}
	for _, k := range []string{"scope", "non_entertainment", "비-K", "", "unknown"} {
		if ValidKind(k) {
			t.Errorf("%q 는 이 구간의 종류가 아니다 — 죽은 명제를 등재하면 안 된다", k)
		}
	}
}

// TestKindsMatchTheMigrationCheck — Go 의 종류 목록과 DB CHECK 가 같은가.
func TestKindsMatchTheMigrationCheck(t *testing.T) {
	b := mustRead(t, "../../../migrations/0150_common_noun_ledger.sql")
	re := regexp.MustCompile(`kind IN \(([^)]*)\)`)
	m := re.FindStringSubmatch(b)
	if m == nil {
		t.Fatal("마이그레이션에서 kind CHECK 를 못 찾았다")
	}
	for _, k := range []string{KindCommonNoun, KindCategory, KindProduct, KindForeignSubject} {
		if !strings.Contains(m[1], "'"+k+"'") {
			t.Errorf("DB CHECK 에 %q 가 없다 — 쓰기가 실패한다", k)
		}
	}
}

// TestLedgerNeverServes — 마이그레이션이 «일반명사는 active 가 될 수 없다»를 못 박는가.
func TestLedgerNeverServes(t *testing.T) {
	b := mustRead(t, "../../../migrations/0150_common_noun_ledger.sql")
	if !strings.Contains(b, "common_noun") || !strings.Contains(b, "CHECK (NOT (entity_type = 'common_noun' AND status = 'active'))") {
		t.Error("common_noun 이 active 가 되는 것을 막는 CHECK 가 없다 — 쿼리마다 조건을 적게 되고 그러면 빠뜨린다")
	}
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s 를 못 읽었다: %v", path, err)
	}
	return string(b)
}

// TestBackfillRefusesTheExpensiveMistakes — 등재는 «다시 묻지 마라»라서 오등재가 비싸다.
//
// ★첫 dry 목록에 조국(person · 7일 요청 10건 · Q12616590)이 들어 있었다. 그대로
// 등재했으면 최상위 수요 인물이 영구히 묻혔다. 세 가드가 그것을 막는다.
func TestBackfillRefusesTheExpensiveMistakes(t *testing.T) {
	b := mustRead(t, "commonnoun.go")
	i := strings.Index(b, "func BackfillFromNotes")
	if i < 0 {
		t.Fatal("BackfillFromNotes 가 없다")
	}
	win := b[i:]
	if j := strings.Index(win, "\nfunc "); j > 0 {
		win = win[:j]
	}
	for _, want := range []struct{ frag, why string }{
		{"kwave_entity_external_refs", "위키 앵커가 있는 행(조국·오너·페이즈)을 거르지 않는다"},
		{"!~ $3", "노트가 이름 붙인 0143 종류(연합회·대학교)를 거르지 않는다"},
		{"kwave_kdb_request_terms", "수요가 큰 낱말을 조용히 묻는다"},
		{"a.status='active'", "같은 이름이 서빙 중인데 등재한다"},
	} {
		if !strings.Contains(win, want.frag) {
			t.Errorf("%s (없는 조각: %q)", want.why, want.frag)
		}
	}
}
