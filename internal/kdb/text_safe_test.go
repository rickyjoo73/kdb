package kdb

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// 운영에서 실제로 난 오류를 재현한다:
//   ERROR: invalid byte sequence for encoding "UTF8": 0xeb
// 한글은 UTF-8 3바이트라 바이트 단위 절단이 글자를 반 토막 낸다. 그 값이 DB 로 가면
// 문장 전체가 실패하는데, 호출부들이 오류를 삼켜 조용히 사라졌다.
func TestTruncateSafeNeverProducesInvalidUTF8(t *testing.T) {
	ko := strings.Repeat("가나다라마", 100) // 1,500바이트
	for _, max := range []int{1, 2, 3, 4, 7, 100, 199, 200, 201, 499, 500, 501, 1499} {
		got := TruncateSafe(ko, max)
		if !utf8.ValidString(got) {
			t.Fatalf("max=%d: 잘린 글자가 남았다 — DB 가 거부한다: %q", max, got)
		}
		if len(got) > max {
			t.Fatalf("max=%d: 상한을 넘었다 len=%d", max, len(got))
		}
	}
	// 종전 방식이 실제로 깨진다는 것도 함께 못박는다. 이게 깨지지 않으면 이 시험은 무의미하다.
	if utf8.ValidString(ko[:500]) {
		t.Fatal("전제가 틀렸다: 500바이트 절단이 한글 경계와 맞아떨어졌다 — 픽스처를 바꿔야 한다")
	}
}

// 외부에서 온 깨진 인코딩(예: EUC-KR 바이트가 섞인 query string)도 걸러야 한다.
func TestTruncateSafeDropsInvalidBytes(t *testing.T) {
	dirty := "정상" + string([]byte{0xeb, 0xb0}) + "끝"
	got := TruncateSafe(dirty, 100)
	if !utf8.ValidString(got) {
		t.Fatalf("깨진 바이트가 남았다: %q", got)
	}
	if !strings.Contains(got, "정상") || !strings.Contains(got, "끝") {
		t.Fatalf("멀쩡한 글자까지 버렸다: %q", got)
	}
}

func TestTruncateSafeShortStringUnchanged(t *testing.T) {
	if got := TruncateSafe("짧다", 100); got != "짧다" {
		t.Fatalf("상한 이하인데 바뀌었다: %q", got)
	}
}
