package kdbapi

import (
	"context"
	"testing"
	"time"
)

// 판별 예산은 요청에 남은 시간 안에서만 잡혀야 한다. 고정 8s 였을 때는 요청
// 타임아웃(10s)의 80% 를 판별이 먹어, 앞단 조회가 조금만 길어져도 응답 자체가
// 데드라인을 넘겼다(실측 p99 8.6s). 예산 + 여유분이 남은 시간을 넘으면 안 된다.
func TestDisambiguateBudget_FitsWithinRemaining(t *testing.T) {
	cases := []struct {
		name      string
		remaining time.Duration
		want      time.Duration // 0 = 판별 생략
	}{
		{"여유 충분 — 상한으로 절삭", 10 * time.Second, disambiguateMaxBudget},
		{"딱 상한+여유", disambiguateMaxBudget + disambiguateReserve, disambiguateMaxBudget},
		{"조회가 오래 걸려 4s 남음", 4 * time.Second, 4*time.Second - disambiguateReserve},
		{"최소 예산 미달 — 생략", 2 * time.Second, 0},
		{"이미 초과 — 생략", 100 * time.Millisecond, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), c.remaining)
			defer cancel()
			got := disambiguateBudget(ctx)
			// time.Until 로 계산하므로 미세한 오차를 허용한다.
			if diff := got - c.want; diff > 50*time.Millisecond || diff < -50*time.Millisecond {
				t.Fatalf("남은시간 %v: budget=%v, want≈%v", c.remaining, got, c.want)
			}
			if got > 0 && got+disambiguateReserve > c.remaining+50*time.Millisecond {
				t.Fatalf("남은시간 %v 를 예산 %v + 여유 %v 가 초과", c.remaining, got, disambiguateReserve)
			}
		})
	}
}

// 데드라인이 없는 컨텍스트(테스트·내부호출)는 종전 고정 상한을 유지한다.
func TestDisambiguateBudget_NoDeadlineKeepsMax(t *testing.T) {
	if got := disambiguateBudget(context.Background()); got != disambiguateMaxBudget {
		t.Fatalf("데드라인 없음: budget=%v, want %v", got, disambiguateMaxBudget)
	}
}
