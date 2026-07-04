// Package sourcehealth — 외부소스 호출 결과를 인메모리로 집계해 운영 점검(admin
// /admin/ops/health)에 노출한다. 오너 방침(2026-07-04): 처리는 즉시, "점검"은 주기 —
// 네이버 쿼터 소진·외부소스(SearXNG/TMDb/iTunes) 429·에러 급증을 감지하기 위한 계측.
//
// 인메모리라 프로세스 재시작 시 리셋된다(현재 세션 사용량 근사). 네이버 일일 쿼터(1,000/일)는
// DayCalls 로 추적하되 자정(UTC) 경계에서 리셋 — 정확한 잔량은 네이버가 안 알려주므로 근사치.
package sourcehealth

import (
	"fmt"
	"sync"
	"time"
)

// Stat — 소스별 집계 스냅샷.
type Stat struct {
	Calls     int64     // 누적 호출(프로세스 생명주기)
	Errors    int64     // 에러(err 또는 status>=400)
	TooMany   int64     // 429(쿼터/레이트 초과)
	DayCalls  int64     // 오늘(UTC) 호출 수 — 쿼터 추적
	Day       string    // 오늘 날짜(YYYY-MM-DD)
	LastErr   string    // 마지막 에러 메시지
	LastErrAt time.Time // 마지막 에러 시각
}

var (
	mu  sync.Mutex
	reg = map[string]*Stat{}
)

// Record — 외부소스 1회 호출 결과를 집계한다. status=HTTP 상태(0=연결실패 등 미상),
// err=호출 에러(nil 이면 성공 또는 status 로만 판단). 클라이언트의 Do 직후 호출한다.
func Record(source string, status int, err error) {
	mu.Lock()
	defer mu.Unlock()
	s := reg[source]
	if s == nil {
		s = &Stat{}
		reg[source] = s
	}
	today := time.Now().UTC().Format("2006-01-02")
	if s.Day != today {
		s.Day = today
		s.DayCalls = 0
	}
	s.Calls++
	s.DayCalls++
	if err != nil || status >= 400 {
		s.Errors++
		if status == 429 {
			s.TooMany++
		}
		if err != nil {
			s.LastErr = err.Error()
		} else {
			s.LastErr = fmt.Sprintf("http %d", status)
		}
		s.LastErrAt = time.Now()
	}
}

// Snapshot — 현재 집계의 복사본(source→Stat).
func Snapshot() map[string]Stat {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]Stat, len(reg))
	for k, v := range reg {
		out[k] = *v
	}
	return out
}
