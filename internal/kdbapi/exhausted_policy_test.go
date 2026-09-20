package kdbapi

import (
	"os"
	"strings"
	"testing"
)

// ★이 시험이 지키는 것 (2026-09-20 실측).
//
//	`exhausted` 불리언만 보면 정책으로 멈춘 칸을 영영 못 본다. exhausted 는
//	attempts>=2 일 때만 서는데, 근거 없는 칸은 정책 스킵(ground-strict-skip)으로
//	끝나고 그 경로는 시도로 세지 않는다 — attempts 가 영원히 0 이다.
//
//	  canonical_zh attempts=0 4,564건 중 ground-strict-skip 2,481건(54%)
//	  30일 이전에 멈춘 것 3,605건 · 가장 오래된 것 2026-06-13
//
//	그동안 소비자는 `preparing` 을 받았다 — 기다리면 채워진다는 뜻인데 채워질 일이
//	없다. 주 271회 요청(120낱말)이 그 거짓 답을 받고 있었다.

func TestExhaustedLocales_정책으로_멈춘_칸도_본다(t *testing.T) {
	b, err := os.ReadFile("api.go")
	if err != nil {
		t.Fatalf("api.go 를 못 읽었다: %v", err)
	}
	src := string(b)
	for _, want := range []string{
		"ground-strict-skip", // 정책 스킵 경로를 본다
		"OR (last_source =", // exhausted 하나만 보지 않는다
	} {
		if !strings.Contains(src, want) {
			t.Errorf("ExhaustedLocales 가 %q 를 보지 않는다 — 정책으로 멈춘 칸이 영영 preparing 으로 나간다", want)
		}
	}
	// 쿨다운(7일)보다 임계가 넉넉해야 「아직 재방문 중인 것」을 소진이라 부르지 않는다.
	if !strings.Contains(src, "interval '30 days'") {
		t.Error("정책 스킵 소진 임계가 사라졌다 — 7일 쿨다운보다 넉넉해야 한다")
	}
}
