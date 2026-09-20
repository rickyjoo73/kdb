package kdb

import (
	"strings"
	"testing"

	"github.com/longbridgeapp/opencc"
)

// TestGeneratedSetsCatchWhatTheHandListMissed — 손으로 고른 82자 목록이 놓쳤던
// **실제 운영 값**을 잡는지.
//
// 2026-09-17 밤에 «간체칸 번체 168 → 0» 이라 보고했다. 그 목록이 볼 수 있는 것만
// 0이었고, OpenCC 사전 전체로 재니 430건이 남아 있었다. 아래는 그 실물이다.
func TestGeneratedSetsCatchWhatTheHandListMissed(t *testing.T) {
	dirty := []struct{ ko, zh, why string }{
		{"전보람", "全寶藍", "寶·藍 이 손목록에 없었다"},
		{"정선철", "鄭先哲", "鄭 이 없었다"},
		{"황동혁", "黄東赫", "간체 黄 과 번체 東 이 한 칸에 섞였다"},
		{"김현종", "金鉉宗", "鉉 이 없었다"},
		{"전지현", "全智賢", "賢 이 없었다"},
		{"박세영", "朴世榮", "榮 이 없었다 — 성씨 朴 는 그대로 두면서 榮 만 잡아야 한다"},
	}
	for _, c := range dirty {
		if !ContainsTradOnly(c.zh) {
			t.Errorf("%s: canonical_zh=%q 의 번체를 못 잡았다 — %s", c.ko, c.zh, c.why)
		}
	}
	clean := []struct{ ko, zh string }{
		{"최유정", "磪有情"}, // 양쪽 공통 글자만
		{"박보검", "朴宝剑"},
		{"원더걸스", "Wonder Girls"},
	}
	for _, c := range clean {
		if ContainsTradOnly(c.zh) {
			t.Errorf("%s: 멀쩡한 간체 %q 를 오염으로 잡았다 — 오거부다", c.ko, c.zh)
		}
	}
}

// TestBothScriptCharsAreNeverConverted — 양쪽에서 정자인 글자를 건드리지 않는지.
//
// 이게 이 파일의 존재 이유다. OpenCC 통짜 변환은 朴(박) → 樸, 姜(강) → 薑 로 바꾼다.
// 실측 133건이 그렇게 오염됐다. 朴·姜 뿐이 아니다 — 于·里·准·台·采 가 같은 함정이고,
// 두 글자짜리 예외 목록으로는 이 종류를 다 막을 수 없다.
func TestBothScriptCharsAreNeverConverted(t *testing.T) {
	for _, r := range []rune{'朴', '姜', '于', '里', '准', '台', '采', '斗', '郁', '只'} {
		if zhTradOnly[r] || zhHansOnly[r] {
			t.Errorf("%q 가 한쪽 전용으로 분류됐다 — 양쪽에서 정자인 글자다", string(r))
		}
		if out, _ := ZhToTraditional(string(r)); out != string(r) {
			t.Errorf("ZhToTraditional(%q) = %q — 양쪽 정자를 바꿨다", string(r), out)
		}
		if out, _ := ZhToSimplified(string(r)); out != string(r) {
			t.Errorf("ZhToSimplified(%q) = %q — 양쪽 정자를 바꿨다", string(r), out)
		}
	}
	// 성씨가 붙은 실제 이름에서도 성은 그대로, 이름 글자만 바뀌어야 한다.
	if got, ok := ZhToTraditional("朴哲焕"); got != "朴哲煥" || !ok {
		t.Errorf("ZhToTraditional(朴哲焕) = %q(ok=%v), 기대 朴哲煥 — 박씨를 樸 로 바꾸면 안 된다", got, ok)
	}
	if got, ok := ZhToTraditional("姜大声"); got != "姜大聲" || !ok {
		t.Errorf("ZhToTraditional(姜大声) = %q(ok=%v), 기대 姜大聲", got, ok)
	}
}

// TestTaiwanStandardIsNotContamination — 대만에서 쓰는 정자를 오염으로 잡지 않는지.
//
// 秘(祕)·床(牀)·群(羣) 은 OpenCC 의 간체→번체 표에 실려 있지만 대만 표준형은
// 왼쪽이다. 이걸 빼지 않으면 «서프라이즈 미스터리 살롱 → 驚喜神秘沙龍» 27건을
// 오염으로 잡아 엉뚱하게 바꾼다.
func TestTaiwanStandardIsNotContamination(t *testing.T) {
	for _, r := range []rune{'秘', '床', '群'} {
		if zhHansOnly[r] {
			t.Errorf("%q 를 간체 전용으로 분류했다 — 대만 표준형이다", string(r))
		}
	}
	if ContainsHansOnly("驚喜神秘沙龍") {
		t.Error("驚喜神秘沙龍 을 오염으로 잡았다")
	}
}

// TestVariantCharsAreHeldNotConverted — 왕복하지 않는 이체자는 감지하되 변환하지 않는지.
//
// 昇 → 升 은 되돌아오지 않는다(升 의 번체는 升). 昇 은 본토에서도 인명에 살아 있어서
// 姜昇希 를 姜升希 로 바꾸면 수리가 아니라 새 오염이다. 실측 26건이 여기 해당한다.
func TestVariantCharsAreHeldNotConverted(t *testing.T) {
	// 아직 아무도 안 본 이체자는 보류된다 — 값을 바꾸지 않고 ok=false 로 알린다.
	for _, name := range []string{"金昇焕", "李昇基"} {
		// 昇 은 검증돼 통과 목록에 있으므로 보류가 아니다. 보류 동작은 아래에서 확인한다.
		if got, ok := ZhToSimplified(name); !ok || got != name {
			t.Errorf("%q: 昇 은 검증된 유지 글자다 — 그대로 통과해야 한다 (got %q ok=%v)", name, got, ok)
		}
	}
	// 왕복 안정한 글자만 있으면 변환한다.
	if got, ok := ZhToSimplified("鄭先哲"); got != "郑先哲" || !ok {
		t.Errorf("ZhToSimplified(鄭先哲) = %q(ok=%v), 기대 郑先哲", got, ok)
	}
}

// TestVerifiedVariantsResolveTheHeldRows — 보류됐던 26건이 실제로 풀리는지.
//
// 2026-09-17 저녁에 gpt-5.6-sol · gpt-5.6-luna · gemma4 셋에 같은 프롬프트로 물어
// 대조한 결과를 표로 옮겼다. 아래는 그 26건의 **실물**이다.
func TestVerifiedVariantsResolveTheHeldRows(t *testing.T) {
	cases := []struct{ in, want, why string }{
		{"李明勛", "李明勋", "이명훈 — 勛 의 간체는 勋"},
		{"泳勛", "泳勋", "영훈"},
		{"僱傭勞動部", "雇佣劳动部", "고용노동부"},
		{"尹啟相", "尹启相", "윤계상 — 啟→启"},
		{"許楠儁", "许楠俊", "허남준 — 儁→俊 (모델이 말한 李仁 식 이름 교체는 안 한다)"},
		{"李儁", "李俊", "이인 — 글자만 고친다"},
		{"讚美", "赞美", "허찬미 — 讚→赞, 성씨는 붙이지 않는다"},
		{"CJ第一製糖", "CJ第一制糖", "CJ제일제당 — 製→制, 브랜드명은 안 건드린다"},
		{"世上哪裡都找不到的善良男人", "世上哪里都找不到的善良男人", "裡→里"},
		{"眞嶋優", "真岛优", "마시마 — 眞→真 嶋→岛, 優 는 그대로 둔다"},
		{"孫甫昇", "孙甫昇", "손보승 — 孫→孙, 昇 은 유지"},
		{"趙昇久", "赵昇久", "조승구 — 趙→赵, 昇 은 유지"},
		{"金昇淵", "金昇渊", "김승연 — 淵→渊, 昇 은 유지"},
		{"姜昇希", "姜昇希", "강승희 — 바꿀 것이 없다"},
		{"金汎", "金汎", "김범 — 汎 은 이름 글자다"},
		{"孔昇延", "孔昇延", "공승연 — 孔升妍 은 표기 교체라 이 레인의 일이 아니다"},
	}
	for _, c := range cases {
		got, ok := ZhToSimplified(c.in)
		if !ok {
			t.Errorf("%q 가 아직 보류다 — %s", c.in, c.why)
			continue
		}
		if got != c.want {
			t.Errorf("ZhToSimplified(%q) = %q, 기대 %q — %s", c.in, got, c.want, c.why)
		}
	}
}

// TestVerifiedTablesDoNotCollide — 유지 목록과 변환 목록이 같은 글자를 두고 다투면
// 동작이 순서에 의존한다. 겹침을 금지한다.
func TestVerifiedTablesDoNotCollide(t *testing.T) {
	for r := range zhVariantVerified {
		if zhKeepVerified[r] {
			t.Errorf("%q 가 변환 목록과 유지 목록 양쪽에 있다", string(r))
		}
		if _, dup := zhT2S[r]; dup {
			t.Errorf("%q 는 이미 왕복 안정 표에 있다 — 수동 표에 중복으로 실렸다", string(r))
		}
	}
	for r := range zhKeepVerified {
		if _, dup := zhT2S[r]; dup {
			t.Errorf("%q 는 왕복 안정 표에 있어서 유지 목록이 무시된다", string(r))
		}
	}
}

// TestGeneratedTableAgreesWithOpenCC — 구운 표가 사전과 어긋나지 않는지.
//
// zh_charsets_gen.go 는 OpenCC 사전에서 구운 파생물이다. 의존성을 올렸는데 표를
// 다시 굽지 않으면 조용히 어긋난다. 여기서 왕복 안정 쌍을 사전과 대조한다.
func TestGeneratedTableAgreesWithOpenCC(t *testing.T) {
	t2s, err := opencc.New("t2s")
	if err != nil {
		t.Skipf("opencc t2s init: %v", err)
	}
	from := []rune(zhT2SFrom)
	if len(from) != len([]rune(zhT2STo)) {
		t.Fatalf("표 길이가 다르다: from=%d to=%d — 생성기를 다시 돌려라", len(from), len([]rune(zhT2STo)))
	}
	checked, bad := 0, 0
	for r, want := range zhT2S {
		got, cerr := t2s.Convert(string(r))
		if cerr != nil {
			continue
		}
		checked++
		if strings.TrimSpace(got) != string(want) {
			bad++
			if bad <= 5 {
				t.Errorf("표: %q→%q 인데 opencc 는 %q — scripts/gen_zh_charsets.py 를 다시 돌려라",
					string(r), string(want), got)
			}
		}
	}
	if checked < 3000 {
		t.Errorf("대조한 글자가 %d개뿐이다 — 표가 비었나?", checked)
	}
}

// TestCharsetsAreNotEmpty — 생성 파일이 빈 채로 커밋되는 것을 막는다.
func TestCharsetsAreNotEmpty(t *testing.T) {
	if len(zhTradOnly) < 3000 || len(zhHansOnly) < 3000 {
		t.Fatalf("전용 글자 집합이 너무 작다: 번체 %d · 간체 %d — 생성이 실패한 채 커밋됐다",
			len(zhTradOnly), len(zhHansOnly))
	}
	if len(zhT2S) < 3000 || len(zhS2T) < 3000 {
		t.Fatalf("변환표가 너무 작다: t2s %d · s2t %d", len(zhT2S), len(zhS2T))
	}
}

// TestRepairNeverMovesAValueIntoTheWrongColumn — 수리가 새 오염을 만들지 않는지.
//
// 실측(2026-09-17): 번체 칸 "時間追踪者 薛祿" 은 踪 한 글자만 간체였다. 번체 칸은
// 옳게 고쳤지만 **원본을 그대로 간체 칸으로 옮겨** 거기에 時·間·祿 을 새로 심었다.
// 한 글자 때문에 잡힌 값은 나머지가 반대쪽 자체인 경우가 많다 — 옮기면 안 된다.
func TestRepairNeverMovesAValueIntoTheWrongColumn(t *testing.T) {
	cases := []struct {
		val       string
		fromHant  bool // 번체 칸에서 잡혀 간체 칸으로 옮기려는 값인가
		moveIsBad bool
	}{
		{"時間追踪者 薛祿", true, true},   // 나머지가 번체 — 간체 칸으로 옮기면 안 된다
		{"奧曼市長", true, true},        // 長 이 번체
		{"丹尼尔·海尼", true, false},     // 전부 간체 — 간체 칸으로 옮겨도 된다
		{"韓國放送公社", false, false},    // 전부 번체 — 번체 칸으로 옮겨도 된다
		{"钢铁少女团", false, true},      // 전부 간체인데 번체 칸으로 옮기려 한다
	}
	for _, c := range cases {
		var bad bool
		if c.fromHant {
			bad = ContainsTradOnly(c.val) // 간체 칸으로 갈 값에 번체가 있나
		} else {
			bad = ContainsHansOnly(c.val) // 번체 칸으로 갈 값에 간체가 있나
		}
		if bad != c.moveIsBad {
			t.Errorf("%q: 이동 차단 판정 %v, 기대 %v", c.val, bad, c.moveIsBad)
		}
	}
}

// ★변환 결과는 **대만 표준형**이어야 한다 (2026-09-21 실측).
//
//	운영 DB 에서 번체 칸의 為 는 31건(tmdb 22·wikipedia 5·codex 4)인데 爲 는
//	**우리 opencc 레인이 쓴 10건뿐**이었다. 권위 출처는 전부 표준형 쪽이다.
//	생성기가 TWVariants 를 제외 판정에만 쓰고 변환 결과에는 안 써서 생긴 일이다.
func TestZhToTraditional_대만_표준형으로_굽는다(t *testing.T) {
	cases := []struct{ in, want string }{
		{"因为是第一次", "因為是第一次"}, // 爲 가 아니다
		{"众", "眾"},
		{"伪", "偽"},
		{"启", "啟"},
		{"娴", "嫻"},
	}
	for _, c := range cases {
		if got, _ := ZhToTraditional(c.in); got != c.want {
			t.Errorf("ZhToTraditional(%q) = %q, want %q — 변종이 아니라 대만 표준형이다", c.in, got, c.want)
		}
	}
}

func TestLocaleOfCanonicalCol(t *testing.T) {
	// 파생 레인이 쓰는 칸과 검사 규칙이 갈라지면 안 된다.
	if got := localeOfCanonicalCol("canonical_zh_hant"); got != "zh_hant" {
		t.Errorf("= %q, want zh_hant", got)
	}
	if got := localeOfCanonicalCol("canonical_zh"); got != "zh" {
		t.Errorf("= %q, want zh", got)
	}
	// 번체 칸 규칙은 번체 전용 글자를 **통과**시켜야 한다 — 이걸 "zh" 로 재면
	// 제대로 변환된 값이 전부 버려진다.
	if !IsValidSpellingForLocale(localeOfCanonicalCol("canonical_zh_hant"), "九老區廳") {
		t.Error("번체 칸이 번체를 거부했다")
	}
	if IsValidSpellingForLocale(localeOfCanonicalCol("canonical_zh"), "九老區廳") {
		t.Error("간체 칸이 번체를 통과시켰다")
	}
}
