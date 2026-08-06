package kdb

// fill_retry.go — 채움 레인 공통의 재시도 규칙 (2026-08-06, migrations/0100).
//
// 이 파일이 존재하는 이유: 같은 질문("이 칸을 다시 채워볼까?")에 레인마다 다른 답을
// 갖고 있었다 — mt-fill 30일, gt-fill 30일, tmdb-locale 30일(성공·실패 무관),
// enrichground 7일, ground-strict-skip 7일, cascade 는 7일 창의 exhausted. 여섯 규칙이
// 전부 "시간이 지났나"만 보고 "입력이 바뀌었나"는 보지 않았다.
//
// 그 결과가 08-06 실측이다: 07-26 대량 백필 한 번이 백로그 전체의 시도권을 하루에
// 소진시켜, 상시 cron 이 예산 620 을 받고 6건을 처리하고 있었다. 레인 4개의 실제 선정
// 건수가 전부 0 이었다(mt-fill:zh 1,568→0, mt-fill:ja 1,014→0, gt-fill:en 79→0,
// tmdb-locale 255→0).
//
// 규칙은 이제 하나다:
//
//	같은 입력으로 이미 실패했으면 다시 하지 않는다. 입력이 바뀌면 즉시 다시 한다.
//
// 시간은 안전판으로만 남는다 — 우리 입력이 안 변해도 외부 소스가 그 사이 값을 얻었을
// 수 있으므로 FillRetryRevisitDays 마다 1회 재방문한다.

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"
)

// FillRetryRevisitDays — 입력이 그대로여도 이 기간이 지나면 1회 재방문한다.
//
// 왜 필요한가: input_hash 는 "우리가 아는 것"의 지문이라, Wikidata 가 다음 달에 zh
// 라벨을 새로 얻는 것 같은 **바깥의 변화**는 잡지 못한다. 그것까지 영구히 포기하면
// 자기치유가 사라진다.
//
// 왜 30 이 아니라 90 인가: 30일 회전은 값을 하나도 만들지 못하면서 백로그를 "재시도
// 대기"로 위장시켰다(42차 §11). 재방문은 드물어야 하고, 실제 회수는 지문 변화가
// 담당해야 한다 — 앵커가 붙거나 다른 로케일이 채워지면 90일을 기다리지 않는다.
const FillRetryRevisitDays = 90

// FillRetryPredicate — 선정 쿼리에 그대로 끼워 쓰는 술어. true 면 "지금 시도해도 된다".
//
// entityAlias 는 바깥 쿼리의 kwave_entities 별칭, fieldParam 은 field 값이 들어갈
// 파라미터 자리("$2" 등)다. 술어 안에서 별칭 a 를 쓰므로 바깥 쿼리는 a 를 피할 것.
//
// ★주의: 이 술어가 제외하는 것은 **결정적 실패 기록**뿐이다. 전송실패(LLM·외부 API
// 장애)는 애초에 원장에 들어가면 안 된다 — MarkFillAttempt 주석 참조.
func FillRetryPredicate(entityAlias, fieldParam string) string {
	return `NOT EXISTS (
    SELECT 1 FROM kwave_kdb_enrich_attempts a
     WHERE a.entity_id = ` + entityAlias + `.id
       AND a.field = ` + fieldParam + `
       AND a.input_hash = kdb_fill_input_hash(` + entityAlias + `.id)
       AND a.last_attempt_at > now() - interval '` + strconv.Itoa(FillRetryRevisitDays) + ` days')`
}

// MarkFillAttempt — 결정적 실패를 원장에 기록한다. input_hash 를 함께 남기므로 "무슨
// 입력에 대해 실패했는지"가 보존되고, 그 입력이 바뀌면 선정 쿼리가 자동으로 다시 집는다.
//
// ★여기 오면 안 되는 것: LLM 게이트 오류·번역 API 장애·타임아웃 같은 **전송실패**.
// 그건 "이 값이 나쁘다"가 아니라 "물어보지 못했다"이고, 기록하면 멀쩡한 후보가 낙인
// 찍힌다. 호출측이 err 를 보고 마킹 없이 다음 회차로 넘겨야 한다.
// (같은 계열의 결함을 이 저장소는 이미 네 번 고쳤다 — a022567, enricher/layers.go 의
// transport-failure 분기, 79f4d9a.)
//
// verdict 는 판정 종류(no-translation/bad-title/literal/guard-reject 등),
// reason 은 그 근거 한 줄이다. reason 이 있어야 사후에 "게이트가 나쁘다고 했다" 와
// "게이트를 못 불렀다" 를 구분할 수 있다.
func MarkFillAttempt(ctx context.Context, pool *pgxpool.Pool, entityID, field, verdict, reason string) {
	if pool == nil {
		return
	}
	_, _ = pool.Exec(ctx, `
INSERT INTO kwave_kdb_enrich_attempts
       (entity_id, field, attempts, last_attempt_at, last_source, last_reason, input_hash)
VALUES ($1, $2, 1, now(), $3, $4, kdb_fill_input_hash($1))
ON CONFLICT (entity_id, field) DO UPDATE
   SET attempts        = kwave_kdb_enrich_attempts.attempts + 1,
       last_attempt_at = now(),
       last_source     = EXCLUDED.last_source,
       last_reason     = EXCLUDED.last_reason,
       input_hash      = EXCLUDED.input_hash`, entityID, field, verdict, reason)
}

// ClearFillAttempt — 칸이 채워졌으면 원장에서 지운다. 남겨두면 나중에 그 값이 오염
// 판정으로 비워졌을 때(dataqa) 옛 실패 기록이 재시도를 막는다.
func ClearFillAttempt(ctx context.Context, pool *pgxpool.Pool, entityID, field string) {
	if pool == nil {
		return
	}
	_, _ = pool.Exec(ctx,
		`DELETE FROM kwave_kdb_enrich_attempts WHERE entity_id=$1 AND field=$2`, entityID, field)
}
