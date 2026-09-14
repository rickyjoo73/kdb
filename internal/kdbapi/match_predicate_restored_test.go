package kdbapi

import (
	"context"
	"testing"
	"time"

	"github.com/rickyjoo73/kdb/internal/testdb"
)

// 종전 조건 — 2~3자 순수한글 정본마다 **새 정규식을 지어 컴파일**한다.
const oldMatchPred = `
        (char_length(canonical_ko) >= 4 AND strpos($1, canonical_ko) > 0)
        OR (char_length(canonical_ko) BETWEEN 2 AND 3 AND canonical_ko ~ '^[가-힣]+$'
            AND $1 ~ ('(^|[^가-힣])' || canonical_ko ||
                      '(은|는|이|가|을|를|와|과|의|에|에서|에게|한테|도|로|으로|만|까지|부터|보다|처럼|랑|이랑|[^가-힣]|$)'))
        OR (char_length(canonical_ko) BETWEEN 2 AND 3 AND canonical_ko !~ '^[가-힣]+$'
            AND strpos($1, canonical_ko) > 0)`

// 새 조건은 **사본을 두지 않는다** — api.go 의 matchWordBoundaryPredicate 를 그대로 쓴다.
// 사본을 두면 본문이 바뀌어도 시험은 옛 사본을 검사하며 통과한다.
var newMatchPred = matchWordBoundaryPredicate

// 빨라진 것은 좋지만, 빨라지면서 결과가 달라졌다면 최적화가 아니라 결함이다.
// 옛 조건과 새 조건을 **같은 실데이터에 같은 본문으로** 돌려 결과 집합이 같은지 본다.
//
// 왜 필요한가: 동치라는 판단은 "정규식이 맞으려면 그 이름이 본문에 글자 그대로 들어
// 있어야 한다"는 추론에 기대고 있다. 추론은 그럴듯해도 틀릴 수 있다. 데이터에 물어본다.
//
// 본문을 여럿 쓴다. 조사부착·영문·숫자혼합·짧은 이름·긴 이름이 각기 다른 가지를 타므로
// 하나만 보면 안 타는 가지를 놓친다.
func TestRestoredMatchPredicateRewriteKeepsSameRows(t *testing.T) {
	pool := testdb.Restored(t)
	ctx := context.Background()

	texts := []string{
		"아이유와 BTS가 서울 강남구 코엑스에서 열린 시상식에 참석했다. 방탄소년단 정국과 배우 이정재도 함께했다.",
		"가을바람이 불고 진짜 나비가 날았다. 오늘은 그냥 평범한 하루였다.", // 부분문자열 오매칭 유발
		"봉준호 감독의 기생충이 부산국제영화제에서 상영된다고 한다.",
		"NCT 127 과 aespa 가 SM 엔터테인먼트 소속으로 활동 중이다.", // 라틴·숫자 혼합
		"그는 제주도 성산일출봉에서 사진을 찍었고, 이후 한라산으로 향했다.",
		"손흥민은 토트넘에서, 김민재는 바이에른 뮌헨에서 뛴다.",
		"", // 빈 본문 — 양쪽 다 0건이어야 한다
		"점 점점 지점 거점 백화점 본점",
	}

	total := 0
	for i, txt := range texts {
		var onlyOld, onlyNew, both int
		err := pool.QueryRow(ctx, `
WITH o AS (SELECT id FROM kwave_entities WHERE `+oldMatchPred+`),
     n AS (SELECT id FROM kwave_entities WHERE `+newMatchPred+`)
SELECT (SELECT count(*) FROM o WHERE id NOT IN (SELECT id FROM n)),
       (SELECT count(*) FROM n WHERE id NOT IN (SELECT id FROM o)),
       (SELECT count(*) FROM o WHERE id IN (SELECT id FROM n))`, txt).
			Scan(&onlyOld, &onlyNew, &both)
		if err != nil {
			t.Fatal(i, err)
		}
		if onlyOld != 0 || onlyNew != 0 {
			t.Fatalf("본문 %d 에서 결과가 갈렸다 — 옛것만 %d건, 새것만 %d건, 공통 %d건\n%q",
				i, onlyOld, onlyNew, both, txt)
		}
		total += both
		t.Logf("본문 %d: 일치 %d건", i, both)
	}
	// 모든 본문이 0건이면 "같다"가 아무것도 증명하지 않는다. 실제로 걸린 것이 있어야 한다.
	if total == 0 {
		t.Fatal("여덟 본문 전부 0건 — 이 시험은 아무것도 대조하지 못했다")
	}
}

// 바꾼 이유가 속도이므로 속도도 지킨다. 절대 시간은 장비마다 다르니 **두 조건의 비율**을
// 본다 — 누군가 정규식을 다시 앞세우면 여기서 걸린다.
//
// 최솟값으로 비교한다. 평균은 다른 부하에 흔들리지만 "가장 빨랐을 때"는 그 질의가 낼 수
// 있는 속도에 가깝다. 여유는 3배로 잡았다 — 실측 차이가 19배라 3배면 오판할 여지가 없다.
func TestRestoredMatchPredicateRewriteIsFaster(t *testing.T) {
	if testing.Short() {
		t.Skip("타이밍 시험")
	}
	pool := testdb.Restored(t)
	ctx := context.Background()
	const txt = "아이유와 BTS가 서울 강남구 코엑스에서 열린 시상식에 참석했다. 방탄소년단 정국과 배우 이정재도 함께했다."

	// 2~3자 순수한글이 충분히 있어야 차이가 드러난다. 없으면 아무것도 재지 못한다.
	var pureShort int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities
 WHERE char_length(canonical_ko) BETWEEN 2 AND 3 AND canonical_ko ~ '^[가-힣]+$'`).Scan(&pureShort); err != nil {
		t.Fatal(err)
	}
	if pureShort < 500 {
		t.Skipf("2~3자 순수한글 정본이 %d건뿐 — 차이가 드러날 표본이 아니다", pureShort)
	}

	run := func(pred string) time.Duration {
		best := time.Hour
		for i := 0; i < 3; i++ {
			var n int
			start := time.Now()
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entities WHERE `+pred, txt).Scan(&n); err != nil {
				t.Fatal(err)
			}
			if d := time.Since(start); d < best {
				best = d
			}
		}
		return best
	}
	run(oldMatchPred) // 몸풀기 — 한쪽만 캐시가 식은 채로 재지 않게
	run(newMatchPred)
	oldBest, newBest := run(oldMatchPred), run(newMatchPred)
	t.Logf("옛 조건 %v / 새 조건 %v (2~3자 순수한글 %d건)", oldBest, newBest, pureShort)

	if newBest*3 > oldBest {
		t.Fatalf("정규식 컴파일을 걷어낸 이득이 사라졌다 — 옛 %v, 새 %v (3배 이상이어야 한다)", oldBest, newBest)
	}
}
