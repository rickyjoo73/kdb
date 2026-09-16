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
//	③ 재요청은 큐 INSERT 가 중복으로 걸러지고, 재개 UPDATE 는 precheck_status 가
//	   'legacy'·'review' 인 행만 연다. 'pass' 로 닫힌 행은 done 에 머물고 워커가
//	   집지 않는다.
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
	CoolDown  int // 1시간 안에 이미 본 행이라 건너뜀
	Ran       int // 선점에 성공해 실제로 일을 한 횟수
	Enriched  int // 앵커가 없어 찾아본 횟수
	AnchoredSkip int // 앵커가 이미 있어 다시 긁지 않은 횟수
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

// staleAfter — 같은 행을 다시 보기까지의 최소 간격. bgEnrich 와 **같은 값·같은 칸**이다.
//
// ★따로 두면 안 되는 이유. 오늘 트래픽은 낱말 1,523건 중 고유 1,255건이다 —
// 같은 낱말이 하루에 여러 번 들어온다. 소비자가 1분마다 폴링하면 훅도 1분마다
// 걸리는데, 쿨다운이 없으면 그때마다 위키데이터·네이버·gemma 를 부른다.
const staleAfter = time.Hour

// run — **찾는 일과 판정하는 일을 상태로 가른다.**
//
// ★처음엔 research worker 의 규칙("enrich 의 위키데이터 레이어가 돌았으면 승급")을
//
//	그대로 썼다. 운영 데이터를 보고 물렸다.
//
//	`이재명` 은 오늘 20번 요청됐고, 앵커 Q6514101 이 붙은 채 candidate 에 멈춰 있다.
//	그 QID 를 열어 보면 **1991년생 축구선수 이재명**이다. 동명이인이다.
//
//	그 규칙을 쓰면 어떻게 되나. 앵커가 이미 있는 행은 runWikidata 가 이름검색 대신
//	그 QID 를 직접 Fetch 하고(QID-pin), 저장된 ref 라 동명이인·QID유일성 가드가
//	면제되며, ko 라벨이 "이재명"이라 라벨 가드도 통과한다. 레이어는 돌고, 승급된다.
//	**축구선수의 표기가 이재명으로 나간다.**
//
// ★그래서 규칙을 상태로 가른다. 승급 판단은 이 레인이 하지 않는다.
//
//	앵커 없음 → 아직 **못 찾은** 것이다. 찾아본다(enrich cascade). 찾으면 앵커가
//	            붙고 빈칸이 채워진다. 승급은 그래도 여기서 하지 않는다.
//	앵커 있음 → 붙었는데도 candidate 라는 것은 **그 앵커가 의심스럽다**는 뜻이다.
//	            같은 QID 를 다시 긁으면 잘못된 표기만 더 깊이 박힌다. 긁지 않는다.
//
//	그리고 두 경우 모두 **뉴스근거 단건 판정**(CandidateEvidenceOne)에 넘긴다.
//	그게 "이 이름이 실재하고, 우리가 생각하는 그것이 맞는가"를 보라고 만든 자리다.
//	동명이인을 가릴 판단은 gemma+기사맥락이 하지, 레이어가 돌았다는 사실이 하지 않는다.
func (l *Lane) run(ctx context.Context, id uuid.UUID) {
	if l.Pool == nil {
		return
	}
	// ★되풀이 방지는 **일을 시작하기 전에** 건다.
	//
	//   bgEnrich 가 쓰는 last_enriched_at 칸을 그대로 claim 한다. 칸을 공유해야
	//   두 경로가 같은 행을 동시에 붙잡고 같은 외부 호출을 두 번 하지 않는다
	//   (cand-evidence 도 같은 이유로 이 칸을 공유한다고 적어 두었다).
	//
	//   조건부 UPDATE 하나로 검사와 선점을 같이 한다 — 읽고 나서 쓰면 그 사이에
	//   다른 요청이 끼어든다.
	var claimed bool
	err := l.Pool.QueryRow(ctx, `
UPDATE kwave_entities
   SET last_enriched_at = now()
 WHERE id = $1
   AND status = 'candidate'
   AND operator_locked = false
   AND (last_enriched_at IS NULL OR last_enriched_at < now() - $2::interval)
 RETURNING true`, id, staleAfter.String()).Scan(&claimed)
	if err != nil || !claimed {
		// 최근에 봤거나, 이미 active 로 올라갔거나, 운영자가 잠갔다. 전부 정상이다.
		l.mu.Lock()
		l.stats.CoolDown++
		l.mu.Unlock()
		return
	}
	l.mu.Lock()
	l.stats.Ran++
	l.mu.Unlock()

	// ① 앵커가 없을 때만 찾아본다.
	var anchored bool
	if qerr := l.Pool.QueryRow(ctx, `
SELECT EXISTS (SELECT 1 FROM kwave_entity_external_refs
                WHERE entity_id = $1 AND provider = 'wikidata' AND COALESCE(external_id,'') <> '')`,
		id).Scan(&anchored); qerr != nil {
		log.Printf("kdb.demand: %s 앵커 조회 실패: %v", id, qerr)
		return
	}
	if !anchored {
		if _, eerr := l.Orch.Enrich(ctx, id); eerr != nil {
			// 전송실패(외부 API 장애·타임아웃)일 수 있다. 낙인 찍지 않고 넘어간다 —
			// 쿨다운 한 시간 뒤 다시 온다.
			log.Printf("kdb.demand: %s enrich err=%v", id, eerr)
		} else {
			l.mu.Lock()
			l.stats.Enriched++
			l.mu.Unlock()
		}
	} else {
		l.mu.Lock()
		l.stats.AnchoredSkip++
		l.mu.Unlock()
	}

	// ② 승급 판정은 **이미 있는 판정기**가 한다. 이 레인은 그 앞에 데려다 놓을 뿐이다.
	//    쿨다운(1시간)도 그쪽 안에 있다.
	promoted, cerr := verify.CandidateEvidenceOne(ctx, l.Pool, id.String())
	if cerr != nil {
		log.Printf("kdb.demand: %s cand-evidence err=%v", id, cerr)
		return
	}
	if promoted {
		l.mu.Lock()
		l.stats.Evidenced++
		l.mu.Unlock()
		log.Printf("kdb.demand: %s 뉴스근거 승급(요청 훅)", id)
	}
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
	log.Printf("kdb.demand: 요청훅 누적 걸림=%d 실행=%d 쿨다운=%d 캡버림=%d 앵커찾음=%d 앵커있어건너뜀=%d 승급=%d",
		s.Triggered, s.Ran, s.CoolDown, s.Dropped, s.Enriched, s.AnchoredSkip, s.Evidenced)
}
