# KDB handoff — 16차 (2026-06-22): LLM 전체 워크플로우 단계화 + 자가복구 + FillVerifier

오너 요구: `/admin/hermes` 가 "알아볼 수 없다". "도메인"=LLM 역할이 아니라 **실제 전체 워크플로우**(수집·입력·검수·매칭·제공 + API 참조/요청/수정 + 품질작업). 목표 = 워크플로우를 단계로 분해 → 각 단계에 담당 에이전트 배치(판단 단계만 LLM) → 감독(장애 시 gemma 복구) → codex 사용 최소·gemma 기본(느리니 timeout 충분). **설계 SSOT = `.claude/plans/partitioned-forging-patterson.md`** (승인됨).

## §0 — 30초 상태 체크 (새 세션 첫 명령)
```sh
# 라이브 헬스 + 자율운영 flag
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
docker exec kdb-app sh -c 'echo HERMES=$KDB_HERMES_ENABLED FILLVERIFY=$KDB_FILLVERIFY_ENABLED GEMMA_TIMEOUT=$KDB_GEMMA_TIMEOUT_MS'
# FillVerifier canary (status=ok·dropped=0·out>0 이어야 정상)
docker exec kdb-db psql -U kdb -d kdb -P pager=off -c "SELECT status,items_in,items_out,items_dropped,self_check_ok,to_char(created_at,'MM-DD HH24:MI') t FROM kwave_kdb_hermes_runs WHERE role='FillVerifier' ORDER BY created_at DESC LIMIT 8;"
# codex-fallback 추이 (12,496 enable 시점 → 하락해야 정상)
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s='codex-fallback';"
```
배포 = `docker restart kdb-app`(코드만, 마운트 소스 자동 재빌드) / **env(.env) 변경 시엔 `docker compose -f docker-compose.kdb.yml up -d kdb-app`(recreate)** — `restart`는 env_file 재주입 안 함(godotenv.Load 가 기존 env 안 덮음). 컨테이너 빌드/테스트: `docker exec -w /app kdb-app sh -c 'GOFLAGS=-buildvcs=false go build ./... / go test ...'`. **테스트는 `env -u KDB_LLM_PROVIDER -u KDB_GEMMA_BASE_URL -u KDB_GEMMA_API_KEY` 로 돌릴 것**(컨테이너 프로덕션 env가 codexcli 테스트를 gemma로 라우팅시켜 깨뜨림).

## §1 — 이번 세션 완료 (전부 라이브)

### (1) `/admin/hermes` 전체 워크플로우 맵 — Phase A (표시 전용, 동작 불변)
- 평면 LLM-역할 테이블 → **2트랙 × 단계 표**: 트랙 A 요청처리(동기 A1~A14: 인증→입력게이트→[키워드교정]→매칭(lookup/match/alias)→[문맥동명이인]→[miss추출]→제공 + 수정수신/검증), 트랙 B 자율품질(비동기 B1~B15: 수집→cheap-gate→추출→관찰→게이트→분류→인물→동명이인→dataqa→QA교환→cascade L2/L3/L4→fallback검증→QualityReview→finalizer).
- 각 단계 행 = 담당 에이전트 · LLM 배지(provider/effort 또는 "LLM 미투입") · Hermes 감독여부 배지 · 라이브 지표 · 갭 경고. 상단 자율성 heartbeat 바. 기존 LLM-역할 카드는 `<details>`로 흡수.
- 코드: `internal/kdbadmin/handlers_hermes_workflow.go`(신규: workflowSpecs/buildWorkflowTracks/collectWorkflowStats/pipelineHeartbeats/workflowMetric) + `handlers_hermes.go`(handleHermes 에 Tracks/Heartbeats 추가) + `templates/kdb_hermes.html`(workflowStepRow partial). provider/effort 는 기존 `hermesResolveProvider/Effort`로 라이브 해소.
- **실측 LLM/감독 지도(라이브)**: 요청트랙(매칭/제공) 거의 전부 LLM 0·감독 0(순수 lexical). 자율트랙 LLM: 추출 gemma/low·게이트 gemma·분류 gemma·인물 gemma·동명이인 codex·dataqa codex·보강L4 gemma·QA교환 gemma(서버22)·수정검증 codex. Hermes 감독 = B5/B6/B7/B8/B12/B14 + FillVerifier. **codex-fallback 39%·평균 언어확보율 95%·needs_disambig 61·corrections 미해결 4**.

### (2) gemma 자가복구 양방향 메시 + timeout (오너 방침: codex 최소·gemma 기본·gemma 느리니 시간 충분)
- `codexcli.CodexDown` 훅 신규(= `kdb.BreakerIsOpen`, cmd/kdb·kdb-worker 와이어) → **양방향**: gemma↓→codex(기존 GemmaDown), **codex breaker open→gemma 인계(신규, gemma 가용 시)**. codex 장애 시 codex-role(동명이인/dataqa/정정검증) 자동 gemma 인계(degraded-but-done, 복구 시 자동 환원). `RoleProvider` 에 구현. 테스트 `TestRoleProviderSelfHealMesh`.
- `.env KDB_GEMMA_TIMEOUT_MS 120000→240000`(gemma.Complete 자체 timeout, codexcli 90s 무관 — 느린 gemma 헛failure→codex폴백 방지 = codex 절감).
- codex 라우팅은 이미 최소(동명이인·dataqa·정정검증만), 나머지 gemma. 신규 LLM도 gemma 기본.

### (3) B13 FillVerifier — codex-fallback 39% 품질 회수 (Phase C, flag+canary, **ON**)
- 신규 `internal/kdb/agents/fillverifier/`(agent.go + criterion.go + fillverifier_test.go). codex-fallback(검증 안 된 LLM값) 중 **Wikidata QID 보유분(1,524/2,908)**을 `wikidata.Fetch(qid)` 공식 라벨로 재검증:
  - 값 일치 → **source 승급**(codex-fallback→wikidata-label, 값 불변). LLM 불필요.
  - 값 다름 → **gemma 판정**(use_source/keep/uncertain, 불확실·에러는 무조건 keep) → 교체 + 감사로그(dataqa_log verdict='fillverify-replace', revert 가능).
- **안전 가드: 모든 UPDATE에 `WHERE ..._source='codex-fallback'`** → operator/api/media/wikidata 값 절대 안 건드림. enrich_attempts field='fillverify' 7d 쿨다운 수렴.
- 등록: `autopilot/agents.go RegisterRoleAgents` 에 `KDB_FILLVERIFY_ENABLED=1` 게이트(.env 적용). Hermes 감독.
- **canary 통과 + 버그 1건 발견·수정**: charset 가드(IsValidSpellingForLocale)를 normEq 앞에 둬서 Wikidata가 라틴으로 라벨한 그룹(zh="ARTMS"/"f(x)") 전부 스킵→out=0이었음. **수정: charset 가드는 값을 새로 쓰는 replace 케이스에만. 값 불변 upgrade(source만)엔 미적용**(이미 DB 값). 수정 후 라이브 cycle: status=ok·in=20·out=10·dropped=0. codex-fallback 12,496→12,466 하락 시작(20/cycle, ~10-17 승급).
- 라이브 검증 팁: throwaway `go test`로 프로덕션 경로(Select→Run) 실측 후 삭제(실행 중 바이너리 ≠ 새 컴파일 코드 — 재기동 필요).

## §2 — 라이브 배포 상태
HEAD 빌드 = 컨테이너 `/tmp/kdb-app`(restart 시 자동 재빌드). flag: `KDB_HERMES_ENABLED=1`, `KDB_FILLVERIFY_ENABLED=1`(둘 다 .env), `KDB_GEMMA_TIMEOUT_MS=240000`. api/admin 200.

## §3 — 남은 작업 (우선순위)
1. **FillVerifier canary 장기 관찰** (오너 요청, 진행 중): 몇 cycle 더 codex-fallback% 안정 하락·leak0·incident0 확인. (이번 세션 백그라운드 canary 태스크는 세션 종료 시 사라짐 → §0 명령으로 수동 재확인.)
2. **A8 MatchMissExtractor** (Phase C, 미구현): `/v1/match` miss(internal/kdbapi/api.go matchEntities, 현재 빈 응답)에서 source_text를 LLM(gemma) 추출→재매칭+research큐 적재(lookup/prepare의 enqueueDiscovery와 동등화). 비동기(핫패스 latency 보호). flag `KDB_MATCH_LLM_EXTRACT`. API handler엔 LLM 추출기 없음 → `kdb.NewCodexExtractor()`(provider gemma로) 와이어 필요(handler struct에 추가, NewRouterWithOptions에서 주입).
3. **#7 페이지 자가복구 표시**: 워크플로우 맵에 codex/gemma 폴백 활성상태 + heartbeat-stall 경고 노출.
4. **#6 감독 편입(in-place)**: 추출(fast 30s)·dataqa(20m)·정정검증(async)·finalizer 6종을 **제자리** 감독(run row/incident 기록)으로 편입. ⚠️ registry 단순 편입 금지(autopilot 30m cycle 밖이라 cadence 깨짐).
5. **보류**: A3 KeywordFixer·A7 ContextDisambig(핫패스 요청마다 LLM 비용 → 캐시 전제로 추후).

## §4 — 롤백/안전
- FillVerifier 이상 시: `.env KDB_FILLVERIFY_ENABLED=0` → `compose up -d kdb-app`. 승급은 값 불변(비파괴), 교체는 `kwave_kdb_dataqa_log verdict='fillverify-replace'` 로 revert 가능.
- 자가복구 메시는 장애 시에만 발동(평소 inert), codex 복구 시 자동 환원.
- Phase A 페이지는 SELECT+렌더만(autopilot 동작 불변).

## §5 — git / 커밋
- 베이스 HEAD `2295ac7`(main). 이번 세션 변경 = `cmd/kdb/main.go`·`cmd/kdb-worker/main.go`·`internal/kdb/agents/agent.go`·`internal/kdb/autopilot/agents.go`·`internal/kdb/codexcli/codexcli.go`·`codexcli/effort_test.go`·`internal/kdbadmin/{handlers_hermes.go,handlers_hermes_test.go,handlers_hermes_workflow.go(신규),templates/kdb_hermes.html}`·`internal/kdb/agents/fillverifier/`(신규) + `.env`(timeout·flag, gitignore일 수 있음).
- (참고) `internal/kdb/source_priority.go` 는 이번 세션 전부터 M 상태였음(이전 작업 미커밋).
- push 방법: gh CLI `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`. **오너 승인 후에만 push.** (코드는 이미 라이브라 push 는 기록 동기화.)

## §6 — 설계 문서
`.claude/plans/partitioned-forging-patterson.md`(2트랙 × 단계 × 에이전트 배치 전체 설계, Phase A~D). 토픽 메모리 `[[reference-kdb-hermes-agents]]` 에도 요약 반영됨.
