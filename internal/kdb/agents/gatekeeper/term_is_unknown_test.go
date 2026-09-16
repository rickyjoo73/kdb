package gatekeeper

import (
	"strings"
	"testing"
)

// TestTermMeansUnknownBoxNotCommonNoun — `term` 은 "일반어다"가 아니라
// **"어느 칸인지 모르겠다"** 이다.
//
// ★실측 (2026-09-16). type=term 이면 통째로 기각했다. 30일 195건인데 표본이
// 거의 전부 진짜 고유명사였다:
//
//	탭! 탭! 레이서즈 · 리듬 러너! · 엑소스 히어로즈 · 드래곤 레이드(게임)
//	무기의 신 · 권력의 문장(웹툰)   환상동화 · 젊은 베르테르의 슬픔(뮤지컬)
//	유애나(팬덤명)                  강수그룹(회사)
//
// 담을 칸이 없던 시절 소비자는 모르는 것을 term 으로 보냈고, 우리는 그것을
// "일반어라고 소비자가 말했다"로 읽었다. 칸이 열 개 늘었다(0143·0146).
func TestTermMeansUnknownBoxNotCommonNoun(t *testing.T) {
	for _, term := range []string{"테일즈런너", "엑소스 히어로즈", "무기의 신", "환상동화", "강수그룹"} {
		d := DecideIntake(IntakeInput{Term: term, EntityType: "term"})
		if d.Verdict == IntakeReject && d.ReasonCode == "term_not_proper_noun" {
			t.Errorf("%q 를 유형만 보고 기각했다", term)
		}
	}
	// ★진짜 일반어는 여전히 막힌다 — **이름으로** 막는다(categoryOnlyTerms).
	for _, term := range []string{"가수", "배우", "드라마", "컴백"} {
		d := DecideIntake(IntakeInput{Term: term, EntityType: "term"})
		if d.Verdict != IntakeReject {
			t.Errorf("일반어 %q 가 통과했다: %v/%s", term, d.Verdict, d.ReasonCode)
		}
	}
	// 상거래 어미도 그대로 막힌다.
	if d := DecideIntake(IntakeInput{Term: "합정역광고", EntityType: "term"}); d.Verdict != IntakeReject {
		t.Errorf("상거래 키워드가 통과했다: %v/%s", d.Verdict, d.ReasonCode)
	}
}

// TestTypeFromContextCuesOnlyWhenUnambiguous — 문맥이 **한 유형만** 가리킬 때만 쓴다.
//
// 운영자 지시: "우리가 가이드를 제대로 주면 기사원문에서 분류해서 모두 올려줄거야
// 그것을 기반으로 하면 되지." 다만 단서가 갈리면 추측이 된다 — 유형을 틀리게 붙이면
// 그 대상의 표기가 다른 유형의 규칙으로 채워진다(빈칸보다 나쁘다, D-37).
func TestTypeFromContextCuesOnlyWhenUnambiguous(t *testing.T) {
	cues := TypeCues()
	pick := func(typ string) string {
		if len(cues[typ]) == 0 {
			t.Fatalf("%s 에 단서가 없다 — 표가 비었다", typ)
		}
		return cues[typ][0]
	}
	// 한 유형만 가리키는 문맥.
	ctx := "모바일 " + pick("game") + " 테일즈런너가 업데이트를 예고했다"
	got, ok := TypeFromContextCues(ctx, "테일즈런너")
	if !ok || got != "game" {
		t.Errorf("게임 단서를 못 읽었다: (%q,%v) ctx=%q", got, ok, ctx)
	}
	// 문맥이 없으면 추론하지 않는다.
	if _, ok := TypeFromContextCues("", "테일즈런너"); ok {
		t.Error("빈 문맥에서 유형을 지어냈다")
	}
	// 표제어가 문맥에 없으면 추론하지 않는다 — 옆 문단의 단서가 붙으면 안 된다.
	if _, ok := TypeFromContextCues("전혀 다른 기사 본문 "+pick("game"), "테일즈런너"); ok {
		t.Error("표제어가 없는 문맥에서 유형을 붙였다")
	}
	// 단서가 갈리면 쓰지 않는다.
	mixed := pick("game") + " 테일즈런너 " + pick("movie")
	if got, ok := TypeFromContextCues(mixed, "테일즈런너"); ok {
		t.Errorf("갈리는 단서로 %q 를 골랐다", got)
	}
}

// TestInferredTypeReachesTheCaller — 추론한 유형이 호출부에 **돌아가야 한다.**
// 게이트만 알고 원장이 모르면 다음 라운드가 같은 요청을 다시 term 으로 본다.
func TestInferredTypeReachesTheCaller(t *testing.T) {
	cue := TypeCues()["game"][0]
	d := DecideIntake(IntakeInput{
		Term: "테일즈런너", EntityType: "term",
		Context: "모바일 " + cue + " 테일즈런너가 업데이트를 예고했다",
	})
	if d.ResolvedType != "game" {
		t.Errorf("ResolvedType 이 비었다(%q) — 추론이 큐에 안 남는다", d.ResolvedType)
	}
	var flagged bool
	for _, f := range d.Flags {
		if strings.HasPrefix(f, "type_from_context_cue:") {
			flagged = true
		}
	}
	if !flagged {
		t.Errorf("추론 근거가 플래그에 안 남았다: %v", d.Flags)
	}
}

// TestRuleVersionMovedSoOldVerdictsExpire — 규칙을 바꿨으면 판본을 올린다.
// 안 올리면 옛 규칙으로 내린 종결이 그대로 재생되어, 고친 것이 소비자에게 안 닿는다.
func TestRuleVersionMovedSoOldVerdictsExpire(t *testing.T) {
	if IntakeRuleVersion == "scope-korea-v4-20260915" {
		t.Error("term 기각 규칙을 바꿨는데 판본이 그대로다 — 옛 종결이 만료되지 않는다")
	}
}
