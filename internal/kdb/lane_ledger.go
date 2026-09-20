package kdb

// lane_ledger — **레인이 무엇을 했는지 남기고, 조용한 0건을 스스로 찾아낸다.**
//
// ★왜 (2026-09-20). 하루에 같은 자리에서 세 번 데였다:
//
//	zhwiki    dry-run 이 「389건 채움」이라 말하는 동안 원장은 0건 바뀌었다
//	localfill 로그 13줄을 24시간치로 읽었다 — 앱이 48분 전 재시작한 것이었다
//	org-anchor 유형 둘을 넓혔는데 실제 수확은 50건 검사에 0건이었다
//
//	셋 다 사람이 로그를 뒤져 찾아냈다. 그런데 로그는 앱 수명만큼만 살고, 0건일 때
//	아예 안 찍는 레인이 많다. **안 돈 것과 돌았는데 0건인 것을 구별할 수 없다.**
//
// ★그래서 세 수를 나눠 적는다 — scanned(뽑은 것) · applied(원장이 바뀐 것) ·
//	skipped(+사유별 계수). 이 둘을 섞어 적은 것이 389건 거짓말의 원인이었다.
//
// ★기록이 레인을 방해하면 안 된다. 실패는 로그 한 줄로 끝내고 레인은 계속 간다 —
//	계측기가 본체를 죽이는 것이 가장 나쁘다.

import (
	"context"
	"encoding/json"
	"log"
	"sort"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LaneRun — 레인 1회 실행의 관측. 레인이 돌면서 채우고 끝에 Record 한다.
type LaneRun struct {
	Lane    string
	Started time.Time
	Scanned int
	Applied int
	Skipped int
	Reasons map[string]int
	Dry     bool
}

// NewLaneRun — 레인 시작. Started 를 지금으로 잡는다.
func NewLaneRun(lane string, dry bool) *LaneRun {
	return &LaneRun{Lane: lane, Started: time.Now(), Dry: dry, Reasons: map[string]int{}}
}

// Scan — 선정 쿼리가 뽑은 수를 적는다.
func (r *LaneRun) Scan(n int) {
	if r == nil || n <= 0 {
		return
	}
	r.Scanned += n
}

// Apply — 원장이 **실제로 바뀐** 한 건.
func (r *LaneRun) Apply() {
	if r == nil {
		return
	}
	r.Applied++
}

// Skip — 뽑았지만 쓰지 않은 한 건과 그 사유.
//
// 사유를 빈 문자열로 두지 않는다. 뭉뚱그린 0건은 「대상이 없다」와 「못 찾았다」를
// 구분하지 못하고, 그 구분이 없으면 다음에 무엇을 고쳐야 할지 알 수 없다.
func (r *LaneRun) Skip(reason string) {
	if r == nil {
		return
	}
	r.Skipped++
	if reason == "" {
		reason = "(사유없음)"
	}
	if r.Reasons == nil {
		r.Reasons = map[string]int{}
	}
	r.Reasons[reason]++
}

// SilentZero — 이번 회차가 조용한 0건인가. 뽑았는데 하나도 안 썼다.
func (r *LaneRun) SilentZero() bool {
	return r != nil && r.Scanned > 0 && r.Applied == 0
}

// Summary — 로그 한 줄. 사유는 많은 것부터.
func (r *LaneRun) Summary() string {
	if r == nil {
		return ""
	}
	b, _ := json.Marshal(r.topReasons(5))
	tag := ""
	if r.Dry {
		tag = " [dry]"
	}
	if r.SilentZero() {
		tag += " ★조용한0건"
	}
	return r.Lane + tag + ": scanned=" + strconv.Itoa(r.Scanned) + " applied=" + strconv.Itoa(r.Applied) +
		" skipped=" + strconv.Itoa(r.Skipped) + " reasons=" + string(b)
}

// topReasons — 많은 사유부터 n 개. 동수는 이름순으로 고정한다(로그가 회차마다 흔들리지 않게).
func (r *LaneRun) topReasons(n int) map[string]int {
	type kv struct {
		k string
		v int
	}
	all := make([]kv, 0, len(r.Reasons))
	for k, v := range r.Reasons {
		all = append(all, kv{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].v != all[j].v {
			return all[i].v > all[j].v
		}
		return all[i].k < all[j].k
	})
	if len(all) > n {
		all = all[:n]
	}
	out := make(map[string]int, len(all))
	for _, e := range all {
		out[e.k] = e.v
	}
	return out
}

// Record — 원장에 남긴다. 실패해도 레인을 막지 않는다.
func (r *LaneRun) Record(ctx context.Context, pool *pgxpool.Pool) {
	if r == nil || pool == nil || r.Lane == "" {
		return
	}
	// applied ≤ scanned 는 DB 제약이다. 레인이 scanned 를 안 적었는데 쓰기만 한 경우
	// (선정을 여러 번 하는 레인)에도 기록이 통째로 날아가지 않게 여기서 맞춘다.
	if r.Applied > r.Scanned {
		r.Scanned = r.Applied
	}
	reasons, err := json.Marshal(r.Reasons)
	if err != nil || len(reasons) == 0 {
		reasons = []byte("{}")
	}
	ms := int(time.Since(r.Started) / time.Millisecond)
	if _, err := pool.Exec(ctx, `
INSERT INTO kwave_kdb_lane_runs (lane, started_at, duration_ms, scanned, applied, skipped, reasons, dry)
VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8)`,
		r.Lane, r.Started, ms, r.Scanned, r.Applied, r.Skipped, string(reasons), r.Dry); err != nil {
		log.Printf("kdb.lane-ledger: %s 기록 실패(레인은 계속): %v", r.Lane, err)
		return
	}
	log.Printf("kdb.lane-ledger: %s", r.Summary())
}

// LaneHealth — 한 레인의 최근 성적.
type LaneHealth struct {
	Lane    string
	Runs    int
	Scanned int
	Applied int
	Silent  bool // 뽑기는 하는데 한 건도 안 쓴다
}

// IsSilentZero — 조용한 0건 판정. **순수 함수**라 시험이 규칙을 고정한다.
//
// 한 회차만 0건인 것은 정상이다(대상이 없을 수 있다). 여러 회차 동안 **뽑기는
// 하는데 한 건도 안 쓰는 것**이 조용한 0건이다 — 선정과 쓰기가 다른 조건을 보고
// 있다는 신호다.
func IsSilentZero(runs, scanned, applied, minRuns int) bool {
	if minRuns <= 0 {
		minRuns = 3
	}
	return runs >= minRuns && scanned > 0 && applied == 0
}

// LaneHealthSince — 최근 구간의 레인별 성적. 조용한 0건을 **질의 한 줄로** 찾는다.
func LaneHealthSince(ctx context.Context, pool *pgxpool.Pool, since time.Duration, minRuns int) ([]LaneHealth, error) {
	if pool == nil {
		return nil, nil
	}
	if since <= 0 {
		since = 24 * time.Hour
	}
	rows, err := pool.Query(ctx, `
SELECT lane, count(*)::int, coalesce(sum(scanned),0)::int, coalesce(sum(applied),0)::int
  FROM kwave_kdb_lane_runs
 WHERE dry = false AND started_at > now() - $1::interval
 GROUP BY lane
 ORDER BY coalesce(sum(scanned),0) DESC`, since.String())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LaneHealth
	for rows.Next() {
		var h LaneHealth
		if err := rows.Scan(&h.Lane, &h.Runs, &h.Scanned, &h.Applied); err != nil {
			continue
		}
		h.Silent = IsSilentZero(h.Runs, h.Scanned, h.Applied, minRuns)
		out = append(out, h)
	}
	return out, rows.Err()
}
