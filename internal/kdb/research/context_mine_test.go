package research

import (
	"os"
	"strings"
	"testing"
)

// TestWorkerMinesRequestContext — 워커가 요청 문맥을 표기 근거로 쓰는지.
//
// 실측(2026-09-18): prepare 요청의 99%(6,370/6,425)가 근거 URL 과 문맥을 함께 보내는데
// 문맥은 게이트 판정에만 쓰이고 버려졌다. 답이 요청 안에 있는데 위키데이터에만 물은 뒤
// no_match 로 끝냈다 — 괄호 표기 255건 중 196건이 그렇게 죽었다.
func TestWorkerMinesRequestContext(t *testing.T) {
	src, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("worker.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "kdb.ExtractContextSpelling(koHint, contextHint)") {
		t.Fatal("워커가 문맥에서 표기를 뽑지 않는다 — 요청이 들고 온 근거를 버린다")
	}
	// ★값을 직접 쓰지 않고 관측으로 쌓아야 한다. 기사 하나로 표기를 바꾸면 안 된다.
	if !strings.Contains(body, "NewObservationStore(w.Pool).Save(") {
		t.Error("문맥 표기를 관측으로 쌓지 않는다 — 매체 합의를 우회하면 기사 하나가 표기를 바꾼다")
	}
	if strings.Contains(body, "UPDATE kwave_entities\n   SET canonical_en") {
		t.Error("문맥 표기를 canonical 에 직접 쓴다 — 합의를 거쳐야 한다")
	}
}

// TestContextMiningSkipsAmbiguousHan — 한자 괄호를 로케일로 단정하지 않는지.
//
// 한국 기사의 «홍길동(洪吉童)» 은 그 사람의 한자 이름이지 중국어·일본어 표기가 아니다.
// zh 로 넣으면 중국 독자에게 틀린 표기가 나간다.
func TestContextMiningSkipsAmbiguousHan(t *testing.T) {
	src, err := os.ReadFile("worker.go")
	if err != nil {
		t.Fatalf("worker.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "ExtractContextSpelling(koHint, contextHint)")
	if i < 0 {
		t.Fatal("추출 호출이 없다")
	}
	win := body[i:]
	if j := strings.Index(win, "\n\t// 3)"); j > 0 {
		win = win[:j]
	}
	if !strings.Contains(win, `cs.Locale != "en" && cs.Locale != "ja"`) {
		t.Error("en/ja 외 로케일을 걸러내지 않는다 — 한자 괄호가 zh 로 들어간다")
	}
	if !strings.Contains(win, "IsValidSpellingForLocale") {
		t.Error("로케일 문자 검증을 통과시키지 않는다")
	}
}

// TestSourceDomainOf — 관측의 매체 키가 정규화되는지. 합의는 매체 수로 세므로
// www 유무로 같은 매체가 둘로 갈리면 «독립 2곳» 이 가짜로 채워진다.
func TestSourceDomainOf(t *testing.T) {
	cases := map[string]string{
		"https://www.specialtimes.co.kr/news/articleView.html?idxno=463190": "specialtimes.co.kr",
		"https://specialtimes.co.kr/a":                                      "specialtimes.co.kr",
		"http://WWW.Bntnews.co.kr/x":                                        "bntnews.co.kr",
		"":                                                                  "",
		"not a url":                                                         "",
	}
	for in, want := range cases {
		if got := sourceDomainOf(in); got != want {
			t.Errorf("sourceDomainOf(%q) = %q, 기대 %q", in, got, want)
		}
	}
}
