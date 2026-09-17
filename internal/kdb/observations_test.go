package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestZhVariantColumnsAreNotInterchangeable — 간체 칸과 번체 칸을 구분하는지.
//
// 종전엔 `case "zh", "zh-hant":` 하나로 똑같이 취급해 한글·가나만 봤다. 자체(字體)를
// 안 보니 양방향 오염이 그대로 통과했다 — 2026-09-17 실측:
//
//	간체 칸에 번체 168건 (KBS→韓國放送公社 · 김종국→金鍾國 · 국립국악원→韓國國立國樂院)
//	번체 칸에 간체  46건 (고래별→鲸鱼星 · 넥슨코리아→乐线韩国 · 무쇠소녀단→钢铁少女团)
//
// 오너가 기본 언어를 en·ja·zh(간체, 본토)로 정했으므로 이건 본토 독자에게 그대로
// 번체가 나가는 문제다.
//
// ★공통 한자로만 된 표기는 **반드시 통과해야 한다.** 두 자체에 공통인 글자가 대부분이고
// (金·李·山·三·大…), 거기서 막으면 오거부가 된다 — 이 시스템의 최상위 금칙이다.
func TestZhVariantColumnsAreNotInterchangeable(t *testing.T) {
	// --- 막아야 하는 것: 한쪽 전용 글자가 반대 칸에 들어간 실제 오염 값 ---
	mustReject := []struct{ loc, val, why string }{
		{"zh", "韓國放送公社", "KBS — 간체 칸에 번체(본토는 韩国放送公社)"},
		{"zh", "金鍾國", "김종국 — 간체 칸에 번체"},
		{"zh", "韓國國立國樂院", "국립국악원 — 간체 칸에 번체"},
		{"zh-hant", "鲸鱼星", "고래별 — 번체 칸에 간체"},
		{"zh-hant", "乐线韩国", "넥슨코리아 — 번체 칸에 간체"},
		{"zh-hant", "钢铁少女团", "무쇠소녀단 — 번체 칸에 간체(tmdb 가 간체를 준 사례)"},
	}
	for _, c := range mustReject {
		if IsValidSpellingForLocale(c.loc, c.val) {
			t.Errorf("%s: %q 가 %s 칸을 통과했다 — %s", c.loc, c.val, c.loc, c.why)
		}
	}

	// --- 통과해야 하는 것: 오거부를 만들지 않는지 ---
	mustAccept := []struct{ loc, val, why string }{
		{"zh", "金俊秀", "공통 글자만 — 간체로도 번체로도 옳다"},
		{"zh-hant", "金俊秀", "같은 값이 번체 칸에서도 옳다"},
		{"zh", "李俊昊", "이준호 — 공통 글자"},
		{"zh", "三星", "공통 글자"},
		{"zh", "韩国放送公社", "제대로 된 간체"},
		{"zh-hant", "韓國放送公社", "제대로 된 번체"},
		{"zh", "Wavve", "라틴 브랜드명 — 한자가 없어도 통과(2026-08-15 계약)"},
		{"zh-hant", "HYBE Labels", "라틴 브랜드명"},
		{"zh", "少女时代", "간체"},
		{"zh-hant", "少女時代", "번체"},
	}
	for _, c := range mustAccept {
		if !IsValidSpellingForLocale(c.loc, c.val) {
			t.Errorf("%s: %q 를 거부했다 — %s (오거부)", c.loc, c.val, c.why)
		}
	}

	// --- 기존 계약이 깨지지 않았는지 ---
	if IsValidSpellingForLocale("zh", "俊한") {
		t.Error("한글 혼입을 통과시켰다")
	}
	if IsValidSpellingForLocale("zh", "ホジュン") {
		t.Error("가나를 통과시켰다")
	}
}

// TestZhVariantSetsDoNotOverlap — 두 전용 글자 목록에 같은 글자가 들어가면 그 글자는
// 어느 칸에서도 거부된다(양쪽 다 오거부). 겹침을 금지한다.
func TestZhVariantSetsDoNotOverlap(t *testing.T) {
	hant := hantOnlyRE.String()
	for _, r := range hansOnlyRE.String() {
		if r == '[' || r == ']' || r == '`' {
			continue
		}
		if strings.ContainsRune(hant, r) {
			t.Errorf("글자 %q 가 간체·번체 전용 목록 양쪽에 있다 — 양쪽에서 거부되어 오거부가 된다", string(r))
		}
	}
}

// TestZhRepairIsReachable — 수리 도구에 부르는 곳이 있는지.
//
// 이 저장소가 반복해 밟는 «장치는 있는데 아무도 안 켠» 을 막는다. RepairZhVariants 를
// 만들어 놓고 CLI 에 걸지 않으면 오염 214건은 그대로 남는다.
func TestZhRepairIsReachable(t *testing.T) {
	src, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "kdb.RepairZhVariants(") {
		t.Error("RepairZhVariants 를 부르는 곳이 없다 — 오염이 그대로 남는다")
	}
	if !strings.Contains(body, `os.Args[1] == "zh-repair"`) {
		t.Error("zh-repair 일회성 명령이 없다")
	}
	// 기본이 dry-run 이어야 한다. active 를 고치는 도구가 기본으로 쓰면 안 된다.
	i := strings.Index(body, `os.Args[1] == "zh-repair"`)
	if i < 0 {
		return
	}
	win := body[i:]
	if end := strings.Index(win, "RepairZhVariants("); end > 0 {
		win = win[:end]
	}
	if !strings.Contains(win, "dry := 500, true") && !strings.Contains(win, ", true") {
		t.Error("zh-repair 가 기본 dry-run 이 아니다 — 실수로 운영 데이터를 고칠 수 있다")
	}
}

// TestKeepProperNouns — OpenCC 가 한국 성씨를 일반 용법으로 과변환하는 것을 막는지.
//
// 실측(2026-09-17): 간체 朴哲焕 를 s2t 하면 樸哲煥 이 된다. «박»의 한자는 간체·번체
// 모두 朴 이므로 틀렸다. DB 에 131건이 樸 를 달고 있었고 108건이 우리 레인 작품이다.
// 권위 출처로 들어온 박훈 朴勳 은 온전했다 — 기계 변환만 틀렸다는 증거다.
func TestKeepProperNouns(t *testing.T) {
	cases := []struct{ src, out, want, why string }{
		{"朴哲焕", "樸哲煥", "朴哲煥", "박철환 — 성씨 朴 유지"},
		{"朴勋", "樸勳", "朴勳", "박훈"},
		{"姜大声", "薑大聲", "姜大聲", "강대성 — 성씨 姜 유지"},
		// 원문에 朴 가 없으면 건드리지 않는다 — 진짜 «소박하다» 는 樸 로 남아야 한다.
		{"质朴", "質樸", "質朴", "원문에 朴 가 있으면 되돌린다(같은 글자라 구분 불가 — 보수적으로 유지)"},
		{"简单", "簡單", "簡單", "관계없는 값은 그대로"},
		{"李俊昊", "李俊昊", "李俊昊", "바뀔 게 없는 값"},
	}
	for _, c := range cases {
		if got := keepProperNouns(c.src, c.out); got != c.want {
			t.Errorf("keepProperNouns(%q,%q) = %q, 기대 %q — %s", c.src, c.out, got, c.want, c.why)
		}
	}
}

// TestProvisionalIsAlwaysDisplaceable — 잠정 표기가 무엇에든 밀리는지.
//
// 이게 이 등급의 존재 이유다. 근거 없이 채운 값이 공신력 있는 값을 막으면 «채우는
// 편이 낫다»는 판단 자체가 무너진다(2026-09-17 오너 지시: "우선순위를 뒤로 두는거지
// 그리고 추후 공신력기관에서 찾아 업데이트되면 채우는 방식").
func TestProvisionalIsAlwaysDisplaceable(t *testing.T) {
	provPrio := Priority(SourceLLMProvisional)

	// 어떤 실제 출처보다도 낮아야(숫자가 커야) 한다.
	for _, s := range []Source{
		SourceOperatorLocked, SourceWikidataLabel, SourceWikipediaLanglinks,
		SourceRomanization, SourceOpenCC, SourceGTranslate, SourceCodexFallback,
	} {
		if p := Priority(s); p >= provPrio {
			t.Errorf("%s 의 우선순위 %d 가 잠정 %d 보다 낮거나 같다 — 잠정값이 실제 근거를 막는다",
				s, p, provPrio)
		}
	}
	// 기계번역보다도 아래여야 한다 — 번역기는 적어도 번역기라는 근거가 있다.
	if provPrio <= Priority(SourceGTranslate) {
		t.Error("잠정이 gtranslate 보다 우선이다 — 근거 없는 값이 번역기를 이긴다")
	}
	// 알 수 없는 출처(99)보다는 위여야 한다. 그래야 빈칸/미상 자리를 채운다.
	if provPrio >= Priority(Source("아무거나-없는-출처")) {
		t.Error("잠정이 미상 출처보다 낮다 — 채울 수가 없다")
	}
}

// TestProvisionalLocalesIsOptIn — 스위치가 꺼져 있으면 아무것도 안 바뀌는지.
//
// strict 는 8개 로케일을 지킨다. 통째로 끄면 근거 없는 추측이 모든 언어로 퍼진다.
// 그래서 로케일별 opt-in 이어야 하고, 기본은 «아무것도 안 함» 이어야 한다.
func TestProvisionalLocalesIsOptIn(t *testing.T) {
	t.Setenv("KDB_ENRICH_PROVISIONAL_LOCALES", "")
	if got := ProvisionalLocales(); len(got) != 0 {
		t.Errorf("설정이 비었는데 %v 를 켰다 — 기본은 아무것도 안 하는 것이어야 한다", got)
	}
	t.Setenv("KDB_ENRICH_PROVISIONAL_LOCALES", "zh, zh_hant")
	got := ProvisionalLocales()
	for _, want := range []string{"zh", "zh_hant", "canonical_zh", "canonical_zh_hant"} {
		if !got[want] {
			t.Errorf("%q 가 잠정 대상에 없다 — 호출측이 컬럼명으로도 물어본다", want)
		}
	}
	if got["ja"] || got["canonical_ja"] {
		t.Error("지정하지 않은 로케일이 켜졌다 — 다른 언어로 추측이 퍼진다")
	}
}
