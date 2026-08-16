package main

// nightdrain.go — 새벽 유휴시간에 **백로그를 끝까지 소진**하는 오케스트레이터.
//
// ★왜 만들었나(2026-08-16, 오너 지시: "적어도 12시부터 5시까지 새벽시간에는 모든 것을
// 해결할 수 있도록 자율로 처리"). 종전 일일 블록은 05:00–07:00 창에서 **고정 배치를 한 번**
// 돌리고 끝났다(type-retrace 150 · cand-evidence 100 · active-audit 150 · evidence 1000).
// 백로그가 배치보다 크면 다음 날로 넘어가고, 그래서 3,365건짜리 백로그에 11일이 걸렸다.
// 배치 크기를 올리는 건 답이 아니었다 — **창이 닫힐 때까지 반복**해야 한다.
//
// 설계 원칙 넷:
//
//  1. **마감 우선.** 창(기본 00:00–05:00 KST)이 닫히면 진행 중인 라운드를 마치고 멈춘다.
//     ctx 에 deadline 을 걸어 외부 호출까지 함께 끊는다.
//  2. **진전 없으면 그 레인은 끝.** 라운드가 0건을 내면 그 레인은 소진된 것이다. 더 돌려도
//     같은 답이라 다음 레인으로 넘어간다(연속 무진전 2회 — 일시 장애로 한 번 0 이 나올 수
//     있어서 1회로는 조기 종료된다).
//  3. **레인 하나가 창을 독점하지 않는다.** 라운드로 나눠 순환시킨다. evidence 백로그가
//     3천이라고 type-retrace 가 굶으면 안 된다.
//  4. **하루 한 번.** 창 안에서 여러 tick 이 들어와도 그날 이미 돌았으면 건너뛴다.
//
// 안전은 각 Pass 안에 있다(08-15/16 에 넣은 것들) — 전송실패와 판정의 분리, 근거 대장
// 적재 없으면 승급 금지, 연속 무응답 시 회차 종료. 그래서 오래 돌려도 오염이 누적되지
// 않는다. **속도를 올리기 전에 그 가드들을 먼저 넣은 이유가 이것이다.**

import (
	"context"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
	"github.com/rickyjoo73/kdb/internal/kdb/verify"
)

// kstZone — 창 판정은 서버 TZ 가 아니라 KST 고정으로 한다(컨테이너 TZ 가 UTC 여도 동일).
var kstZone = time.FixedZone("KST", 9*3600)

// nightWindow — 야간 창 [start, end) 시(hour). 기본 00–05.
func nightWindow() (int, int) {
	start, end := 0, 5
	if v, err := strconv.Atoi(os.Getenv("KDB_NIGHT_START_HOUR")); err == nil && v >= 0 && v <= 23 {
		start = v
	}
	if v, err := strconv.Atoi(os.Getenv("KDB_NIGHT_END_HOUR")); err == nil && v >= 1 && v <= 24 {
		end = v
	}
	if end <= start {
		end = start + 1
	}
	return start, end
}

// inNightWindow — 지금이 창 안인가. 창의 남은 시간도 함께 준다.
func inNightWindow(now time.Time) (bool, time.Duration) {
	start, end := nightWindow()
	h := now.Hour()
	if h < start || h >= end {
		return false, 0
	}
	closeAt := time.Date(now.Year(), now.Month(), now.Day(), end, 0, 0, 0, kstZone)
	return true, closeAt.Sub(now)
}

// nightLane — 야간에 소진할 레인 하나. run 은 (진전 건수, 처리 건수) 를 돌려준다.
// 진전 0 이 연속 nightIdleRounds 회면 소진으로 보고 뺀다.
type nightLane struct {
	name  string
	batch int
	run   func(ctx context.Context) (progress, processed int)
	done  bool
	idle  int
}

// nightIdleRounds — 이만큼 연속으로 진전이 0 이면 그 레인은 소진.
// 2 인 이유: 외부 API 일시 장애로 한 라운드가 통째로 0 이 될 수 있어 1 회로는 조기 종료된다.
const nightIdleRounds = 2

// runNightDrain — 창이 닫힐 때까지 레인들을 순환하며 백로그를 소진한다.
//
// 반환값 없음. 진행은 로그로 남기고, 마지막에 요약 한 줄을 찍는다 — 아침에 이 한 줄만
// 읽으면 밤새 무슨 일이 있었는지 알 수 있어야 한다.
func runNightDrain(ctx context.Context, pool *pgxpool.Pool) {
	now := time.Now().In(kstZone)
	ok, remain := inNightWindow(now)
	if !ok {
		return
	}
	// 창 끝을 ctx 마감으로 건다 — 외부 호출까지 함께 끊긴다. 30초 여유를 둬서 마지막
	// 라운드가 DB 쓰기 도중 잘리지 않게 한다.
	if remain > 30*time.Second {
		remain -= 30 * time.Second
	}
	dctx, cancel := context.WithTimeout(ctx, remain)
	defer cancel()

	start, end := nightWindow()
	log.Printf("kdb.night: 야간 드레인 시작 (창 %02d:00–%02d:00 KST, 남은 %s)",
		start, end, remain.Round(time.Minute))

	lanes := []*nightLane{
		{
			// 근거 승급 — 가장 큰 백로그(unverified). Google News RSS 라 쿼터 제약이 없다.
			name: "evidence", batch: 300,
			run: func(c context.Context) (int, int) {
				up, _, proc, err := verify.EvidencePass(c, pool, 300)
				if err != nil {
					log.Printf("kdb.night[evidence]: %v", err)
					return 0, 0
				}
				return up, proc
			},
		},
		{
			// ko.wikipedia 앵커 — 키·쿼터 없음. 뉴스에 안 실리지만 위키백과에는 있는 계층을
			// 잡는다(실측 수율 10%, 정밀도 8/8). evidence 레인이 꼬리에서 무너지는 구간을
			// 이쪽이 받는다.
			name: "kowiki-anchor", batch: 200,
			run: func(c context.Context) (int, int) {
				return kdb.DrainKoWikiAnchors(c, pool, 200)
			},
		},
		{
			// iTunes 카탈로그 앵커 — 키 없음. unverified 최대 버킷인 song_album(667건)을
			// 받는다. 곡은 위키백과 문서도 뉴스도 잘 없어 위 두 레인이 구조적으로 못 잡는다.
			// ★배치가 작은 건 처리량이 아니라 **분당 20회 제한** 때문이다(3.2s×100 ≈ 5분).
			// 라운드가 짧아야 다른 레인이 굶지 않는다.
			name: "itunes-anchor", batch: 100,
			run: func(c context.Context) (int, int) {
				return kdb.DrainITunesAnchors(c, pool, itunes.New(), 100)
			},
		},
		{
			// 무ref active 오염 감사 — 30일 커서라 매일 일정량이 자격을 얻는다.
			name: "active-audit", batch: 200,
			run: func(c context.Context) (int, int) {
				rej, flag, up, proc, err := verify.ActiveAuditPass(c, pool, 200, kdb.TriageKeywordConfirmed)
				if err != nil {
					log.Printf("kdb.night[active-audit]: %v", err)
					return 0, 0
				}
				return rej + flag + up, proc
			},
		},
		{
			// 요청대기 candidate 뉴스근거 승급.
			name: "cand-evidence", batch: 150,
			run: func(c context.Context) (int, int) {
				up, flag, proc, err := verify.CandidateEvidencePass(c, pool, 150)
				if err != nil {
					log.Printf("kdb.night[cand-evidence]: %v", err)
					return 0, 0
				}
				return up + flag, proc
			},
		},
		{
			// auto_evidence 경로 엔티티의 타입 재판정.
			name: "type-retrace", batch: 150,
			run: func(c context.Context) (int, int) {
				fixed, _, flagged, proc, err := verify.TypeRetracePass(c, pool, 150)
				if err != nil {
					log.Printf("kdb.night[type-retrace]: %v", err)
					return 0, 0
				}
				return fixed + flagged, proc
			},
		},
	}

	totals := map[string]int{}
	rounds := 0
	for {
		if dctx.Err() != nil {
			log.Printf("kdb.night: 창 마감 — 라운드 %d 에서 종료", rounds)
			break
		}
		active := 0
		for _, l := range lanes {
			if l.done {
				continue
			}
			active++
			if dctx.Err() != nil {
				break
			}
			prog, proc := l.run(dctx)
			totals[l.name] += prog
			// ★소진 판정은 **판정 건수(proc)** 로 한다 — 승급 건수(prog)가 아니다
			// (2026-08-16 수정). 기각을 원장에 적는 것도 후보를 90일 동안 영구히
			// 소모하는 진전이다. 종전 `prog == 0` 규칙이면 수율 33%인 iTunes 레인이
			// 한 라운드 0건을 내는 순간 낙인이 찍히고, 두 번이면 후보 700건을 남긴 채
			// 그날 밤 퇴장한다. **"소진"은 "얻을 게 없다"가 아니라 "물어볼 게 없다"다.**
			//
			// 무한루프 걱정은 각 Pass 가 막는다 — proc 은 **판정에 도달한 건수**만
			// 세고 전송실패는 안 센다. 외부 API 가 죽으면 proc=0 → 정상적으로 소진 처리.
			if proc == 0 {
				l.idle++
				if l.idle >= nightIdleRounds {
					l.done = true
					log.Printf("kdb.night[%s]: 소진(연속 무판정 %d회) — 누적 %d건",
						l.name, l.idle, totals[l.name])
				}
			} else {
				l.idle = 0
			}
		}
		if active == 0 {
			log.Printf("kdb.night: 모든 레인 소진 — 라운드 %d 에서 완료", rounds)
			break
		}
		rounds++
	}

	// ★결정론 스윕으로 마감한다. 밤새 붙은 ref·근거가 등급에 반영되어야 아침에 보는
	// 숫자가 실제 상태와 맞는다(그러지 않으면 다음 10분 tick 까지 어긋나 보인다).
	if c, err := verify.SweepDeterministic(ctx, pool); err == nil {
		log.Printf("kdb.night: 마감 스윕 — authoritative=%d evidenced=%d unverified=%d",
			c.Authoritative, c.Evidenced, c.Unverified)
	}
	log.Printf("kdb.night: 완료 라운드=%d evidence=%d kowiki-anchor=%d itunes-anchor=%d active-audit=%d cand-evidence=%d type-retrace=%d",
		rounds, totals["evidence"], totals["kowiki-anchor"], totals["itunes-anchor"],
		totals["active-audit"], totals["cand-evidence"], totals["type-retrace"])
}
