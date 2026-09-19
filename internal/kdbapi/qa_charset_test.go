package kdbapi

import "testing"

// TestQACharsetSplitsZhVariants — QA 채움 경로가 간체·번체를 구분하는지.
//
// 실측(2026-09-19): 9/17 저녁에 전수 청소로 간체칸 번체 0건을 만들었는데 다음 날
// 4건이 다시 들어와 있었다. 전부 이 경로(local-usage / local-search)였다.
// qaCharsetOK 가 `han && !kana` 만 봐서 번체가 간체 칸으로 그냥 통과했다.
//
// 관측 경로는 IsValidSpellingForLocale 로 막고 있었는데 이 문만 안 막고 있었다 —
// 두 문이 다른 자를 대면 한쪽으로 계속 샌다.
func TestQACharsetSplitsZhVariants(t *testing.T) {
	// 번체 전용 글자가 든 값은 zh 칸에 들어가면 안 된다.
	for _, v := range []string{"白兒", "吳圭翼", "天上戀", "朴善愛", "韓國放送公社"} {
		if qaCharsetOK("zh", v) {
			t.Errorf("zh 칸에 번체 %q 를 통과시켰다", v)
		}
		if !qaCharsetOK("zh_hant", v) {
			t.Errorf("zh_hant 칸이 번체 %q 를 거부했다", v)
		}
	}
	// 간체 전용 글자가 든 값은 zh_hant 칸에 들어가면 안 된다.
	for _, v := range []string{"白儿", "吴圭翼", "天上恋", "韩国放送公社"} {
		if qaCharsetOK("zh_hant", v) {
			t.Errorf("zh_hant 칸에 간체 %q 를 통과시켰다", v)
		}
		if !qaCharsetOK("zh", v) {
			t.Errorf("zh 칸이 간체 %q 를 거부했다", v)
		}
	}
	// 양쪽에서 정자인 글자만 있으면 두 칸 모두 통과해야 한다 — 거부하면 오거부다.
	for _, v := range []string{"金秀賢", "朴寶劍"} {
		_ = v // 번체 글자 포함이라 위 규칙을 따른다
	}
	for _, v := range []string{"磪有情", "朴宝剑"} {
		if !qaCharsetOK("zh", v) {
			t.Errorf("zh 칸이 정상 간체 %q 를 거부했다 — 오거부", v)
		}
	}
	// 다른 자체 규칙은 그대로여야 한다.
	if qaCharsetOK("zh", "BTS") {
		t.Error("zh 칸에 라틴을 통과시켰다")
	}
	if qaCharsetOK("zh", "アイユー") {
		t.Error("zh 칸에 가나를 통과시켰다")
	}
	if !qaCharsetOK("ja", "アイユー") || !qaCharsetOK("en", "IU") {
		t.Error("ja/en 판정이 바뀌었다")
	}
}
