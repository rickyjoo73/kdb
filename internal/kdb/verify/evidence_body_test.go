package verify

import (
	"os"
	"strings"
	"testing"
)

// ★구글뉴스 RSS 줄은 본문이 아니다. `제목 — 매체명` 이 전부다.
func TestGoogleNewsLineIsNotBody(t *testing.T) {
	h := evHit{
		Title:    "세븐틴 호시, 14일 신곡 '아기자기' 공개…로맨틱한 감성으로 솔로 존재감",
		Line:     "세븐틴 호시, 14일 신곡 '아기자기' 공개…로맨틱한 감성으로 솔로 존재감 — the-biz.co.kr",
		Provider: "google-news",
	}
	if hasBody(h) {
		t.Error("제목+매체명을 본문으로 쳤다 — 판정기가 읽을 것이 없는데 있다고 센다")
	}
}

// ★네이버 줄은 본문이다. description 이 붙는다.
func TestNaverLineIsBody(t *testing.T) {
	h := evHit{
		Title:    "세븐틴 호시, 군복무 중 솔로곡 공개",
		Line:     "세븐틴 호시, 군복무 중 솔로곡 공개 — 그룹 세븐틴의 호시가 군복무 중 깜짝 신곡을 공개했다. 소속사는 14일 정오 음원을 발매한다고 밝혔다.",
		Provider: "naver-news",
	}
	if !hasBody(h) {
		t.Error("네이버 description 을 본문이 아니라고 했다 — 네이버를 부르고도 안 쓴다")
	}
}

// ★출처 이름으로 가르지 않는다. 구글이 언젠가 description 을 주면 그때는 본문이다.
func TestBodyIsJudgedByContentNotProvider(t *testing.T) {
	rich := evHit{
		Title:    "짧은 제목",
		Line:     "짧은 제목 — 이 기사는 실제 본문 요약을 담고 있으며 제목 너머의 내용을 충분히 전달한다고 볼 수 있다.",
		Provider: "google-news", // 출처는 구글인데 내용은 있다
	}
	if !hasBody(rich) {
		t.Error("출처 이름으로 판정하고 있다 — 내용을 봐야 한다")
	}
	b, err := os.ReadFile("verify_evidence.go")
	if err != nil {
		t.Fatalf("verify_evidence.go 를 못 읽었다: %v", err)
	}
	i := strings.Index(string(b), "func hasBody(")
	if i < 0 {
		t.Fatal("hasBody 를 못 찾았다")
	}
	body := string(b)[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	if strings.Contains(body, "google-news") || strings.Contains(body, "naver-news") {
		t.Error("hasBody 가 출처 이름을 본다 — 출처가 바뀌면 판정이 낡는다")
	}
}

func TestHasBodyHit(t *testing.T) {
	titleOnly := []evHit{
		{Title: "제목 하나", Line: "제목 하나 — 매체A"},
		{Title: "제목 둘", Line: "제목 둘 — 매체B"},
	}
	if hasBodyHit(titleOnly) {
		t.Error("제목뿐인 묶음을 본문 있음으로 쳤다 — 네이버를 안 부르게 된다")
	}
	withBody := append(append([]evHit{}, titleOnly...),
		evHit{Title: "제목 셋", Line: "제목 셋 — 본문이 충분히 붙어 있어서 제목 너머로 읽을 내용이 실제로 존재하는 줄이다."})
	if !hasBodyHit(withBody) {
		t.Error("본문이 있는데 없다고 했다 — 쿼터를 헛되이 쓴다")
	}
	if hasBodyHit(nil) {
		t.Error("빈 묶음을 본문 있음으로 쳤다")
	}
}

// ★네이버 호출 조건이 «건수»가 아니라 «본문 유무» 여야 한다.
//
//	종전 `len(hits) < 2` 는 구글뉴스가 한 번에 5건을 주므로 사실상 실행되지 않았다.
//	오늘 승급 285건 중 238건(84%)이 구글뉴스 단독이었다.
func TestNaverIsTriggeredByMissingBodyNotCount(t *testing.T) {
	b, err := os.ReadFile("verify_evidence.go")
	if err != nil {
		t.Fatalf("verify_evidence.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "nv.Search(ctx, \"news\"")
	if i < 0 {
		t.Fatal("네이버 호출을 못 찾았다")
	}
	head := src[max0(i-300):i]
	if strings.Contains(head, "len(hits) < 2 && nv != nil") {
		t.Error("네이버가 아직 건수 조건에 걸려 있다 — 구글이 5건을 주면 영영 안 불린다")
	}
	if !strings.Contains(head, "hasBodyHit(hits)") {
		t.Error("본문 유무로 네이버를 부르지 않는다")
	}
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ★예산 가드가 실제로 상한을 지키는가.
//
//	네이버를 «본문이 없으면» 부르게 바꾸면 판정마다 한 번씩 부르게 된다. 쿼터는
//	1,000/일인데 유입 레인이 이미 600을 쓴다. 가드가 없으면 쿼터가 떨어지고,
//	그 순간 전송실패가 «근거 없음》으로 읽혀 멀쩡한 엔티티가 내려간다.
func TestNaverBudgetStopsAtLimit(t *testing.T) {
	t.Setenv("KDB_VERIFY_NAVER_DAILY_CALLS", "3")
	naverBudgetMu.Lock()
	naverBudgetDay, naverBudgetUsed = "", 0
	naverBudgetMu.Unlock()

	for i := 1; i <= 3; i++ {
		if !naverBudgetTake() {
			t.Fatalf("%d 번째 호출이 예산 안인데 거부됐다", i)
		}
	}
	if naverBudgetTake() {
		t.Fatal("상한을 넘겨 호출을 허용했다 — 쿼터가 터진다")
	}
	used, limit := NaverBudgetSnapshot()
	if used != 3 || limit != 3 {
		t.Fatalf("집계가 어긋난다: used=%d limit=%d", used, limit)
	}
}

// 날이 바뀌면 예산이 되살아난다.
func TestNaverBudgetResetsDaily(t *testing.T) {
	t.Setenv("KDB_VERIFY_NAVER_DAILY_CALLS", "1")
	naverBudgetMu.Lock()
	naverBudgetDay, naverBudgetUsed = "1999-01-01", 99
	naverBudgetMu.Unlock()
	if !naverBudgetTake() {
		t.Fatal("어제 예산이 오늘을 막고 있다")
	}
}

// ★예산이 떨어져도 **판정을 멈추지 않는다.** 구글뉴스만으로라도 간다 —
// 예산 소진을 «근거 없음》으로 만들면 그게 이 파일이 8-15 에 겪은 그 실패다.
func TestBudgetExhaustionDoesNotBlockJudgement(t *testing.T) {
	b, err := os.ReadFile("verify_evidence.go")
	if err != nil {
		t.Fatalf("verify_evidence.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "naverBudgetTake()")
	if i < 0 {
		t.Fatal("예산 가드를 못 찾았다")
	}
	// 가드는 네이버 호출 조건에만 걸려야 한다 — 함수를 일찍 끝내면 안 된다.
	seg := src[i:min2(i+200, len(src))]
	if strings.Contains(seg, "return nil") || strings.Contains(seg, "return hits, false") {
		t.Error("예산이 떨어지면 판정 자체를 접는다 — 소진이 «근거 없음》으로 읽힌다")
	}
}

func min2(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ★예산 집계를 **부르는 곳이 있어야 한다.** 만들어 놓고 안 부르면 남은 예산을
// 알 방법이 없고, 기본값 400 이 맞는 수인지도 영영 모른다.
func TestBudgetSnapshotIsActuallyLogged(t *testing.T) {
	b, err := os.ReadFile("../../../cmd/kdb/main.go")
	if err != nil {
		t.Skipf("main.go 를 못 읽었다(경로 다름): %v", err)
	}
	if !strings.Contains(string(b), "verify.NaverBudgetSnapshot()") {
		t.Error("예산 집계를 아무도 안 부른다 — 남은 예산을 볼 방법이 없다")
	}
}
