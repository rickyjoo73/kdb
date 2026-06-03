// Package httpx — 외부 API 클라이언트 공용 HTTP 재시도 헬퍼(의존성 없음).
//
// wikidata/tmdb/kofic/musicbrainz 가 단발 GET 으로 외부 API 를 호출하는데, 일시
// 5xx/timeout/429 한 번에 enrich 전체가 실패했다. 소량 bounded 재시도(GET 은
// body 가 없어 재전송 안전)로 일시 장애 회복력을 높인다. ctx 취소는 즉시 존중.
package httpx

import (
	"net/http"
	"strconv"
	"time"
)

// Do — req 를 최대 attempts 회 시도한다. 전송에러/5xx/429 면 backoff 후 재시도,
// 429 는 Retry-After(초)를 존중(상한 적용). 그 외 응답은 즉시 반환. GET(nil body)
// 전용 — 재전송 안전.
func Do(client *http.Client, req *http.Request, attempts int) (*http.Response, error) {
	if attempts < 1 {
		attempts = 1
	}
	var resp *http.Response
	var err error
	for i := 0; i < attempts; i++ {
		resp, err = client.Do(req)
		if err == nil && !retryableStatus(resp.StatusCode) {
			return resp, nil // 성공 또는 비재시도 응답(4xx 등) — 그대로 반환.
		}
		if i == attempts-1 {
			break // 마지막 시도 — 결과(에러 또는 마지막 resp) 반환.
		}
		wait := backoff(i)
		if resp != nil {
			if ra := retryAfter(resp); ra > 0 {
				wait = ra
			}
			resp.Body.Close() // 버리는 응답 body 정리(연결 재사용).
		}
		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	return resp, err
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || code >= 500
}

// backoff — 고정 단계 backoff(랜덤 없음): 0.4s, 1.0s, 2.0s …
func backoff(attempt int) time.Duration {
	switch attempt {
	case 0:
		return 400 * time.Millisecond
	case 1:
		return 1 * time.Second
	default:
		return 2 * time.Second
	}
}

// retryAfter — 429 Retry-After(초) 헤더를 파싱(상한 5s). 없으면 0.
func retryAfter(resp *http.Response) time.Duration {
	if resp.StatusCode != http.StatusTooManyRequests {
		return 0
	}
	v := resp.Header.Get("Retry-After")
	if v == "" {
		return 0
	}
	sec, err := strconv.Atoi(v)
	if err != nil || sec <= 0 {
		return 0
	}
	if sec > 5 {
		sec = 5
	}
	return time.Duration(sec) * time.Second
}
