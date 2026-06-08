package wikidata

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/httpx"
)

type stubRT struct{}

func (stubRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{}`)),
		Header:     make(http.Header),
	}, nil
}

// TestGetConcurrent_race — 단일 Client 를 다수 goroutine 이 공유해 get 을 동시
// 호출해도 data race 가 없음을 보장(`go test -race`). limiter 가 버스트를
// 페이싱하면서도 동시성 안전하다.
func TestGetConcurrent_race(t *testing.T) {
	c := New()
	c.HTTPClient = &http.Client{Transport: stubRT{}}
	c.limiter = httpx.NewLimiter(time.Millisecond)

	q := url.Values{}
	q.Set("action", "wbgetentities")

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.get(context.Background(), q)
		}()
	}
	wg.Wait()
}
