package kdb

import (
	"strconv"
	"strings"
	"testing"
)

// 이 술어가 회귀하면 07-26 사고가 재발한다: 시간만 보는 규칙으로 되돌아가면 대량
// 백필 한 번이 백로그 전체의 시도권을 하루에 소진시키고, 상시 cron 이 예산 620 을
// 받고 6건을 처리하는 상태로 돌아간다(2026-08-06 실측).
func TestFillRetryPredicate_입력해시를_본다(t *testing.T) {
	sql := FillRetryPredicate("e", "$2")

	// 지문은 실체화 컬럼에서 읽는다(0104). 함수 호출로 되돌리면 캐스케이드 선정이
	// 11초가 된다(2026-08-07 실측, 컬럼 비교는 32ms).
	if !strings.Contains(sql, "a.input_hash = e.fill_input_hash") {
		t.Error("원장의 input_hash 와 현재 지문 컬럼을 등호로 비교해야 한다")
	}
	if strings.Contains(sql, "kdb_fill_input_hash(") {
		t.Error("선정 술어가 다시 해시 함수를 호출한다 — 11초 쿼리로 회귀")
	}
	if !strings.Contains(sql, "a.field = $2") {
		t.Error("field 파라미터가 술어에 들어가지 않았다")
	}
	if !strings.Contains(sql, "NOT EXISTS") {
		t.Error("제외 술어가 아니다")
	}
	// exhausted 는 더 이상 선정에서 보지 않는다 — 레인마다 다른 임계(3회/9회)를 들고
	// 있던 게 규칙 분화의 원인이었다. 소진 여부는 "같은 지문의 실패 기록 존재"로 표현된다.
	if strings.Contains(sql, "exhausted") {
		t.Error("선정 술어가 다시 exhausted 를 보고 있다 — 규칙이 또 갈라진다")
	}
}

func TestFillRetryPredicate_재방문_안전판(t *testing.T) {
	sql := FillRetryPredicate("e", "$2")
	want := "interval '" + strconv.Itoa(FillRetryRevisitDays) + " days'"
	if !strings.Contains(sql, want) {
		t.Errorf("재방문 안전판(%s)이 술어에 없다 — 외부 소스 변화에 자기치유가 사라진다", want)
	}
	// 30일은 종전 값이다. 값을 하나도 만들지 못하면서 백로그를 "재시도 대기"로
	// 위장시켰다(42차 §11). 실제 회수는 지문 변화가 담당해야 한다.
	if FillRetryRevisitDays <= 30 {
		t.Errorf("FillRetryRevisitDays=%d — 종전 30일 회전으로 되돌아갔다", FillRetryRevisitDays)
	}
}

// 별칭이 다르면 술어도 그 별칭을 써야 한다(바깥 쿼리가 e 가 아닌 경우).
// 캐스케이드(필드 단위 원장)와 나머지 레인이 같은 규칙을 써야 한다. 갈라지면 Select 가
// 뽑은 엔티티를 exhaustedFields 가 다시 걸러 Noop 이 되거나, 그 반대가 된다.
func TestFillRetryBlockedFields_같은_규칙(t *testing.T) {
	sql := FillRetryBlockedFields("e")
	if !strings.Contains(sql, "a.input_hash = e.fill_input_hash") {
		t.Error("필드 판정이 지문을 보지 않는다")
	}
	if !strings.Contains(sql, "interval '"+strconv.Itoa(FillRetryRevisitDays)+" days'") {
		t.Error("재방문 안전판이 선정 술어와 다르다 — 규칙이 갈라졌다")
	}
	if strings.Contains(sql, "exhausted") || strings.Contains(sql, "ground-strict-skip") {
		t.Error("옛 7일 창 모델로 회귀 — 앵커 보유 엔티티의 96.9%가 다시 막힌다")
	}
}

func TestFillRetryPredicate_별칭을_따른다(t *testing.T) {
	sql := FillRetryPredicate("x", "'tmdb-locale'")
	if !strings.Contains(sql, "a.entity_id = x.id") || !strings.Contains(sql, "x.fill_input_hash") {
		t.Errorf("별칭이 반영되지 않았다:\n%s", sql)
	}
	if !strings.Contains(sql, "a.field = 'tmdb-locale'") {
		t.Errorf("리터럴 field 가 반영되지 않았다:\n%s", sql)
	}
}
