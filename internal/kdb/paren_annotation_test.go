package kdb

import (
	"os"
	"strings"
	"testing"
)

// TestStripSourceAnnotation — 걷어야 할 것과 **절대 걷으면 안 되는 것**.
func TestStripSourceAnnotation(t *testing.T) {
	strip := []struct{ loc, typ, ko, val, want, why string }{
		{"zh", "person", "김병욱", "金炳旭 (1965年)", "金炳旭", "생년 동음이의"},
		{"zh", "person", "김영식", "金英植 (1959年)", "金英植", "생년"},
		{"zh", "person", "박태준", "朴泰俊 (跆拳道运动员)", "朴泰俊", "직업"},
		{"zh", "person", "박상원", "朴相元_(击剑运动员)", "朴相元", "밑줄로 이어붙은 위키 제목"},
		{"zh", "person", "이수호", "李相勋（演员）", "李相勋", "전각 괄호"},
		{"zh", "person", "임수연", "林秀妍（音译）", "林秀妍", "«음역» 은 위키 내부 용어다"},
		{"zh", "person", "이새롬", "李赛纶（独立条目）", "李赛纶", "«독립 항목» 도 마찬가지"},
		{"zh", "group", "넬", "Nell (乐团)", "Nell", "단체 종류"},
		{"zh", "group", "유니버스", "UNIVERSE (平台)", "UNIVERSE", "매체 종류"},
		{"zh", "song_album", "Masquerade", "Masquerade (2PM单曲)", "Masquerade", "곡 동음이의"},
		{"zh_hant", "person", "박태준", "朴泰俊 (跆拳道運動員)", "朴泰俊", "번체 어휘"},
		{"ja", "person", "김병욱", "キム・ビョンウク (1965年)", "キム・ビョンウク", "일본어 생년"},
		{"en", "person", "김병욱", "Kim Byung-wook (politician)", "Kim Byung-wook", "영어 직업"},
	}
	for _, c := range strip {
		got, ok := StripSourceAnnotation(c.loc, c.typ, c.ko, c.val)
		if !ok || got != c.want {
			t.Errorf("%s %q: %q → (%q,%v), 기대 %q — %s", c.loc, c.ko, c.val, got, ok, c.want, c.why)
		}
	}

	// ★여기가 이 파일의 핵심이다. 걷으면 **버려야 할 쪽을 남기는** 실수가 된다.
	keep := []struct{ loc, typ, ko, val, why string }{
		{"zh", "group", "에프엑스", "f(x)", "★이게 그룹 이름이다. 걷으면 f 가 된다"},
		{"zh", "group", "올아워즈", "ALL(H)OURS", "괄호가 끝이 아니다 — 이름 한가운데다"},
		{"zh", "person", "BTS 진", "Jin（金硕珍）", "괄호 **안**이 맞는 값이다. 머리를 남기면 틀린 쪽을 남긴다"},
		{"zh", "person", "바비", "Bobby (金知元）", "같은 계열 — 어휘에 안 걸려 자동으로 건너뛴다"},
		{"zh", "person", "크리스탈", "Crystal (郑秀晶)", "같은 계열"},
		{"zh", "brand_place", "킨텍스", "Korea International Exhibition Center（韩国国际展览中心）", "괄호 안이 맞는 값"},
		{"zh", "government_body", "주홍콩한국문화원", "韩国文化院（香港）", "«홍콩» 은 공식 명칭의 일부다"},
		{"zh", "movie", "러브", "爱 (Love)", "원제 병기 — 주석 어휘가 아니다"},
		{"zh", "song_album", "Amigo (Feat. 민수)", "Amigo (Feat. 民秀)", "한국어 정본에 괄호가 있다 = 제목의 일부"},
		{"zh", "song_album", "운명 (2025)", "命运（2025）", "한국어에 괄호가 있다"},
		{"zh", "song_album", "작은 것들을 위한 시(Boy With Luv)", "献给小事的诗（Boy With Luv）", "한국어에 괄호"},
		{"zh", "person", "우지", "李知勋 (SEVENTEEN)", "소속 그룹 — 주석 어휘 목록에 없으므로 손대지 않는다"},
		{"zh", "person", "홍길동", "洪吉童", "괄호가 없다"},
		{"zh", "person", "무엇", "(演员)", "머리가 비었다"},
	}
	for _, c := range keep {
		got, ok := StripSourceAnnotation(c.loc, c.typ, c.ko, c.val)
		if ok {
			t.Errorf("%s %q: %q 를 걷어 %q 가 됐다 — %s", c.loc, c.ko, c.val, got, c.why)
		}
	}
}

// TestStripNeverProducesAnInvalidSpelling — 걷은 결과가 그 칸에서 유효해야 한다.
//
// 지금 값이 나쁘다고 **더 나쁜 값**으로 바꿀 이유는 없다.
func TestStripNeverProducesAnInvalidSpelling(t *testing.T) {
	cases := []struct{ loc, typ, ko, val string }{
		{"zh", "person", "가나다", "ホジュン (俳優)"},   // 가나가 남는다 — zh 칸에 들어가면 안 된다
		{"ja", "person", "가나다", "金炳旭 (1965年)"}, // 한자만 남음 — ja 는 한자 허용이라 통과할 수도 있다
	}
	for _, c := range cases {
		got, ok := StripSourceAnnotation(c.loc, c.typ, c.ko, c.val)
		if ok && !IsValidSpellingForLocale(c.loc, got) {
			t.Errorf("%s: %q → %q 가 그 칸에서 유효하지 않다", c.loc, c.val, got)
		}
	}
}

// TestParenAnnotationIsReachable — 만들어 놓고 부르는 곳이 있는지.
func TestParenAnnotationIsReachable(t *testing.T) {
	src, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 읽기 실패: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "kdb.DrainParenAnnotations(") {
		t.Error("DrainParenAnnotations 를 부르는 곳이 없다 — 601칸이 그대로 나간다")
	}
	i := strings.Index(body, `os.Args[1] == "paren-annot"`)
	if i < 0 {
		t.Fatal("paren-annot 일회성 명령이 없다")
	}
	win := body[i:]
	if end := strings.Index(win, "DrainParenAnnotations("); end > 0 {
		win = win[:end]
	}
	if !strings.Contains(win, "dry := 500, true") {
		t.Error("paren-annot 이 기본 dry-run 이 아니다")
	}
}

// TestPersonWithoutHanIsLeftVisible — 사람의 중국어 칸에 한자가 없으면 그대로 둔다.
//
// ★"Yuna (演员)" 를 걷으면 "Yuna" 가 된다. 주석은 사라지지만 **사람 이름의 중국어
// 표기가 라틴이라는 사실**은 그대로고, 오히려 Wavve 같은 정상적인 라틴 브랜드명처럼
// 보여 구멍이 눈에 덜 띈다. 틀린 값을 그럴듯하게 다듬는 일은 하지 않는다.
//
// 단체·작품은 다르다 — Nell · T1 · The Rose 는 중국어권에서도 라틴을 그대로 쓴다.
func TestPersonWithoutHanIsLeftVisible(t *testing.T) {
	if _, ok := StripSourceAnnotation("zh", "person", "전소현", "Yuna (演员)"); ok {
		t.Error("사람의 중국어 칸에서 한자 없는 머리를 남겼다 — 구멍이 값처럼 보인다")
	}
	if _, ok := StripSourceAnnotation("zh_hant", "person", "전소현", "Yuna (演員)"); ok {
		t.Error("번체 칸도 같아야 한다")
	}
	// 단체는 걷어야 한다.
	if got, ok := StripSourceAnnotation("zh", "group", "넬", "Nell (乐团)"); !ok || got != "Nell" {
		t.Errorf("단체의 라틴 이름을 안 걷었다: %q %v", got, ok)
	}
	// 사람이라도 한자가 있으면 걷는다.
	if got, ok := StripSourceAnnotation("zh", "person", "박태준", "朴泰俊 (跆拳道运动员)"); !ok || got != "朴泰俊" {
		t.Errorf("한자 있는 사람 이름을 안 걷었다: %q %v", got, ok)
	}
}

// TestParenAnnotationRunsOnASchedule — 주기로 도는지.
//
// ★오늘 같은 모양을 세 번 봤다 — 레인을 끄거나 고쳤는데 그 레인이 남긴 표시를
// 아무도 안 걷었다(`[revert-term:reject]` · `[scope:review]` · 오늘 내가 만든
// `[occup-scope-restore]`). 같은 실수를 여기서 또 하지 않는다.
//
// 괄호 주석을 만드는 출처(wikidata-label · wikipedia-zh-variant · opencc · tmdb)는
// 지금도 돈다. 한 번 걷고 끝내면 내일 다시 쌓인다.
func TestParenAnnotationRunsOnASchedule(t *testing.T) {
	src, err := os.ReadFile("../../cmd/kdb/main.go")
	if err != nil {
		t.Fatalf("main.go 읽기 실패: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "case <-parenAnnotTicker.C:")
	if i < 0 {
		t.Fatal("parenAnnotTicker 를 받는 case 가 없다 — 주석이 내일 다시 쌓인다")
	}
	win := body[i:]
	if end := strings.Index(win, "\n\t\tcase <-"); end > 0 {
		win = win[:end]
	}
	if !strings.Contains(win, "DrainParenAnnotations(") {
		t.Error("주기 case 가 DrainParenAnnotations 를 부르지 않는다")
	}
	if !strings.Contains(win, ", false)") {
		t.Errorf("주기 실행이 dry-run 이다 — 로그만 남고 주석은 그대로다:\n%s", win)
	}
}

// TestJapaneseReadingGlossIsNeverStripped — 일본어 칸의 «읽기 병기»를 지키는지.
//
// ★일본어는 한자·라틴 표기 뒤에 가타카나 읽기를 붙이는 것이 관행이다.
// 그것까지 걷으면 일본 독자가 **읽을 방법을 잃는다.** 실측 ja 233칸 중 대부분이
// 이것이다 — 걷어야 할 것은 직업·매체 종류뿐이다.
func TestJapaneseReadingGlossIsNeverStripped(t *testing.T) {
	keep := []struct{ ko, val, why string }{
		{"추노", "推奴（チュノ）", "한자 + 읽기"},
		{"농심", "農心 (ノンシム)", "한자 + 읽기"},
		{"주몽", "朱蒙（チュモン）", "한자 + 읽기"},
		{"파과", "破果 (パグァ)", "한자 + 읽기"},
		{"공허해", "空虚(コンホヘ)", "붙여 쓴 읽기"},
		{"새소년", "SE SO NEON（セソニョン）", "라틴 + 읽기"},
		{"V", "V（ヴィ）", "짧은 이름 + 읽기"},
	}
	for _, c := range keep {
		if got, ok := StripSourceAnnotation("ja", "group", c.ko, c.val); ok {
			t.Errorf("읽기 병기를 걷었다: %q → %q — %s", c.val, got, c.why)
		}
	}
	strip := []struct{ ko, val, want string }{
		{"이준식", "李俊植 (技術者)", "李俊植"},
		{"CTS기독교TV", "CTS (放送社)", "CTS"},
		{"펭수", "ペンス (キャラクター)", "ペンス"},
		{"안영미", "アン・ヨンミ (コメディアン)", "アン・ヨンミ"},
	}
	for _, c := range strip {
		got, ok := StripSourceAnnotation("ja", "person", c.ko, c.val)
		if !ok || got != c.want {
			t.Errorf("직업 주석을 안 걷었다: %q → (%q,%v), 기대 %q", c.val, got, ok, c.want)
		}
	}
}
