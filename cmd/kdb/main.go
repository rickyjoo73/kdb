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
	"github.com/rickyjoo73/kdb/internal/kdb/agents/enricher"
	"github.com/rickyjoo73/kdb/internal/kdb/agents/fillverifier"
	"github.com/rickyjoo73/kdb/internal/kdb/aijudge"
	"github.com/rickyjoo73/kdb/internal/kdb/apikeys"
	"github.com/rickyjoo73/kdb/internal/kdb/autopilot"
	"github.com/rickyjoo73/kdb/internal/kdb/claudejudge"
	"github.com/rickyjoo73/kdb/internal/kdb/codexcli"
	"github.com/rickyjoo73/kdb/internal/kdb/corrections"
	"github.com/rickyjoo73/kdb/internal/kdb/dataqa"
	"github.com/rickyjoo73/kdb/internal/kdb/discogs"
	"github.com/rickyjoo73/kdb/internal/kdb/enrich"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/hermes"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
	"github.com/rickyjoo73/kdb/internal/kdb/research"
	"github.com/rickyjoo73/kdb/internal/kdb/zhvariant"
	"github.com/rickyjoo73/kdb/internal/kdbadmin"
	"github.com/rickyjoo73/kdb/internal/kdbapi"
)

func main() {
	_ = godotenv.Load()
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds | log.LUTC | log.Lshortfile)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.New(ctx)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

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
	// `kdb-app romanize-persons` — person/group 의 빈칸/codex vi/es/id/pt_br 를 검증된
	// canonical_en(로마자)로 재속성. 외부호출 0·결정적·벌크안전. source='romanization'.
	if len(os.Args) > 1 && os.Args[1] == "romanize-persons" {
		log.Printf("kdb-app: romanize-persons start (Latin locale 재속성)")
		f := kdb.DrainRomanizePersons(ctx, pool)
		r := kdb.DrainReattributeRomanization(ctx, pool)
		log.Printf("kdb-app: romanize-persons done (filled=%d relabeled=%d cells)", f, r)
		return
	}

	// ─── one-shot: opencc-convert (zh↔zh_hant 결정적 변환) ─────────
	// `kdb-app opencc-convert` — 검증된 zh↔zh_hant 를 OpenCC(s2t/t2s)로 상호 변환해 빈/codex
	// 변종을 채운다. 외부호출 0·결정적·벌크안전. source='opencc'.
	if len(os.Args) > 1 && os.Args[1] == "opencc-convert" {
		log.Printf("kdb-app: opencc-convert start (zh↔zh_hant)")
		f := kdb.DrainZhVariants(ctx, pool)
		log.Printf("kdb-app: opencc-convert done (filled=%d cells)", f)
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
		nv, err := naver.New()
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
		cf, an := kdb.DrainDiscogsSongs(ctx, pool, discogs.New(), n)
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
		for _, a := range os.Args[2:] {
			if a == "--dry" {
				dry = true
			} else if a == "--reground" {
				reground = true // 빈칸뿐 아니라 codex-fallback locale 도 재그라운딩(강증거만 교체)
			} else if v, e := strconv.Atoi(a); e == nil && v > 0 {
				n = v
			}
		}
		perEntity := 2
		if reground {
			perEntity = 3 // 부하최소 기본(상향은 KDB_LOCALFILL_PER_ENTITY)
		}
		if v := os.Getenv("KDB_LOCALFILL_PER_ENTITY"); v != "" {
			if pe, e := strconv.Atoi(v); e == nil && pe > 0 {
				perEntity = pe
			}
		}
		log.Printf("kdb-app: localfill start (n=%d perEntity=%d dry=%v reground=%v)", n, perEntity, dry, reground)
		applied, e := kdb.LocalFillRun(ctx, pool, n, perEntity, dry, reground)
		if e != nil {
			log.Printf("kdb-app: localfill err: %v", e)
		}
		log.Printf("kdb-app: localfill done (applied=%d dry=%v reground=%v)", applied, dry, reground)
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
	go runWorker(ctx, pool)

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

	// Hermes supervisor (opt-in, additive). KDB_HERMES_ENABLED=1 runs the
	// existing 8 sweep steps as audited agents under the supervisor (per-step
	// run rows in kwave_kdb_hermes_runs + item-conservation/leak detection)
	// instead of the plain auto.Run. Default (unset) keeps the running
	// autopilot behaviour exactly as before. Requires migration 0061.
	runAutopilot := buildAutopilotRunner(pool, auto)

	runFast(ctx, pool)
	runPoll(ctx, pool)
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
	dataqaTicker := time.NewTicker(dataqaInterval)
	defer dataqaTicker.Stop()

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
			// bumpable(Wikidata 소스 보유) 저신뢰 entity 일괄 confidence 승급.
			go auto.DrainQualityGuarded(ctx, 100)
		case <-dataqaTicker.C:
			if dataqaOn {
				go runDataQATick(ctx, pool)
			}
		}
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
	n, err := kdb.LocalFillRun(ctx, pool, batch, perEntity, false, reground)
	st := "ok"
	if err != nil {
		st = "incident"
		log.Printf("kdb-app: localfill(auto) err: %v", err)
	}
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "LocalFill", Status: st, ItemsOut: n, SelfCheckOK: err == nil,
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
}

// runAutonomousSourceExpand — 매 autopilot cycle 권위/결정적 소스 드레인을 소량씩 돌려 codex
// 꼬리를 지속 충당한다(오너 지시: "한쪽에서 발굴하며 소스 파이프라인을 계속 가동·업데이트").
// 전부 안전: romanize/opencc 는 외부호출 0(결정적), langlink-upgrade 는 ko-label 매칭 게이트 +
// 14d 쿨다운, itunes 는 confirm-only + 30d 쿨다운 + 2.5s pacing. 쿨다운이 처리분을 스킵하므로
// cycle 마다 미처리분만 점진(자기수렴). KDB_SOURCE_EXPAND_ENABLED=0 으로 비활성화 가능(기본 ON).
func runAutonomousSourceExpand(ctx context.Context, pool *pgxpool.Pool) {
	if os.Getenv("KDB_SOURCE_EXPAND_ENABLED") == "0" {
		return
	}
	start := time.Now()
	rf := kdb.DrainRomanizePersons(ctx, pool)                        // person/group Latin codex/빈칸 → 로마자
	rr := kdb.DrainReattributeRomanization(ctx, pool)                // 값정답 codex → romanization 재라벨
	oc := kdb.DrainZhVariants(ctx, pool)                             // zh↔zh_hant 결정적 변환
	llProc, llUp := enrich.New(pool).DrainLanglinkUpgrade(ctx, 30)   // QID 사이트링크로 codex 교체
	itCf, itAn := kdb.DrainITunesSongs(ctx, pool, itunes.New(), 10)  // song_album 현지표기 confirm + 아티스트앵커
	dgCf, dgAn := kdb.DrainDiscogsSongs(ctx, pool, discogs.New(), 8) // iTunes 폴백 confirm + release/artist 앵커
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "SourceExpand", Status: "ok",
		ItemsOut: rf + rr + oc + llUp + itCf + dgCf, SelfCheckOK: true, StartedAt: start,
		Detail: fmt.Sprintf("romanize fill=%d relabel=%d · opencc=%d · langlink up=%d/%d · itunes confirm=%d anchor=%d · discogs confirm=%d anchor=%d",
			rf, rr, oc, llUp, llProc, itCf, itAn, dgCf, dgAn),
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
