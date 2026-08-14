package kdb

import "testing"

// TestZhWikiTitleFromURL — 제목 추출과 닫힌 목록 동음이의 처리.
func TestZhWikiTitleFromURL(t *testing.T) {
	cases := []struct {
		name, url, want string
		wantReject      bool
	}{
		{"라틴 단순", "https://zh.wikipedia.org/wiki/MBLAQ", "MBLAQ", false},
		{"밑줄→공백", "https://zh.wikipedia.org/wiki/Baby_V.O.X", "Baby V.O.X", false},
		{"퍼센트 한자", "https://zh.wikipedia.org/wiki/%E7%A3%AA%E6%9C%89%E6%83%85", "磪有情", false},
		{"허용 꼬리표 歌手", "https://zh.wikipedia.org/wiki/Gray_(%E6%AD%8C%E6%89%8B)", "Gray", false},
		{"허용 꼬리표 組合", "https://zh.wikipedia.org/wiki/GOOD_DAY_(%E7%B5%84%E5%90%88)", "GOOD DAY", false},
		{"허용 꼬리표 女子團體", "https://zh.wikipedia.org/wiki/ANS_(%E5%A5%B3%E5%AD%90%E5%9C%98%E9%AB%94)", "ANS", false},
		// ★닫힌 목록 밖 괄호는 벗기지 않고 통째로 기각한다 — 이름의 일부일 수 있다.
		{"목록 밖 꼬리표", "https://zh.wikipedia.org/wiki/Foo_(%E6%9C%AA%E7%9F%A5%E8%AA%9E)", "", true},
		{"이름 안의 괄호", "https://zh.wikipedia.org/wiki/Sixteen_(Pristin_song)", "", true},
		{"네임스페이스", "https://zh.wikipedia.org/wiki/Category:K-pop", "", true},
		{"섹션 앵커는 잘라낸다", "https://zh.wikipedia.org/wiki/MBLAQ#%E6%88%90%E5%93%A1", "MBLAQ", false},
		{"wiki 경로 없음", "https://zh.wikipedia.org/", "", true},
	}
	for _, c := range cases {
		got, why := zhWikiTitleFromURL(c.url)
		if c.wantReject {
			if got != "" {
				t.Errorf("%s: 기각해야 하는데 %q 를 뽑았다", c.name, got)
			}
			if why == "" {
				t.Errorf("%s: 기각 사유가 비었다 — 원장에 남길 게 없다", c.name)
			}
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %q want %q (why=%s)", c.name, got, c.want, why)
		}
	}
}

// TestFoldForCompare — 표기 변형은 접히고 다른 이름은 안 접힌다.
func TestFoldForCompare(t *testing.T) {
	same := [][2]string{
		{"M.I.L.K", "M.I.L.K."},
		{"Ledapple", "Led Apple"},
		{"The EastLight.", "The East Light"},
		{"4minute", "4Minute"},
		{"F-VE DOLLS", "F-ve Dolls"},
		{"SISTAR19", "Sistar19"},
	}
	for _, p := range same {
		if foldForCompare(p[0]) != foldForCompare(p[1]) {
			t.Errorf("같은 이름인데 안 접혔다: %q vs %q", p[0], p[1])
		}
	}
	diff := [][2]string{
		{"ANS", "AEN"},     // 걸그룹 vs 솔로 — 실측 오매칭
		{"Lami", "Haram"},  // 실측 오매칭
		{"磪有情", "Yujeong"}, // 한자 제목은 en 과 겹칠 수 없다
		{"Reset", "Resets"},
	}
	for _, p := range diff {
		if foldForCompare(p[0]) == foldForCompare(p[1]) {
			t.Errorf("다른 이름인데 접혔다: %q vs %q", p[0], p[1])
		}
	}
}

// TestZhWikiVerdict — 레인의 핵심 가드. 실측된 오매칭 3건이 전부 걸려야 한다.
func TestZhWikiVerdict(t *testing.T) {
	cases := []struct {
		name, en    string
		urls        []string
		wantVerdict string
		wantTitle   string
	}{
		{"통과", "MBLAQ", []string{"https://zh.wikipedia.org/wiki/MBLAQ"}, "", "MBLAQ"},
		{"통과(꼬리표)", "Chakra", []string{"https://zh.wikipedia.org/wiki/Chakra_(%E7%B5%84%E5%90%88)"}, "", "Chakra"},
		{"실측 오매칭 에이엔", "AEN", []string{"https://zh.wikipedia.org/wiki/ANS_(%E5%A5%B3%E5%AD%90%E5%9C%98%E9%AB%94)"}, "en-mismatch", ""},
		{"실측 오매칭 라미", "Haram", []string{"https://zh.wikipedia.org/wiki/Lami"}, "en-mismatch", ""},
		{"실측 오매칭 유정", "Yujeong", []string{"https://zh.wikipedia.org/wiki/%E7%A3%AA%E6%9C%89%E6%83%85"}, "en-mismatch", ""},
		{"문서 둘", "Foo", []string{"https://zh.wikipedia.org/wiki/Foo", "https://zh.wikipedia.org/wiki/Bar"}, "multi-url", ""},
		{"URL 없음", "Foo", nil, "no-url", ""},
		{"한글 혼입", "가나다", []string{"https://zh.wikipedia.org/wiki/%EA%B0%80%EB%82%98%EB%8B%A4"}, "script-mismatch", ""},
		{"폭0 서식문자", "Billlie", []string{"https://zh.wikipedia.org/wiki/Billlie%E2%80%8E"}, "zero-width", ""},
	}
	for _, c := range cases {
		v, reason, title := zhWikiVerdict(c.en, c.urls)
		if v != c.wantVerdict {
			t.Errorf("%s: verdict=%q want %q (reason=%s)", c.name, v, c.wantVerdict, reason)
			continue
		}
		if title != c.wantTitle {
			t.Errorf("%s: title=%q want %q", c.name, title, c.wantTitle)
		}
		if v != "" && reason == "" {
			t.Errorf("%s: 기각인데 사유가 비었다", c.name)
		}
	}
}
