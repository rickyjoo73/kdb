package musicbrainz

import (
	"context"
	"io"
	"net/http"
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
// 호출해도 data race 가 없음을 보장(`go test -race`). 종전 lastCall 공유필드의
// race 를 limiter 가 대체했다.
func TestGetConcurrent_race(t *testing.T) {
	c := New()
	c.HTTPClient = &http.Client{Transport: stubRT{}}
	c.limiter = httpx.NewLimiter(time.Millisecond) // 페이싱 mutex 도 동시 노출.

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = c.get(context.Background(), "/artist?x=1")
		}()
	}
	wg.Wait()
}
