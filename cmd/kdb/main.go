// kdb-app — consolidated single-process KDB platform.
//
// Runs the lookup API (KDB_API_PORT, default 9100), the admin UI
// (KDB_ADMIN_PORT, default 9101), and the worker/autopilot loop in one process.
// LLM calls exec the `codex` CLI directly (internal/kdb/codexcli) — no Node
// bridge. The old per-service binaries (cmd/kdb-api, cmd/kdb-admin,
// cmd/kdb-worker) remain buildable for fallback.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/rickyjoo73/kdb/internal/db"
	"github.com/rickyjoo73/kdb/internal/kdb"
	"github.com/rickyjoo73/kdb/internal/kdb/agents"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/disambiguator"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/enricher"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/fillverifier"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
	"github.com/rickyjoo73/kdb/internal/kdb/autopilot"
	"github.com/rickyjoo73/kdb/internal/kdb/claudejudge"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/corrections"
	"github.com/rickyjoo73/kdb/internal/kdb/dataqa"
	"github.com/rickyjoo73/kdb/internal/kdb/demand"
	"github.com/rickyjoo73/kdb/internal/kdb/discogs"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/hermes"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
	"github.com/rickyjoo73/kdb/internal/kdb/kmdb"
	"github.com/rickyjoo73/kdb/internal/kdb/kofic"
	"github.com/rickyjoo73/kdb/internal/kdb/kopis"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/readiness"
	"github.com/rickyjoo73/kdb/internal/kdb/research"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
	"github.com/rickyjoo73/kdb/internal/kdb/verify"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/kdb/zhvariant"
	"github.com/rickyjoo73/kdb/internal/kdbadmin"
	"github.com/rickyjoo73/kdb/internal/kdbapi"
	"github.com/rickyjoo73/kdb/internal/kentity"
)

func main() {
	_ = godotenv.Load()
	// LUTC 제외 — 로컬시각(컨테이너 TZ=Asia/Seoul)으로 찍는다. cron 레인 스크립트가
	// 같은 로그 파일에 KST 로 찍는데 여기가 UTC 면 9시간 어긋난 두 시각이 섞인다.
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.Lshortfile)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()
	if len(os.Args) > 1 && os.Args[1] == "tdb-shadow-observe" {
		if err := tdbObservationCommand(ctx, pool, os.Args[2:], os.Stdin, os.Stdout); err != nil {
			log.Print("TDB ID observation failed; no source payload is logged")
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "tdb-shadow-import" {
		if err := tdbShadowCommand(ctx, pool, os.Args[2:], os.Stdin, os.Stdout); err != nil {
			log.Print("TDB shadow import failed: ", err)
			os.Exit(1)
		}
		return
	}

	// ─── one-shot subcommand: translate-backlog ───────────────────
	// `kdb-app translate-backlog [N]` — miss(request_terms preparing/new) 한글 제목류를
	// Google 번역 원형으로 재매칭해 리포트(active/candidate/rejected/none). DB 쓰기는
	// 번역캐시·오거부 플래그만(읽기 경로 원칙 — canonical/aliases 불변). 오너 승인 07-15.
	if len(os.Args) > 1 && os.Args[1] == "translate-backlog" {
		n := 200
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: translate-backlog start (limit=%d)", n)
		kdb.TranslateBacklog(ctx, pool, n)
		return
	}

	// ─── one-shot subcommand: translate-fill ──────────────────────
	// `kdb-app translate-fill [N]` — active 엔티티의 canonical_en 빈칸을 구글 번역
	// 폴백으로 일괄 채움(source=gtranslate, 상위 소스가 자동 업그레이드). 오너 방침 07-16:
	// "공식소스가 못 채우면 기계번역이라도 채워 서빙". Enrich L5 와 동일 가드 공유.
	if len(os.Args) > 1 && os.Args[1] == "translate-fill" {
		n := 200
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: translate-fill start (limit=%d)", n)
		enrich.New(pool).TranslateFillBacklog(ctx, n)
		return
	}

	// ─── one-shot subcommand: kowiki-anchor ───────────────────────
	// `kdb-app kowiki-anchor [n]` — unverified 엔티티에 ko.wikipedia 유래 위키데이터 앵커를
	// 붙인다(키·쿼터 없음). 평소엔 야간 드레인이 레인으로 돌린다.
	if len(os.Args) > 1 && os.Args[1] == "kowiki-anchor" {
		n := 50
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: kowiki-anchor start (n=%d)", n)
		a, c := kdb.DrainKoWikiAnchors(ctx, pool, n)
		log.Printf("kdb-app: kowiki-anchor done anchored=%d /%d", a, c)
		return
	}

	// ─── one-shot subcommand: itunes-anchor ───────────────────────
	// `kdb-app itunes-anchor [n]` — unverified song_album 에 iTunes 카탈로그 앵커를
	// 붙인다(키 없음). 곡은 위키백과 문서도 뉴스도 잘 없어 앞의 두 레인이 구조적으로
	// 못 잡는 계층이고, unverified 최대 버킷이다. 분당 ~20회 제한이라 3.2s 간격으로 돈다.
	if len(os.Args) > 1 && os.Args[1] == "itunes-anchor" {
		n := 50
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: itunes-anchor start (n=%d)", n)
		a, c := kdb.DrainITunesAnchors(ctx, pool, itunes.New(), n)
		log.Printf("kdb-app: itunes-anchor done anchored=%d /%d", a, c)
		return
	}

	// ─── one-shot subcommand: night-drain ─────────────────────────
	// `kdb-app night-drain [--force]` — 야간 백로그 소진을 수동으로 돌린다.
	// 평소엔 서버 루프가 창(기본 00:00–05:00 KST) 안에서 하루 한 번 자동 실행한다.
	// --force 는 창 밖에서도 돌린다(마감은 지금부터 창 길이만큼) — 점검·긴급 소진용.
	if len(os.Args) > 1 && os.Args[1] == "night-drain" {
		force := false
		for _, a := range os.Args[2:] {
			if a == "--force" {
				force = true
			}
		}
		if force {
			// 창 밖 강제 실행: 창 시작시각을 지금 시(hour)로 옮겨 in-window 로 만든다.
			os.Setenv("KDB_NIGHT_START_HOUR", strconv.Itoa(time.Now().In(kstZone).Hour()))
		}
		log.Printf("kdb-app: night-drain start (force=%v)", force)
		runNightDrain(ctx, pool)
		return
	}

	// ─── one-shot subcommand: merge-evidence-repair ───────────────
	// `kdb-app merge-evidence-repair [n] [--dry]` — 과거 병합이 버린 신원 근거를 승자에게
	// 옮긴다(일회성 백필). 종전 applyMerge 는 aliases_ko·person_details 만 옮기고 외부
	// 식별자·source_urls·로케일·검증등급을 패자와 함께 죽였다 — 근거 없는 쪽이 LLM 의
	// same_as 로 승자가 되면 근거가 통째로 사라진다(실측 59쌍 · 승자 53건).
	// 재발 방지는 applyMerge 안의 carryEvidence 가 담당하고, 이 명령은 이미 벌어진 것만 고친다.
	if len(os.Args) > 1 && os.Args[1] == "merge-evidence-repair" {
		n, dry := 200, false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: merge-evidence-repair start (n=%d dry=%v)", n, dry)
		rep, scan := disambiguator.RepairMergedEvidence(ctx, pool, n, dry)
		log.Printf("kdb-app: merge-evidence-repair done repaired=%d /%d (dry=%v)", rep, scan, dry)
		return
	}

	// ─── one-shot subcommand: mt-translit-fill ────────────────────
	// `kdb-app mt-translit-fill [ja|zh] [n] [--dry]` — 오너 07-21: "직역은 버리고 구글번역".
	// ja/zh 빈칸을 구글번역→gemma 음차게이트로 채움(음차만, 직역 버림). --dry=판정·미리보기.
	if len(os.Args) > 1 && os.Args[1] == "mt-translit-fill" {
		locale, n, dry := "ja", 40, false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if a == "ja" || a == "zh" {
				locale = a
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: mt-translit-fill start (locale=%s n=%d dry=%v)", locale, n, dry)
		filled, discarded, proc := enrich.New(pool).MTTranslitFill(ctx, locale, n, dry)
		log.Printf("kdb-app: mt-translit-fill done filled=%d discarded=%d /%d (dry=%v)", filled, discarded, proc, dry)
		return
	}

	// ─── one-shot subcommand: drain-candidates ────────────────────
	// `kdb-app drain-candidates [workers]` — 적체된 candidate 전체를 gpt 로
	// 분류해 인물DB / 고유명사DB / reject 로 일괄 정리하고 종료 (서버 미기동).
	if len(os.Args) > 1 && os.Args[1] == "drain-candidates" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-candidates start (workers=%d)", workers)
		autopilot.New(pool).DrainCandidatesConcurrent(ctx, workers)
		log.Printf("kdb-app: drain-candidates done")
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── one-shot subcommand: resolve-ondemand ───────────────────
	// `kdb-app resolve-ondemand [N]` — on-demand(lookup-miss) candidate 적체를
	// 검색증강 enrich 로 검증 → 외부근거 확보분만 active 승급, 무검증은 마킹하고 종료.
	if len(os.Args) > 1 && os.Args[1] == "resolve-ondemand" {
		n := 50
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: resolve-ondemand start (limit=%d)", n)
		prom, proc := autopilot.New(pool).ResolveOnDemand(ctx, n)
		log.Printf("kdb-app: resolve-ondemand done processed=%d promoted=%d", proc, prom)
		return
	}

	// ─── one-shot subcommand: drain-quality ──────────────────────
	// `kdb-app drain-quality [N]` — 품질검토 적체(저신뢰·bumpable)를 Wikidata 검증
	// enrich 로 대량 처리해 confidence 승급하고 종료.
	if len(os.Args) > 1 && os.Args[1] == "drain-quality" {
		n := 40
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: drain-quality start (limit=%d)", n)
		bumped, proc := autopilot.New(pool).DrainQuality(ctx, n)
		log.Printf("kdb-app: drain-quality done processed=%d bumped=%d", proc, bumped)
		return
	}

	// ─── one-shot subcommand: drain-persons ───────────────────────
	// `kdb-app drain-persons [workers]` — 고유명사DB 에 섞인 인명(unknown
	// candidate)을 gpt 로 분류해 person 인 것만 인물DB 로 이동하고 종료.
	if len(os.Args) > 1 && os.Args[1] == "drain-persons" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-persons start (workers=%d)", workers)
		autopilot.New(pool).DrainPersonsConcurrent(ctx, workers)
		log.Printf("kdb-app: drain-persons done")
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── one-shot subcommand: drain-bucket ────────────────────────
	// `kdb-app drain-bucket [workers]` — 남은 unknown candidate 를 gpt 로 분류해
	// 실체 type(고유명사/person)으로 버킷팅하거나 일반어를 reject 하고 종료.
	// drain-persons 와 달리 모든 실체 type 을 대상으로 하고 영어 제목도 분류.
	if len(os.Args) > 1 && os.Args[1] == "drain-bucket" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-bucket start (workers=%d)", workers)
		autopilot.New(pool).DrainBucketConcurrent(ctx, workers)
		log.Printf("kdb-app: drain-bucket done")
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── one-shot subcommand: drain-enrich ────────────────────────
	// `kdb-app drain-enrich [workers]` — 빈 외국어 locale/인물필드를 가진 active
	// entity backlog 를 Enricher 에이전트 cascade(L2 MusicBrainz→L3 Wikidata→L4
	// codex)로 단번에 비운다. 30분 cycle 의 budget(20) 제약 없이 backlog 0/수렴까지
	// 라운드 반복. Wikidata(~76%)는 worker 병렬, codex(~15%)는 codexGate 직렬.
	if len(os.Args) > 1 && os.Args[1] == "drain-enrich" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: drain-enrich start (workers=%d)", workers)
		enricher.New(codexcli.NewRunner()).DrainConcurrent(ctx, pool, workers)
		log.Printf("kdb-app: drain-enrich done")
		return
	}

	// ─── one-shot: drain-fillverify ───────────────────────────────
	// `kdb-app drain-fillverify [budget]` — codex-fallback QID 엔티티를 Wikidata 라벨로
	// 결정론 일괄 검증(일치→source 승급 codex-fallback→wikidata-label, 상이→gemma 판정 후
	// 교체). 검색·throttle 불요라 reground(검색 그라운딩)보다 훨씬 빠른 드레인 — codex-fallback
	// 의 ~48%(QID 보유분)를 빠르게 비운다. 7d 쿨다운(autopilot FillVerifier와 공유)으로 라운드
	// 마다 새 batch, Select 0 이면 수렴. 비-QID 하드테일은 reground(별도)가 담당.
	if len(os.Args) > 1 && os.Args[1] == "drain-fillverify" {
		budget := 200
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				budget = n
			}
		}
		fv := fillverifier.New(codexcli.NewRunner())
		log.Printf("kdb-app: drain-fillverify start (budget/round=%d)", budget)
		totalSel, totalActed := 0, 0
		for round := 1; round <= 200; round++ {
			if ctx.Err() != nil {
				break
			}
			ids, err := fv.Select(ctx, pool, budget)
			if err != nil {
				log.Printf("kdb-app: drain-fillverify select round=%d: %v", round, err)
				break
			}
			if len(ids) == 0 {
				log.Printf("kdb-app: drain-fillverify converged (round=%d)", round)
				break
			}
			rep, err := fv.Run(ctx, pool, agents.RunInput{IDs: ids, Budget: budget})
			if err != nil {
				log.Printf("kdb-app: drain-fillverify run round=%d: %v", round, err)
			}
			totalSel += rep.Selected
			totalActed += rep.Acted
			log.Printf("kdb-app: drain-fillverify round=%d sel=%d acted=%d (cum sel=%d acted=%d)",
				round, rep.Selected, rep.Acted, totalSel, totalActed)
		}
		log.Printf("kdb-app: drain-fillverify done (selected=%d acted=%d)", totalSel, totalActed)
		return
	}

	// ─── one-shot: claude-adjudicate (2단계 판정 최종단계) ─────────
	// `kdb-app claude-adjudicate [n] [--reject]` — Gemma 거름망이 [scope:review]/[contam:review]
	// 로 플래그한 의심군을 claude(Sonnet)로 최종 판정. --reject 없으면 권고만(dry, reject 안 함),
	// --reject 면 비-K/정크 확정분 실제 reject(오염DB). 실제 K 확정분은 항상 플래그 해제(구제).
	if len(os.Args) > 1 && os.Args[1] == "claude-adjudicate" {
		n := 30
		autoReject := false
		for _, a := range os.Args[2:] {
			if a == "--reject" {
				autoReject = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: claude-adjudicate start (n=%d autoReject=%v)", n, autoReject)
		j, rj, rs := kdb.DrainClaudeAdjudicate(ctx, pool, claudejudge.New(), n, autoReject)
		log.Printf("kdb-app: claude-adjudicate done (judged=%d rejected=%d rescued=%d)", j, rj, rs)
		return
	}

	// ─── one-shot: expire-candidates (TTL 초과 미결 candidate 종결) ──────
	// `kdb-app expire-candidates [n]` — 상시 레인(candidate-ttl, 30분)과 같은 코드.
	// 배포 직후 검증·운영자 수동 배수용. 기각이되 tombstone 아님(재요청 시 재발굴).
	if len(os.Args) > 1 && os.Args[1] == "expire-candidates" {
		n := 25
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: expire-candidates start (n=%d TTL=%d일)", n, kdb.CandidateTTLDays())
		rejected, checked := kdb.DrainExpireStaleCandidates(ctx, pool, n)
		log.Printf("kdb-app: expire-candidates done (checked=%d rejected=%d)", checked, rejected)
		return
	}

	// ─── one-shot: contam-review (오염-의심 비-person 재판정) ──────
	// `kdb-app contam-review [n]` — 공식 외국어 표기가 적은(오염후보 1위) active 비-person 부터
	// Gemma 로 정크/범위밖 vs 실제 재판정. 정크만 [contam:review] 플래그(자동reject 안 함, 운영자
	// 검토), 실제는 [contam:ok]. autopilot 매 cycle 에도 편입됨(stepContamReview).
	if len(os.Args) > 1 && os.Args[1] == "contam-review" {
		n := 50
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: contam-review start (n=%d)", n)
		flagged := autopilot.New(pool).RunContamReview(ctx, n)
		log.Printf("kdb-app: contam-review done (flagged=%d)", flagged)
		return
	}

	// ─── one-shot: same-qid-merge (한 대상이 여러 줄로 있는 것을 합친다) ──
	// `kdb-app same-qid-merge [n] [go]` — 같은 위키데이터 QID·같은 유형인 활성 행을
	// 하나로 합친다. 진 쪽의 이름·별칭은 이긴 쪽 별칭으로 옮겨진다(호칭이 ID 에 포함).
	// 누가 남는지는 **자체 원장**이 정한다(I03) — 운영자 잠금 > 근거 수 > 가장 오래된 ID.
	// QID 는 '합쳐도 되는가'를 받치는 보조 근거일 뿐이다.
	// 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "same-qid-merge" {
		n, dry := 500, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: same-qid-merge start (n=%d dry=%v)", n, dry)
		r := disambiguator.DrainSameQIDMerge(ctx, pool, n, dry)
		log.Printf("kdb-app: same-qid-merge 무리 %d · 합침 %d · 건너뜀 %d (dry=%v)",
			r.Groups, r.Merged, r.Skipped, dry)
		for _, v := range r.Review {
			log.Printf("   [검수] %s", v)
		}
		return
	}

	// ─── one-shot: demand-evidence (여러 매체가 거듭 묻는 것을 후보로 연다) ──
	// `kdb-app demand-evidence [n] [go]` — 출처 2곳 이상 × 3일 이상 반복 요청됐는데
	// 서빙 못 하는 낱말을 candidate 로 연다. active 로 올리지 않는다 — 수요는 존재의
	// 근거이지 표기의 근거가 아니다. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "demand-evidence" {
		n, dry := 300, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: demand-evidence start (n=%d dry=%v)", n, dry)
		r := kdb.DrainDemandEvidence(ctx, pool, n, dry)
		log.Printf("kdb-app: demand-evidence 대상 %d · 되살림 %d · 신규후보 %d · 건너뜀 %d (dry=%v)",
			r.Found, r.Opened, r.Created, r.Skipped, dry)
		return
	}

	// ─── one-shot: occupation-fill (이미 만들어 둔 직업 영역 칸을 채운다) ──
	// `kdb-app occupation-fill [n] [go]` — 활성 인물 5,407 중 영역이 채워진 것이
	// 78건(1.4%)뿐이었다(2026-09-16 실측). 칸도 판정표도 있는데 그 값을 쓰는 곳이
	// enrich 캐스케이드 한 군데뿐이라 그 경로를 탄 것만 채워졌다.
	// 이름을 검색하지 않는다 — 확정된 QID 로만 묻는다. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "occupation-fill" {
		n, dry := 500, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: occupation-fill start (n=%d dry=%v)", n, dry)
		r := kdb.DrainOccupationDomain(ctx, pool, wikidata.New(), n, dry)
		log.Printf("kdb-app: occupation-fill 조회 %d · 직업받음 %d · 영역판정 %d · 원자료만(표에없음) %d · 성별 %d | 위키데이터에직업없음 %d · 조회실패 %d (dry=%v)",
			r.Checked, r.Fetched, r.Domain, r.Raw, r.Gender, r.NoP106, r.Failed, dry)
		if top := kdb.TopUnknownOccupations(r.UnknownQIDs, 25); len(top) > 0 {
			log.Printf("kdb-app: occupation-fill 표에 없는 P106 상위 — %s", strings.Join(top, " "))
		}
		return
	}

	// ─── one-shot: org-anchor (새 유형 후보에 위키데이터 앵커를 붙인다) ──
	// `kdb-app org-anchor [n] [go]` — 정당·기관·기업·단체·구단·학교·게임·뮤지컬·웹툰·출판
	// 후보 103건이 **앵커 0건**이었다(2026-09-16 실측). 앵커가 없으면 승급이 안 되고,
	// 승급이 안 되면 다국어가 안 채워진다 — 그래서 새 유형의 ja/zh/vi 가 전부 0 이다.
	// P31 유형 일치 + P17/P495 국가 두 관문을 쓴다. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "org-anchor" {
		n, dry := 120, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: org-anchor start (n=%d dry=%v)", n, dry)
		r := kdb.DrainOrgAnchors(ctx, pool, wikidata.New(), n, dry)
		log.Printf("kdb-app: org-anchor 조회 %d · 앵커+승급 %d(=%d) | 못 붙인 사유: 한국근거없음(미기록) %d · 중복QID(병합대상) %d · 저장실패 %d · 검색실패 %d · 위키데이터에없음 %d · 이름불일치 %d · 유형어긋남 %d · 표에없는P31 %d · 해외 %d · 이름항목 %d (dry=%v)",
			r.Checked, r.Anchored, r.Promoted, r.Held, r.QIDTaken, r.WriteFailed,
			r.SearchFailed, r.NoHit, r.NameMismatch, r.TypeMismatch, r.TypeUnknown, r.Foreign, r.NameElement, dry)
		return
	}

	// ─── one-shot: active-anchor ──────────────────────────────────
	// `kdb-app active-anchor [n] [go]` — **이미 서빙 중인 행**에 앵커를 붙인다.
	//
	// ★앵커 레인들이 전부 candidate 만 본다. 그래서 앵커 없는 active 5,137건 중
	//   4,206건(82%)이 앵커를 한 번도 찾아본 적이 없고, 3,819건(74%)이 기계번역
	//   표기를 내보내고 있다. 앵커가 붙은 행은 기계번역이 16%다 — 붙이고 안 붙이고가
	//   표기의 질을 가른다(2026-09-16 실측).
	//
	// ★active 는 candidate 보다 조심해야 한다. 지금 나가는 값이 «무해한 추측»이라면
	//   틀린 앵커는 그것을 «확신에 찬 남의 이름»으로 바꾼다. 그래서 **기본 dry-run**
	//   이고, `go` 를 줘야 실제로 쓴다. 승급은 하지 않는다 — 앵커만 붙인다.
	if len(os.Args) > 1 && os.Args[1] == "active-anchor" {
		n, dry := 100, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: active-anchor start (n=%d dry=%v)", n, dry)
		r := kdb.DrainActiveAnchors(ctx, pool, wikidata.New(), n, dry)
		log.Printf("kdb-app: active-anchor %s (dry=%v)", r.Summary(), dry)
		return
	}

	// ─── one-shot: zh-repair ──────────────────────────────────────
	// `kdb-app zh-repair [n] [go]` — 간체 칸에 번체가, 번체 칸에 간체가 들어간 행을
	// 제자리로 돌린다. 기본 dry-run.
	//
	// ★오너가 기본 언어를 en·ja·zh(간체·본토)로 정했다(2026-09-17). 그런데 간체 칸에
	//   번체가, 번체 칸에 간체가 들어 있었다 — 본토 독자에게 번체가 그대로 나가는
	//   상태였다.
	//
	//   ★수치를 두 번 보고했다. 처음엔 «168 → 0» 이라 했는데 그건 손으로 고른 82자
	//     정규식이 **볼 수 있는 것만** 0이었다. OpenCC 사전 전체로 다시 재니 간체 칸
	//     430건, 번체 칸 98건이 남아 있었다(全寶藍·鄭先哲·黄東赫 …). 판정을 사전에서
	//     굽는 것으로 바꾼 이유다 — scripts/gen_zh_charsets.py.
	//
	// ★버리지 않고 옮긴다. 간체 칸의 번체 값은 틀린 값이 아니라 **칸을 잘못 찾아간**
	//   값이다(wikidata-label·tmdb 등 진짜 표기다). 그냥 비우면 57건이 빈칸으로 남는다.
	//   번체 칸으로 옮기고 간체는 t2s 로 채우면 두 칸이 다 산다.
	//
	// ★operator_locked 는 손대지 않는다 — 삼성전자 三星電子, 농심 農心 같은 것은
	//   오너가 직접 넣은 값이고 어느 자체로 쓸지는 오너 판단이다.
	if len(os.Args) > 1 && os.Args[1] == "zh-repair" {
		n, dry, locked := 500, true, false
		for _, a := range os.Args[2:] {
			switch a {
			case "go":
				dry = false
				continue
			case "locked":
				// 오너가 직접 잠근 행도 고친다. **잠금은 풀지 않는다** — 값만 바로잡는다.
				locked = true
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: zh-repair start (n=%d dry=%v locked=%v)", n, dry, locked)
		r := kdb.RepairZhVariants(ctx, pool, n, dry, locked)
		log.Printf("kdb-app: zh-repair 판정 %d · 수리 %d · 칸이동 %d · 이체자보류 %d · 잠금보류 %d · 건너뜀 %d (dry=%v)",
			r.Checked, r.Repaired, r.MovedToHant, r.VariantHeld, r.OperatorHeld, r.Skipped, dry)
		return
	}

	// ─── one-shot: scope-reopen (옛 범위로 죽은 한국 대상 되살리기) ──
	// `kdb-app scope-reopen [n] [go]` — 범위 확대(0143) 전에 "K-엔터테인먼트가 아님"을
	// 이유로 기각된 행 중, 위키데이터가 한국 대상이라 말하는 것을 candidate 로 되돌린다.
	// active 로 올리지 않는다 — 승급은 평소 경로가 근거를 보고 한다. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "scope-reopen" {
		n, dry := 1000, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: scope-reopen start (n=%d dry=%v)", n, dry)
		r := kdb.DrainScopeReopen(ctx, pool, wikidata.New(), n, dry)
		log.Printf("kdb-app: scope-reopen 판정 %d · 되살림 %d · 앵커철회 %d · 이름항목 %d · 개념 %d · 해외유지 %d · 근거없음 %d (dry=%v)",
			r.Checked, r.Reopened, r.AnchorDropped, r.NameElement, r.Concept, r.StillForeign, r.NoEvidence, dry)
		return
	}

	// ─── one-shot: occup-scope-restore (직업 범위로 묻힌 행 되살리기) ──
	// `kdb-app occup-scope-restore [n] [go]` — `[revert-term:reject]` 를 달았지만 사유가
	// `직업이 비-엔터: "South Korean …"` 인 행. 표시의 명제(QID 가 비-K)를 **같은 줄이
	// 부정한다.** 2026-07-21 감사가 강등시킨 727행 중 334 가 그대로 누워 있었다.
	// rejected → candidate, candidate+authoritative+앵커성립 → active. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "occup-scope-restore" {
		n, dry := 500, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: occup-scope-restore start (n=%d dry=%v)", n, dry)
		r := kdb.DrainOccupationScopeRestore(ctx, pool, wikidata.New(), n, dry)
		log.Printf("kdb-app: occup-scope-restore 판정 %d · 승급 %d · 되살림 %d · 앵커철회 %d · 이름항목 %d · 개념 %d · 해외유지 %d · 동명active %d · 근거없음 %d · 조회실패 %d (dry=%v)",
			r.Checked, r.Promoted, r.Reopened, r.AnchorDropped, r.NameElement, r.Concept,
			r.StillForeign, r.TwinActive, r.NoEvidence, r.FetchFailed, dry)
		if r.HomonymHeld > 0 {
			log.Printf("kdb-app: occup-scope-restore 동명이인 보류 %d — 되살리되 active 로는 안 올린다", r.HomonymHeld)
		}
		// ★조회 실패는 판정이 아니다. 전량 실패면 일감이 아니라 **망이 문제**다.
		if r.FetchFailed > 0 && r.Checked == 0 {
			log.Printf("kdb-app: occup-scope-restore ⚠ 한 건도 물어보지 못했다 — 인증서·망을 먼저 본다(판정 0 은 «고칠 것이 없다»가 아니다)")
		}
		for _, sm := range r.Samples {
			log.Printf("    %s", sm)
		}
		return
	}

	// ─── one-shot: dead-scope-flag (죽은 범위로 찍힌 검토 표시 걷기) ──
	// `kdb-app dead-scope-flag [n] [go]` — `[cand-evidence:review]` 중 사유가
	// «K-콘텐츠가 아니다» 인 것. 판정기 프롬프트를 0143 범위로 고쳤으므로(2026-09-20)
	// 표시를 걷어 **고쳐진 판정기가 다시 보게** 연다. status 는 건드리지 않는다.
	// 실측 1,623행 중 옛 범위 문구가 근거인 것 1,133행. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "dead-scope-flag" {
		n, dry := 500, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: dead-scope-flag start (n=%d dry=%v)", n, dry)
		r := kdb.DrainDeadScopeFlags(ctx, pool, n, dry)
		log.Printf("kdb-app: dead-scope-flag 걷음 %d (dry=%v)", r.Cleared, dry)
		for _, sm := range r.Samples {
			log.Printf("    %s", sm)
		}
		return
	}

	// ─── one-shot: paren-annot (출처가 붙인 동음이의 주석 걷기) ──
	// `kdb-app paren-annot [n] [go]` — "金炳旭 (1965年)" · "朴泰俊 (跆拳道运动员)" ·
	// "林秀妍（音译）" 처럼 위키백과가 문서를 가르려고 붙인 말이 표기 자리에 들어온 것.
	// 실측 active 601칸(zh 128 · zh_hant 121 · ja 235 · en 117).
	// f(x)·ALL(H)OURS 처럼 **이름 자체에 괄호가 있는 것**은 건드리지 않는다. 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "paren-annot" {
		n, dry := 500, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: paren-annot start (n=%d dry=%v)", n, dry)
		r := kdb.DrainParenAnnotations(ctx, pool, n, dry)
		log.Printf("kdb-app: paren-annot 조회 %d · 걷음 %d · 그대로둠 %d (dry=%v)",
			r.Checked, r.Stripped, r.Held, dry)
		return
	}

	// ─── one-shot: catchall-retype (칸이 없어 눌러 담긴 유형 옮기기) ──
	// `kdb-app catchall-retype [n] [go]` — brand_place·term·unknown 에 앉은 행 중
	// 위키데이터 P31 이 **한 유형만** 가리키는 것을 그 유형으로 옮긴다.
	// 국민의힘·SK하이닉스 가 brand_place 였던 이유가 노트에 그대로 있다:
	// "사용 가능한 분류 중 brand_place가 가장 가깝습니다". 이제 칸이 있다.
	// status 는 건드리지 않는다 — 유형이 맞다는 것이 서빙해도 된다는 뜻이 아니다.
	// 기본 dry-run.
	if len(os.Args) > 1 && os.Args[1] == "catchall-retype" {
		n, dry := 500, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: catchall-retype start (n=%d dry=%v)", n, dry)
		r := kdb.DrainCatchallRetype(ctx, pool, wikidata.New(), n, dry)
		log.Printf("kdb-app: catchall-retype 판정 %d · 재유형 %d · 클래스갈림 %d · 클래스없음 %d · 근거없음 %d (dry=%v)",
			r.Checked, r.Retyped, r.Ambiguous, r.NoClass, r.NoEvidence, dry)
		for _, s := range r.Samples {
			log.Printf("  %s", s)
		}
		return
	}

	// ─── one-shot: kana-audit (일본어 칸의 성씨 어긋남) ────────────
	// `kdb-app kana-audit [n] [go]` — ja 칸의 성씨가 canonical_ko 와 어긋나는 행을 찾는다.
	// 가나는 음역이라 성씨가 1:1 이므로(김→キム, 하→ハ) 어긋나면 다른 사람의 표기다.
	// 기본 dry-run. go 를 주면 그 칸을 비운다(값을 지어내지 않는다 — kana-rule 이 다시 채운다).
	if len(os.Args) > 1 && os.Args[1] == "kana-audit" {
		n, dry := 20000, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
				continue
			}
			if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: kana-audit start (n=%d dry=%v)", n, dry)
		r := kdb.AuditKanaSurnames(ctx, pool, n, dry)
		log.Printf("kdb-app: kana-audit 판정 %d · 어긋남 %d · 비움 %d (dry=%v)",
			r.Checked, r.Mismatched, r.Cleared, dry)
		for src, c := range r.BySource {
			label := src
			if label == "" {
				label = "(출처없음)"
			}
			log.Printf("   출처 %-24s %d", label, c)
		}
		return
	}

	// ─── one-shot: refill-anchored (누락정보 빠른 확보) ────────────
	// `kdb-app refill-anchored [n]` — Wikidata QID 를 보유했지만 빈칸/codex locale 이 남은
	// 엔티티에 권위 refill(QID 직접 Fetch → 라벨/langlink 로 빈칸채움+codex 업그레이드).
	// FillVerifier(gemma 검증)와 달리 QID-pin 이라 추측 없이 공식표기를 당겨온다. 14d 쿨다운.
	if len(os.Args) > 1 && os.Args[1] == "refill-anchored" {
		n := 200
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: refill-anchored start (n=%d, QID 권위 refill)", n)
		proc, up := enrich.New(pool).DrainAnchoredRefill(ctx, n)
		log.Printf("kdb-app: refill-anchored done (processed=%d upgraded=%d)", proc, up)
		return
	}

	// ─── one-shot: langlink-upgrade (QID 사이트링크로 codex 셀 업그레이드) ──
	// `kdb-app langlink-upgrade [n]` — QID 보유 codex 셀을 Wikipedia langlink(언어판 문서
	// 제목)로 교체. 일반 enrich 는 langlink 를 빈칸전용 적용이라 codex 잔존 → ko-label 매칭
	// 게이트 후 codex 교체(zh_hant=zhwiki 주수율). 14d 쿨다운. opencc-convert 와 병행 권장.
	if len(os.Args) > 1 && os.Args[1] == "langlink-upgrade" {
		n := 200
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: langlink-upgrade start (n=%d, QID langlink codex 교체)", n)
		proc, up := enrich.New(pool).DrainLanglinkUpgrade(ctx, n)
		log.Printf("kdb-app: langlink-upgrade done (processed=%d upgraded=%d)", proc, up)
		return
	}

	// ─── one-shot: romanize-persons (Latin locale 로마자 재속성) ────
	// `kdb-app romanize-persons` — 전 타입(unknown/term 제외)의 빈칸/codex vi/es/id/pt_br 를
	// 검증된 canonical_en(로마자·영문표기)로 재속성. 외부호출 0·결정적·벌크안전.
	// ★ko 원제가 이미 라틴표기인 건의 en 빈칸을 먼저 채운다(en 이 비면 Latin 4종도 함께 빔).
	// 명령 이름은 하위호환으로 유지(cron/런북 참조).
	if len(os.Args) > 1 && os.Args[1] == "romanize-persons" {
		log.Printf("kdb-app: romanize-persons start (Latin locale 재속성)")
		e := kdb.DrainLatinKoToEN(ctx, pool)
		// ★DrainRomanizeLatin(en→Latin 4종 전파) **앞**이어야 같은 회차에 5셀이 함께 열린다.
		p := kdb.DrainParenLatinToEN(ctx, pool)
		c := kdb.DrainLatinKoToCJK(ctx, pool)
		// ★승계 **뒤**여야 한다 — 방금 채워진 건을 기각으로 잘못 낙인찍지 않는다.
		m := kdb.MarkLatinOriginRejects(ctx, pool)
		f := kdb.DrainRomanizeLatin(ctx, pool)
		r := kdb.DrainReattributeRomanization(ctx, pool)
		log.Printf("kdb-app: romanize-persons done (en=%d paren-en=%d cjk=%d rejected=%d filled=%d relabeled=%d cells)", e, p, c, m, f, r)
		return
	}

	// ─── one-shot: kana-fill (person/group/character ja 빈칸 가타카나 규칙채움) ──
	// `kdb-app kana-fill [n]` — 한글 인명 ja 빈칸을 결정 가타카나 변환으로 채움(폴백티어,
	// source='kana-rule' prio8, verified_only 제외, dataqa_log 스냅샷 복원가능). 외부호출 0.
	if len(os.Args) > 1 && os.Args[1] == "kana-fill" {
		n := 1000
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		f := kdb.DrainKanaFillPersons(ctx, pool, n)
		log.Printf("kdb-app: kana-fill done (filled=%d cells)", f)
		return
	}

	// ─── one-shot: opencc-convert (zh↔zh_hant 결정적 변환) ─────────
	// `kdb-app opencc-convert` — 검증된 zh↔zh_hant 를 OpenCC(s2t/t2s)로 상호 변환해 빈/codex
	// 변종을 채운다. 외부호출 0·결정적·벌크안전. source='opencc'.
	// ─── one-shot: zh-variant-audit (읽기 전용) ────────────────────
	// `kdb-app zh-variant-audit [N]` — zh/zh_hant 칸에 반대 변종이 든 행을 **세기만** 한다.
	// 쓰기 없음. 교정 전에 무엇이 바뀌는지 눈으로 보기 위한 것이다 — 글자표로 셌더니
	// 오탐이 섞여 있었고(박솔라·박희순·여고생왕후), 그래서 opencc 자신의 판정을 본다.
	if len(os.Args) > 1 && os.Args[1] == "zh-variant-audit" {
		n := 40
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		for _, m := range kdb.AuditZhVariantMismatch(ctx, pool, n) {
			log.Printf("  %-14s %-12s %-24s %q → %q  (출처 %s)",
				m.KO, m.EntityType, m.Col, m.Have, m.Want, m.Source)
		}
		return
	}

	// ─── one-shot: anchor-audit (읽기 전용) ─────────────────────────
	// `kdb-app anchor-audit [N]` — **활성** person/character 의 wikidata 앵커가
	// 그 유형과 맞는지 조회해 어긋난 것만 찍는다. 쓰기 없음.
	// 들어올 때 거는 가드는 넷이나 있는데(resolution·common_fill·tdb_mapping·
	// IsNameElement) 전부 인입 시점이라, 이미 앉아 있는 active 는 아무도 안 봤다.
	if len(os.Args) > 1 && os.Args[1] == "anchor-audit" {
		n := 200
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		wd := wikidata.New()
		bad, checked := kdb.AuditPersonAnchors(ctx, pool, wd, n)
		for _, m := range bad {
			log.Printf("  %-14s %-10s %-12s %-18s %s  (등급 %s · ja=%q 출처 %s)",
				m.KO, m.EntityType, m.QID, m.Verdict, m.Desc, m.Tier, m.JA, m.JASource)
		}
		log.Printf("kdb-app: anchor-audit 조회 %d건 중 어긋남 %d건", checked, len(bad))
		return
	}

	// ─── one-shot: anchor-withdraw ────────────────────────────────
	// `kdb-app anchor-withdraw [N] [go]` — 감사가 근거로 어긋났다고 판정한 앵커를 뗀다.
	// **기본은 dry-run.** `go` 를 줘야 실제로 쓴다. 대상은 앵커이지 사람이 아니다.
	if len(os.Args) > 1 && os.Args[1] == "anchor-withdraw" {
		n, dry := 200, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: anchor-withdraw start (n=%d dry=%v)", n, dry)
		r := kdb.DrainWithdrawWrongAnchors(ctx, pool, wikidata.New(), n, dry)
		for _, m := range r.Review {
			log.Printf("  [검수] %-14s %-12s %-18s %s", m.KO, m.QID, m.Verdict, m.Desc)
		}
		log.Printf("kdb-app: anchor-withdraw 조회 %d · 철회 %d · 표기 %d칸 · 등급강등 %d · 검수로 %d (dry=%v)",
			r.Checked, r.Withdrawn, r.CellsCleared, r.Downgraded, len(r.Review), dry)
		if dry {
			log.Printf("  ※ dry-run 에서는 등급강등을 계산하지 않는다(쓰기 경로에서만 잰다).")
		}
		return
	}

	// ─── one-shot: retype-person ──────────────────────────────────
	// `kdb-app retype-person [N] [go]` — 사람인데 작품·그룹으로 분류된 행의 유형만 되돌린다.
	// **근거 셋이 같은 방향일 때만** 고친다(P31=Q5 · 한국 인명꼴 · 인명 로마자꼴).
	// 기본 dry-run. 같은 이름의 person 과 부딪히면 합치지 않고 검수로 보낸다(I05·M06).
	if len(os.Args) > 1 && os.Args[1] == "retype-person" {
		n, dry := 400, true
		for _, a := range os.Args[2:] {
			if a == "go" {
				dry = false
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: retype-person start (n=%d dry=%v)", n, dry)
		r := kdb.DrainRetypePersonMisfiled(ctx, pool, wikidata.New(), n, dry)
		for _, m := range r.Review {
			log.Printf("  [검수] %-16s %-12s %-24s %s", m.KO, m.EntityType, m.QID, m.Desc)
		}
		log.Printf("kdb-app: retype-person 근거확인 %d · 교정 %d · 이름충돌 %d · 검수로 %d (dry=%v)",
			r.Checked, r.Retyped, r.Collided, len(r.Review), dry)
		return
	}

	// ─── one-shot: correction-audit (읽기 전용) ────────────────────
	// `kdb-app correction-audit [N]` — correction-verified 로 박힌 값이 지금의
	// 위키데이터와 같은지 본다. 쓰기 없음.
	// 이 값들은 **가변 소스의 한 시점 사본**이다: 우리 `에반` 의 es 가 `Heeseung love`
	// 였는데 위키데이터는 그 뒤 `Heeseung` 으로 고쳐졌다. 우리 사본만 남았다.
	if len(os.Args) > 1 && os.Args[1] == "correction-audit" {
		n := 200
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		drift, checked := kdb.AuditCorrectionSnapshots(ctx, pool, wikidata.New(), n)
		anchorless := 0
		for _, d := range drift {
			if !d.Anchored {
				anchorless++
				continue
			}
			log.Printf("  %-16s %-8s 우리=%-24s 지금=%-24s %s", d.KO, d.Locale, d.Ours, d.NowLabel, d.QID)
		}
		log.Printf("kdb-app: correction-audit 조회 %d · 어긋남 %d · 앵커없어 대조불가 %d",
			checked, len(drift)-anchorless, anchorless)
		return
	}

	if len(os.Args) > 1 && os.Args[1] == "opencc-convert" {
		log.Printf("kdb-app: opencc-convert start (zh↔zh_hant)")
		f := kdb.DrainZhVariants(ctx, pool)
		li := kdb.DrainZhLatinIdentity(ctx, pool) // zh 에 한자가 없으면 변환이 아니라 항등
		lr := kdb.DrainZhLatinRelabel(ctx, pool)  // 값은 이미 같은데 라벨만 codex 인 칸 교정(값불변)
		log.Printf("kdb-app: opencc-convert done (filled=%d latin-identity=%d relabel=%d cells)", f, li, lr)
		return
	}

	// ─── one-shot: zhwiki-title (보유 zh.wikipedia URL → zh 표기) ────
	// `kdb-app zhwiki-title [dry]` — source_urls 에 이미 있는 zh.wikipedia 문서 제목을 zh
	// 빈칸에 채운다. 외부호출 0·결정적. canonical_en 과 접어 일치할 때만 쓴다(문서가 남의
	// 것인 경우를 거른다 — internal/kdb/zhwiki_title_drain.go 주석).
	if len(os.Args) > 1 && os.Args[1] == "zhwiki-title" {
		dry := len(os.Args) > 2 && os.Args[2] == "dry"
		log.Printf("kdb-app: zhwiki-title start (dry=%v)", dry)
		f, m := kdb.DrainZhWikiTitle(ctx, pool, dry)
		log.Printf("kdb-app: zhwiki-title done (filled=%d rejected=%d)", f, m)
		return
	}

	// ─── one-shot: naver-verify (검색기반 오염판별 — 네이버 지식백과 정체성 확인) ──
	// `kdb-app naver-verify [n]` — active 엔티티 N건을 encyc 에서 정체성 확인. 역할토큰
	// 매칭=confirmed, 불일치=review(자동거부X), 미등재=no_entry. 쿼터 1,000/일(1건=1콜).
	if len(os.Args) > 1 && os.Args[1] == "naver-verify" {
		n := 20
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		nv, err := naver.NewFromSettings(ctx, pool)
		if err != nil {
			log.Fatalf("kdb-app: naver-verify: %v", err)
		}
		rows, err := pool.Query(ctx, `SELECT canonical_ko, entity_type::text FROM kwave_entities WHERE status='active' AND canonical_ko <> '' ORDER BY confidence ASC, updated_at DESC LIMIT $1`, n)
		if err != nil {
			log.Fatalf("kdb-app: naver-verify query: %v", err)
		}
		type nvEnt struct{ ko, typ string }
		var ents []nvEnt
		for rows.Next() {
			var e nvEnt
			if rows.Scan(&e.ko, &e.typ) == nil {
				ents = append(ents, e)
			}
		}
		rows.Close()
		log.Printf("kdb-app: naver-verify start (n=%d)", len(ents))
		var conf, rev, none int
		for _, e := range ents {
			v, err := nv.VerifyIdentity(ctx, e.ko, e.typ)
			if err != nil {
				log.Printf("  [err] %s (%s): %v", e.ko, e.typ, err)
				continue
			}
			switch v.Status {
			case "confirmed":
				conf++
			case "review":
				rev++
				log.Printf("  [review] %s (%s) total=%d · %s", v.Ko, v.Type, v.Total, v.Evidence)
			case "no_entry":
				none++
				log.Printf("  [no_entry] %s (%s)", v.Ko, v.Type)
			}
		}
		log.Printf("kdb-app: naver-verify done — confirmed=%d review=%d no_entry=%d (of %d)", conf, rev, none, len(ents))
		return
	}

	// ─── one-shot: verify-entities (엔티티-레벨 정체성 검증 tier — 증분2) ──────
	// `kdb-app verify-entities`             — 결정론 스윕(전 active 재분류, 무료·즉시).
	// `kdb-app verify-entities evidence [n]` — unverified 상위 n 개를 네이버news+gemma 로
	//   업그레이드 시도(쿼터 캡). 결정론 스윕은 강등하지 않고 보존('search+gemma%').
	if len(os.Args) > 1 && os.Args[1] == "verify-entities" {
		if len(os.Args) > 2 && os.Args[2] == "revert-contam" {
			n := 1000
			if len(os.Args) > 3 {
				if v, e := strconv.Atoi(os.Args[3]); e == nil && v > 0 {
					n = v
				}
			}
			r, err := verify.RevertContamRejects(ctx, pool, n)
			if err != nil {
				log.Fatalf("kdb-app: verify-entities revert-contam: %v", err)
			}
			log.Printf("kdb-app: verify-entities revert-contam done — restored=%d", r)
			return
		}
		if len(os.Args) > 2 && os.Args[2] == "type-retrace" {
			// 타입 힌트 오염 역추적(오너 승인 07-16): auto_evidence 생성 엔티티의 타입을
			// 이름만 무편향 재검색 + gemma 재판정으로 교정. 엔티티당 네이버 1콜.
			n := 60
			if len(os.Args) > 3 {
				if v, e := strconv.Atoi(os.Args[3]); e == nil && v > 0 {
					n = v
				}
			}
			fixed, confirmed, flagged, proc, err := verify.TypeRetracePass(ctx, pool, n)
			if err != nil {
				log.Fatalf("kdb-app: verify-entities type-retrace: %v", err)
			}
			log.Printf("kdb-app: verify-entities type-retrace done — fixed=%d confirmed=%d contam?=%d /%d",
				fixed, confirmed, flagged, proc)
			return
		}
		if len(os.Args) > 2 && os.Args[2] == "audit" {
			// active 고유명사 전수 오염 감사(오너 지시 07-17) — 이중 게이트(뉴스근거×내용
			// 이중판정) 동의 시만 tombstone 기각. 복원=verify-entities revert-contam.
			n := 150
			if len(os.Args) > 3 {
				if v, e := strconv.Atoi(os.Args[3]); e == nil && v > 0 {
					n = v
				}
			}
			rej, rev, up, proc, err := verify.ActiveAuditPass(ctx, pool, n, kdb.TriageKeywordConfirmed)
			if err != nil {
				log.Fatalf("kdb-app: verify-entities audit: %v", err)
			}
			log.Printf("kdb-app: verify-entities audit done — rejected=%d review=%d upgraded=%d /%d", rej, rev, up, proc)
			return
		}
		if len(os.Args) > 2 && os.Args[2] == "cand-evidence" {
			// 요청대기 candidate 뉴스근거 승급(공식앵커 없는 실존 롱테일 — 미해결 보류 해소).
			n := 100
			if len(os.Args) > 3 {
				if v, e := strconv.Atoi(os.Args[3]); e == nil && v > 0 {
					n = v
				}
			}
			up, flagged, proc, err := verify.CandidateEvidencePass(ctx, pool, n)
			if err != nil {
				log.Fatalf("kdb-app: verify-entities cand-evidence: %v", err)
			}
			log.Printf("kdb-app: verify-entities cand-evidence done — promoted=%d contam?=%d /%d", up, flagged, proc)
			return
		}
		if len(os.Args) > 2 && os.Args[2] == "evidence" {
			n := 100
			if len(os.Args) > 3 {
				if v, e := strconv.Atoi(os.Args[3]); e == nil && v > 0 {
					n = v
				}
			}
			up, flagged, proc, err := verify.EvidencePass(ctx, pool, n)
			if err != nil {
				log.Fatalf("kdb-app: verify-entities evidence: %v", err)
			}
			log.Printf("kdb-app: verify-entities evidence done — real=%d contam?=%d /%d", up, flagged, proc)
			c, _ := verify.Tally(ctx, pool)
			log.Printf("  tier: authoritative=%d evidenced=%d unverified=%d (total=%d)",
				c.Authoritative, c.Evidenced, c.Unverified, c.Total())
			return
		}
		c, err := verify.SweepDeterministic(ctx, pool)
		if err != nil {
			log.Fatalf("kdb-app: verify-entities: %v", err)
		}
		log.Printf("kdb-app: verify-entities sweep done — authoritative=%d evidenced=%d unverified=%d (total=%d)",
			c.Authoritative, c.Evidenced, c.Unverified, c.Total())
		return
	}

	// ─── one-shot: itunes-songs (song_album 현지표기 confirm + 아티스트앵커) ──
	// `kdb-app itunes-songs [n]` — song_album 의 codex ja/zh/zh_hant 를 iTunes 국가 스토어
	// 제목으로 confirm(값불변·source→itunes 권위승급) + artistName 을 external_ref 로 저장.
	// confirm-only(동명곡 오매칭 방지), 30d 쿨다운. 영어/로마자 제목 곡이 주대상.
	if len(os.Args) > 1 && os.Args[1] == "itunes-songs" {
		n := 100
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: itunes-songs start (n=%d)", n)
		cf, an := kdb.DrainITunesSongs(ctx, pool, itunes.New(), n)
		log.Printf("kdb-app: itunes-songs done (confirmed=%d anchored=%d)", cf, an)
		return
	}

	// ─── one-shot: mb-songs (Phase1 — song_album 승급, MB recording 아티스트 스코프) ──
	if len(os.Args) > 1 && os.Args[1] == "mb-songs" {
		n := 20
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: mb-songs start (n=%d)", n)
		pr, ck := kdb.DrainMusicBrainzSongs(ctx, pool, musicbrainz.New(), n)
		log.Printf("kdb-app: mb-songs done (promoted=%d checked=%d)", pr, ck)
		return
	}

	// ─── one-shot: itunes-cands (Phase1 — song_album 승급, iTunes KR 아티스트 스코프) ──
	if len(os.Args) > 1 && os.Args[1] == "itunes-cands" {
		n := 20
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: itunes-cands start (n=%d)", n)
		pr, ck := kdb.DrainITunesSongCandidates(ctx, pool, itunes.New(), n)
		log.Printf("kdb-app: itunes-cands done (promoted=%d checked=%d)", pr, ck)
		return
	}

	// ─── one-shot: kopis-events (Phase1 — event_tour 승급, KOPIS 공연 카탈로그) ──
	if len(os.Args) > 1 && os.Args[1] == "kopis-events" {
		n := 20
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		key, _ := apikeys.Resolve(ctx, pool, "KDB_KOPIS_API_KEY")
		log.Printf("kdb-app: kopis-events start (n=%d key=%v)", n, key != "")
		pr, ck := kdb.DrainKopisEvents(ctx, pool, kopis.New(), key, n)
		log.Printf("kdb-app: kopis-events done (promoted=%d checked=%d)", pr, ck)
		return
	}

	// ─── one-shot: discogs-songs (iTunes 폴백 confirm + release/artist 앵커) ──
	// `kdb-app discogs-songs [n]` — song_album 잔존 codex 를 Discogs release 제목으로 confirm
	// (값불변·source→discogs) + artist/release external_ref. iTunes confirm 분은 자동 제외.
	// confirm-only, 45d 쿨다운, 2.5s pacing. KDB_DISCOGS_TOKEN 있으면 rate-limit 완화.
	if len(os.Args) > 1 && os.Args[1] == "discogs-songs" {
		n := 100
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: discogs-songs start (n=%d)", n)
		cf, an := kdb.DrainDiscogsSongs(ctx, pool, newDiscogsClient(ctx, pool), n)
		log.Printf("kdb-app: discogs-songs done (confirmed=%d anchored=%d)", cf, an)
		return
	}

	// ─── one-shot subcommand: tmdb-refresh ────────────────────────
	// `kdb-app tmdb-refresh [n]` — 작품(movie/drama/show) n건의 TMDb 제목을 재적용.
	// enrich 가 빈칸전용이라 교정 못한 wikidata/codex 영어복사값(오징어게임 pt_br=
	// Squid Game)을 TMDb 우선순위(4)로 교체(→Round 6). + alternative_titles 보강.
	if len(os.Args) > 1 && os.Args[1] == "tmdb-refresh" {
		n := 50
		ko := ""
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v // 숫자 → batch 모드
			} else {
				ko = os.Args[2] // 문자열 → 단일 작품(검증용)
			}
		}
		log.Printf("kdb-app: tmdb-refresh start (n=%d ko=%q)", n, ko)
		w, f, ra, e := enrich.New(pool).RefreshVideoTitles(ctx, n, ko)
		if e != nil {
			log.Printf("kdb-app: tmdb-refresh err: %v", e)
		}
		log.Printf("kdb-app: tmdb-refresh done (works=%d, locales_filled=%d, reattributed=%d)", w, f, ra)
		return
	}

	// ─── one-shot subcommand: ott-fill ────────────────────────────
	// `kdb-app ott-fill [n]` — 무QID·무TMDb 작품의 codex/빈칸 locale 을 넷플릭스 공식
	// 현지제목으로 채운다(ID-앵커링). ★IP 차단 방지: 매 조회 사이 10초(KDB_OTT_MIN_INTERVAL_MS)
	// pacing — 절대 벌크 아님. source='netflix'(prio 4). 7d 쿨다운(field='ottfill').
	if len(os.Args) > 1 && os.Args[1] == "ott-fill" {
		n := 5
		ko := ""
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v // 숫자 → batch
			} else {
				ko, n = os.Args[2], 1 // 문자열 → 단일 작품(검증용)
			}
		}
		log.Printf("kdb-app: ott-fill start (n=%d ko=%q, 폴백체인 Disney→Netflix, 10초 pacing — no bulk)", n, ko)
		proc, filled := kdb.DrainOTTCascade(ctx, pool, n, ko)
		log.Printf("kdb-app: ott-fill done (processed=%d, filled=%d)", proc, filled)
		return
	}

	// ─── one-shot subcommand: disney-fill ─────────────────────────
	// `kdb-app disney-fill [n|작품명]` — 작품의 codex/빈 locale 을 Disney+ 지역 공식제목으로
	// 채운다(ID-앵커링, ja/es/zh_hant). source='disney'(prio 4). 매 조회 사이 10초
	// (KDB_DISNEY_MIN_INTERVAL_MS) pacing — 절대 벌크 아님. 7d 쿨다운(field='disneyfill').
	if len(os.Args) > 1 && os.Args[1] == "disney-fill" {
		n := 5
		ko := ""
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			} else {
				ko, n = os.Args[2], 1
			}
		}
		log.Printf("kdb-app: disney-fill start (n=%d ko=%q, 10s pacing — no bulk)", n, ko)
		proc, filled := kdb.DrainDisneyWorks(ctx, pool, n, ko)
		log.Printf("kdb-app: disney-fill done (processed=%d, filled=%d)", proc, filled)
		return
	}

	// ─── one-shot subcommand: rejudge-rejects (CR-1 백로그 회복) ────
	// `kdb-app rejudge-rejects [n] [--dry]` — 과거 하드 reject 된 엔티티를 Wikidata 로
	// 재심해 실존 K-엔티티면 candidate(운영자 검토)로 복원. Wikidata 무존재는 rejected 유지.
	if len(os.Args) > 1 && os.Args[1] == "rejudge-rejects" {
		n, dry := 50, false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: rejudge-rejects start (n=%d dry=%v)", n, dry)
		checked, restored := kdb.RejudgeRejects(ctx, pool, n, dry)
		log.Printf("kdb-app: rejudge-rejects done (checked=%d restored=%d dry=%v)", checked, restored, dry)
		return
	}

	// ─── one-shot subcommand: mdl-fill (실험적) ───────────────────
	// `kdb-app mdl-fill [n|작품명]` — 작품의 codex/빈 ja 를 MyDramaList "Also Known As"
	// 의 일본어 제목으로 채운다(한국어 원제 앵커 + 가나 결정적탐지). source='mydramalist'
	// (prio 7, codex 만 교체). 매 HTTP 사이 4초(KDB_MDL_MIN_INTERVAL_MS) pacing.
	// ★실증: niche 작품은 MDL 도 일본어 제목 미보유(수율 낮음) → autopilot 미편입, 수동/검증용.
	if len(os.Args) > 1 && os.Args[1] == "mdl-fill" {
		n := 5
		ko := ""
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			} else {
				ko, n = os.Args[2], 1
			}
		}
		log.Printf("kdb-app: mdl-fill start (n=%d ko=%q, 4s pacing — no bulk)", n, ko)
		proc, filled := kdb.DrainMDLWorks(ctx, pool, n, ko)
		log.Printf("kdb-app: mdl-fill done (processed=%d, filled=%d)", proc, filled)
		return
	}

	// ─── one-shot subcommand: localfill ───────────────────────────
	// `kdb-app localfill [n] [--dry]` — 빈 locale n건 엔티티를 websearch(SearXNG)+
	// gemma 다회투표로 현지표기 검색보강 → /v1/qa/result(2단계 local-search/local-usage).
	// --dry 면 검색·추출만(쓰기 X, 검증용). server22 워커 없이 KDB 자체 가동.
	if len(os.Args) > 1 && os.Args[1] == "localfill" {
		n, dry, reground := 5, false, false
		var koParts []string
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if a == "--reground" {
				reground = true // 빈칸뿐 아니라 codex-fallback locale 도 재그라운딩(강증거만 교체)
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			} else if strings.TrimSpace(a) != "" {
				koParts = append(koParts, a)
			}
		}
		ko := strings.Join(koParts, " ")
		perEntity := 2
		if reground {
			perEntity = 3 // 부하최소 기본(상향은 KDB_LOCALFILL_PER_ENTITY)
		}
		if v := os.Getenv("KDB_LOCALFILL_PER_ENTITY"); v != "" {
			if pe, e := strconv.Atoi(v); e == nil && pe > 0 {
				perEntity = pe
			}
		}
		log.Printf("kdb-app: localfill start (n=%d ko=%q perEntity=%d dry=%v reground=%v)", n, ko, perEntity, dry, reground)
		var applied int
		var e error
		if ko != "" {
			applied, e = kdb.LocalFillRunForName(ctx, pool, ko, perEntity, dry, reground)
		} else {
			applied, e = kdb.LocalFillRun(ctx, pool, n, perEntity, dry, reground)
		}
		if e != nil {
			log.Printf("kdb-app: localfill err: %v", e)
		}
		log.Printf("kdb-app: localfill done (applied=%d ko=%q dry=%v reground=%v)", applied, ko, dry, reground)
		return
	}

	// ─── one-shot subcommand: autoverify-drain ────────────────────
	// `kdb-app autoverify-drain [total]` — 미해결 보류를 반복 Run 으로 일괄 소진.
	// 별도 프로세스라 Naver 예산 회계가 분리되므로 KDB_INTAKE_AUTOVERIFY_DAILY_CALLS=1
	// 로 실행 권장(웹검색 폴백만 사용 — 상주 워커의 Naver 예산을 침범하지 않음).
	if len(os.Args) > 1 && os.Args[1] == "autoverify-drain" {
		total := 2000
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				total = v
			}
		}
		v := kdb.NewIntakeAutoVerifier(pool)
		done, promotedAll := 0, 0
		for done < total {
			batch := total - done
			if batch > 200 {
				batch = 200
			}
			checked, promoted := v.Run(ctx, batch)
			if checked == 0 {
				break
			}
			done += checked
			promotedAll += promoted
			log.Printf("kdb-app: autoverify-drain progress %d/%d (promoted %d)", done, total, promotedAll)
		}
		log.Printf("kdb-app: autoverify-drain done checked=%d promoted=%d", done, promotedAll)
		return
	}

	// ─── one-shot subcommand: drain-kmdb ──────────────────────────
	// `kdb-app drain-kmdb [n]` — KMDb 승급·채움 수동 실행(일 100건 쿼터 주의).
	if len(os.Args) > 1 && os.Args[1] == "drain-kmdb" {
		n := 30
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		key, src := apikeys.Resolve(ctx, pool, "KDB_KMDB_API_KEY")
		log.Printf("kdb-app: drain-kmdb start (limit=%d, key=%s)", n, src)
		promoted, filled, checked := kdb.DrainKMDb(ctx, pool, kmdb.New(), key, n)
		log.Printf("kdb-app: drain-kmdb done promoted=%d filled=%d /%d", promoted, filled, checked)
		return
	}

	// ─── one-shot subcommand: drain-tmdb ──────────────────────────
	// `kdb-app drain-tmdb [n] [--dry]` — TMDb 정확제목 유일매치로 movie/drama/show
	// candidate 승급(수동/검증). --dry 는 판정·로그만 하고 DB 미변경.
	if len(os.Args) > 1 && os.Args[1] == "drain-tmdb" {
		n := 20
		dry := false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		token, src := apikeys.Resolve(ctx, pool, "KDB_TMDB_API_TOKEN")
		log.Printf("kdb-app: drain-tmdb start (limit=%d dry=%v token=%s)", n, dry, src)
		promoted, filled, checked := kdb.DrainTMDbCandidates(ctx, pool, tmdb.New(), token, n, dry)
		log.Printf("kdb-app: drain-tmdb done promoted=%d filled=%d /%d (dry=%v)", promoted, filled, checked, dry)
		return
	}

	// ─── one-shot subcommand: anchor-tmdb ─────────────────────────
	// `kdb-app anchor-tmdb [n] [--dry]` — **active** 작품의 TMDb 앵커 사각지대(핸드오프
	// 43차 §5)를 닫는다. ref 만 붙이고 로케일은 tmdb-locale 레인이 이어받는다.
	// ★큰 쓰기 전에는 반드시 --dry 로 표본을 눈으로 볼 것 — 건수만 보면 오매칭을 놓친다.
	if len(os.Args) > 1 && os.Args[1] == "anchor-tmdb" {
		n := 20
		dry := false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		token, src := apikeys.Resolve(ctx, pool, "KDB_TMDB_API_TOKEN")
		log.Printf("kdb-app: anchor-tmdb start (limit=%d dry=%v token=%s)", n, dry, src)
		st := kdb.DrainTMDbAnchors(ctx, pool, tmdb.New(), token, n, dry)
		log.Printf("kdb-app: anchor-tmdb done anchored=%d /%d (no-match=%d 동명작=%d 비ko=%d 상위시리즈=%d 실패=%d dry=%v)",
			st.Anchored, st.Checked, st.NoMatch, st.Ambiguous, st.Foreign, st.SeasonOnly, st.Failed, dry)
		return
	}

	// ─── one-shot subcommand: anchor-mbgroup ──────────────────────
	// `kdb-app anchor-mbgroup [n] [--dry]` — active·unverified group 의 MusicBrainz
	// 앵커 사각지대를 닫는다(승급 드레인은 candidate 전용이라 이 계층을 안 본다).
	// ★큰 쓰기 전에는 반드시 --dry 로 표본을 눈으로 볼 것 — musicbrainz 는 최상위 등급을 준다.
	if len(os.Args) > 1 && os.Args[1] == "anchor-mbgroup" {
		n := 20
		dry := false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		log.Printf("kdb-app: anchor-mbgroup start (limit=%d dry=%v)", n, dry)
		st := kdb.DrainMBGroupAnchors(ctx, pool, musicbrainz.New(), n, dry)
		log.Printf("kdb-app: anchor-mbgroup done anchored=%d /%d (무매칭=%d 동명=%d 증명실패=%d 짧음=%d 실패=%d dry=%v)",
			st.Anchored, st.Checked, st.NoMatch, st.Ambiguous, st.NoKorProof, st.TooShort, st.Failed, dry)
		return
	}

	// ─── one-shot subcommand: triage-eval ─────────────────────────
	// `kdb-app triage-eval` — 오염 판별 프롬프트 골든셋 평가(프롬프트 변경 시 필수 게이트).
	if len(os.Args) > 1 && os.Args[1] == "triage-eval" {
		fr, mg := kdb.TriageGoldenEval(ctx)
		if fr != 0 {
			log.Fatalf("kdb-app: triage-eval FAIL — 오거부 %d건(기준 0). 프롬프트 배포 금지.", fr)
		}
		if mg > 2 {
			log.Fatalf("kdb-app: triage-eval FAIL — 쓰레기 놓침 %d건(기준 ≤2).", mg)
		}
		log.Printf("kdb-app: triage-eval PASS")
		return
	}

	// ─── one-shot subcommand: triage-exhausted ────────────────────
	// `kdb-app triage-exhausted [n]` — 자동검증이 포기한 보류(autoverify_exhausted)를
	// 오염 판별 에이전트(gemma)로 일괄 선별: garbage 기각 / 나머지 운영자 몫 태그.
	if len(os.Args) > 1 && os.Args[1] == "triage-exhausted" {
		n := 700
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		rej, kept, proc := kdb.TriageExhaustedBacklog(ctx, pool, n)
		log.Printf("kdb-app: triage-exhausted done rejected=%d kept=%d /%d", rej, kept, proc)
		return
	}

	// ─── one-shot subcommand: triage-candidates ───────────────────
	// `kdb-app triage-candidates [n]` — 3일+ 정체 저신뢰 candidate 오염 선별(gemma).
	if len(os.Args) > 1 && os.Args[1] == "triage-candidates" {
		n := 1500
		if len(os.Args) > 2 {
			if v, e := strconv.Atoi(os.Args[2]); e == nil && v > 0 {
				n = v
			}
		}
		rej, kept, proc := kdb.TriageStuckCandidates(ctx, pool, n)
		log.Printf("kdb-app: triage-candidates done rejected=%d kept=%d /%d", rej, kept, proc)
		return
	}

	// ─── one-shot subcommand: triage-residual ─────────────────────
	// `kdb-app triage-residual [n] [--dry]` — 엔티티없음 review 잔존을 이중판정 전수 판별
	// (기존 도구 필터 사각). garbage 만 큐 종결(복원가능), 나머지 유지 태그. --dry=판정만.
	if len(os.Args) > 1 && os.Args[1] == "triage-residual" {
		n := 300
		dry := false
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		rej, kept, proc := kdb.TriageReviewResidual(ctx, pool, n, dry)
		log.Printf("kdb-app: triage-residual done rejected=%d kept=%d /%d (dry=%v)", rej, kept, proc, dry)
		return
	}

	// ─── one-shot subcommand: resolve-unknowns ────────────────────
	// `kdb-app resolve-unknowns [workers]` — entity_type='unknown' 을 0 으로.
	// 로컬+Google News 검색 문맥으로 gpt 재분류 → 실체면 제 타입 active(인물은
	// 인물DB), 비실체면 term+rejected. "모르면 검색" 루프.
	if len(os.Args) > 1 && os.Args[1] == "resolve-unknowns" {
		workers := 4
		if len(os.Args) > 2 {
			if n, e := strconv.Atoi(os.Args[2]); e == nil && n > 0 {
				workers = n
			}
		}
		log.Printf("kdb-app: resolve-unknowns start (workers=%d)", workers)
		autopilot.New(pool).ResolveUnknownsConcurrent(ctx, workers)
		log.Printf("kdb-app: resolve-unknowns done")
		autoEnrichAfterClassify(ctx, pool, workers)
		return
	}

	// ─── data QA: dataqa ──────────────────────────────────────────
	// `kdb-app dataqa [--apply]` — person/group 로마자 locale 오염을 gpt-5.5 로 검수.
	// 기본 dry-run(리포트만). --apply 시 오염 locale 을 감사로그 남기고 비운다(복구 가능).
	if len(os.Args) > 1 && os.Args[1] == "dataqa" {
		apply := false
		for _, a := range os.Args[2:] {
			if a == "--apply" {
				apply = true
			}
		}
		runDataQA(ctx, pool, apply)
		return
	}

	// ─── corrections review: corrections ──────────────────────────
	// `kdb-app corrections [list|approve <id>|reject <id> [사유]]`
	// 외부 소비자 정정 신고 큐(자동반영 미달분)를 운영자가 심사.
	if len(os.Args) > 1 && os.Args[1] == "corrections" {
		runCorrections(ctx, pool, os.Args[2:])
		return
	}

	// ─── zh 간체/번체 정규화: zh-normalize ────────────────────────
	// `kdb-app zh-normalize [limit]` — 기존 canonical_zh(간체)/zh_hant(번체)를
	// MediaWiki 변환으로 일괄 교정(간체 칸의 번체 등). convertible source 만.
	if len(os.Args) > 1 && os.Args[1] == "zh-normalize" {
		limit := 100000
		if len(os.Args) > 2 {
			if n, err := strconv.Atoi(os.Args[2]); err == nil && n > 0 {
				limit = n
			}
		}
		runZhNormalize(ctx, pool, limit)
		return
	}

	// ─── one-shot subcommand: import-kenterhub ────────────────────
	// `kdb-app import-kenterhub <json>` — kenterhub.com /api/celebrities 덤프를
	// candidate 로 등록(Observe 경로 재사용 — PreGate·동명이인 안전 그대로).
	if len(os.Args) > 2 && os.Args[1] == "import-kenterhub" {
		log.Printf("kdb-app: import-kenterhub start (%s)", os.Args[2])
		runImportKenterhub(ctx, pool, os.Args[2])
		return
	}

	// ─── diagnostic: api-test ─────────────────────────────────────
	// `kdb-app api-test` — 외부 API 연결을 실측 점검(키는 DB/.env). OK/FAIL 출력.
	if len(os.Args) > 1 && os.Args[1] == "api-test" {
		for _, p := range apikeys.Probe(ctx, pool) {
			st := "OK  "
			if p.Skipped {
				st = "SKIP"
			} else if !p.OK {
				st = "FAIL"
			}
			log.Printf("api-test [%s] %-16s %s", st, p.Title, p.Detail)
		}
		return
	}

	// ─── diagnostic: enrich-test ──────────────────────────────────
	// `kdb-app enrich-test <ko>` — canonical_ko 로 entity 찾아 enrich 1회 실행 후
	// 결과(채운 locale/source)를 출력. TMDb/KOFIC/Wikidata 연동 점검용.
	if len(os.Args) > 2 && os.Args[1] == "enrich-test" {
		var id string
		if err := pool.QueryRow(ctx,
			`SELECT id::text FROM kwave_entities WHERE canonical_ko=$1 ORDER BY (status='active') DESC LIMIT 1`,
			os.Args[2]).Scan(&id); err != nil {
			log.Printf("enrich-test: entity 없음 ko=%q: %v", os.Args[2], err)
			return
		}
		uid, _ := uuid.Parse(id)
		rep, err := enrich.New(pool).Enrich(ctx, uid)
		log.Printf("enrich-test ko=%q type=%s err=%v layers=%v filled=%+v stillEmpty=%v",
			os.Args[2], rep.EntityType, err, rep.LayersRun, rep.Filled, rep.StillEmpty)
		return
	}

	// ─── diagnostic: classify-test ────────────────────────────────
	// `kdb-app classify-test <ko>` — 단건 gpt 분류 결과를 출력하고 종료.
	if len(os.Args) > 2 && os.Args[1] == "classify-test" {
		j := aijudge.New()
		res, err := j.Classify(ctx, &aijudge.ClassifyInput{Ko: os.Args[2]})
		log.Printf("classify-test ko=%q err=%v result=%+v", os.Args[2], err, res)
		return
	}

	// ─── API server (same options as cmd/kdb-api) ─────────────────
	if os.Getenv("KDB_READINESS_ENABLED") == "1" {
		go (&readiness.Worker{Store: &readiness.Store{Pool: pool, CommonEnabled: os.Getenv("KDB_COMMON_READINESS_ENABLED") == "1" && os.Getenv("KDB_COMMON_ENTITY_ENABLED") == "1", CommonFillEnabled: os.Getenv("KDB_COMMON_FILL_ENABLED") == "1"}, Source: wikidata.New()}).Run(ctx)
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") == "1" && os.Getenv("KDB_ENTITY_RESOLVER_ENABLED") == "1" {
		go (&kentity.Resolver{Store: &kentity.Store{Pool: pool}, Source: wikidata.New()}).Run(ctx)
	}
	if os.Getenv("KDB_COMMON_ENTITY_ENABLED") == "1" && os.Getenv("KDB_TDB_SHADOW_ENABLED") == "1" {
		go (&kentity.TDBShadowWorker{Store: &kentity.Store{Pool: pool}, Source: wikidata.New()}).Run(ctx)
	}
	apiPort := os.Getenv("KDB_API_PORT")
	if apiPort == "" {
		apiPort = "9100"
	}
	apiSrv := &http.Server{
		Addr: ":" + apiPort,
		Handler: kdbapi.NewRouterWithOptions(pool, kdbapi.RouterOptions{
			RequestTimeout: envDurationSeconds("KDB_API_REQUEST_TIMEOUT_SECONDS", 10*time.Second),
			LogRequests:    true,
			APIKeys:        envCSV("KDB_API_KEYS"),
			OnResearchEnqueue: func() {
				select {
				case researchKick <- struct{}{}: // non-blocking: 이미 대기 신호 있으면 스킵
				default:
				}
			},
			OnReviewParked: func(rowID string) {
				select {
				case intakeReviewKick <- rowID: // 유입 즉시 자동검증(오너: 바로 심사)
				default: // 가득이면 스킵 — backlog tick 이 수거
				}
			},
			OnDemandCandidate: func(entityID string) {
				select {
				case demandKick <- entityID: // 소비자가 기다리는 candidate 즉시 재추진
				default: // 가득이면 스킵 — 20분 스위프가 이어받는다
				}
			},
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// ─── Admin server (same options as cmd/kdb-admin) ─────────────
	adminPort := os.Getenv("KDB_ADMIN_PORT")
	if adminPort == "" {
		adminPort = "9101"
	}
	var secret []byte
	if s := os.Getenv("KDB_ADMIN_SESSION_SECRET"); s != "" {
		secret = []byte(s)
	}
	adminSrv := &http.Server{
		Addr: ":" + adminPort,
		Handler: kdbadmin.NewRouter(pool, kdbadmin.Options{
			SessionSecret: secret,
			LogRequests:   true,
		}),
		ReadHeaderTimeout: 5 * time.Second,
	}

	// ─── graceful shutdown of both servers ────────────────────────
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := apiSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("api shutdown: %v", err)
		}
		if err := adminSrv.Shutdown(shutdownCtx); err != nil {
			log.Printf("admin shutdown: %v", err)
		}
	}()

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		log.Printf("kdb-app api listening on :%s", apiPort)
		if err := apiSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("api listen: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		log.Printf("kdb-app admin listening on :%s", adminPort)
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("admin listen: %v", err)
		}
	}()

	// ─── worker loop (identical to cmd/kdb-worker) ────────────────
	// ★KDB_WORKER_ENABLED=0 이면 **읽기만 하는 대기 상태**로 뜬다 (2026-09-14).
	// 종전엔 스위치가 없어 프로세스가 뜨는 순간 무조건 일을 시작했다. 그러면 같은 DB 를
	// 복제한 두 번째 인스턴스를 **시험 삼아 띄울 수가 없다** — 둘이 같은 외부 API 를
	// 중복 호출하고 각자 다른 DB 에 쓴다(서버 이전 예행연습에서 실제로 막힌 자리).
	// 기본값은 1 이라 지금 운영 동작은 그대로다. 끄는 것은 의도적으로 적어야만 된다.
	if os.Getenv("KDB_WORKER_ENABLED") == "0" {
		log.Printf("kdb-app worker disabled (KDB_WORKER_ENABLED=0) — 읽기 전용 대기 상태")
	} else {
		go runWorker(ctx, pool)
	}

	wg.Wait()
}

// runWorker — fast 30s (BridgeHealthCheck + SweeperTick), poll 15m (PollerTick),
// autopilot 30m (autopilot.New(pool).Run, first run 30s after start).
func runWorker(ctx context.Context, pool *pgxpool.Pool) {
	fastInterval := envDurationSeconds("KDB_WORKER_FAST_INTERVAL_SECONDS", 30*time.Second)
	pollInterval := envDurationSeconds("KDB_WORKER_POLL_INTERVAL_SECONDS", 15*time.Minute)
	autoInterval := envDurationSeconds("KDB_AUTOPILOT_INTERVAL_SECONDS", 30*time.Minute)
	researchInterval := envDurationSeconds("KDB_RESEARCH_INTERVAL_SECONDS", 60*time.Second)
	dataqaInterval := envDurationSeconds("KDB_DATAQA_INTERVAL_SECONDS", 20*time.Minute)
	dataqaOn := os.Getenv("KDB_DATAQA_ENABLED") == "1"
	// 정체성 검증 tier 결정론 스윕(증분2) — 신규/재enrich 로 stale 해진 tier 를 주기적
	// 재계산해 서빙 캐시를 최신 유지. set-based 단일 UPDATE(무료·무쿼터, <1s).
	verifyInterval := envDurationSeconds("KDB_VERIFY_SWEEP_INTERVAL_SECONDS", 10*time.Minute)
	// Quality confidence drain is maintenance, not request processing. Running
	// it on the (operationally 8s) research ticker produced hundreds of zero-yield
	// scans per hour; keep it on an independently tunable daily cadence.
	qualityInterval := envDurationSeconds("KDB_QUALITY_INTERVAL_SECONDS", 24*time.Hour)
	// rejudge(오거부 자동복구, 2026-07-08): 매 주기 소량 rejected 를 Wikidata 재심 → 실존 K 는
	// candidate 로 복원(이후 게이트키퍼/승급 재심). 쿨다운 내장이라 신규 오거부만 점진 처리.
	// Wikidata pacing(300ms) 내장이라 부하 적음. 0 이하면 비활성(env 로 조절).
	rejudgeInterval := envDurationSeconds("KDB_REJUDGE_INTERVAL_SECONDS", 30*time.Minute)
	// MusicBrainz group candidate drain. Explicit ENABLED flags below are
	// default-off canary gates for every newly added automatic promotion lane.
	musicbrainzInterval := envDurationSeconds("KDB_MUSICBRAINZ_INTERVAL_SECONDS", 10*time.Minute)
	// KOFIC(K-영화 candidate 승급, 2026-07-08): 극장영화 정확제목 매칭만 → movie candidate 승급.
	koficInterval := envDurationSeconds("KDB_KOFIC_INTERVAL_SECONDS", 15*time.Minute)
	kmdbInterval := envDurationSeconds("KDB_KMDB_INTERVAL_SECONDS", time.Hour) // 일 100건 쿼터 — 시간당 4건이면 96/일
	// TMDb candidate 승급(2026-07-21): movie/drama/show 정확제목 유일매치 → active. KOFIC/
	// KMDb(movie만)가 못 닿는 drama/show 커버리지 레버. 기본 OFF(dry 검증 후 KDB_TMDB_DRAIN_ENABLED=1).
	tmdbInterval := envDurationSeconds("KDB_TMDB_INTERVAL_SECONDS", 2*time.Minute)
	// Wikidata person 승급(K-Wave 인물, 2026-07-08): Search(filterKWave) description 게이트로
	// 비-K(외국·역사·비연예) 배제 → 실K 인물 candidate(≈494 최대버킷) active 승급.
	wdPersonInterval := envDurationSeconds("KDB_WDPERSON_INTERVAL_SECONDS", 10*time.Minute)
	// QID 보유 active 엔티티의 로케일 빈칸 회수(2026-08-07). 조회 1건당 200ms 라 배치를
	// 크게 잡아도 싸다 — 실측 백로그 359건을 몇 시간 안에 소진하는 주기로 둔다.
	wdLocaleInterval := envDurationSeconds("KDB_WDLOCALE_INTERVAL_SECONDS", 5*time.Minute)
	// 새 유형 앵커 — 1회 12건 × (검색 300ms + 최대 3회 조회 250ms) ≈ 13초.
	// 후보가 103건뿐이라 10분 주기면 한 시간 반이면 한 바퀴 돈다.
	orgAnchorInterval := envDurationSeconds("KDB_ORG_ANCHOR_INTERVAL_SECONDS", 10*time.Minute)
	// 직업 영역 뒤채움 — 묶음 조회라 1회 200명이 4회 호출이면 끝난다(몇 초).
	// 뒤채울 것이 3,600여 건이라 5분 주기면 하루 안에 다 돈다.
	occupationInterval := envDurationSeconds("KDB_OCCUPATION_FILL_INTERVAL_SECONDS", 5*time.Minute)
	// 인테이크 자동 검증(2026-07-13, 오너: "없으면 검증 후 바로 추가작업"): review 보류
	// 키워드의 근거(type·문맥·출처)를 Naver 로 KDB 가 직접 수집 → DecideIntake 재평가
	// 통과분만 approved 승격 → 발굴 진행. 기본 on(KDB_INTAKE_AUTOVERIFY=0 으로 끔).
	autoVerifyInterval := envDurationSeconds("KDB_INTAKE_AUTOVERIFY_INTERVAL_SECONDS", 2*time.Minute)
	// 요청대기 candidate 뉴스근거 승급 상시화(2026-07-21, 오너: "대기 없이 바로바로"): 하루 1회
	// (5-7 KST 배치)만으론 낮에 온 요청이 다음날 새벽까지 홀드 → 20분 티커로 쿨다운 풀리는
	// 즉시 승급. 엔티티당 Naver 1콜·7d 쿨다운이라 대상풀 소진 후엔 대부분 no-op(예산 안전).
	// 오염=자동기각 금지(review 플래그만). single-flight 로 겹침 방지.
	candEvidenceInterval := envDurationSeconds("KDB_CAND_EVIDENCE_INTERVAL_SECONDS", 20*time.Minute)
	// KOPIS event_tour 승급(2026-07-23 Phase1): 공연예술통합전산망 공연명 대조 —
	// event_tour 첫 결정적 앵커. 기본 OFF(KDB_KOPIS_DRAIN_ENABLED=1 카나리).
	kopisInterval := envDurationSeconds("KDB_KOPIS_INTERVAL_SECONDS", 10*time.Minute)
	revertTermInterval := envDurationSeconds("KDB_REVERT_TERM_INTERVAL_SECONDS", 15*time.Minute)
	// ★자체 수리를 주기로 돌린다 (2026-09-20).
	//
	//   종전엔 일회성 명령뿐이었다. 그래서 어제 손으로 «간체 칸 번체 0» 을 만들어도,
	//   그 뒤에 들어오는 값은 아무도 안 봤다. 오늘 내가 직접 증명했다 — occup-scope-restore
	//   로 155행을 active 로 되돌렸더니 그중 16칸이 오염된 채 서빙 대상이 됐고,
	//   내가 우연히 표본을 눈으로 보지 않았으면 그대로 나갔다.
	//
	//   ★핵심은 «채우는 경로»가 아니라 «승급 경로»다. QA 자체검사(qaCharsetOK)는 값을
	//     채울 때만 본다. 이미 값을 가진 행이 candidate/rejected 에서 active 로 올라오면
	//     그 검사를 한 번도 안 거친다. 승급 경로는 하나가 아니라 여럿이라(kopis·kmdb·
	//     tmdb·kofic·on-demand·occup-scope) 각각에 가드를 다는 것보다 **결과를 주기로
	//     보는 편**이 새 경로가 생겨도 안 새는 유일한 방법이다.
	zhRepairInterval := envDurationSeconds("KDB_ZH_REPAIR_INTERVAL_SECONDS", 30*time.Minute)
	// ★앵커 붙이기도 주기로 돌린다 (2026-09-20). 같은 이유다 — 일회성 명령뿐이라
	//   **사람이 칠 때만** 돌았다(실측 시도 추이 9/15:28 · 9/16:53 · 9/17:6 · 9/18:20 ·
	//   9/19:9 · 9/20:3 — 전부 내가 손으로 친 날이다).
	//
	//   ★그런데 이게 지금 가장 큰 갭의 병목이다. 중국어 빈칸 3,371행 중 위키데이터
	//     앵커가 있는 것은 **266행뿐**이다(8%). 번역이 안 되는 게 아니라 대상을
	//     특정할 근거가 없는 것이다. 적격 1,378행이 그대로 쌓여 있다.
	//
	//   수율은 레인 주석의 실측대로 낮다(정확일치 32% → 가드 통과 10%). 그래도
	//   1,378행이면 대략 140건이 authoritative 로 올라가고, 앵커가 붙으면 wd-locale
	//   이 zh·ja 라벨을 끌어온다. 한 시간에 60행이면 하루 남짓에 소진된다.
	kowikiAnchorInterval := envDurationSeconds("KDB_KOWIKI_ANCHOR_INTERVAL_SECONDS", time.Hour)
	// ★주석 걷기도 주기로 돌린다 (2026-09-20).
	//
	//   오늘 같은 모양을 세 번 봤다 — 레인을 끄거나 고쳤는데 그 레인이 남긴 표시를
	//   아무도 안 걷었다(`[revert-term:reject]` · `[scope:review]` · 그리고 내가
	//   오늘 만든 `[occup-scope-restore]`). 같은 실수를 여기서 또 하지 않는다.
	//
	//   괄호 주석을 만드는 출처(wikidata-label · wikipedia-zh-variant · opencc · tmdb)는
	//   지금도 돌고 있다. 한 번 걷고 끝내면 내일 다시 쌓인다.
	parenAnnotInterval := envDurationSeconds("KDB_PAREN_ANNOT_INTERVAL_SECONDS", time.Hour)
	// api-source-no-ref 회수(2026-08-03): musicbrainz/kofic 라벨은 달렸는데 그 provider
	// ref 가 없는 active 를 재검색해 식별자를 되찾는다(도입 시 484+85). 승급 레인이
	// 아니라 **기록 복구** 레인이라 카나리 플래그 없이 기본 on — 대상이 유한하고
	// 30일 쿨다운으로 스스로 마른다. 끄려면 KDB_APIREF_RECOVER=0.
	apiRefRecoverInterval := envDurationSeconds("KDB_APIREF_RECOVER_INTERVAL_SECONDS", 10*time.Minute)
	backlogWatchIntv := envDurationSeconds("KDB_BACKLOG_WATCH_INTERVAL_SECONDS", kdb.BacklogWatchInterval())

	log.Printf("kdb-app worker starting fast=%s poll=%s autopilot=%s research=%s dataqa=%v(%s)", fastInterval, pollInterval, autoInterval, researchInterval, dataqaOn, dataqaInterval)

	// 자율 폴백 와이어(2026-06-20): Gemma 게이트웨이가 헬스 모니터상 다운이면 RoleProvider
	// 가 gemma 라우팅(CLASSIFY/FILL/FILLPERSON)을 Codex 로 자동 폴백한다. import cycle
	// 회피용 hook. (codexcli 는 kdb 를 import 못 하므로 cmd 에서 와이어.)
	codexcli.GemmaDown = func() bool { return !kdb.GemmaHealthy() }
	// 거울 와이어(2026-06-22): Codex bridge breaker 가 열리면 codex 라우팅 role
	// (동명이인/정정검증/dataqa 등)을 로컬 gemma 로 자동 인계(자가복구). 양방향 메시.
	codexcli.CodexDown = kdb.BreakerIsOpen

	auto := autopilot.New(pool)
	researchWorker := research.New(pool)
	mbClient := musicbrainz.New()
	koficClient := kofic.New()
	kmdbClient := kmdb.New()
	kopisClient := kopis.New()
	tmdbClient := tmdb.New()
	wdClient := wikidata.New()
	intakeVerifier := kdb.NewIntakeAutoVerifier(pool)
	intakeVerifier.Kick = func() {
		select {
		case researchKick <- struct{}{}: // 승격 즉시 발굴 시작(온디맨드 빠른 채움)
		default:
		}
	}
	// fresh 레인 소비자: 유입 순간 해당 키워드만 즉시 검증(직렬 — Naver throttle 존중).
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case rowID := <-intakeReviewKick:
				intakeVerifier.VerifyFresh(ctx, rowID)
			}
		}
	}()

	// ★요청 훅 소비자 (2026-09-16). 소비자가 기다리는 candidate 를 즉시 재추진한다.
	//
	//   Trigger 안에 동시 3 캡이 있어 여기 루프는 직렬이어도 된다 — 오히려 직렬이라야
	//   한 소비자의 50낱말 bulk 가 채널을 비우는 속도와 실제 외부 호출 속도가 어긋나지
	//   않는다. 되풀이 방지(엔티티당 1시간)는 Enrich·CandidateEvidenceOne 안에 이미
	//   있으므로 여기서 또 세지 않는다 — 시계를 두 개 두면 서로 다른 답을 한다.
	demandLane := demand.New(pool)
	if demandLane == nil {
		log.Printf("kdb-app 요청훅 꺼짐 (KDB_DEMAND_LANE=0)")
	} else {
		// 집계를 주기적으로 남긴다. **부르는 곳이 없으면 집계는 없는 것과 같다** —
		// 오늘만 "장치는 있는데 아무도 안 켠" 결함을 다섯 번 만났다.
		demandStatTicker := time.NewTicker(envDurationSeconds("KDB_DEMAND_STAT_INTERVAL_SECONDS", 15*time.Minute))
		go func() {
			defer demandStatTicker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-demandKick:
					demandLane.Trigger(id)
				case <-demandStatTicker.C:
					demandLane.LogStats()
				}
			}
		}()
	}

	rejudgeLane := &laneRunner{name: "rejudge", fn: func(runCtx context.Context) {
		start := time.Now()
		checked, restored := kdb.RejudgeRejects(runCtx, pool, 40, false)
		hermes.RecordRun(runCtx, pool, hermes.RunRecord{
			Role: "Rejudge", Status: "ok", ItemsIn: checked, ItemsOut: restored,
			SelfCheckOK: true, StartedAt: start, Detail: "Wikidata rejected candidate recheck",
		})
	}}
	musicbrainzLane := &laneRunner{name: "musicbrainz", fn: func(runCtx context.Context) {
		start := time.Now()
		promoted, checked := kdb.DrainMusicBrainzCandidates(runCtx, pool, mbClient, 8)
		hermes.RecordRun(runCtx, pool, hermes.RunRecord{
			Role: "MusicBrainzDrain", Status: "ok", ItemsIn: checked, ItemsOut: promoted,
			SelfCheckOK: true, StartedAt: start, Detail: "group -> MusicBrainz Group artist exact-name",
		})
		// 2026-07-23 Phase1: song_album 승급 — recording/release-group 아티스트 스코프.
		sStart := time.Now()
		sPromoted, sChecked := kdb.DrainMusicBrainzSongs(runCtx, pool, mbClient, 8)
		hermes.RecordRun(runCtx, pool, hermes.RunRecord{
			Role: "MBSongDrain", Status: "ok", ItemsIn: sChecked, ItemsOut: sPromoted,
			SelfCheckOK: true, StartedAt: sStart, Detail: "song_album -> MB recording/release-group artist-scoped exact",
		})
	}}
	kopisLane := &laneRunner{name: "kopis", fn: func(runCtx context.Context) {
		start := time.Now()
		kopisKey, _ := apikeys.Resolve(runCtx, pool, "KDB_KOPIS_API_KEY")
		promoted, checked := kdb.DrainKopisEvents(runCtx, pool, kopisClient, kopisKey, 8)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "KOPISDrain", Status: "ok", ItemsIn: checked, ItemsOut: promoted,
				SelfCheckOK: true, StartedAt: start, Detail: "event_tour -> KOPIS 공연 containment+corroborate",
			})
		}
	}}
	koficLane := &laneRunner{name: "kofic", fn: func(runCtx context.Context) {
		start := time.Now()
		koficKey, _ := apikeys.Resolve(runCtx, pool, "KDB_KOFIC_API_KEY")
		promoted, checked := kdb.DrainKoficCandidates(runCtx, pool, koficClient, koficKey, 8)
		hermes.RecordRun(runCtx, pool, hermes.RunRecord{
			Role: "KOFICDrain", Status: "ok", ItemsIn: checked, ItemsOut: promoted,
			SelfCheckOK: true, StartedAt: start, Detail: "default-off candidate promotion canary",
		})
	}}
	// apiRefRecoverLane — 라벨은 있고 ref 는 없는 건의 식별자 회수 + 장식 ref 교정.
	// MusicBrainz 는 1 req/s 리미터를 musicbrainzLane 과 공유하므로 배치를 작게 잡는다
	// (1건당 검색1+상세최대3 ≈ 4.4s → 12건 ≈ 55s/tick, 484건이면 약 7시간에 소진).
	apiRefRecoverLane := &laneRunner{name: "apiref-recover", fn: func(runCtx context.Context) {
		start := time.Now()
		rec, unres, checked := kdb.RecoverMusicBrainzRefs(runCtx, pool, mbClient, 12)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "APIRefRecoverMB", Status: "ok", ItemsIn: checked, ItemsOut: rec,
				SelfCheckOK: true, StartedAt: start,
				Detail: fmt.Sprintf("musicbrainz 라벨 보유·ref 없음 → MBID 회수(미확인 %d)", unres),
			})
		}
		koficKey, _ := apikeys.Resolve(runCtx, pool, "KDB_KOFIC_API_KEY")
		kStart := time.Now()
		kRec, kUnres, kChecked := kdb.RecoverKoficRefs(runCtx, pool, koficClient, koficKey, 12)
		if kChecked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "APIRefRecoverKOFIC", Status: "ok", ItemsIn: kChecked, ItemsOut: kRec,
				SelfCheckOK: true, StartedAt: kStart,
				Detail: fmt.Sprintf("kofic 라벨 보유·ref 없음 → movieCd 회수(미확인 %d)", kUnres),
			})
		}
		// 장식 ref 교정 — external_id 에 영어 제목이 들어간 기존 30건.
		rStart := time.Now()
		fixed, rChecked := kdb.RepairKoficDecorativeRefs(runCtx, pool, koficClient, koficKey, 12)
		if rChecked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "APIRefRepairKOFIC", Status: "ok", ItemsIn: rChecked, ItemsOut: fixed,
				SelfCheckOK: true, StartedAt: rStart, Detail: "kofic external_id 제목→movieCd 교정",
			})
		}
	}}
	tmdbLane := &laneRunner{name: "tmdb", fn: func(runCtx context.Context) {
		start := time.Now()
		tmdbToken, _ := apikeys.Resolve(runCtx, pool, "KDB_TMDB_API_TOKEN")
		promoted, filled, checked := kdb.DrainTMDbCandidates(runCtx, pool, tmdbClient, tmdbToken, 8, false)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "TMDbDrain", Status: "ok", ItemsIn: checked, ItemsOut: promoted + filled,
				SelfCheckOK: true, StartedAt: start, Detail: "movie/drama/show candidate promotion (exact-unique)",
			})
		}
		// ★로케일 채움(2026-07-31): 위 승급 드레인은 candidate 전용·en 만 채워서, TMDb id 를
		// 이미 보유한 active 작품의 ja/zh 빈칸 192건이 어떤 레인에도 안 잡혔다. 같은 레인에
		// 붙여 TMDb 레이트 예산과 enable 플래그를 공유한다.
		lStart := time.Now()
		lFilled, lChecked := kdb.DrainTMDbLocaleFill(runCtx, pool, tmdbClient, tmdbToken, 20)
		if lChecked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "TMDbLocaleFill", Status: "ok", ItemsIn: lChecked, ItemsOut: lFilled,
				SelfCheckOK: true, StartedAt: lStart, Detail: "active 작품 ja/zh 공식 현지제목 회수(MT 대체 포함)",
			})
		}
		// ★앵커 부착(2026-08-07, 핸드오프 43차 §5): 위 두 드레인은 앵커가 **이미 있는**
		// 것만 본다 — 승급 드레인은 candidate 전용이라, active 가 된 뒤 앵커가 없는 작품
		// 409건(show 287·drama 81·movie 41)은 어느 레인도 보지 않았다. 여기서 ref 만 붙이면
		// 바로 위 tmdb-locale 이 다음 tick 에 집어 현지제목을 회수한다(ref INSERT 가
		// fill_input_hash 를 바꾼다). 같은 레인에 붙여 TMDb 레이트 예산을 공유한다.
		aStart := time.Now()
		aSt := kdb.DrainTMDbAnchors(runCtx, pool, tmdbClient, tmdbToken, 8, false)
		if aSt.Checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "TMDbAnchorAttach", Status: "ok", ItemsIn: aSt.Checked, ItemsOut: aSt.Anchored,
				SelfCheckOK: true, StartedAt: aStart, Detail: "active 작품 TMDb 앵커 부착(정확·유일·원작ko)",
			})
		}
	}}
	kmdbLane := &laneRunner{name: "kmdb", fn: func(runCtx context.Context) {
		start := time.Now()
		kmdbKey, _ := apikeys.Resolve(runCtx, pool, "KDB_KMDB_API_KEY")
		promoted, filled, checked := kdb.DrainKMDb(runCtx, pool, kmdbClient, kmdbKey, 4)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "KMDbDrain", Status: "ok", ItemsIn: checked, ItemsOut: promoted + filled,
				SelfCheckOK: true, StartedAt: start, Detail: "movie 정확제목 승급+en 채움 (일 100건 쿼터)",
			})
		}
	}}
	// 07-21 강등 잔존 candidate 종결 레인 — 승급 가드와 이름검색 드레인 사이 사각지대를 닫는다.
	// 상세 근거는 internal/kdb/reverted_terminate_drain.go 주석. 기각은 wikidata P31/description
	// 증거가 있을 때만이라 배치를 작게 잡아도 된다(라이브 조회 1건당 350ms).
	revertTermLane := &laneRunner{name: "revert-terminate", fn: func(runCtx context.Context) {
		start := time.Now()
		rejected, checked := kdb.DrainTerminateRevertedCandidates(runCtx, pool, wdClient, 20)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "RevertTerminateDrain", Status: "ok", ItemsIn: checked, ItemsOut: rejected,
				SelfCheckOK: true, StartedAt: start, Detail: "강등 잔존 candidate 비-K 종결(P31/직업 근거)",
			})
		}
	}}
	// ★무조건 종결 레인(오너 지시 2026-07-31 "쭈욱 흘러가도록"). 어느 레인도 집지 않는
	// candidate 가 무한 체류하지 않게 기한을 강제한다. 기각이되 tombstone 은 아니라
	// (api.go 참조) 소비자가 다시 요청하면 재발굴된다 — 종결 + 재진입 가능.
	// 상세 근거는 internal/kdb/candidate_ttl.go 주석.
	candTTLLane := &laneRunner{name: "candidate-ttl", fn: func(runCtx context.Context) {
		start := time.Now()
		rejected, checked := kdb.DrainExpireStaleCandidates(runCtx, pool, 25)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "CandidateTTLDrain", Status: "ok", ItemsIn: checked, ItemsOut: rejected,
				SelfCheckOK: true, StartedAt: start, Detail: "TTL 초과 미결 candidate 종결(재요청 시 재발굴)",
			})
		}
	}}
	// ★로케일 채움(2026-08-07): 아래 승급 드레인은 QID 가 **없는** candidate 전용이라
	// (wikidata_person_drain.go:45 에서 ref 보유분을 제외한다) QID 를 **가진** active
	// 엔티티의 ja/zh 빈칸은 어느 레인도 보지 않았다. 실측 359건이 그 상태였고, 그중
	// ja 레이블 148 · zh 레이블 112 가 위키데이터에 그대로 있었다.
	wdLocaleLane := &laneRunner{name: "wikidata-locale", fn: func(runCtx context.Context) {
		start := time.Now()
		filled, checked := kdb.DrainWikidataLocaleFill(runCtx, pool, wdClient, 40)
		if checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "WikidataLocaleFill", Status: "ok", ItemsIn: checked, ItemsOut: filled,
				SelfCheckOK: true, StartedAt: start, Detail: "QID 보유 active 엔티티 로케일 빈칸 회수(ja/zh 포함)",
			})
		}
	}}
	// ★새 유형 앵커 레인(2026-09-16). person 레인과 달리 **기본 ON** 이다 —
	//   그쪽이 default-off canary 인 이유는 사람 이름의 동명이인 위험이고
	//   (이정후→동명 배우 오매칭), 이 레인은 P31 유형 일치 + P17 국가라는
	//   관문 둘을 더 쓴다. 상세 근거는 internal/kdb/org_anchor_drain.go 주석.
	//   KDB_ORG_ANCHOR=0 으로 끈다.
	// ★직업 영역 뒤채움(2026-09-16). 이름을 검색하지 않고 확정 QID 로만 묻는다 —
	//   동명이인 위험이 구조적으로 없다. 묶음 조회라 1회 200명이 몇 초면 끝난다.
	//   KDB_OCCUPATION_FILL=0 으로 끔.
	occupationLane := &laneRunner{name: "occupation-fill", fn: func(runCtx context.Context) {
		start := time.Now()
		r := kdb.DrainOccupationDomain(runCtx, pool, wdClient, 200, false)
		if r.Checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "OccupationDomainFill", Status: "ok", ItemsIn: r.Checked, ItemsOut: r.Domain,
				SelfCheckOK: true, StartedAt: start,
				Detail: "확정 QID P106/P21 묶음 조회 — 원자료만 " + strconv.Itoa(r.Raw) +
					" · 성별 " + strconv.Itoa(r.Gender) + " · 조회실패 " + strconv.Itoa(r.Failed),
			})
		}
	}}
	orgAnchorLane := &laneRunner{name: "org-anchor", fn: func(runCtx context.Context) {
		start := time.Now()
		r := kdb.DrainOrgAnchors(runCtx, pool, wdClient, 12, false)
		if r.Checked > 0 {
			hermes.RecordRun(runCtx, pool, hermes.RunRecord{
				Role: "OrgAnchorDrain", Status: "ok", ItemsIn: r.Checked, ItemsOut: r.Anchored,
				SelfCheckOK: true, StartedAt: start,
				Detail: "새 유형 후보 위키데이터 앵커(P31 유형일치+P17 국가) — 승급 " +
					strconv.Itoa(r.Promoted) + " · 보류 " + strconv.Itoa(r.Held),
			})
		}
	}}
	wdPersonLane := &laneRunner{name: "wikidata-person", fn: func(runCtx context.Context) {
		start := time.Now()
		promoted, checked := kdb.DrainWikidataPersonCandidates(runCtx, pool, wdClient, 8)
		hermes.RecordRun(runCtx, pool, hermes.RunRecord{
			Role: "WikidataPersonDrain", Status: "ok", ItemsIn: checked, ItemsOut: promoted,
			SelfCheckOK: true, StartedAt: start, Detail: "default-off candidate promotion canary",
		})
	}}

	// Hermes supervisor (opt-in, additive). KDB_HERMES_ENABLED=1 runs the
	// existing 8 sweep steps as audited agents under the supervisor (per-step
	// run rows in kwave_kdb_hermes_runs + item-conservation/leak detection)
	// instead of the plain auto.Run. Default (unset) keeps the running
	// autopilot behaviour exactly as before. Requires migration 0061.
	runAutopilot := buildAutopilotRunner(pool, auto)

	runFast(ctx, pool)
	runPoll(ctx, pool)
	// 백로그 계측은 기동 직후 1회 찍는다. 주기가 6시간이라 재기동 뒤 한참 동안 로그가
	// 비는데, 핸드오프 §0 의 첫 명령이 이 로그라 "빈 출력 = 고장"으로 오인된다.
	// 볼 수 없는 지표는 아무도 안 쓴다 — 언제 붙어도 최근 스냅샷이 있어야 한다.
	if os.Getenv("KDB_BACKLOG_WATCH_ENABLED") != "0" {
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(20 * time.Second): // 기동 폭주 회피
			}
			kdb.WatchBacklogs(ctx, pool)
			kdb.WatchInvariants(ctx, pool)
		}()
	}
	// 첫 autopilot 은 30 초 후 (startup 직후 cascade 호출 폭주 회피).
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			runAutopilot(ctx)
		}
	}()

	fastTicker := time.NewTicker(fastInterval)
	defer fastTicker.Stop()
	pollTicker := time.NewTicker(pollInterval)
	defer pollTicker.Stop()
	autoTicker := time.NewTicker(autoInterval)
	defer autoTicker.Stop()
	researchTicker := time.NewTicker(researchInterval)
	defer researchTicker.Stop()
	// 정정 재검증용 Service — api.go 의 구성과 동일(KDB_LLM_CORRECTION 라우팅 존중).
	correctionsSvc := &corrections.Service{
		Pool: pool,
		WD:   wikidata.New(),
		Judge: codexcli.NewRunner().
			WithProvider(codexcli.RoleProvider("CORRECTION", "codex")).
			WithEffort(codexcli.RoleEffort("CORRECTION", "medium")),
	}
	dataqaTicker := time.NewTicker(dataqaInterval)
	defer dataqaTicker.Stop()
	verifyTicker := time.NewTicker(verifyInterval)
	defer verifyTicker.Stop()
	qualityTicker := time.NewTicker(qualityInterval)
	defer qualityTicker.Stop()
	rejudgeTicker := time.NewTicker(rejudgeInterval)
	defer rejudgeTicker.Stop()
	musicbrainzTicker := time.NewTicker(musicbrainzInterval)
	defer musicbrainzTicker.Stop()
	koficTicker := time.NewTicker(koficInterval)
	defer koficTicker.Stop()
	tmdbTicker := time.NewTicker(tmdbInterval)
	defer tmdbTicker.Stop()
	kmdbTicker := time.NewTicker(kmdbInterval)
	defer kmdbTicker.Stop()
	wdPersonTicker := time.NewTicker(wdPersonInterval)
	defer wdPersonTicker.Stop()
	wdLocaleTicker := time.NewTicker(wdLocaleInterval)
	defer wdLocaleTicker.Stop()
	orgAnchorTicker := time.NewTicker(orgAnchorInterval)
	defer orgAnchorTicker.Stop()
	occupationTicker := time.NewTicker(occupationInterval)
	defer occupationTicker.Stop()
	autoVerifyTicker := time.NewTicker(autoVerifyInterval)
	defer autoVerifyTicker.Stop()
	candEvidenceTicker := time.NewTicker(candEvidenceInterval)
	defer candEvidenceTicker.Stop()
	kopisTicker := time.NewTicker(kopisInterval)
	defer kopisTicker.Stop()
	revertTermTicker := time.NewTicker(revertTermInterval)
	defer revertTermTicker.Stop()
	zhRepairTicker := time.NewTicker(zhRepairInterval)
	defer zhRepairTicker.Stop()
	kowikiAnchorTicker := time.NewTicker(kowikiAnchorInterval)
	defer kowikiAnchorTicker.Stop()
	parenAnnotTicker := time.NewTicker(parenAnnotInterval)
	defer parenAnnotTicker.Stop()
	candTTLTicker := time.NewTicker(kdb.CandidateTTLInterval())
	defer candTTLTicker.Stop()
	apiRefRecoverTicker := time.NewTicker(apiRefRecoverInterval)
	defer apiRefRecoverTicker.Stop()
	backlogWatchTicker := time.NewTicker(backlogWatchIntv)
	defer backlogWatchTicker.Stop()
	// TypeVerifier 일일 스케줄(오너 승인 07-17): 매일 05:30 KST 이후 첫 tick 에 타입
	// 오염 역추적(verify-entities type-retrace)을 150건 실행. Naver 쿼터 리셋 직후라
	// 일과 사용과 경합하지 않는다. KDB_TYPE_RETRACE_DAILY=0 으로 끔.
	typeRetraceTicker := time.NewTicker(10 * time.Minute)
	defer typeRetraceTicker.Stop()
	var lastNightDrainDay string
	// KeywordTriage 일일 스케줄: 소진 보류·정체 candidate 오염 선별을 매일 04~05 KST 에
	// 자동 실행(수동 원샷만 있으면 안 돌린 날은 백로그가 다시 쌓인다). gemma 전용이라
	// Naver 쿼터와 무관. KDB_TRIAGE_DAILY=0 으로 끔.
	var lastTriageDay string

	for {
		select {
		case <-ctx.Done():
			log.Printf("kdb-app worker stopping")
			return
		case <-fastTicker.C:
			runFast(ctx, pool)
		case <-pollTicker.C:
			runPoll(ctx, pool)
		case <-autoTicker.C:
			go runAutopilot(ctx)
		case <-researchTicker.C:
			go researchWorker.Tick(ctx)
			go corrections.ReapStale(ctx, pool) // verifying stuck 복구 + proposed 7일 만료
			// 무인 정체 pending 재검증 종결(감사 07-25: '검증 실패/불가'류 3주 정체).
			// 행 선점(verifying 전이)이 원자적이라 겹치는 틱과 안전.
			go correctionsSvc.RetryStuckPending(ctx, 5)
			// on-demand(lookup-miss) candidate 를 검색증강 enrich 로 검증·승급 (적체 방지).
			// 공유 auto 인스턴스 + 가드 — tick 마다 새 인스턴스면 겹침 방지가 무효.
			go auto.ResolveOnDemandGuarded(ctx, 10)
			// Wikidata 교차검증 완료 적체 교정요청 재처리 (source priority 수정분 소급).
			// #6 in-place 감독: 정정검증 적체 반영분만 run row(60s tick 노이즈 방지 — applied>0).
			go func() {
				start := time.Now()
				if applied := corrections.DrainWikidataVerified(ctx, pool, 50); applied > 0 {
					hermes.RecordRun(ctx, pool, hermes.RunRecord{
						Role: "CorrectionDrain", Status: "ok", ItemsOut: applied,
						SelfCheckOK: true, StartedAt: start, Detail: "Wikidata 검증 정정 적체 반영",
					})
				}
			}()
		case <-typeRetraceTicker.C:
			if os.Getenv("KDB_TRIAGE_DAILY") != "0" {
				now := time.Now().In(time.FixedZone("KST", 9*3600))
				day := now.Format("2006-01-02")
				if now.Hour() >= 4 && now.Hour() < 5 && lastTriageDay != day {
					lastTriageDay = day
					go func() {
						rej, kept, proc := kdb.TriageExhaustedBacklog(ctx, pool, 400)
						log.Printf("kdb.triage(daily): exhausted rejected=%d kept=%d /%d", rej, kept, proc)
						rej, kept, proc = kdb.TriageStuckCandidates(ctx, pool, 400)
						log.Printf("kdb.triage(daily): candidates rejected=%d kept=%d /%d", rej, kept, proc)
					}()
				}
			}
			// ★야간 드레인(2026-08-16, 오너 지시 "새벽시간에는 모든 것을 해결"): 종전에는
			// 05:00–07:00 창에서 **고정 배치를 한 번** 돌리고 끝나 백로그가 배치보다 크면
			// 다음 날로 넘어갔다(3,365건에 11일). 이제 창(기본 00:00–05:00)이 닫힐 때까지
			// 레인을 순환하며 소진한다 — 상세는 nightdrain.go.
			if os.Getenv("KDB_NIGHT_DRAIN") != "0" {
				if nowK := time.Now().In(kstZone); lastNightDrainDay != nowK.Format("2006-01-02") {
					if in, _ := inNightWindow(nowK); in {
						lastNightDrainDay = nowK.Format("2006-01-02")
						go runNightDrain(ctx, pool)
					}
				}
			}
		case <-dataqaTicker.C:
			if dataqaOn {
				go runDataQATick(ctx, pool)
			}
		case <-verifyTicker.C:
			go runVerifySweep(ctx, pool)
			go runHealthCheck(ctx, pool)
		case <-qualityTicker.C:
			go auto.DrainQualityGuarded(ctx, 100)
			// 요청 내용 로그(mig 0092) 30일 보존 — 일 1회 prune.
			go func() {
				_, _ = pool.Exec(ctx, `DELETE FROM kwave_kdb_request_terms WHERE created_at < now()-interval '30 days'`)
			}()
		case <-rejudgeTicker.C:
			if os.Getenv("KDB_REJUDGE_ENABLED") == "1" {
				go rejudgeLane.run(ctx)
			}
		case <-musicbrainzTicker.C:
			if os.Getenv("KDB_MUSICBRAINZ_DRAIN_ENABLED") == "1" {
				go musicbrainzLane.run(ctx)
			}
		case <-koficTicker.C:
			if os.Getenv("KDB_KOFIC_DRAIN_ENABLED") == "1" {
				go koficLane.run(ctx)
			}
		case <-apiRefRecoverTicker.C:
			if os.Getenv("KDB_APIREF_RECOVER") != "0" {
				go apiRefRecoverLane.run(ctx)
			}
		case <-tmdbTicker.C:
			if os.Getenv("KDB_TMDB_DRAIN_ENABLED") == "1" {
				go tmdbLane.run(ctx)
			}
		case <-kmdbTicker.C:
			if os.Getenv("KDB_KMDB_DRAIN_ENABLED") == "1" {
				go kmdbLane.run(ctx)
			}
		case <-wdPersonTicker.C:
			if os.Getenv("KDB_WDPERSON_DRAIN_ENABLED") == "1" {
				go wdPersonLane.run(ctx)
			}
		case <-occupationTicker.C:
			// 기본 ON. 칸은 어제 만들었는데 1.4% 만 차 있었다 — 채우는 레인이 없어서였다.
			if os.Getenv("KDB_OCCUPATION_FILL") != "0" {
				go occupationLane.run(ctx)
			}
		case <-orgAnchorTicker.C:
			// 기본 ON. 새 유형 후보는 앵커가 없으면 승급도 다국어도 영영 안 된다.
			if os.Getenv("KDB_ORG_ANCHOR") != "0" {
				go orgAnchorLane.run(ctx)
			}
		case <-wdLocaleTicker.C:
			// 기본 ON — 승급 드레인(default-off canary)과 달리 이건 이미 확정된 앵커에서
			// 레이블만 꺼내는 회수 작업이라 승급 위험이 없다. KDB_WDLOCALE_FILL=0 으로 끔.
			if os.Getenv("KDB_WDLOCALE_FILL") != "0" {
				go wdLocaleLane.run(ctx)
			}
		case <-candEvidenceTicker.C:
			// 요청대기 candidate 뉴스근거 승급 상시화(하루1회→20분). single-flight.
			if os.Getenv("KDB_CAND_EVIDENCE_CONTINUOUS") != "0" {
				go runCandEvidenceTick(ctx, pool)
			}
		case <-kopisTicker.C:
			if os.Getenv("KDB_KOPIS_DRAIN_ENABLED") == "1" {
				go kopisLane.run(ctx)
			}
		case <-backlogWatchTicker.C:
			// 계측 + 결정적 타입감사. 둘 다 판정하지 않는다(워치는 로그만, 감사는 표식만).
			// 교정은 두 모델 합의를 요구하는 recheck-active 패널이 [typeaudit:mismatch] 를
			// 받아서 한다. KDB_BACKLOG_WATCH_ENABLED=0 으로 끔.
			if os.Getenv("KDB_BACKLOG_WATCH_ENABLED") != "0" {
				go func(c context.Context) {
					kdb.WatchBacklogs(c, pool)
					// 불변식은 나이가 아니라 "참이면 안 되는 상태"를 센다. 백로그 감시가
					// 놓치는 계열(라벨은 붙었는데 실체가 없는 것)을 여기서 잡는다.
					kdb.WatchInvariants(c, pool)
					if n := kdb.DrainTypeConsistencyAudit(c, pool, 200); n > 0 {
						log.Printf("kdb.type-audit: 총 %d건 표시 — recheck-active 패널이 판정", n)
					}
				}(ctx)
			}
		case <-revertTermTicker.C:
			// 기본 ON — 사각지대에 쌓인 강등 잔존분은 방치할수록 드레인 재조회만 늘린다.
			// KDB_REVERT_TERM_ENABLED=0 으로 끔.
			if os.Getenv("KDB_REVERT_TERM_ENABLED") != "0" {
				go revertTermLane.run(ctx)
			}
		case <-zhRepairTicker.C:
			// 기본 ON. 고칠 것이 없으면 한 건도 쓰지 않는다(멱등 — 실측 2회차 0건).
			// KDB_ZH_REPAIR_ENABLED=0 으로 끔.
			if os.Getenv("KDB_ZH_REPAIR_ENABLED") != "0" {
				go func() {
					r := kdb.RepairZhVariants(ctx, pool, 300, false, false)
					// ★0건이면 찍지 않는다. 30분마다 «0» 이 쌓이면 진짜 수리가 묻힌다.
					if r.Repaired > 0 || r.MovedToHant > 0 || r.VariantHeld > 0 {
						log.Printf("kdb-app: zh-repair(주기) 판정 %d · 수리 %d · 칸이동 %d · 이체자보류 %d",
							r.Checked, r.Repaired, r.MovedToHant, r.VariantHeld)
					}
				}()
			}
		case <-parenAnnotTicker.C:
			// 기본 ON. KDB_PAREN_ANNOT_ENABLED=0 으로 끔. 쓰기 전무면 조용하다.
			if os.Getenv("KDB_PAREN_ANNOT_ENABLED") != "0" {
				go func() {
					r := kdb.DrainParenAnnotations(ctx, pool, 500, false)
					if r.Stripped > 0 {
						log.Printf("kdb-app: paren-annot(주기) 조회 %d · 걷음 %d", r.Checked, r.Stripped)
					}
				}()
			}
		case <-kowikiAnchorTicker.C:
			// 기본 ON. KDB_KOWIKI_ANCHOR_ENABLED=0 으로 끔.
			// 위키백과 무키 공개 API — 레인 안에 300ms 간격이 있다.
			if os.Getenv("KDB_KOWIKI_ANCHOR_ENABLED") != "0" {
				go func() {
					a, c := kdb.DrainKoWikiAnchors(ctx, pool, 60)
					// 0건이면 안 찍는다 — 매시 «0» 이 쌓이면 진짜 성과가 묻힌다.
					if a > 0 {
						log.Printf("kdb-app: kowiki-anchor(주기) 앵커 %d /%d", a, c)
					}
				}()
			}
		case <-candTTLTicker.C:
			// 기본 ON — 이 레인이 없으면 어느 레인도 안 집는 candidate 가 영원히 남는다
			// (실측: 승급 경로가 열린 건 604 중 45뿐). KDB_CANDIDATE_TTL=0 으로 끔.
			go candTTLLane.run(ctx)
		case <-autoVerifyTicker.C:
			// review 보류 자동 검증(Tick 자체가 single-flight + 일일 Naver 예산 가드).
			go func() {
				start := time.Now()
				if checked, promoted := intakeVerifier.Tick(ctx); checked > 0 {
					hermes.RecordRun(ctx, pool, hermes.RunRecord{
						Role: "IntakeAutoVerify", Status: "ok", ItemsIn: checked, ItemsOut: promoted,
						SelfCheckOK: true, StartedAt: start,
						Detail: "review 보류 키워드 Naver 근거수집 → 게이트 재평가 승격",
					})
				}
			}()
		case <-researchKick:
			// 소비자 신규 키워드 → 즉시 발굴(Tick 은 single-flight 라 진행 중이면 no-op).
			go researchWorker.Tick(ctx)
		}
	}
}

// verifySweepRunning — 검증 스윕 single-flight(겹침 방지).
var verifySweepRunning atomic.Bool

// candEvidenceRunning — 요청대기 candidate 뉴스근거 승급 상시 티커 single-flight.
var candEvidenceRunning atomic.Bool

// runCandEvidenceTick — 20분 티커: 요청대기(review) candidate 를 뉴스근거+gemma 로 소량
// 승급(배치 40). 대상풀은 7d 쿨다운으로 소진되므로 대부분 tick 은 promoted=0 no-op.
// 승급 0·오염 0 이면 조용히 리턴(로그 소음 억제). 하루1회 데일리 배치는 그대로 병존.
func runCandEvidenceTick(ctx context.Context, pool *pgxpool.Pool) {
	if !candEvidenceRunning.CompareAndSwap(false, true) {
		return
	}
	defer candEvidenceRunning.Store(false)
	up, flagged, proc, err := verify.CandidateEvidencePass(ctx, pool, 40)
	if err != nil {
		log.Printf("kdb.cand-evidence(tick): %v", err)
		return
	}
	// ★네이버 예산을 **여기서 보여 준다** (2026-09-16).
	//
	//   NaverBudgetSnapshot 을 만들어 놓고 부르는 곳을 안 두면 예산이 얼마나 남았는지
	//   알 방법이 없다 — 오늘만 "장치는 있는데 아무도 안 켠" 결함을 여섯 번 만났고
	//   그중 둘은 내가 만들었다. 기본값 400 이 맞는 수인지도 이 줄로만 알 수 있다.
	//
	//   처리가 0건이어도 예산이 줄었으면 적는다 — 쓴 만큼은 보여야 한다.
	used, limit := verify.NaverBudgetSnapshot()
	// ★codex 예산도 같은 줄에 (2026-09-17).
	//
	//   CodexBudgetSnapshot 은 만들어 놓고 **소진된 순간에만** 불리고 있었다
	//   ("일일 상한 소진(60/60)"). 그래서 오늘 상한이 30분 만에 바닥난 것을
	//   *바닥난 뒤에야* 알았다. 남은 양이 안 보이면 상한이 맞는 수인지 영영 모른다.
	//   방금 60 → 상향하면서 더 그렇다 — 새 숫자가 맞는지는 이 줄로만 알 수 있다.
	cxUsed, cxLimit := codexcli.CodexBudgetSnapshot()
	if up > 0 || flagged > 0 || used > 0 || cxUsed > 0 {
		log.Printf("kdb.cand-evidence(tick): promoted=%d contam?=%d /%d · 네이버예산 %d/%d · codex예산 %d/%d",
			up, flagged, proc, used, limit, cxUsed, cxLimit)
	}
}

// researchKick — 소비자가 새 키워드를 던지면(prepare/lookup miss → EnqueueResearch) API
// 핸들러가 이 채널로 워커를 즉시 깨운다(온디맨드 빠른 채움 — 주기 tick ≤15s 대기 제거).
// API 서버 함수와 runWorker 가 별개 함수라 패키지 레벨로 공유. buffered(1)=신호 coalesce.
var researchKick = make(chan struct{}, 1)

// intakeReviewKick — 근거 부족(review)으로 보류된 신규/재요청 키워드의 row id.
// 자동 검증기 fresh 레인이 즉시 소비(오너 07-13: "제대로 된 키워드는 유입 즉시 심사").
var intakeReviewKick = make(chan string, 256)

// demandKick — 소비자가 물었는데 아직 candidate 인 엔티티 id (요청 훅).
//
// ★위 둘과 다른 것을 다룬다. researchKick 은 "새 낱말", intakeReviewKick 은 "근거가
// 모자란 신규 낱말"이다. 이건 **이미 행이 있는데 답이 안 나가는** 경우다 — 재요청은
// 큐 INSERT 가 중복으로 걸러지고 재개 UPDATE 는 legacy·review 만 열어 done 에 머물며,
// matches 기본 status 가 'active' 라 bgEnrich 도 안 걸리는 사각지대였다.
//
// buffered 256 은 intakeReviewKick 과 같다. 소비자 bulk 는 요청당 최대 50낱말이고,
// 레인 자체가 동시 3으로 좁으므로 버퍼는 스파이크 흡수용이다. 가득 차면 버린다 —
// 20분 스위프가 이어받고, 소비자가 다시 물으면 또 걸린다.
var demandKick = make(chan string, 256)

// runVerifySweep — 정체성 검증 tier 결정론 스윕(증분2). set-based UPDATE 로 전 active 재분류.
// evidence 패스가 올린 값('search+gemma%')은 강등하지 않고 보존(verify.SweepDeterministic).
func runVerifySweep(ctx context.Context, pool *pgxpool.Pool) {
	if !verifySweepRunning.CompareAndSwap(false, true) {
		return
	}
	defer verifySweepRunning.Store(false)
	c, err := verify.SweepDeterministic(ctx, pool)
	if err != nil {
		log.Printf("kdb-app verify-sweep: %v", err)
		return
	}
	log.Printf("kdb-app verify-sweep: authoritative=%d evidenced=%d unverified=%d", c.Authoritative, c.Evidenced, c.Unverified)
}

// runHealthCheck — 주기 점검(오너 방침: 처리는 즉시, 점검은 주기). 처리가 안 되는지·장애가
// 있는지 감지해 로그 경고만 남긴다(처리 자체는 안 함). admin /admin/ops/health 와 동일 임계.
func runHealthCheck(ctx context.Context, pool *pgxpool.Pool) {
	var pending, failed7d, done24h, over24h int64
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entity_research_queue WHERE status='pending'`).Scan(&pending)
	_ = pool.QueryRow(ctx, `SELECT count(*) FROM kwave_entity_research_queue WHERE status='failed' AND created_at > now()-interval '7 days'`).Scan(&failed7d)
	_ = pool.QueryRow(ctx, `
SELECT count(*), count(*) FILTER (WHERE EXTRACT(EPOCH FROM (finished_at-created_at)) > 120)
  FROM kwave_entity_research_queue
 WHERE finished_at IS NOT NULL AND created_at > now()-interval '24 hours'`).Scan(&done24h, &over24h)

	if pending > 100 {
		log.Printf("kdb-app health: [장애] 발굴 큐 적체 pending=%d (>100) — 워커 처리량 부족", pending)
	}
	if failed7d > 20 {
		log.Printf("kdb-app health: [장애] 발굴 실패 %d건/7d (>20) — 소스·분류 장애 점검", failed7d)
	}
	if done24h >= 10 && over24h*100/done24h > 50 {
		log.Printf("kdb-app health: [주의] 느린 채움 — 24h 발굴 %d건 중 %d건(%d%%)이 120초 초과", done24h, over24h, over24h*100/done24h)
	}
}

// dataqaRunning — 워커 내 dataqa tick single-flight.
var dataqaRunning atomic.Bool

// runDataQATick — 주기적 자가치유: pending 의심 entity 한 배치를 gpt-5.5 로 검수해
// 오염 locale 정리(감사·복구가능) + duplicate 플래그. codex 는 flock 으로 다른
// 스텝과 직렬화돼 안전. 한 번에 한 배치만(점진 커버리지).
func runDataQATick(ctx context.Context, pool *pgxpool.Pool) {
	if !dataqaRunning.CompareAndSwap(false, true) {
		return
	}
	defer dataqaRunning.Store(false)
	start := time.Now()
	st, _, err := dataqa.RunBatch(ctx, pool, codexcli.NewRunner().WithProvider(codexcli.RoleProvider("DATAQA", "codex")), dataqa.Schema, 20, true)
	if err != nil {
		log.Printf("kdb-app dataqa-tick: %v", err)
		// #6 in-place 감독: dataqa(20m)는 autopilot cycle 밖 — 제자리 run row 로 노출.
		hermes.RecordRun(ctx, pool, hermes.RunRecord{
			Role: "DataQA", Status: "incident", Severity: "warning",
			SelfCheckOK: false, StartedAt: start, ErrText: err.Error(), Detail: "dataqa batch 실패",
		})
		return
	}
	if st.Reviewed > 0 {
		log.Printf("kdb-app dataqa-tick: reviewed=%d contaminated=%d(cleared %d fields) dup=%d unc=%d",
			st.Reviewed, st.Contaminated, st.ClearedFields, st.Duplicate, st.Uncertain)
	}
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "DataQA", Status: "ok",
		ItemsIn: st.Reviewed, ItemsOut: st.ClearedFields,
		SelfCheckOK: true, StartedAt: start, Detail: "dataqa 20m 오염 검수(감사·복구가능)",
	})
}

// runZhNormalize — 기존 canonical_zh/zh_hant 를 간체/번체로 일괄 정규화.
// zh 에 한자가 있고 source 가 convertible(wikidata/wikipedia/codex 등)인 entity 를
// 모아, MediaWiki 변환으로 간체(zh)/번체(zh_hant)를 맞춘다. operator/매체합의 보존.
func runZhNormalize(ctx context.Context, pool *pgxpool.Pool, limit int) {
	rows, err := pool.Query(ctx, `
SELECT id, COALESCE(canonical_zh,''), COALESCE(canonical_zh_source,''),
       COALESCE(canonical_zh_hant,''), COALESCE(canonical_zh_hant_source,'')
  FROM kwave_entities
 WHERE status='active' AND (COALESCE(canonical_zh,'')<>'' OR COALESCE(canonical_zh_hant,'')<>'')
 ORDER BY confidence DESC LIMIT $1`, limit)
	if err != nil {
		log.Fatalf("zh-normalize query: %v", err)
	}
	type row struct {
		id                     uuid.UUID
		zh, zhSrc, zhh, zhhSrc string
	}
	var rs []row
	for rows.Next() {
		var r row
		if rows.Scan(&r.id, &r.zh, &r.zhSrc, &r.zhh, &r.zhhSrc) == nil {
			rs = append(rs, r)
		}
	}
	rows.Close()
	convertible := map[string]bool{"": true, "wikidata-label": true, "wikipedia-langlinks": true,
		"wikipedia-sitelink": true, "wikipedia-zh-variant": true, "codex-fallback": true}
	log.Printf("kdb-app zh-normalize: 대상 %d건 검사", len(rs))
	var fixedZh, fixedZhh int
	for _, r := range rs {
		if ctx.Err() != nil {
			break
		}
		han := r.zh
		if han == "" {
			han = r.zhh
		}
		if !zhvariant.HasHan(han) {
			continue
		}
		if convertible[r.zhSrc] {
			if s := zhvariant.ToSimplified(ctx, han); s != "" && s != r.zh {
				if tag, e := pool.Exec(ctx, `UPDATE kwave_entities SET canonical_zh=$2, canonical_zh_source='wikipedia-zh-variant', updated_at=now() WHERE id=$1 AND COALESCE(canonical_zh,'')<>$2`, r.id, s); e == nil && tag.RowsAffected() > 0 {
					fixedZh++
				}
			}
		}
		if convertible[r.zhhSrc] {
			if t := zhvariant.ToTraditional(ctx, han); t != "" && t != r.zhh {
				if tag, e := pool.Exec(ctx, `UPDATE kwave_entities SET canonical_zh_hant=$2, canonical_zh_hant_source='wikipedia-zh-variant', updated_at=now() WHERE id=$1 AND COALESCE(canonical_zh_hant,'')<>$2`, r.id, t); e == nil && tag.RowsAffected() > 0 {
					fixedZhh++
				}
			}
		}
		if (fixedZh+fixedZhh)%50 == 0 && (fixedZh+fixedZhh) > 0 {
			log.Printf("  진행: zh 교정 %d, zh_hant 교정 %d", fixedZh, fixedZhh)
		}
	}
	log.Printf("kdb-app zh-normalize 완료: zh 교정 %d, zh_hant 교정 %d", fixedZh, fixedZhh)
}

// runCorrections — 외부 소비자 정정 신고 심사 큐 CLI.
//
//	list                 — 대기 큐 출력
//	approve <id>         — suggested 적용(source=operator, 원값 스냅샷으로 revert 가능)
//	reject  <id> [사유]  — 거부
func runCorrections(ctx context.Context, pool *pgxpool.Pool, args []string) {
	op := "list"
	if len(args) > 0 {
		op = args[0]
	}
	switch op {
	case "list":
		n, _ := corrections.CountPending(ctx, pool)
		items, err := corrections.ListPending(ctx, pool, 100)
		if err != nil {
			log.Fatalf("corrections list: %v", err)
		}
		log.Printf("kdb-app corrections: pending=%d", n)
		for _, p := range items {
			// KDB 가 codex 로 검증한 수정안이 있으면 그것이 approve 시 적용된다(★).
			apply := p.Suggested
			tag := ""
			if p.Proposed != "" {
				apply = p.Proposed
				tag = fmt.Sprintf("  ★KDB검증수정안=%q(승인 시 이 값 적용)", p.Proposed)
			}
			log.Printf("  #%d  ko=%q locale=%s  현재=%q 클라제안=%q → 적용=%q%s  근거=%s  신고자=%s  %s",
				p.ID, p.Ko, p.Locale, p.Returned, p.Suggested, apply, tag, p.EvidenceURL, p.Reporter, p.Reason)
		}
		if n > 0 {
			log.Printf("승인: kdb-app corrections approve <id> / 거부: kdb-app corrections reject <id> [사유]")
		}
	case "approve":
		if len(args) < 2 {
			log.Fatalf("usage: kdb-app corrections approve <id>")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			log.Fatalf("bad id %q", args[1])
		}
		if err := corrections.Approve(ctx, pool, id, "cli"); err != nil {
			log.Fatalf("approve #%d: %v", id, err)
		}
		log.Printf("kdb-app corrections: #%d 승인·적용 완료", id)
	case "reject":
		if len(args) < 2 {
			log.Fatalf("usage: kdb-app corrections reject <id> [사유]")
		}
		id, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			log.Fatalf("bad id %q", args[1])
		}
		why := strings.Join(args[2:], " ")
		if err := corrections.Reject(ctx, pool, id, "cli", why); err != nil {
			log.Fatalf("reject #%d: %v", id, err)
		}
		log.Printf("kdb-app corrections: #%d 거부", id)
	default:
		log.Fatalf("unknown: corrections %s (list|approve|reject)", op)
	}
}

// runDataQA — person/group 로마자 locale 오염을 gpt-5.5 로 배치 검수하고, --apply 시
// 오염 locale 을 감사로그 남긴 뒤 비운다(kwave_kdb_dataqa_log 로 복구 가능). codex 는
// 직렬화돼 있어 배치당 ~1분.
func runDataQA(ctx context.Context, pool *pgxpool.Pool, apply bool) {
	total, err := dataqa.CountSuspects(ctx, pool)
	if err != nil {
		log.Fatalf("dataqa count: %v", err)
	}
	log.Printf("kdb-app dataqa: pending suspect person/group=%d (apply=%v)", total, apply)
	runner := codexcli.NewRunner().WithProvider(codexcli.RoleProvider("DATAQA", "codex"))
	const batch = 20
	var agg dataqa.Stats
	for ctx.Err() == nil {
		st, verds, err := dataqa.RunBatch(ctx, pool, runner, dataqa.Schema, batch, apply)
		if err != nil {
			log.Printf("  batch err: %v", err)
			break
		}
		if st.Reviewed == 0 {
			break // pending 소진
		}
		for _, v := range verds {
			if v.Verdict == "contaminated" {
				log.Printf("  [contaminated] %s wrong=%v — %s", v.ID, v.WrongFields, v.Reason)
			} else if v.Verdict == "duplicate" {
				log.Printf("  [dup] %s — %s", v.ID, v.Reason)
			}
		}
		agg.Reviewed += st.Reviewed
		agg.OK += st.OK
		agg.Contaminated += st.Contaminated
		agg.Duplicate += st.Duplicate
		agg.Uncertain += st.Uncertain
		agg.ClearedFields += st.ClearedFields
		agg.FlaggedDup += st.FlaggedDup
		log.Printf("  progress reviewed=%d (ok=%d cont=%d dup=%d unc=%d)", agg.Reviewed, agg.OK, agg.Contaminated, agg.Duplicate, agg.Uncertain)
	}
	log.Printf("kdb-app dataqa done: reviewed=%d ok=%d contaminated=%d duplicate=%d uncertain=%d cleared_fields=%d flagged_dup=%d (apply=%v)",
		agg.Reviewed, agg.OK, agg.Contaminated, agg.Duplicate, agg.Uncertain, agg.ClearedFields, agg.FlaggedDup, apply)
	if !apply && agg.Contaminated > 0 {
		log.Printf("kdb-app dataqa: dry-run — --apply 로 %d 건 오염 locale 정리(복구는 kwave_kdb_dataqa_log)", agg.Contaminated)
	}
}

// buildAutopilotRunner returns the per-cycle autopilot function. When
// KDB_HERMES_ENABLED=1 it wraps the 8 sweep steps as audited agents under the
// Hermes supervisor (cmd-level wiring; no behaviour change to the steps).
// Otherwise it returns the plain auto.Run, preserving current behaviour.
// autoEnrichAfterClassify — Phase 4 유입 제어. 대량 분류 drain(drain-candidates/
// bucket/persons/resolve-unknowns)이 새로 active 시킨 entity 가 빈 locale 인 채로
// enrich backlog 스파이크를 만들지 않게, 분류 직후 Enricher 수렴 패스를 이어 돈다.
// DrainConcurrent 는 source-exhausted 필드를 건너뛰므로 이미 채워졌거나 채울 수
// 없는 기존 건은 재작업하지 않고 사실상 신규분만 처리한다(수렴 상태에선 짧게 끝남).
// KDB_AUTO_ENRICH_AFTER_DRAIN=0 로 끌 수 있다(대량 적체 시 분류만 빠르게 돌릴 때).
func autoEnrichAfterClassify(ctx context.Context, pool *pgxpool.Pool, workers int) {
	if os.Getenv("KDB_AUTO_ENRICH_AFTER_DRAIN") == "0" {
		log.Printf("kdb-app: 분류 후 자동 enrich 비활성(KDB_AUTO_ENRICH_AFTER_DRAIN=0)")
		return
	}
	log.Printf("kdb-app: 분류 후 자동 enrich 시작 (유입 제어, workers=%d)", workers)
	enricher.New(codexcli.NewRunner()).DrainConcurrent(ctx, pool, workers)
	log.Printf("kdb-app: 분류 후 자동 enrich 완료")
}

// runAutonomousLocalFill — flag 게이트(KDB_LOCALFILL_ENABLED) 빈 locale 현지표기 검색보강.
// 매 autopilot cycle(30m) 소량(KDB_LOCALFILL_BATCH, 기본 10)만. throttle 은 기본(2.5s)
// 유지 — SearXNG 상위엔진 rate-limit 회피(낮추지 말 것). 7일 쿨다운이 동일 엔티티 재검색을
// 막아 신규 엔티티 위주로 처리. Hermes run row(LocalFill)로 감독. 강증거만 local-usage 승급.
// KDB_LOCALFILL_REGROUND=1 이면 빈칸뿐 아니라 codex-fallback(LLM 합성) locale 도 재그라운딩
// 대상에 포함(QID 없는 FillVerifier 사각지대 우선, 강증거만 교체·revert 보존).
func runAutonomousLocalFill(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_LOCALFILL_ENABLED") != "1" {
		return
	}
	batch := 10
	if v := os.Getenv("KDB_LOCALFILL_BATCH"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			batch = n
		}
	}
	reground := os.Getenv("KDB_LOCALFILL_REGROUND") == "1"
	// perEntity: 한 엔티티 방문당 처리할 locale 수. 오너 방침(2026-06-23 부하최소·천천히):
	// reground 기본 3(엔티티당 적당히 진행, cycle당 검색 burst 작게). 빈칸채움은 2.
	// 빠르게 몰고 싶으면 KDB_LOCALFILL_PER_ENTITY 로 상향(예: 8=전 locale 한 방문 완결).
	perEntity := 2
	if reground {
		perEntity = 3
	}
	if v := os.Getenv("KDB_LOCALFILL_PER_ENTITY"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			perEntity = n
		}
	}
	start := time.Now()
	n, selected, err := kdb.LocalFillRunWithStats(ctx, pool, batch, perEntity, false, reground)
	st := "ok"
	if err != nil {
		st = "incident"
		log.Printf("kdb-app: localfill(auto) err: %v", err)
	}
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "LocalFill", Status: st, ItemsIn: selected, ItemsOut: n, SelfCheckOK: err == nil,
		StartedAt: start, Detail: "on-demand 빈 locale 현지표기 검색보강(websearch+gemma 다회투표)",
	})
}

// runAutonomousOTT — flag 게이트(KDB_OTT_ENABLED) 넷플릭스 지역페이지 현지제목 그라운딩.
// 매 autopilot cycle 소량(KDB_OTT_BATCH, 기본 3)만 드레인한다. ★IP 차단 방지(오너 절대방침
// "벌크 금지"): DrainNetflixWorks 내장 10초 pacing(KDB_OTT_MIN_INTERVAL_MS)이 버스트를
// 구조적으로 막으므로 배치는 작게 유지(cycle <1800s 보존: 3건 ≈ 최대 ~150s). 7일 쿨다운
// (enrich_attempts.field='ottfill')이 동일 작품 재조회를 막아 미처리 작품 위주로 점진 처리.
// 영어leak 는 ott.go 의 non-ASCII 가드가 봉인. Hermes run row(OTT)로 감독.
func runAutonomousOTT(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_OTT_ENABLED") != "1" {
		return
	}
	batch := 3
	if v := os.Getenv("KDB_OTT_BATCH"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 {
			batch = n
		}
	}
	start := time.Now()
	processed, filled := kdb.DrainOTTCascade(ctx, pool, batch, "")
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "OTT", Status: "ok", ItemsIn: processed, ItemsOut: filled, SelfCheckOK: true,
		StartedAt: start, Detail: "OTT 폴백체인(Disney→Netflix) 현지제목 그라운딩(ID앵커+gemma, 10초 pacing)",
	})
	// 2026-07-23 Phase1 잔여분: drama/show/movie candidate 넷플릭스 ID 승급(소량).
	cStart := time.Now()
	cPromoted, cChecked := kdb.DrainOTTCandidatePromotions(ctx, pool, 4)
	if cChecked > 0 {
		hermes.RecordRun(ctx, pool, hermes.RunRecord{
			Role: "OTTCandDrain", Status: "ok", ItemsIn: cChecked, ItemsOut: cPromoted, SelfCheckOK: true,
			StartedAt: cStart, Detail: "candidate -> netflix 타이틀 ID(ko-제목 앵커) 승급",
		})
	}
}

func newDiscogsClient(ctx context.Context, pool *pgxpool.Pool) *discogs.Client {
	cl := discogs.New()
	if token, _ := apikeys.Resolve(ctx, pool, "KDB_DISCOGS_TOKEN"); strings.TrimSpace(token) != "" {
		cl.Token = strings.TrimSpace(token)
	}
	return cl
}

// runAutonomousSourceExpand — 매 autopilot cycle 권위/결정적 소스 드레인을 소량씩 돌려 codex
// 꼬리를 지속 충당한다(오너 지시: "한쪽에서 발굴하며 소스 파이프라인을 계속 가동·업데이트").
// 순서: 공식/구조화 confirm(Wikidata/iTunes/Discogs) → 결정적 파생(romanize/OpenCC).
// 쿨다운이 처리분을 스킵하므로 cycle 마다 미처리분만 점진(자기수렴).
// KDB_SOURCE_EXPAND_ENABLED=0 으로 비활성화 가능(기본 ON).
func runAutonomousSourceExpand(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_SOURCE_EXPAND_ENABLED") == "0" {
		return
	}
	start := time.Now()
	llProc, llUp := enrich.New(pool).DrainLanglinkUpgrade(ctx, 30)                 // QID 사이트링크로 codex 교체
	itCf, itAn := kdb.DrainITunesSongs(ctx, pool, itunes.New(), 10)                // song_album 현지표기 confirm + 아티스트앵커
	itPr, itCk := kdb.DrainITunesSongCandidates(ctx, pool, itunes.New(), 8)        // ★Phase1: candidate 승급(KR·아티스트 스코프)
	dgCf, dgAn := kdb.DrainDiscogsSongs(ctx, pool, newDiscogsClient(ctx, pool), 8) // iTunes 폴백 confirm + release/artist 앵커
	re := kdb.DrainLatinKoToEN(ctx, pool)                                          // ko 원제가 라틴표기 → en 승계(Latin 전파의 선행조건)
	rp := kdb.DrainParenLatinToEN(ctx, pool)                                       // 한글(LATIN) 병기 → 괄호 안 표기를 en 으로(전파 앞에 와야 함)
	rc := kdb.DrainLatinKoToCJK(ctx, pool)                                         // ko 원제가 라틴표기 → zh/zh_hant/ja 원문 승계(Wikidata 실증)
	rj := kdb.MarkLatinOriginRejects(ctx, pool)                                    // ★승계 뒤 — 못 채운 건에 기각 사유를 남겨 경보가 "미판정"으로 남지 않게
	rf := kdb.DrainRomanizeLatin(ctx, pool)                                        // 전 타입 Latin codex/빈칸 → en 로마자
	rr := kdb.DrainReattributeRomanization(ctx, pool)                              // 값정답 codex → romanization 재라벨
	zw, zr := kdb.DrainZhWikiTitle(ctx, pool, false)                               // ★opencc 앞 — 보유 zh.wikipedia URL → zh, 같은 회차에 zh_hant 까지 열린다
	oc := kdb.DrainZhVariants(ctx, pool)                                           // zh↔zh_hant 결정적 변환
	oi := kdb.DrainZhLatinIdentity(ctx, pool)                                      // zh 에 한자 0자면 변환이 아니라 항등 승계
	or := kdb.DrainZhLatinRelabel(ctx, pool)                                       // 값은 같은데 라벨만 codex 인 zh_hant 교정(값불변)
	kf := 0
	if os.Getenv("KDB_KANA_FILL_ENABLED") != "0" {
		kf = kdb.DrainKanaFillPersons(ctx, pool, 100) // person/group/char ja 빈칸 → 가타카나 규칙(폴백티어)
	}
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "SourceExpand", Status: "ok",
		ItemsOut: re + rp + rc + rf + rr + zw + oc + oi + kf + llUp + itCf + dgCf + itPr, SelfCheckOK: true, StartedAt: start,
		Detail: fmt.Sprintf("romanize ko→en=%d paren→en=%d ko→cjk=%d(기각판정 %d) fill=%d relabel=%d · zhwiki=%d(기각 %d) · opencc=%d(라틴항등 %d 재라벨 %d) · kana=%d · langlink up=%d/%d · itunes confirm=%d anchor=%d cand-promote=%d/%d · discogs confirm=%d anchor=%d",
			re, rp, rc, rj, rf, rr, zw, zr, oc, oi, or, kf, llUp, llProc, itCf, itAn, itPr, itCk, dgCf, dgAn),
	})
}

// runAutonomousAdjudicate — 2단계 판정 최종단계 연속운영(기본 OFF). Gemma 거름망이 플래그한
// 의심군을 claude(Sonnet)로 매 cycle 소량 최종판정. KDB_CLAUDE_ADJUDICATE_ENABLED=1 로 켜고,
// KDB_CLAUDE_ADJUDICATE_AUTOREJECT=1 이면 비-K/정크 확정분 실제 reject(아니면 권고만).
// 파괴적이라 기본 OFF — 운영자가 dry(CLI)로 품질 확인 후 활성화.
func runAutonomousAdjudicate(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_CLAUDE_ADJUDICATE_ENABLED") != "1" {
		return
	}
	autoReject := os.Getenv("KDB_CLAUDE_ADJUDICATE_AUTOREJECT") == "1"
	start := time.Now()
	j, rj, rs := kdb.DrainClaudeAdjudicate(ctx, pool, claudejudge.New(), 10, autoReject)
	if j > 0 {
		hermes.RecordRun(ctx, pool, hermes.RunRecord{
			Role: "ClaudeAdjudicate", Status: "ok", ItemsIn: j, ItemsOut: rj + rs, SelfCheckOK: true,
			StartedAt: start, Detail: fmt.Sprintf("claude(Sonnet) 최종판정 judged=%d reject=%d rescue=%d (autoReject=%v)", j, rj, rs, autoReject),
		})
	}
}

// laneRunner — 이름 붙은 single-flight 실행기 (sweep.go 의 running 가드 패턴).
// KDB_AUTOPILOT_SPLIT=1 의 5-lane 분리에서 각 lane 이 자기 가드만 잡아, 긴 tail
// (LocalFill ~50분·Finalizer ~30분)이 core 승격 경로의 30분 tick 을 삼키지 않는다.
type laneRunner struct {
	name    string
	running atomic.Bool
	fn      func(context.Context)
}

func (l *laneRunner) run(ctx context.Context) {
	if !l.running.CompareAndSwap(false, true) {
		log.Printf("kdb-app: autopilot %s lane still running — skip tick", l.name)
		return
	}
	defer l.running.Store(false)
	l.fn(ctx)
}

func buildAutopilotRunner(pool *pgxpool.Pool, auto *autopilot.Sweeper) func(context.Context) {
	plain := func(ctx context.Context) { auto.Run(ctx) }
	runner := plain

	if os.Getenv("KDB_HERMES_ENABLED") == "1" {
		registry := agents.NewRegistry()
		if err := auto.RegisterSteps(registry); err != nil {
			log.Printf("kdb-app: hermes register steps: %v — falling back to plain autopilot", err)
		} else {
			supervisor := hermes.New(pool)
			// Reuse the existing circuit breaker (internal/kdb) via hooks to avoid
			// an import cycle.
			supervisor.Hooks = hermes.Hooks{
				BreakerIsOpen:       kdb.BreakerIsOpen,
				BreakerRecordResult: kdb.BreakerRecordResult,
			}
			log.Printf("kdb-app: Hermes supervisor enabled (%d steps)", registry.Len())
			// 5-lane 분리 (opt-in): 실측상 블로킹 사이클 ~123분의 63%가 tail(LocalFill 39%
			// + Finalizer 24%)인데 core 와 같은 가드에 직렬로 묶여 tick skip 76회/48h.
			// lane 별 독립 가드로 분리하면 core(승격 경로)가 tail 완료를 기다리지 않는다.
			// row 경합 안전: Enricher empty-only 가드·LocalFill applyQAFills 2단계 가드·
			// scope/contam notes 자기가드 (최악=중복 작업 비용, 오염 없음).
			if os.Getenv("KDB_AUTOPILOT_SPLIT") == "1" {
				lanes := []*laneRunner{
					{name: "core", fn: func(ctx context.Context) { supervisor.SuperviseCycle(ctx, registry) }},
					{name: "finalizer", fn: func(ctx context.Context) {
						rep := auto.RunTail(ctx)
						hermes.RecordRun(ctx, pool, hermes.RunRecord{
							Role: "Finalizer", Status: "ok", ItemsOut: rep.TailActions(),
							SelfCheckOK: true, StartedAt: rep.StartedAt,
							Detail: "DrainOnDemand·FillPersonDetails·DedupEn·SweepContam·ScopeReview·clearDisambig",
						})
					}},
					{name: "localfill", fn: func(ctx context.Context) { runAutonomousLocalFill(ctx, pool) }},
					{name: "sourceexpand", fn: func(ctx context.Context) {
						runAutonomousOTT(ctx, pool)
						runAutonomousSourceExpand(ctx, pool)
					}},
					{name: "adjudicate", fn: func(ctx context.Context) { runAutonomousAdjudicate(ctx, pool) }},
				}
				log.Printf("kdb-app: autopilot 5-lane split enabled (core/finalizer/localfill/sourceexpand/adjudicate)")
				return func(ctx context.Context) {
					for _, l := range lanes {
						go l.run(ctx)
					}
				}
			}
			runner = func(ctx context.Context) {
				supervisor.SuperviseCycle(ctx, registry)
				// Hermes 는 등록된 role-agent 만 돌린다. auto.Run 의 꼬리(미등록 step +
				// finalizer: on-demand drain·person 상세·WF-2 가시화·clearResolvedDisambig)
				// 를 cycle 종료 후 보충 실행해 plain 모드와 동작을 일치시킨다(누락 자율운영 복구).
				// #6 in-place 감독: SuperviseCycle 밖에서 도는 finalizer 도 run row 로 노출.
				rep := auto.RunTail(ctx)
				hermes.RecordRun(ctx, pool, hermes.RunRecord{
					Role: "Finalizer", Status: "ok", ItemsOut: rep.TailActions(),
					SelfCheckOK: true, StartedAt: rep.StartedAt,
					Detail: "DrainOnDemand·FillPersonDetails·DedupEn·SweepContam·ScopeReview·clearDisambig",
				})
				runAutonomousLocalFill(ctx, pool)    // flag 게이트 빈 locale 검색보강(소량·보수 throttle)
				runAutonomousOTT(ctx, pool)          // flag 게이트 OTT 폴백체인(Disney→Netflix) 현지제목 그라운딩(소량·10초 pacing)
				runAutonomousSourceExpand(ctx, pool) // 권위/결정적 소스 지속 충당(romanize·opencc·langlink·itunes, 소량·쿨다운)
				runAutonomousAdjudicate(ctx, pool)   // 2단계 판정 최종단계: Gemma 플래그 의심군 claude(Sonnet) 판정(기본 OFF)
			}
		}
	}

	// Single-flight 가드 (양쪽 모드 공통). cmd 가 30분 ticker 로 `go runAutopilot()`
	// 을 띄우므로, 한 cycle 이 30분을 넘기면 다음 ticker 가 같은 Sweeper/DB 위에서
	// 두 번째 cycle 을 동시에 돌려 Codex 비용 2배 + 같은 row 경합이 난다. plain 경로
	// (auto.Run)는 자체 guard 가 있으나 Hermes 경로(SuperviseCycle)는 없어, 여기서
	// 공통으로 막는다 (Hermes 의 guard 부재 = 리뷰 H5).
	var running atomic.Bool
	return func(ctx context.Context) {
		if !running.CompareAndSwap(false, true) {
			log.Printf("kdb-app: autopilot cycle still running — skip overlapping tick")
			return
		}
		defer running.Store(false)
		runner(ctx)
	}
}

func runFast(ctx context.Context, pool *pgxpool.Pool) {
	kdb.BridgeHealthCheck(ctx, pool) // Codex CLI 감독
	kdb.GemmaHealthCheck(ctx, pool)  // Gemma 게이트웨이(주력 워크호스) 감독 — 다운 시 Codex 폴백
	// #6 in-place 감독: 추출(fast 30s)은 autopilot cycle 밖 — SweeperTick 결과를 hermes
	// run row 로 기록(작업 있을 때만, idle tick 노이즈 방지). registry 미편입(cadence 보존).
	go func() {
		st := kdb.SweeperTick(ctx, pool)
		if st.Processed == 0 {
			return
		}
		status, sev := "ok", ""
		if st.Failed > 0 && st.Succeeded == 0 {
			status, sev = "incident", "warning"
		}
		hermes.RecordRun(ctx, pool, hermes.RunRecord{
			Role: "Extractor", Status: status, Severity: sev,
			ItemsIn: st.Processed, ItemsOut: st.Succeeded, ItemsDropped: st.Failed,
			SelfCheckOK: st.Failed == 0, StartedAt: st.StartedAt, Detail: "fast 30s raw→spellings 추출",
		})
	}()
}

func runPoll(ctx context.Context, pool *pgxpool.Pool) {
	kdb.PollerTick(ctx, pool)
}

func envDurationSeconds(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return fallback
	}
	return time.Duration(n) * time.Second
}

func envCSV(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
