package kdbapi

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 운영 DB 로그에서 발견: invalid byte sequence for encoding "UTF8": 0xeb
// `q = q[:500]` 이 한글(3바이트)을 반 토막 내 INSERT 가 통째로 실패했고,
// detached goroutine 이 `_, _ =` 로 오류를 버려 아무도 모르게 요청 기록이 사라졌다.
func TestSanitizeLogTextKeepsUTF8Valid(t *testing.T) {
	q := "source_text=" + strings.Repeat("해운대해수욕장", 80) // 500바이트를 훌쩍 넘김
	if utf8.ValidString(q[:500]) {
		t.Fatal("전제가 틀렸다: 500바이트 절단이 글자 경계와 맞았다 — 픽스처를 바꿔야 한다")
	}
	got := sanitizeLogText(q, 500)
	if !utf8.ValidString(got) {
		t.Fatalf("잘린 글자가 남았다 — Postgres 가 INSERT 를 거부한다: %q", got)
	}
	if len(got) > 500 {
		t.Fatalf("상한 초과 len=%d", len(got))
	}
}

func TestSanitizeLogTextDropsBrokenEncoding(t *testing.T) {
	got := sanitizeLogText("q="+string([]byte{0xeb, 0xb0})+"값", 500)
	if !utf8.ValidString(got) {
		t.Fatalf("깨진 바이트가 남았다: %q", got)
	}
}
