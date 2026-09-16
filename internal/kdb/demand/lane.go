package demand

// demand — **요청 훅.** 소비자가 물었는데 답할 수 없는 행 하나를, 주기 tick 을
// 기다리지 않고 그 자리에서 다시 민다.
//
// ★왜 필요한가 (실측 2026-09-16).
//
//	오늘 요청 낱말 1,595건 중 답이 나간 것은 316건이다. 발굴은 병목이 아니었다 —
//	발굴 큐 1,281건이 전부 done 이고 picked→finished 가 p50 1.7초다. 막힌 곳은
//	**발굴 뒤**다.
//
//	오늘 요청된 낱말 중 399건은 이미 candidate 행이 있다. 그중 386건은 위키데이터
//	앵커가 없다. 소비자는 그 행을 기다리며 preparing 을 받는데, 그 행에는
//	**아무 일도 일어나지 않는다.**
//
// ★왜 아무 일도 안 일어나는가. 장치가 셋인데 셋 다 이 행에 안 닿는다.
//
//	① bgEnrich.Trigger 는 lookup 응답의 matches 를 보고 건다. 그런데 matches 의
//	   기본 status 가 'active' 다(api.go). candidate 는 애초에 목록에 없다.
//	② CandidateEvidenceOne(단건 패스트레인)은 research worker 가 그 행을 **만든
//	   그 순간 한 번만** 부른다. 내일 소비자가 다시 물어도 다시 불리지 않는다.
//	③ 재요청은 게이트에서 existing_entity 로 판정돼 큐 행이 done 으로 닫힌다.
//	   워커는 done 을 집지 않는다.
//
//	남은 경로는 20분 스위프(1회 40건, 엔티티당 1시간 쿨다운)뿐인데, 앵커 없는
//	candidate 가 1,000건 쌓여 있다.
//
// ★이 레인이 하는 일은 **새 판단이 아니다.**
//
//	research worker 의 3·4단계(enrich cascade → 위키데이터 검증 시 승급, 아니면
//	뉴스근거 단건 판정)를, 워커가 다시는 보지 않을 행에 대해 그대로 한 번 더 돌린다.
//	승급 기준도 신뢰도도 그쪽과 같은 값을 쓴다. 바꾸는 것은 **언제 보는가**뿐이다.
//
//	빠르게 하되 무르게 하지 않는다 — 문턱을 낮추면 그건 다른 일이다.
//
// ★되풀이 방지는 이미 있는 것을 쓴다. 새로 만들지 않는다.
//
//	Enrich 는 last_enriched_at 을 1시간 조건으로 claim 하고, CandidateEvidenceOne 은
//	enrich_attempts(cand-evidence) 1시간 쿨다운을 본다. 그래서 같은 낱말을 100번
//	물어도 실제 외부 호출은 시간당 한 번이다. 여기에 쿨다운을 또 얹으면 두 개의
//	시계가 서로 다른 답을 하게 된다.

import (
	"context"
	"log"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/verify"
)

// maxConcurrent — 동시에 도는 요청 훅 상한.
//
// ★캡이 있어야 하는 이유는 bgEnrich 와 같다. cascade 하나가 위키데이터·네이버·
// gemma 를 부르고 최대 3분까지 간다. 소비자 한 곳이 50낱말짜리 bulk 를 연달아
// 던지면(오늘 실측 최대 batch 50) 캡이 없을 때 수백 개가 동시에 뜬다.
// bgEnrich 가 4 인데 그와 **따로** 4 를 쓰는 것은 두 배를 허용하는 것이라,
// 외부 호출이 실제로 겹치는 자리에서는 더 좁게 잡는다.
const maxConcurrent = 3

// promoteConf — 위키데이터 검증분 신뢰도. research worker 와 같은 값이어야 한다.
// 다르면 같은 근거로 승급한 행의 신뢰도가 경로에 따라 갈린다.
const promoteConf = 0.72

// Lane — 요청 훅 레인. Trigger 는 절대 블록하지 않는다(요청 핫패스에서 불린다).
type Lane struct {
	Pool *pgxpool.Pool
	Orch *enrich.Orchestrator

	sem chan struct{}

	mu      sync.Mutex
	stats   Stats
	started bool
}

// Stats — 이 레인이 실제로 무슨 일을 했는가. **켜져 있다는 말 대신 숫자를 남긴다.**
// 오늘만 "장치는 있는데 아무도 안 켠" 결함을 다섯 번 만났다 — 켠 뒤에 도는지
// 확인할 방법이 없으면 같은 자리로 돌아온다.
type Stats struct {
	Triggered int // 훅이 불린 횟수
	Dropped   int // 캡에 걸려 버린 횟수
	Ran       int // 실제로 cascade 까지 간 횟수
	Promoted  int // 위키데이터 검증으로 승급
	Evidenced int // 뉴스근거 단건 판정으로 승급
}

// New — 레인 생성. KDB_DEMAND_LANE=0 이면 nil 을 돌려준다(호출부는 nil 검사만 하면 된다).
func New(pool *pgxpool.Pool) *Lane {
	if pool == nil || os.Getenv("KDB_DEMAND_LANE") == "0" {
		return nil
	}
	return &Lane{
		Pool: pool,
		Orch: enrich.New(pool),
		sem:  make(chan struct{}, maxConcurrent),
	}
}

// Snapshot — 지금까지의 집계.
func (l *Lane) Snapshot() Stats {
	if l == nil {
		return Stats{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// Trigger — 소비자가 기다리는 candidate 한 건을 즉시 민다.
//
// 호출자는 요청 핸들러다. 그래서 **어떤 경우에도 블록하지 않는다** — 캡이 차 있으면
// 그냥 버린다. 버려도 20분 스위프가 이어받고, 소비자가 다시 물으면 또 걸린다.
func (l *Lane) Trigger(entityID string) {
	if l == nil || entityID == "" {
		return
	}
	id, err := uuid.Parse(entityID)
	if err != nil {
		return
	}
	l.mu.Lock()
	l.stats.Triggered++
	l.mu.Unlock()

	select {
	case l.sem <- struct{}{}:
	default:
		l.mu.Lock()
		l.stats.Dropped++
		l.mu.Unlock()
		return
	}
	go func() {
		defer func() { <-l.sem }()
		// 백그라운드 최선노력이 통합 바이너리(API+admin+worker)를 통째로 죽이지
		// 않게 격리. bgEnrich 와 같은 이유·같은 처리다.
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("kdb.demand: %s panic recovered: %v", id, rec)
			}
		}()
		// 요청 컨텍스트를 물려받지 않는다 — 소비자가 응답을 받고 연결을 끊어도
		// 일은 끝까지 간다. cascade 3분 + 근거판정 90초에 여유를 둔 상한.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		l.run(ctx, id)
	}()
}

// run — research worker 3·4단계와 **같은 순서, 같은 기준**.
func (l *Lane) run(ctx context.Context, id uuid.UUID) {
	l.mu.Lock()
	l.stats.Ran++
	l.mu.Unlock()

	// ① enrich cascade. 위키데이터 이름검증(동명이인·ko라벨·QID유일성 가드 포함)을
	//    거쳐 앵커를 적재하고 빈 locale 을 채운다. 1시간 claim 이 안에 있다.
	rep, err := l.Orch.Enrich(ctx, id)
	if err != nil {
		log.Printf("kdb.demand: %s enrich err=%v", id, err)
		return
	}
	// ② 위키데이터가 이름을 확인해 줬으면 승급한다 — research worker 와 같은 규칙.
	//    runWikidata 는 일치 항목을 못 찾으면 레이어를 남기지 않으므로, 이 레이어가
	//    있다는 것은 **이름이 확인된 QID 가 실제로 저장됐다**는 뜻이다.
	if rep != nil && containsLayer(rep.LayersRun, "wikidata") {
		tag, uerr := l.Pool.Exec(ctx, `
UPDATE kwave_entities
   SET status = 'active',
       confidence = GREATEST(confidence, $2::numeric),
       verification_tier = CASE WHEN COALESCE(verification_tier,'') = ''
                                THEN 'unverified' ELSE verification_tier END,
       updated_at = now()
 WHERE id = $1 AND status = 'candidate' AND operator_locked = false`, id, promoteConf)
		if uerr != nil {
			// ★한 일만 적는다. 오늘 두 번 고친 계열이다 — 쓰기 실패를 승급으로 로그하면
			//   다음 판단이 전부 틀린 전제 위에 선다.
			log.Printf("kdb.demand: %s 승급 저장 실패: %v", id, uerr)
			return
		}
		if tag.RowsAffected() > 0 {
			l.mu.Lock()
			l.stats.Promoted++
			l.mu.Unlock()
			log.Printf("kdb.demand: %s 위키데이터 검증 승급(요청 훅)", id)
		}
		return
	}
	// ③ 위키데이터가 확인해 주지 않았다. 뉴스근거 단건 판정에 기회를 준다 —
	//    research worker 가 생성 직후에 부르는 그 함수다. 쿨다운은 그쪽에 있다.
	promoted, cerr := verify.CandidateEvidenceOne(ctx, l.Pool, id.String())
	if cerr != nil {
		log.Printf("kdb.demand: %s cand-evidence err=%v", id, cerr)
		return
	}
	if promoted {
		l.mu.Lock()
		l.stats.Evidenced++
		l.mu.Unlock()
	}
}

// containsLayer — research/worker.go 의 같은 이름 함수와 같은 판정.
// 그쪽은 패키지 비공개라 쓸 수 없어 여기 둔다(두 줄짜리를 공개 API 로 올리지 않는다).
func containsLayer(layers []string, want string) bool {
	for _, l := range layers {
		if l == want {
			return true
		}
	}
	return false
}

// LogStats — 주기적으로 집계를 남긴다. 0건이어도 **적는다** — "안 돌았다"와
// "돌았는데 할 일이 없었다"는 다음에 할 일이 완전히 다르다.
func (l *Lane) LogStats() {
	if l == nil {
		return
	}
	s := l.Snapshot()
	if s.Triggered == 0 {
		return
	}
	log.Printf("kdb.demand: 요청훅 누적 걸림=%d 실행=%d 캡버림=%d 승급(위키)=%d 승급(근거)=%d",
		s.Triggered, s.Ran, s.Dropped, s.Promoted, s.Evidenced)
}
