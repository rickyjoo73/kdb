package kdb

import (
	"encoding/json"
	"testing"
)

// TestKoWikiTitleMatches — 해소된 제목이 우리 이름과 같은가.
//
// 이 가드가 없으면 리다이렉트가 **다른 대상**으로 데려간다. 실측 오답 5건이 전부
// 이 유형이었다: 벨리타→벨리타 모레노(미국 배우), 산보→산책(walking),
// 파리바게뜨→파리크라상(모회사), 전주디지털독립영화관→전주영화제작소.
func TestKoWikiTitleMatches(t *testing.T) {
	cases := []struct {
		ko, title string
		want      bool
	}{
		{"육사오", "육사오(6/45)", true},     // 괄호 주석은 위키백과 관행 — 허용
		{"사냥개들 시즌2", "사냥개들 시즌2", true}, // 정확일치
		{"크리스영", "크리스 영", true},        // 공백만 다름
		{"남자의 자격", "남자의 자격", true},
		{"벨리타", "벨리타 모레노", false},        // 다른 사람으로 리다이렉트
		{"산보", "산책", false},              // 뜻이 같은 다른 낱말
		{"파리바게뜨", "파리크라상", false},        // 모회사로
		{"전주디지털독립영화관", "전주영화제작소", false}, // 다른 시설명
		{"", "무엇이든", false},              // 빈 이름은 비교 불가
	}
	for _, c := range cases {
		if got := koWikiTitleMatches(c.ko, c.title); got != c.want {
			t.Errorf("koWikiTitleMatches(%q,%q) = %v; want %v", c.ko, c.title, got, c.want)
		}
	}
}

// TestKoWikiVerdict — 페이지 판정. 가드 셋이 각각 살아 있는지.
func TestKoWikiVerdict(t *testing.T) {
	disamb := "" // pageprops.disambiguation 은 빈 문자열로 존재한다
	page := func(title, extract, qid string, isDisamb, missing bool) *koWikiPage {
		p := &koWikiPage{Title: title, Extract: extract}
		p.PageProps.WikibaseItem = qid
		if isDisamb {
			p.PageProps.Disambiguation = &disamb
		}
		if missing {
			// MediaWiki 는 빈 문자열을 준다 — 그 형태 그대로 테스트한다.
			p.Missing = json.RawMessage(`""`)
		}
		return p
	}
	cases := []struct {
		name, ko    string
		p           *koWikiPage
		wantOK      bool
		wantVerdict string
	}{
		{"정상 — 영화", "육사오",
			page("육사오(6/45)", "《육사오(6/45)》는 2022년 개봉한 대한민국의 코미디 영화이다.", "Q111948809", false, false),
			true, ""},
		{"정상 — 방송사만 언급", "별이 빛나는 밤에",
			page("별이 빛나는 밤에", "《별이 빛나는 밤에》는 MBC FM4U에서 방송되는 라디오 프로그램이다.", "Q12598249", false, false),
			true, ""},
		{"문서 없음", "레난", page("", "", "", false, true), false, "no-article"},
		{"동음이의어", "베드",
			page("베드", "베드는 다음을 가리킨다.", "Q408716", true, false), false, "disambiguation"},
		{"이름 불일치", "산보",
			page("산책", "산책은 여가를 위해 걷는 행위이다.", "Q1051130", false, false), false, "title-mismatch"},
		{"QID 없음", "무명작",
			page("무명작", "무명작은 대한민국의 영화이다.", "", false, false), false, "no-qid"},
		{"의미 불일치 — 중국 연호", "홍가",
			page("홍가", "홍가(鴻嘉)는 중국 전한 성제의 네 번째 연호이다.", "Q833919", false, false),
			false, "foreign-topic"},
		{"의미 불일치 — 미국 영화", "시스터 액트",
			page("시스터 액트", "《시스터 액트》는 1992년 에밀 아돌리노가 연출한 영화이다.", "Q776302", false, false),
			false, "foreign-topic"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ok, verdict, reason := koWikiVerdict(c.ko, c.p, nil)
			if ok != c.wantOK {
				t.Fatalf("ok = %v; want %v (verdict=%s reason=%s)", ok, c.wantOK, verdict, reason)
			}
			if !ok && verdict != c.wantVerdict {
				t.Errorf("verdict = %q; want %q", verdict, c.wantVerdict)
			}
			if !ok && reason == "" {
				t.Error("기각에는 사유가 있어야 한다 — 없으면 원장에서 원인을 못 되짚는다")
			}
		})
	}
}

// TestKoWikiCtxRejectsNonKorean — 맥락 정규식이 비한국 주제를 실제로 거른다.
// 이게 느슨해지면 `홍가`(중국 연호) 같은 오탐이 authoritative 로 올라간다.
func TestKoWikiCtxRejectsNonKorean(t *testing.T) {
	reject := []string{
		"홍가(鴻嘉)는 중국 전한 성제의 네 번째 연호이다.",
		"산책은 여가를 위해 걷는 행위를 말한다.",
		"벨리타 모레노는 미국의 배우이다.",
		// ★실전 오탐(08-16): 장르어 `드라마` 를 넣었더니 지민의 곡 `라이크 크레이지` 가
		// 2011년 미국 영화에 붙었다. 장르어는 나라를 가리지 않는다.
		"《라이크 크레이지》(영어: Like Crazy)는 2011년에 개봉한 미국의 로맨틱 드라마 영화로,",
		"1992년 에밀 아돌리노가 연출한 코미디 영화이다.",
	}
	for _, s := range reject {
		if koWikiCtxRe.MatchString(s) {
			t.Errorf("걸러야 할 도입부가 통과했다: %q", s)
		}
	}
	accept := []string{
		"2022년 개봉한 대한민국의 코미디 영화이다.",
		"MBC FM4U에서 방송되는 라디오 프로그램이다.",
		"한국의 가수 전인권이 발표한 노래이다.",
		"넷플릭스에서 공개된 시리즈이다.",
		// 국적 수식이 외국이어도 한국 활동 신호가 있으면 통과해야 한다(뱀뱀·샘 해밍턴).
		"뱀뱀은 태국의 가수이자 대한민국에서 활동하는 아이돌이다.",
		"샘 해밍턴은 오스트레일리아 출신 대한민국의 방송인이다.",
		// ★08-16 2차 보강분 — 이 넷은 종전 규칙에서 억울하게 기각됐다.
		"1992년 11월 19일부터 문화방송에서 방송되었던 코미디 프로그램이다.",
		"2023년 12월 21일부터 TV CHOSUN에서 방송된 트롯 오디션 프로그램이다.",
		"2015년에 EBS1에서 방송된 청소년 요리 경연 프로그램이다.",
		"SK브로드밴드의 자회사 주식회사 미디어에스에서 운영하는 채널S이다.",
	}
	for _, s := range accept {
		if !koWikiCtxRe.MatchString(s) {
			t.Errorf("통과해야 할 도입부가 걸렸다: %q", s)
		}
	}
}

// TestKoWikiLangStrip — 한국어 표기 병기는 국적 신호가 아니다.
//
// ★실전 오탐 2건(08-16): `산`(래퍼)이 지형 문서 Q8502 에, `WANNABE`(그룹)가 스파이스
// 걸스의 노래 Q418833 에 붙었다. 둘 다 도입부의 유일한 `한국`이 "한국어"였다.
func TestKoWikiLangStrip(t *testing.T) {
	reject := []string{
		"산(山)은 주위보다 높이 솟아 있는 지형을 말한다. 한국어 고유어로는, 뫼 또는 메라고 부르며,",
		"〈Wannabe〉(한국어: 워너비)는 영국의 걸 그룹 스파이스 걸스의 노래로, 데뷔 싱글이다.",
	}
	for _, s := range reject {
		if koWikiCtxRe.MatchString(koWikiLangRe.ReplaceAllString(s, "")) {
			t.Errorf("한국어 표기를 지운 뒤에도 통과했다: %q", s)
		}
		if !koWikiCtxRe.MatchString(s) {
			t.Errorf("전제가 깨졌다 — 지우기 전에는 통과해야 이 가드가 의미가 있다: %q", s)
		}
	}
}

// TestKoWikiNameSetBoundary — 이름 사전은 **낱말 경계를 지켜야** 한다.
//
// ★부분문자열 매칭이 곧 오탐이었다(08-16 실측): `에스파`가 에스파냐(스페인)에 걸려
// `보고타`를, `이시아`가 동남아시아·말레이시아에 걸려 `로미`·`봉선화`를 오탐시켰다.
func TestKoWikiNameSetBoundary(t *testing.T) {
	ns := &koWikiNameSet{names: []string{"방탄소년단", "더블랙레이블", "에스파", "이시아"}}
	cases := []struct {
		text, want string
	}{
		{"《호르몬 전쟁》은 방탄소년단의 첫 번째 정규 음반의 수록곡이다.", "방탄소년단"},
		{"레코드 레이블은 더블랙레이블, 애틀랜틱 레코드이다.", "더블랙레이블"},
		// 낱말 안쪽 — 걸리면 안 된다.
		{"보고타는 콜롬비아의 수도이며, 에스파냐어를 쓴다.", ""},
		{"중국 남부 및 동남아시아에서 먹는 국수 요리이다.", ""},
		{"인도·말레이시아·중국 원산으로 봉선화과의 한해살이풀이다.", ""},
		// 경계가 한글이 아니면(문장부호·공백·문자열 끝) 통과해야 한다.
		{"에스파", "에스파"},
		{"신인 그룹 에스파, 데뷔.", "에스파"},
		// 조사가 붙은 형태는 통과해야 한다 — 한국어 문장의 기본형이다.
		{"블랙핑크의 붐바야는 데뷔곡이다.", ""}, // 사전에 없는 이름
		{"방탄소년단은 2013년 데뷔했다.", "방탄소년단"},
		{"방탄소년단과 라인프렌즈의 협업.", "방탄소년단"},
	}
	for _, c := range cases {
		if got := ns.hit(c.text); got != c.want {
			t.Errorf("hit(%q) = %q; want %q", c.text, got, c.want)
		}
	}
	// nil 사전은 안전해야 한다 — 적재 실패해도 레인이 멈추면 안 된다.
	var nilSet *koWikiNameSet
	if got := nilSet.hit("방탄소년단"); got != "" {
		t.Errorf("nil 사전이 %q 를 반환했다", got)
	}
}
