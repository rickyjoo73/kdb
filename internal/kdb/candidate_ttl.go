package kdb

// candidate_ttl — candidate 의 **무조건 종결 기한**.
//
// ★왜(오너 지시 2026-07-31 "쭈욱 흘러가도록 설계돼 있어? 안 빠지고 있잖아"):
// 전수 훑어보니 이 시스템에는 종결 규칙이 사실상 하나뿐이었고(intake_autoverify 의 21일
// TTL) 그마저 `triage_kept` 플래그 조건부였다. 나머지 경로는 전부 "제외"만 한다 —
// notes 플래그를 참조하는 코드 65곳 중 62곳(95%)이 `NOT LIKE`(=이 레인은 안 함)이고
// 선택(`LIKE`)은 3곳뿐이다. 모든 레인이 "내 것 아님"이라 말할 수 있는데 "내 것"이라고
// 말해야 하는 주체가 없으니, 플래그가 붙는 순간 잔여물이 되어 무한 누적된다.
// 실측: candidate 604 중 승급 경로가 열린 건 45(7.4%). 나머지는 플래그로 제외됨.
//
// ★기준 컬럼이 created_at 인 이유: `updated_at` 은 정체 지표로 못 쓴다. 레인들이 결론
// 없이 계속 건드려서 candidate 604 중 603 이 21일 내 갱신돼 있다(실측). 우리가 재고
// 싶은 건 "손을 탔나"가 아니라 "**얼마나 오래 미결인가**"다.
//
// ★되살린 행은 시계를 다시 시작한다 (2026-09-15).
//
//	`JTBC` 를 기각에서 후보로 되살렸더니 **28분 만에** 다시 기각됐다 —
//	created_at 이 83일 전이라 TTL 이 즉시 걸렸다. 그런데 그 83일 중 대부분은
//	**기각돼 있던 기간**이고, 기각은 미결이 아니라 결론이다. 결론이 난 채로 있던
//	시간을 "기한 내 실증 실패"로 세면, 되살린 행은 실증할 기회를 한 번도 못 얻는다.
//	오래된 행은 영원히 되살릴 수 없게 된다.
//
//	그래서 notes 의 `[reopened:YYYY-MM-DD]` 표시를 시계의 새 출발점으로 읽는다.
//	created_at 을 고치지 않는다 — 역사를 바꾸는 대신 **무엇이 언제 다시 열렸는지**를
//	기록하고 그것을 센다.
//
// ★기각인데 tombstone 이 아니다: `Tombstoned()` 는 **이름 기준**이라, 기각하면 그 이름의
// 재조회가 lookup/prepare 에서 막힌다(2026-07-31 김은정 사고). 이 기각의 명제는
// "이 레코드가 기한 내 실증되지 않았다"이지 "이 이름의 K-엔티티가 없다"가 아니다.
// 그래서 api.go 의 tombstone 근거에서 `[ttl-expire:reject]` 를 제외한다 — 소비자가 다시
// 요청하면 정상적으로 재발굴된다. **종결 + 재진입 가능**이 이 설계의 핵심이다.
//
// 전건 kwave_kdb_recheck_log(verdict='ttl-expire')에 남겨 revert 가능하다.

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CandidateTTLDays — 미결 candidate 의 최대 체류일. 큐 쪽 TTL(21일, 오너 승인 07-17)과
// 같은 값으로 맞춘다 — 같은 "포기 시점"을 두 파이프가 다르게 잡을 이유가 없다.
func CandidateTTLDays() int {
	if v := os.Getenv("KDB_CANDIDATE_TTL_DAYS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return 21
}

// CandidateTTLEnabled — 0 으로 즉시 정지(데이터를 바꾸는 레인이라 off-switch 필수).
func CandidateTTLEnabled() bool { return os.Getenv("KDB_CANDIDATE_TTL") != "0" }

// CandidateTTLInterval — 실행 주기.
func CandidateTTLInterval() time.Duration { return 30 * time.Minute }

// DrainExpireStaleCandidates — TTL 초과 미결 candidate 를 기각으로 종결한다.
// operator_locked 는 제외(운영자 결정이 자동 종결보다 상위).
// 반환 (기각 수, 검사 수).
func DrainExpireStaleCandidates(ctx context.Context, pool *pgxpool.Pool, limit int) (rejected, checked int) {
	if pool == nil || !CandidateTTLEnabled() {
		return 0, 0
	}
	ttl := CandidateTTLDays()
	rows, err := pool.Query(ctx, `
WITH c AS (
  SELECT e.id, e.canonical_ko, e.created_at,
         GREATEST(e.created_at, COALESCE((
           SELECT max(m[1]::timestamptz)
             FROM regexp_matches(COALESCE(e.notes,''), '\[reopened:([0-9]{4}-[0-9]{2}-[0-9]{2})\]', 'g') m
         ), e.created_at)) AS pending_since
    FROM kwave_entities e
   WHERE e.status='candidate'
     AND e.operator_locked=false
     AND COALESCE(e.notes,'') NOT LIKE '%[ttl-expire:%')
SELECT c.id::text, c.canonical_ko, EXTRACT(day FROM now()-c.pending_since)::int
  FROM c
 WHERE c.pending_since < now() - make_interval(days => $1)
 ORDER BY c.pending_since ASC
 LIMIT $2`, ttl, limit)
	if err != nil {
		log.Printf("kdb.candidate-ttl: select: %v", err)
		return 0, 0
	}
	type item struct {
		id, ko string
		age    int
	}
	var items []item
	for rows.Next() {
		var it item
		if rows.Scan(&it.id, &it.ko, &it.age) == nil {
			items = append(items, it)
		}
	}
	rows.Close()

	for _, it := range items {
		checked++
		reason := fmt.Sprintf("%d일 미결(TTL %d일) — 기한 내 승급 근거 확보 실패. 재요청 시 재발굴됨", it.age, ttl)
		tag, uerr := pool.Exec(ctx, `
UPDATE kwave_entities
   SET status='rejected', confidence=0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') || '[ttl-expire:reject] ' || $2,
       updated_at=now()
 WHERE id=$1::uuid AND status='candidate' AND operator_locked=false`, it.id, reason)
		if uerr != nil || tag.RowsAffected() == 0 {
			continue
		}
		rejected++
		_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, models, agreed, evidence)
VALUES ($1::uuid, $2, 'ttl-expire', 'deterministic-ttl', true, $3)`, it.id, it.ko, reason)
	}
	if checked > 0 {
		log.Printf("kdb.candidate-ttl: checked=%d rejected=%d (TTL %d일, 재요청 시 재발굴 가능)",
			checked, rejected, ttl)
	}
	return rejected, checked
}
