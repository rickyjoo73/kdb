package kdbadmin

import (
	"context"
	"net/http"
	"os"
	"sort"
	"strconv"
	"time"

	"github.com/rickyjoo73/kdb/internal/kdb/sourcehealth"
)

// --- 운영 점검 (/admin/ops/health) ------------------------------------------
// 오너 방침(2026-07-04): 처리는 "즉시"(키워드 들어오자마자 발굴 cascade 가 공식소스로 채움),
// 점검은 "주기". 이 뷰(+워커 주기 이상감지 로그)가 점검 계층 — 처리가 안 되는지·장애가
// 있는지 한눈에 본다. 처리 자체는 여기서 안 함(감지·노출만).
//
// 점검 3축:
//   ① 처리 현황 — 발굴 큐 상태·최근 지연·SLA(빠른 채움) 위반.
//   ② 채움 품질 — 신규 발굴이 공식소스로 채워지나(공식/근거/미검증 tier 비율).
//   ③ 장애 신호 — 발굴 실패·큐 적체·재수집(stale reap).

const slaSeconds = 120 // 채움 지연 SLA(초). 초과분 = "느린 처리" 경고.

type opsHealthData struct {
	BuildVersion string
	// 큐
	QPending, QProgress, QFailed, QDone int64
	// 24h 발굴 지연
	Done24h, Over24h int64
	AvgSec, P50Sec   int64
	OverPct          int
	// 7d 채움 품질
	New7d, Official7d, Evidenced7d, Unverified7d int64
	OfficialPct                                  int
	// 장애 신호
	Failed7d int64
	// DB 백업 상태 (kwave_kdb_backup_log)
	LastBackupAt *time.Time
	BackupAgeH   int
	BackupSizeMB int64
	// 외부소스 헬스 (인메모리 계측)
	Sources []sourceRow
	// 종합 이상 판정
	Alerts []opsAlert
}

// sourceRow — 외부소스 1건의 헬스(admin 표시용).
type sourceRow struct {
	Name      string
	Calls     int64
	DayCalls  int64
	Errors    int64
	TooMany   int64
	ErrPct    int
	Quota     int // 일일 쿼터(0=미설정). 네이버=1000.
	QuotaPct  int // DayCalls/Quota
	LastErr   string
	LastErrAt *time.Time
}

// naverDailyQuota — 네이버 오픈API 일일 호출 한도(오너 확인).
const naverDailyQuota = 1000

type opsAlert struct {
	Level string // crit | warn
	Msg   string
}

func (s *Server) opsHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	var d opsHealthData
	d.BuildVersion = os.Getenv("KDB_BUILD_VERSION")

	// ① 처리 현황 — 큐 상태
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FILTER (WHERE status='pending'),
       count(*) FILTER (WHERE status='in_progress'),
       count(*) FILTER (WHERE status='failed'),
       count(*) FILTER (WHERE status='done')
FROM kwave_entity_research_queue`).Scan(&d.QPending, &d.QProgress, &d.QFailed, &d.QDone)

	// 24h 발굴 지연 (빠른 채움 SLA)
	_ = s.pool.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE EXTRACT(EPOCH FROM (finished_at-created_at)) > $1),
       COALESCE(round(avg(EXTRACT(EPOCH FROM (finished_at-created_at)))::numeric,0),0),
       COALESCE(round(percentile_cont(0.5) WITHIN GROUP (ORDER BY EXTRACT(EPOCH FROM (finished_at-created_at)))::numeric,0),0)
FROM kwave_entity_research_queue
WHERE finished_at IS NOT NULL AND created_at > now()-interval '24 hours'`,
		slaSeconds).Scan(&d.Done24h, &d.Over24h, &d.AvgSec, &d.P50Sec)
	if d.Done24h > 0 {
		d.OverPct = int(d.Over24h * 100 / d.Done24h)
	}

	// ② 채움 품질 — 7d 신규 발굴 tier
	_ = s.pool.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE verification_tier='authoritative'),
       count(*) FILTER (WHERE verification_tier='evidenced'),
       count(*) FILTER (WHERE verification_tier='unverified')
FROM kwave_entities
WHERE status='active' AND created_at > now()-interval '7 days'`).
		Scan(&d.New7d, &d.Official7d, &d.Evidenced7d, &d.Unverified7d)
	if d.New7d > 0 {
		d.OfficialPct = int((d.Official7d + d.Evidenced7d) * 100 / d.New7d)
	}

	// ③ 장애 신호 — 발굴 실패
	_ = s.pool.QueryRow(ctx, `
SELECT count(*) FROM kwave_entity_research_queue
WHERE status='failed' AND created_at > now()-interval '7 days'`).Scan(&d.Failed7d)

	// DB 백업 상태 — 마지막 성공 백업 시각/크기(백업이 실제로 도는지 감시).
	var sizeBytes int64
	if err := s.pool.QueryRow(ctx, `
SELECT created_at, COALESCE(size_bytes,0) FROM kwave_kdb_backup_log
 WHERE status='ok' ORDER BY created_at DESC LIMIT 1`).Scan(&d.LastBackupAt, &sizeBytes); err == nil && d.LastBackupAt != nil {
		d.BackupAgeH = int(time.Since(*d.LastBackupAt).Hours())
		d.BackupSizeMB = sizeBytes / (1024 * 1024)
	}

	// 외부소스 헬스 (인메모리 계측 스냅샷)
	d.Sources = collectSourceHealth()

	// 종합 이상 판정
	d.Alerts = opsEvaluate(d)

	s.render(w, r, "ops_health.html", map[string]any{
		"title": "운영 점검 · 처리/장애",
		"d":     d,
		"sla":   slaSeconds,
		"page":  "/admin/ops/health",
	})
}

// collectSourceHealth — 외부소스 계측 스냅샷을 admin 표시용 행으로 변환(이름순).
func collectSourceHealth() []sourceRow {
	snap := sourcehealth.Snapshot()
	rows := make([]sourceRow, 0, len(snap))
	for name, st := range snap {
		r := sourceRow{
			Name: name, Calls: st.Calls, DayCalls: st.DayCalls,
			Errors: st.Errors, TooMany: st.TooMany, LastErr: st.LastErr,
		}
		if st.Calls > 0 {
			r.ErrPct = int(st.Errors * 100 / st.Calls)
		}
		if name == "naver" {
			r.Quota = naverDailyQuota
			r.QuotaPct = int(st.DayCalls * 100 / naverDailyQuota)
		}
		if !st.LastErrAt.IsZero() {
			t := st.LastErrAt
			r.LastErrAt = &t
		}
		rows = append(rows, r)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	return rows
}

// opsEvaluate — 지표에서 이상(장애·미처리)을 판정한다. 워커 주기 이상감지(runHealthCheck)와
// 동일 임계 — admin 뷰와 로그경고가 같은 기준으로 움직인다.
func opsEvaluate(d opsHealthData) []opsAlert {
	var a []opsAlert
	i := func(n int64) string { return strconv.FormatInt(n, 10) }
	if d.QPending > 100 {
		a = append(a, opsAlert{"crit", "발굴 큐 적체: pending " + i(d.QPending) + "건 (>100). 워커 처리량 부족 의심."})
	} else if d.QPending > 30 {
		a = append(a, opsAlert{"warn", "발굴 큐 대기 " + i(d.QPending) + "건 누적."})
	}
	if d.Failed7d > 20 {
		a = append(a, opsAlert{"crit", "발굴 실패 " + i(d.Failed7d) + "건/7d (>20). 소스·분류 장애 점검."})
	} else if d.Failed7d > 5 {
		a = append(a, opsAlert{"warn", "발굴 실패 " + i(d.Failed7d) + "건/7d."})
	}
	if d.Done24h >= 10 && d.OverPct > 50 {
		a = append(a, opsAlert{"warn", "느린 채움: 24h 발굴의 " + i(int64(d.OverPct)) + "%가 " + i(slaSeconds) + "초 초과. enrich cascade 지연 점검."})
	}
	if d.New7d >= 20 && d.OfficialPct < 60 {
		a = append(a, opsAlert{"warn", "공식 채움률 저하: 7d 신규의 " + i(int64(d.OfficialPct)) + "%만 공식/근거(<60%). 공식소스 응답 점검."})
	}
	// DB 백업 감시: 백업이 없거나 오래되면 경고(데이터 손실 리스크).
	if d.LastBackupAt == nil {
		a = append(a, opsAlert{"crit", "DB 백업 기록 없음 — pg_dump cron·scripts/kdb-db-backup.sh 점검. 데이터 손실 시 복구 불가."})
	} else if d.BackupAgeH >= 26 {
		a = append(a, opsAlert{"crit", "DB 백업 " + i(int64(d.BackupAgeH)) + "시간 전(>26h) — 백업이 멈췄는지 점검."})
	} else if d.BackupAgeH >= 14 {
		a = append(a, opsAlert{"warn", "DB 백업 " + i(int64(d.BackupAgeH)) + "시간 전 — 다음 예정 백업 확인."})
	}
	// 외부소스 이상: 네이버 쿼터 임박, 429, 에러율 급증.
	for _, sr := range d.Sources {
		if sr.Quota > 0 && sr.QuotaPct >= 90 {
			a = append(a, opsAlert{"crit", sr.Name + " 쿼터 임박: 오늘 " + i(sr.DayCalls) + "/" + i(int64(sr.Quota)) + " (" + i(int64(sr.QuotaPct)) + "%). 소진 시 검색 채움 마비."})
		} else if sr.Quota > 0 && sr.QuotaPct >= 75 {
			a = append(a, opsAlert{"warn", sr.Name + " 쿼터 " + i(int64(sr.QuotaPct)) + "% 소비(" + i(sr.DayCalls) + "/" + i(int64(sr.Quota)) + ")."})
		}
		if sr.TooMany > 0 {
			a = append(a, opsAlert{"warn", sr.Name + " 429(레이트/쿼터 초과) " + i(sr.TooMany) + "회 — 백오프·페이싱 점검."})
		}
		if sr.Calls >= 20 && sr.ErrPct >= 30 {
			a = append(a, opsAlert{"crit", sr.Name + " 에러율 " + i(int64(sr.ErrPct)) + "% (" + i(sr.Errors) + "/" + i(sr.Calls) + ") — 소스 장애 의심."})
		}
	}
	return a
}
