package googlenews

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const sampleFeed = `<?xml version="1.0"?><rss version="2.0"><channel>
<item><title>니쥬, 日 첫 베스트 앨범 오리콘 1위 등극 - 알파경제</title>
<link>https://news.google.com/rss/articles/AAA</link><source url="x">알파경제</source></item>
<item><title>[단독] 'JYP 글로벌 그룹' 니쥬, 9월 한국서 첫 미니앨범</title>
<link>https://news.google.com/rss/articles/BBB</link></item>
<item><title></title><link>https://news.google.com/rss/articles/CCC</link></item>
<item><title>링크 없는 기사 - 어떤매체</title><link></link></item>
</channel></rss>`

func newTestClient(h http.HandlerFunc) (*Client, *httptest.Server) {
	srv := httptest.NewServer(h)
	c := &Client{HTTP: srv.Client()}
	return c, srv
}

// TestSearchParsesTitleAndSource — 제목 꼬리의 매체명을 분리하고, source 요소가 있으면
// 그쪽을 쓴다. Google News 제목은 항상 "본문 - 매체" 꼴이라 분리하지 않으면 적합성
// 게이트가 매체명까지 엔티티명으로 착각할 여지가 생긴다.
func TestSearchParsesTitleAndSource(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("hl") != "ko" || r.URL.Query().Get("ceid") != "KR:ko" {
			t.Errorf("한국어 로케일이 고정되어야 한다: %s", r.URL.RawQuery)
		}
		if r.Header.Get("User-Agent") == "" {
			t.Error("UA 가 없으면 간헐적으로 빈 피드가 온다")
		}
		_, _ = w.Write([]byte(sampleFeed))
	})
	defer srv.Close()
	c.HTTP = srv.Client()
	// 테스트 서버로 향하도록 Search 내부 URL 을 쓰지 않고 직접 파싱 경로를 검증한다.
	items, err := c.searchURL(context.Background(), srv.URL+"?hl=ko&ceid=KR:ko", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("빈 제목·빈 링크는 버려야 한다: got %d items %+v", len(items), items)
	}
	if items[0].Title != "니쥬, 日 첫 베스트 앨범 오리콘 1위 등극" {
		t.Errorf("제목에서 매체명 꼬리를 떼야 한다: %q", items[0].Title)
	}
	if items[0].Source != "알파경제" {
		t.Errorf("source = %q; want 알파경제", items[0].Source)
	}
	if items[1].Source != "" || items[1].Title == "" {
		t.Errorf("꼬리가 없는 제목은 그대로 두고 source 는 빈값: %+v", items[1])
	}
}

// TestSearchMaxCap — max 를 넘겨 받지 않는다(호출측 예산 통제).
func TestSearchMaxCap(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleFeed))
	})
	defer srv.Close()
	c.HTTP = srv.Client()
	items, err := c.searchURL(context.Background(), srv.URL, 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("max=1 인데 %d건", len(items))
	}
}

// TestSearchEmptyQuery — 빈 질의는 호출하지 않는다.
func TestSearchEmptyQuery(t *testing.T) {
	c := &Client{HTTP: &http.Client{Timeout: time.Second}}
	if _, err := c.Search(context.Background(), "   ", 5); err == nil {
		t.Error("빈 질의는 오류여야 한다")
	}
}

// TestSearchNon200 — 비200 은 오류다. 이게 nil 오류로 새면 호출측이 "뉴스 소스가
// 답했다"고 오인해 근거 없는 엔티티를 강등한다(08-16 결함 계열).
func TestSearchNon200(t *testing.T) {
	c, srv := newTestClient(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	defer srv.Close()
	c.HTTP = srv.Client()
	if _, err := c.searchURL(context.Background(), srv.URL, 5); err == nil {
		t.Error("403 은 오류여야 한다 — 무응답을 성공으로 세면 오강등이 난다")
	}
}
