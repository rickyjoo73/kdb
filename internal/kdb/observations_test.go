package kdb

import (
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
