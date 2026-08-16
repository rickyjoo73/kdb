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
			ok, verdict, reason := koWikiVerdict(c.ko, c.p)
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
	}
	for _, s := range accept {
		if !koWikiCtxRe.MatchString(s) {
			t.Errorf("통과해야 할 도입부가 걸렸다: %q", s)
		}
	}
}
