package kdb

// backlog_watch — "어떤 조건의 엔티티가 어느 레인에도 안 닿고 늙어가는가"를 상시 계측한다.
//
// ★왜 만드는가(오너 지시 2026-07-31 "땜방이 아니라 전반적으로 해결될 배선"):
// 같은 날 병목 진단에서 **동일한 결함을 네 번** 발견했다.
//   ① ondemand      — 셀렉트가 term 을 통과시키는데 승급 함수는 term 을 거부 → 22일 공회전
//   ② retype-stuck  — 대상이 'autoverify_type_reassigned' 플래그에 묶여 term/unknown 미포함
//   ③ revert-term   — 승급 가드와 이름검색 드레인 사이 사각지대에 191건 68일 방치
//   ④ tmdb-locale   — 승급 드레인이 candidate 전용이라 active 로케일 빈칸 193건 미도달
// 공통 원인은 하나다: **레인의 대상 선정이 "고치려는 조건" 이 아니라 부수 속성(유입경로·
// notes 표식·ref 보유 여부·status)에 묶여 있다.** 그래서 조건을 만족하는데도 어떤 레인도
// 집지 않는 집합이 조용히 쌓인다. 넷 다 사람이 손으로 찾았다 — 그게 문제다.
//
// 이 워치는 개별 결함을 고치지 않는다. **결함이 생겼다는 사실을 자동으로 드러낸다.**
// 조건별로 (백로그 크기 · 가장 오래 안 움직인 항목의 나이 · 임계 초과 수)를 주기적으로
// 기록한다. 어떤 레인이 자기 백로그를 못 덮으면 "oldest_days" 가 단조 증가하므로,
// 지표만 보면 위 네 사례가 전부 사전에 드러났을 것이다.
//
// 판정은 하지 않는다(데이터 변경 0). 계측과 경보만 한다 — 이 워치 자체가 또 하나의
// 손볼 대상이 되지 않도록.

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// backlogCondition — 감시할 "고쳐야 할 상태". where 는 kwave_entities e 기준 술어이고,
// staleCol 은 "마지막으로 이 항목이 움직인 시각" 으로 볼 컬럼이다.
//
// 조건은 레인 이름이 아니라 **상태** 로 정의한다. 레인 구현이 바뀌어도 감시가 따라
// 무너지지 않게 하려는 것 — 레인에 묶으면 이 파일이 또 같은 병에 걸린다.
type backlogCondition struct {
	Name      string
	Where     string
	WarnDays  int // 이 나이를 넘긴 항목이 있으면 경보
	StaleCol  string
	Rationale string
}

var backlogConditions = []backlogCondition{
	{
		Name:      "candidate-stuck",
		Where:     `e.status='candidate' AND e.operator_locked=false`,
		WarnDays:  30,
		StaleCol:  `COALESCE(e.last_enriched_at, e.created_at)`,
		Rationale: "승급도 기각도 안 된 채 정체 — ①②③ 유형",
	},
	{
		Name:      "active-cjk-gap",
		Where:     `e.status='active' AND (COALESCE(e.canonical_ja,'')='' OR COALESCE(e.canonical_zh,'')='' OR COALESCE(e.canonical_zh_hant,'')='')`,
		WarnDays:  60,
		StaleCol:  `e.updated_at`,
		Rationale: "서빙 중인데 CJK 표기 빈칸 — ④ 유형",
	},
	{
		Name:      "active-latin-gap",
		Where:     `e.status='active' AND (COALESCE(e.canonical_en,'')='' OR COALESCE(e.canonical_vi,'')='' OR COALESCE(e.canonical_es,'')='' OR COALESCE(e.canonical_id,'')='' OR COALESCE(e.canonical_pt_br,'')='')`,
		WarnDays:  60,
		StaleCol:  `e.updated_at`,
		Rationale: "라틴 로케일 빈칸(규칙으로 채워지는 층이라 남으면 배선 문제)",
	},
	{
		Name:      "active-no-anchor",
		Where:     `e.status='active' AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id) AND COALESCE(array_length(e.source_urls,1),0)=0`,
		WarnDays:  90,
		StaleCol:  `e.updated_at`,
		Rationale: "서빙 중인데 권위 앵커 전무 — 오염 재심 대상",
	},
	{
		// ★active-no-anchor 의 부분집합. 둘을 나눠 세는 이유(2026-07-31):
		// no-anchor 4,346 중 대다수는 뉴스근거로 승급된 것이고(최근 7일 유입의 95%가
		// [cand-evidence]), 그건 "앵커 없음"이지 "근거 없음"이 아니다. 두 명제를 한 지표로
		// 뭉치면 ①진짜 무근거가 뉴스근거 더미에 묻히고 ②근거 URL 을 채우는 것만으로
		// 지표가 내려가 개선한 것처럼 보인다. 앵커 확보와 근거 추적은 각각 세야 정직하다.
		Name: "active-no-evidence",
		Where: `e.status='active'
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id)
   AND COALESCE(array_length(e.source_urls,1),0)=0
   AND NOT EXISTS (SELECT 1 FROM kwave_kdb_evidence_refs v WHERE v.entity_id=e.id)`,
		WarnDays:  90,
		StaleCol:  `e.updated_at`,
		Rationale: "서빙 중인데 앵커도 근거 URL 도 없음 — 되짚을 수 있는 게 아무것도 없는 진짜 사각지대",
	},
	{
		Name:      "type-mismatch",
		Where:     `e.status IN ('active','candidate') AND COALESCE(e.notes,'') LIKE '%[typeaudit:mismatch%' AND COALESCE(e.notes,'') NOT LIKE '%[typeaudit:cleared%'`,
		WarnDays:  14,
		StaleCol:  `e.updated_at`,
		Rationale: "결정적 감사가 타입 불일치로 표시 — 판정 레인이 받아야 함",
	},
	{
		Name:      "never-examined",
		Where:     `e.status IN ('active','candidate') AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id)`,
		WarnDays:  90,
		StaleCol:  `e.created_at`,
		Rationale: "어떤 드레인도 한 번도 시도한 적 없음 — 커버리지 구멍의 직접 지표",
	},
}

// BacklogSnapshot — 조건 1건의 계측 결과.
type BacklogSnapshot struct {
	Name       string
	Total      int
	OverWarn   int
	OldestDays int
}

// WatchBacklogs — 조건별 백로그를 계측해 로그로 남기고 스냅샷을 반환한다.
// 데이터는 변경하지 않는다.
func WatchBacklogs(ctx context.Context, pool *pgxpool.Pool) []BacklogSnapshot {
	if pool == nil {
		return nil
	}
	out := make([]BacklogSnapshot, 0, len(backlogConditions))
	for _, c := range backlogConditions {
		q := `
SELECT count(*),
       count(*) FILTER (WHERE ` + c.StaleCol + ` < now() - make_interval(days => $1)),
       COALESCE(EXTRACT(day FROM now() - min(` + c.StaleCol + `))::int, 0)
  FROM kwave_entities e
 WHERE ` + c.Where
		var s BacklogSnapshot
		s.Name = c.Name
		if err := pool.QueryRow(ctx, q, c.WarnDays).Scan(&s.Total, &s.OverWarn, &s.OldestDays); err != nil {
			log.Printf("kdb.backlog-watch: %s: %v", c.Name, err)
			continue
		}
		out = append(out, s)
		if s.OverWarn > 0 {
			// 경보: 임계를 넘긴 항목이 있다 = 이 조건을 담당하는 레인이 백로그를 못 덮고 있다.
			log.Printf("kdb.backlog-watch: ⚠ %s total=%d over_%dd=%d oldest=%dd — %s",
				c.Name, s.Total, c.WarnDays, s.OverWarn, s.OldestDays, c.Rationale)
		} else {
			log.Printf("kdb.backlog-watch: %s total=%d oldest=%dd (임계 %dd 내)",
				c.Name, s.Total, s.OldestDays, c.WarnDays)
		}
	}
	return out
}

// backlogWatchInterval — 계측 주기. 지표라 자주 볼 필요 없다.
func backlogWatchInterval() time.Duration { return 6 * time.Hour }

// BacklogWatchInterval — 외부(cmd) 노출용.
func BacklogWatchInterval() time.Duration { return backlogWatchInterval() }
