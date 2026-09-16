package kentity

import (
	"strconv"
	"strings"
	"testing"
)

// TestCatalogScopeMakesOneClausePerGivenFilter — **안 준 필터는 절을 안 만든다.**
//
// ★왜 시험으로 고정하나 (2026-09-16). 종전 모양은 필터마다 `$n='' OR …` 로 분기했다.
// 값이 무엇이냐에 따라 뜻이 달라지므로 계획기가 EXISTS 를 **세미조인으로 못 접는다** —
// 555,877행마다 상관 서브플랜을 돌렸다. `?domain=politics&status=candidate` 한 화면이
// 4초를 먹었고 핸들러 예산이 5초라 조금만 느려지면 «조회 실패»가 떴다(회귀 2회 실패).
//
//	목록 질의   361.617 ms → 0.250 ms
//	총계 질의   107.394 ms → 0.127 ms   (회귀 DB 실측)
//
// 이 파일은 같은 덫을 이미 한 번 겪었다(FILTER 안 상관 서브쿼리 1,024ms → 97ms).
// 그때는 산술로 피했고 여기서는 절을 아예 안 만들어 피한다.
func TestCatalogScopeMakesOneClausePerGivenFilter(t *testing.T) {
	// 빈 필터 → 조건 없음. 남는 것은 WHERE true 뿐이다.
	where, args := catalogScope(CatalogFilter{})
	if strings.Contains(where, "AND") || len(args) != 0 {
		t.Errorf("빈 필터인데 조건이 생겼다: %q args=%v", where, args)
	}

	// domain 을 주면 EXISTS 절 **하나**만. `=''  OR` 분기가 남아 있으면 안 된다.
	where, args = catalogScope(CatalogFilter{Domain: "politics", Status: "candidate"})
	if strings.Contains(where, "=''") {
		t.Errorf("파라미터 빈값 분기가 남아 있다 — 세미조인으로 안 접힌다: %q", where)
	}
	if strings.Count(where, "EXISTS") != 1 {
		t.Errorf("domain 절이 하나가 아니다: %q", where)
	}
	if len(args) != 2 || args[0] != "politics" || args[1] != "candidate" {
		t.Errorf("인자가 절과 안 맞는다: %v", args)
	}

	// unassigned 는 NOT EXISTS 이고 값을 파라미터로 보내지 않는다.
	where, args = catalogScope(CatalogFilter{Domain: "unassigned"})
	if !strings.Contains(where, "NOT EXISTS") || len(args) != 0 {
		t.Errorf("unassigned 처리가 틀렸다: %q args=%v", where, args)
	}

	// in_scope 는 값이 아니라 뜻이다 — 파라미터로 나가면 e.status='in_scope' 가 된다.
	where, args = catalogScope(CatalogFilter{Status: "in_scope"})
	if !strings.Contains(where, "e.status<>'rejected'") || len(args) != 0 {
		t.Errorf("in_scope 처리가 틀렸다: %q args=%v", where, args)
	}

	// ★자리표 번호는 **인자 개수와 끝까지 맞아야 한다.** 목록 질의가 뒤에 LIMIT/OFFSET 을
	//   $n+1·$n+2 로 붙이므로, 하나라도 어긋나면 "bind message supplies N parameters" 로 죽는다.
	full := CatalogFilter{Q: "김", Type: "person", Domain: "sports", Status: "active",
		Origin: "kdb", Period: "24h", Classify: "pending"}
	where, args = catalogScope(full)
	for i := 1; i <= len(args); i++ {
		if !strings.Contains(where, "$"+strconv.Itoa(i)) {
			t.Errorf("$%d 가 절에 없다 — 인자와 자리표가 어긋났다: %q", i, where)
		}
	}
	if strings.Contains(where, "$"+strconv.Itoa(len(args)+1)) {
		t.Errorf("인자보다 자리표가 많다: %q args=%d", where, len(args))
	}
	// Period·Classify 는 값이 아니라 뜻이라 파라미터가 아니다.
	if len(args) != 5 {
		t.Errorf("파라미터 개수가 예상과 다르다: %d (%v)", len(args), args)
	}
}
