package research

import (
	"context"
	"errors"
	"testing"
)

func TestIsTransientErr(t *testing.T) {
	transient := []error{
		context.DeadlineExceeded,
		context.Canceled,
		errors.New("codex timeout after 90000ms"),
		errors.New("dial tcp: connection refused"),
		errors.New("mb status 503"),
	}
	for _, e := range transient {
		if !isTransientErr(e) {
			t.Errorf("want transient: %v", e)
		}
	}
	// 영구 실패가 transient 로 오분류되면 무한 재시도 → 반드시 false.
	permanent := []error{
		errors.New("json: unexpected EOF parsing response"), // 'eof' 부분문자열이지만 영구 파싱오류
		errors.New("wikidata: no entity Q123 in response"),
		errors.New("invalid schema for response_format"),
		nil,
	}
	for _, e := range permanent {
		if isTransientErr(e) {
			t.Errorf("must NOT be transient (would retry forever): %v", e)
		}
	}
}
