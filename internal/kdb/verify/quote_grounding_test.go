package verify

import (
	"os"
	"strings"
	"testing"
)

// ★이 시험의 절반은 «막는가»가 아니라 «멀쩡한 것을 붙잡지 않는가»다.
//
//	먼저 만든 규칙(판정문에서 고유명사를 뽑아 근거와 대조)은 운영 데이터 2,026건 중
//	1,251건(62%)을 잡았는데 대부분 오탐이었다 — "김재환의"(조사) · "NiziU"(근거는
//	"니쥬") · "장편영화"(일반어). 오거부는 이 저장소의 최상위 금칙이라, 관대한 쪽으로
//	틀리게 만들어 두고 그 관대함을 시험으로 고정한다.

func TestQuoteGroundedAcceptsVerbatim(t *testing.T) {
	hits := []string{
		"고막 남친 ‘김재환’의 섬세한 감성 ‘찾지 않을게’ — sbs.co.kr",
		"[뮤즈★] 지윤서, 해군 군악대 입대 — Mnet 디지털 채널 M2의 새 리얼리티 ‘이븐아워’를...",
	}
	for _, q := range []string{
		"고막 남친 ‘김재환’의 섬세한 감성",        // 그대로
		"고막 남친 '김재환'의 섬세한 감성",        // 따옴표 모양만 다름
		"고막남친 ‘김재환’의 섬세한 감성",         // 공백 차이
		"Mnet 디지털 채널 M2의 새 리얼리티",     // 두 번째 스니펫
		"  Mnet 디지털 채널 M2의 새 리얼리티  ", // 앞뒤 공백
	} {
		if !QuoteGrounded(q, hits) {
			t.Errorf("멀쩡한 인용을 붙잡았다: %q", q)
		}
	}
}

// ★지어낸 인용은 막는다. 운영에서 실제로 나온 것들이다.
func TestQuoteGroundedRejectsFabricated(t *testing.T) {
	// 유성영: 근거 4건이 전부 다른 사람이다. gemma 는 "SF9 유성영" 이라 적었다.
	hits := []string{
		"[유성영의 골프피팅] 골프클럽을 알면 골프가 더 재밌다",
		"악필교정 전문가 유성영의 『빠른 글씨 바른 글씨』 출간",
		"핵융합연 유성영 선임 ‘2025 기업가정신 주간’서 이사장 표창",
	}
	for _, q := range []string{
		"미라클 유성영",            // gemma 가 인용이라며 지어낸 구절
		"SF9 멤버 유성영으로 소개되었다", // 스니펫에 없다
		"",    // 못 댔다
		"가수",  // 너무 짧다 — 아무 데나 있다
		"유성영", // 이름만으론 근거가 아니다
	} {
		if QuoteGrounded(q, hits) {
			t.Errorf("지어낸 인용을 통과시켰다: %q", q)
		}
	}
}

// ★짧은 인용은 대조해도 의미가 없다. "가수" 두 글자는 어떤 연예 기사에나 있다.
//
//	길이는 **공백을 뺀 뒤** 센다. 공백은 근거를 담지 않고, 스니펫마다 줄바꿈·전각
//	공백이 제각각이라 원문 길이로 세면 같은 인용이 통과했다 말았다 한다.
//	그래서 문턱 10자는 실제로는 띄어쓰기 포함 12~14자쯤이다.
func TestShortQuoteIsNotEvidence(t *testing.T) {
	hits := []string{"가수 아이유가 신곡을 발표했다고 소속사가 밝혔다"}
	for _, tooShort := range []string{
		"가수",
		"가수 아이유가 신곡을", // 공백 빼면 9자 — 문턱 아래다
	} {
		if QuoteGrounded(tooShort, hits) {
			t.Errorf("짧은 인용을 근거로 인정했다: %q", tooShort)
		}
	}
	if !QuoteGrounded("가수 아이유가 신곡을 발표했다", hits) {
		t.Error("충분히 긴 실제 인용을 붙잡았다")
	}
}

// ★프롬프트에 **실제 그룹 이름을 예시로 적지 않는다.**
//
//	종전 프롬프트가 identity 예시로 «예: SF9 멤버» 를 줬고, 그 이름이 답으로 샜다 —
//	identity 에 SF9 가 들어간 활성 행 5건 중 4건의 근거에 SF9 가 한 줄도 없었다
//	(옌안은 근거가 전부 중국 도시 옌안 기사였다). 예시를 주면 근거가 빈 자리를
//	그 예시로 메운다.
func TestPromptCarriesNoConcreteGroupExample(t *testing.T) {
	b, err := os.ReadFile("verify_evidence.go")
	if err != nil {
		t.Fatalf("verify_evidence.go 를 못 읽었다: %v", err)
	}
	// 프롬프트를 만드는 함수 본문만 본다 — 위 주석에는 사연으로 적혀 있어야 한다.
	src := string(b)
	i := strings.Index(src, "func buildVerifyPrompt(")
	if i < 0 {
		t.Fatal("buildVerifyPrompt 를 못 찾았다")
	}
	body := src[i:]
	if j := strings.Index(body, "\nfunc "); j > 0 {
		body = body[:j]
	}
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue // 주석은 사연이다 — 프롬프트로 안 나간다
		}
		for _, banned := range []string{"SF9", "SM엔터", "하이브"} {
			if strings.Contains(line, banned) {
				t.Errorf("프롬프트에 실제 이름 %q 가 예시로 들어갔다 — 그대로 답으로 샌다: %s",
					banned, strings.TrimSpace(line))
			}
		}
	}
	// 그리고 베껴 내라는 요구가 실제로 있어야 한다.
	if !strings.Contains(body, "quote") {
		t.Error("프롬프트가 인용을 요구하지 않는다 — 대조할 것이 없어진다")
	}
}

// ★승급하는 문 **셋 모두**에 같은 규칙이 걸려 있어야 한다.
//
//	한 곳만 막으면 나머지 둘로 그대로 들어온다. 이 저장소가 여러 번 겪은 패턴이라
//	(“네 곳이 같은 명제를 들고 있다”) 문 수를 시험으로 고정한다.
func TestQuoteGateOnEveryPromotionDoor(t *testing.T) {
	for _, f := range []string{"candidate_evidence.go", "verify_evidence.go", "active_audit.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("%s 를 못 읽었다: %v", f, err)
		}
		if !strings.Contains(string(b), "QuoteGrounded(v.Quote, hits)") {
			t.Errorf("%s 에 인용 대조가 없다 — 이 문으로는 지어낸 근거가 그대로 승급된다", f)
		}
	}
}

// ★막는 것이 **기각이 아니라 보류**여야 한다. 근거를 못 댄 것과 K-엔티티가
// 아닌 것은 다르다. 후자로 적으면 오거부가 되고, 그건 최상위 금칙이다.
func TestUngroundedQuoteHoldsRatherThanRejects(t *testing.T) {
	b, err := os.ReadFile("candidate_evidence.go")
	if err != nil {
		t.Fatalf("candidate_evidence.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	i := strings.Index(src, "QuoteGrounded(v.Quote, hits)")
	if i < 0 {
		t.Fatal("인용 대조를 못 찾았다")
	}
	block := src[i:min(i+600, len(src))]
	if strings.Contains(block, "contaminated") {
		t.Error("인용 미확인을 오염으로 적는다 — 오거부다")
	}
	if !strings.Contains(block, "markCandEvidenceInsufficient") {
		t.Error("보류 기록을 안 남긴다 — 같은 건이 매 라운드 다시 뽑힌다")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
