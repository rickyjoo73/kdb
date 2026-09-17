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
	for _, name := range []string{"姜昇希", "李明勛", "李儁", "尹啟相", "金汎"} {
		got, ok := ZhToSimplified(name)
		if ok {
			t.Errorf("%q 를 자동 변환 가능으로 판정했다 — 이체자는 사람이 봐야 한다 (결과 %q)", name, got)
		}
		if got != name {
			t.Errorf("%q 보류인데 값이 바뀌었다 → %q", name, got)
		}
	}
	// 반대로, 왕복 안정한 글자만 있으면 변환한다.
	if got, ok := ZhToSimplified("鄭先哲"); got != "郑先哲" || !ok {
		t.Errorf("ZhToSimplified(鄭先哲) = %q(ok=%v), 기대 郑先哲", got, ok)
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
