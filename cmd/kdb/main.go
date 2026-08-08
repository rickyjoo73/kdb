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
	"github.com/rickyjoo73/kdb/internal/kdb/hermes"
	"github.com/rickyjoo73/kdb/internal/kdb/itunes"
	"github.com/rickyjoo73/kdb/internal/kdb/kmdb"
	"github.com/rickyjoo73/kdb/internal/kdb/kofic"
	"github.com/rickyjoo73/kdb/internal/kdb/kopis"
	"github.com/rickyjoo73/kdb/internal/kdb/tmdb"
	"github.com/rickyjoo73/kdb/internal/kdb/musicbrainz"
	"github.com/rickyjoo73/kdb/internal/kdb/naver"
	"github.com/rickyjoo73/kdb/internal/kdb/research"
	"github.com/rickyjoo73/kdb/internal/kdb/verify"
	"github.com/rickyjoo73/kdb/internal/kdb/wikidata"
	"github.com/rickyjoo73/kdb/internal/kdb/zhvariant"
	"github.com/rickyjoo73/kdb/internal/kdbadmin"
	"github.com/rickyjoo73/kdb/internal/kdbapi"
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
	if len(os.Args) > 1 && os.Args[1] == "opencc-convert" {
		log.Printf("kdb-app: opencc-convert start (zh↔zh_hant)")
		f := kdb.DrainZhVariants(ctx, pool)
		li := kdb.DrainZhLatinIdentity(ctx, pool) // zh 에 한자가 없으면 변환이 아니라 항등
		log.Printf("kdb-app: opencc-convert done (filled=%d latin-identity=%d cells)", f, li)
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
	autoVerifyTicker := time.NewTicker(autoVerifyInterval)
	defer autoVerifyTicker.Stop()
	candEvidenceTicker := time.NewTicker(candEvidenceInterval)
	defer candEvidenceTicker.Stop()
	kopisTicker := time.NewTicker(kopisInterval)
	defer kopisTicker.Stop()
	revertTermTicker := time.NewTicker(revertTermInterval)
	defer revertTermTicker.Stop()
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
	var lastTypeRetraceDay string
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
			if os.Getenv("KDB_TYPE_RETRACE_DAILY") != "0" {
				now := time.Now().In(time.FixedZone("KST", 9*3600))
				day := now.Format("2006-01-02")
				if now.Hour() >= 5 && now.Hour() < 7 && lastTypeRetraceDay != day {
					lastTypeRetraceDay = day
					go func() {
						fixed, confirmed, flagged, proc, err := verify.TypeRetracePass(ctx, pool, 150)
						if err != nil {
							log.Printf("kdb.type-retrace(daily): %v", err)
							return
						}
						log.Printf("kdb.type-retrace(daily): fixed=%d confirmed=%d contam?=%d /%d", fixed, confirmed, flagged, proc)
						// 요청대기 candidate 뉴스근거 승급 — 같은 쿼터리셋 창 활용(엔티티당 1콜).
						// 미해결 보류의 "candidate 있음, 승급만 남음" 버킷을 매일 소진.
						if os.Getenv("KDB_CAND_EVIDENCE_DAILY") != "0" {
							up, cflag, cproc, cerr := verify.CandidateEvidencePass(ctx, pool, 100)
							if cerr != nil {
								log.Printf("kdb.cand-evidence(daily): %v", cerr)
								return
							}
							log.Printf("kdb.cand-evidence(daily): promoted=%d contam?=%d /%d", up, cflag, cproc)
						}
						// active 전수 오염 감사(오너 지시 07-17) — 무ref ~2.4k 를 매일 150씩
						// 1회전(30d 커서 재감사). KDB_ACTIVE_AUDIT_DAILY=0 끔.
						if os.Getenv("KDB_ACTIVE_AUDIT_DAILY") != "0" {
							arej, arev, aup, aproc, aerr := verify.ActiveAuditPass(ctx, pool, 150, kdb.TriageKeywordConfirmed)
							if aerr != nil {
								log.Printf("kdb.active-audit(daily): %v", aerr)
								return
							}
							log.Printf("kdb.active-audit(daily): rejected=%d review=%d upgraded=%d /%d", arej, arev, aup, aproc)
						}
					}()
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
	if up > 0 || flagged > 0 {
		log.Printf("kdb.cand-evidence(tick): promoted=%d contam?=%d /%d", up, flagged, proc)
	}
}

// researchKick — 소비자가 새 키워드를 던지면(prepare/lookup miss → EnqueueResearch) API
// 핸들러가 이 채널로 워커를 즉시 깨운다(온디맨드 빠른 채움 — 주기 tick ≤15s 대기 제거).
// API 서버 함수와 runWorker 가 별개 함수라 패키지 레벨로 공유. buffered(1)=신호 coalesce.
var researchKick = make(chan struct{}, 1)

// intakeReviewKick — 근거 부족(review)으로 보류된 신규/재요청 키워드의 row id.
// 자동 검증기 fresh 레인이 즉시 소비(오너 07-13: "제대로 된 키워드는 유입 즉시 심사").
var intakeReviewKick = make(chan string, 256)

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
	oc := kdb.DrainZhVariants(ctx, pool)                                           // zh↔zh_hant 결정적 변환
	oi := kdb.DrainZhLatinIdentity(ctx, pool)                                      // zh 에 한자 0자면 변환이 아니라 항등 승계
	kf := 0
	if os.Getenv("KDB_KANA_FILL_ENABLED") != "0" {
		kf = kdb.DrainKanaFillPersons(ctx, pool, 100) // person/group/char ja 빈칸 → 가타카나 규칙(폴백티어)
	}
	hermes.RecordRun(ctx, pool, hermes.RunRecord{
		Role: "SourceExpand", Status: "ok",
		ItemsOut: re + rp + rc + rf + rr + oc + oi + kf + llUp + itCf + dgCf + itPr, SelfCheckOK: true, StartedAt: start,
		Detail: fmt.Sprintf("romanize ko→en=%d paren→en=%d ko→cjk=%d(기각판정 %d) fill=%d relabel=%d · opencc=%d(라틴항등 %d) · kana=%d · langlink up=%d/%d · itunes confirm=%d anchor=%d cand-promote=%d/%d · discogs confirm=%d anchor=%d",
			re, rp, rc, rj, rf, rr, oc, oi, kf, llUp, llProc, itCf, itAn, itPr, itCk, dgCf, dgAn),
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
