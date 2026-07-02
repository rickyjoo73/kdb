# KDB handoff — 26차 (2026-07-02): 처리량 병목 해소 (5-lane 분리 + gemma 팬아웃 + research 병렬)

25차 [[KDB.handoff-20260629-4.md]] 이어짐. 오너 판정: "백업·보안도 필요하지만 **근본 문제 = 병목** — 외부 제공용 엔티티를 빠르게 확보 못 하는 것". 실측 프로파일링 → 설계 → 구현 → 배포 → 검증 완료 세션.

## §0 — 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health   # 200
# tick 정시성 + 승격 처리량 (24h 창 — 27차 첫 확인 항목)
docker exec kdb-db psql -U kdb -d kdb -c "SELECT date_trunc('hour', ran_at) h, count(*) cycles, round(avg(duration_ms)/1000) avg_s, sum(promoted) promoted FROM kwave_kdb_autopilot_log WHERE ran_at > now()-interval '24 hours' GROUP BY 1 ORDER BY 1;"
# role별 소요 (LocalFill/Finalizer/ReviewCandidates 급감 유지 확인)
docker exec kdb-db psql -U kdb -d kdb -c "SELECT role, count(*), round(avg(extract(epoch FROM finished_at-started_at))) avg_s FROM kwave_kdb_hermes_runs WHERE started_at > now()-interval '24 hours' GROUP BY role ORDER BY avg_s DESC LIMIT 10;"
docker logs kdb-app --since 24h 2>&1 | grep -c "context deadline exceeded"   # 0 기대
```

## §1 — 결론 (TL;DR)
- **실측 진단**: 관측된 "사이클 43~73분"은 착시. `main.go` 단일 single-flight 가드가 SuperviseCycle(~3,090s)+Finalizer(1,833s)+LocalFill(2,972s)+OTT+SourceExpand+Adjudicate를 한 고루틴 직렬로 묶어 **실제 블로킹 ~123분**, 48h간 tick 76회 skip, **사이클당 승격 ~2.6건**. gemma 동시성 24는 직렬 루프들 때문에 사장(병렬 사용처 stepEnrichEmpty 하나).
- **구현·배포 (HEAD `88bd0d3`, 로컬 커밋 — push 미완, §5)**: 전부 env 게이팅.
  - **P1 5-lane 분리** `KDB_AUTOPILOT_SPLIT=1`: core/finalizer/localfill/sourceexpand/adjudicate 각자 독립 single-flight(laneRunner) — tail이 승격 경로 tick을 안 삼킴.
  - **P2 팬아웃** `KDB_STEP_FANOUT=8`: fanOut 헬퍼(autopilot/fanout.go)로 scope/contam/ReviewCandidates/ResolveUnknowns/PromoteConsensus/Enricher 병렬화. LocalFill 3표 투표 병렬 + `KDB_LOCALFILL_FANOUT=3`(websearch 2.5s 전역 throttle 불변).
  - **P4 on-demand**: research tick 60s→**15s**, batch 5→**16**, 워커 **4병렬**(claim SKIP LOCKED 재사용), ResolveOnDemand/DrainQuality 겹침 가드(종전 매 tick `New(pool)`=가드 무효 버그 수정).
  - **P5 gemma 강건화**: content 마지막-JSON 폴백 + 추출실패만 재시도 1회(`KDB_GEMMA_JSON_RETRY=1`).
  - **P3 중복 제거**: ReviewCandidates select에 `needs_disambig IS NOT TRUE`(quarantine 행 재분류 낭비 제거). `KDB_REVIEW_TERM_REJECT=0`이면 reject 권한 gatekeeper 단일화(현재 미설정=현행 유지).
- **첫 사이클 실측 (배포 직후)**: LocalFill 2,972→**206s(14×)**, Finalizer 1,833→**566s(3.2×)**, ReviewCandidates 920→**182s(5×)**, Enricher 614→**103s(6×)**. 블로킹 사이클 ~123분→**~40분**, lane 분리로 tail은 매 30분 정시. deadline/JSON실패 0.

## §2 — ★배포 중 발견·조치: ai1 게이트웨이 병렬 부하 못 견딤
- FANOUT 활성화 직후 **ai1(gemma4-moe, Ollama) 호출이 60~120s로 폭증 → context deadline exceeded 대량**(15분에 28건). ai1 단독 실측: 부하 중 trivial 호출도 62~80s(단일 추론 큐). 동시성 24→8로 낮춰도 지속.
- **조치: ai1을 라운드로빈에서 일시 제외** — `.env` `KDB_GEMMA_BASE_URL=https://ai2.aiinplanet.com`(단독), `KDB_GEMMA_MODEL=gemma-4-26b-a4b`, `KDB_GEMMA_CONCURRENCY=8`. ai2(llama.cpp 4bit)는 1.7s/호출로 병렬 잘 견딤. **전환 후 deadline 0, JSON실패 0.**
- **원복 조건**: ai1 서버에서 Ollama 병렬성(num_parallel)/큐 개선 후 `KDB_GEMMA_BASE_URL=https://ai1...,https://ai2...` CSV 복원. 품질 우려시 참고 — Gemma는 거름망이고 최종판단은 Claude adjudicate(25차)라 ai2 단독의 품질 리스크는 제한적.

## §3 — 검증 (27차에서 24h 창으로 재확인)
- 목표: (a) autopilot_log 시간당 ~2사이클(core는 EnrichEmpty 1,113s 때문에 40분 걸릴 수 있음 — lane들은 정시), (b) 일일 promoted 4배↑(기존 ~26/일), (c) research e2e p90 ≤120s(현재 유입 0건이라 미측정 — 소비자 트래픽 들어오면 §0 쿼리로), (d) deadline exceeded 0 유지, (e) dataqa contaminated=0 유지(병렬화로 인한 오염 없음 확인).
- 롤백(전부 env, `docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app`로 재주입): `KDB_AUTOPILOT_SPLIT=0`·`KDB_STEP_FANOUT=1`·`KDB_LOCALFILL_FANOUT=1`·`KDB_RESEARCH_BATCH=5`·`KDB_RESEARCH_WORKERS=1`·`KDB_GEMMA_JSON_RETRY=0`.
- ★ops 리마인드: .env 변경은 restart로 안 먹음 — compose up -d --no-deps 필수(25차 §3).

## §4 — 남은 병목 (다음 지렛대)
- **step:EnrichEmpty 1,113s가 새 1위** — codex(gpt-5.5) L4 경로가 codexSem(3)에 묶임. 레버: KDB_CODEX_CONCURRENCY 상향(OAuth flock 제약 확인 필요) or L4 gemma 위임 확대.
- core 사이클 ~40분(>30분 tick) — core lane만 간헐 skip(무해, 로그 "core lane still running"). EnrichEmpty 해소되면 <30분.
- promoted 표본 아직 작음(첫 사이클 0) — 24h 관측 필요. candidate 148 중 다수가 이미 처리된 잔량이라 유입(RSS/on-demand)이 실제 상한일 수 있음.

## §5 — 미결·후순위
- ~~push 미완~~ → **push 완료**(오너 지시, `53cff4c..6db9082` origin/main). deploy key는 read-only라 gh CLI credential helper 경유(6차 핸드오프 §push 방법).
- 26차 진단에서 나온 후순위(P0' 백업/보안): DB 일별 자동백업 부재(최대 갭), `.gitignore`에 `.hermes/`·`.ssh/`·`*.bak` 추가, PII 덤프(kdb-mediafine-dump.sql 13MB 등) 리포 밖 이전, dataqa revert 트랜잭션화, `.env.example` 드리프트(12키 vs ~50키). 계획: `.claude/plans/sequential-jumping-grove.md`.

연관: [[KDB.handoff-20260629-4.md]] [[reference-kdb-handoff]] [[reference-kdb-gemma-discovery]] [[feedback-honest-visibility]].
