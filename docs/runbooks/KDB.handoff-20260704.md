# KDB handoff — 27차 (2026-07-04): 프로액티브→온디맨드 전환 + ai4 gemma + admin 메뉴 재설계 1단계 + 네이버 오염판별 인프라

26차 [[KDB.handoff-20260702.md]] 이어짐. 오너 방침 대전환: **RSS 크롤·상시 LLM 오염감사 폐기 → kstory 소비자가 보내는 요청 키워드를 빠르고 정확하게 서빙**. "속도와 정확도가 기본"(외부 답변 품질).

## §0 — 상태 체크 (새 세션 첫 명령)
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health   # 200
# 온디맨드 처리(요청 키워드 → 결과) — 새 admin 페이지가 보는 것
docker exec kdb-db psql -U kdb -d kdb -c "SELECT status, count(*) FROM kwave_entity_research_queue GROUP BY 1;"
# 소비자 트래픽(kstory 실사용 개시했나 — last_used NULL이면 아직)
docker exec kdb-db psql -U kdb -d kdb -c "SELECT label, active, last_used_at, created_at FROM kwave_kdb_api_consumers ORDER BY last_used_at DESC NULLS LAST;"
# gemma 안정성(ai4 llama.cpp 12B QAT 붙은 뒤 deadline 0 유지 확인)
docker logs kdb-app --since 24h 2>&1 | grep -c "context deadline exceeded"   # 0 기대
# admin 신규 페이지 라우트 (302=정상)
for p in /admin/ondemand/queue /admin/ondemand/consumers /admin/quality/verification; do curl -s -o /dev/null -w "$p %{http_code}\n" http://127.0.0.1:9101$p; done
# 검증 tier 분포(증분2 — 서빙이 실제 내보내는 신뢰도). 미검증(null) 0 기대
docker exec kdb-db psql -U kdb -d kdb -c "SELECT COALESCE(verification_tier,'(null)'), count(*) FROM kwave_entities WHERE status='active' GROUP BY 1 ORDER BY 1;"
```

## §1 — 이번 세션 한 일 (TL;DR)
1. **26차 처리량 수정 검증**: 사이클 ~3.5× 단축(2964s→최근 ~900s), tick 시간당 2회 복구, role 시간 유지(LocalFill 560s·Finalizer 349s·EnrichEmpty 308s), deadline 사실상 0. **단 일일 승격 40 vs 기존 ~26 = 1.5×**(4× 아님) — 병목이 **파이프라인 속도→유입**으로 이동(research_queue 고갈: pending 6/done 2580).
2. **ai4 gemma 엔드포인트 신설**: 26B-A4B MoE Q2(2bit)·parallel1 → **12B-it QAT Q4 dense·parallel4** 교체. KDB round-robin에 편입(ai2+ai4, CONCURRENCY 12). §3.
3. **전략 전환(오너 지시)**: RSS 키워드수집·상시 LLM 오염감사 **폐기** → on-demand. env 플래그로 실행(되돌림 가능). ★**공신력 채움(Wikidata source-expand) 잘못 끈 것 오너 지적→재활성화**. §4.
4. **admin 메뉴 재설계 1단계 완료·배포·커밋**: 발굴 큐·소비자 대시보드 신설 + 네비 재편. §5. (PR #1)
5. **네이버 검색기반 오염판별 인프라(증분1)**: 클라이언트+CLI. ★encyc 단독 판별 노이지 실측→news+gemma 재설계 합의. §6.
6. **★증분2 = 엔티티 정체성 검증 tier 구현·배포·검증 완료**(같은 07-04 이어서): "1회 검증→캐시→빠른 서빙" 게이트. 결정론 스윕(3125/770/669)+네이버news·gemma 증거승급+서빙노출(/v1/lookup)+주기스윕+admin 검증뷰. PR#1 `7776612`·`9ec8ef6`·`3e428ff`. §6 ✅.

## §2 — 배포/커밋 상태
- **브랜치 `admin/requests-keyword-locale-dashboard`**, **PR #1** (https://github.com/rickyjoo73/kdb/pull/1). main 미머지(오너 판단 대기 or 머지 요청).
  - `27f0ee8` 요청키워드 로깅 + locale-gaps 현황판
  - `dc2615f` 메뉴 재설계 1단계(발굴큐·소비자·네비·대시보드)
  - `1af5a06` 네이버 클라이언트 + naver-verify CLI (증분1, 프로덕션 미배선)
- **라이브 배포**: 코드는 `docker restart kdb-app`(go build→exec, 11차 방식)로 이미 적용됨. .env 변경은 `docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app`.
- **push 방법**(6차): deploy key read-only → `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:<branch>` (gh=aiinplanet 계정).
- **컴파일 체크**: `docker exec -e GOFLAGS=-mod=mod -e GOMODCACHE=/app/.gomodcache kdb-app sh -c 'cd /app && go build ./internal/... ./cmd/kdb/'`
- **.env 백업**: `.env.bak.20260703-ai4`·`.env.bak.20260703-audit-discard`·`.env.bak.20260704-reenable-authoritative`·`.env.bak.20260704-naver`.

### 현재 .env 핵심 플래그 (변경분)
```
KDB_GEMMA_BASE_URL=https://ai2.aiinplanet.com,https://ai4.aiinplanet.com   # ai4 추가
KDB_GEMMA_CONCURRENCY=12          # 8→12
KDB_DATAQA_ENABLED=0              # 폐기(상시 LLM 오염감사)
KDB_FILLVERIFY_ENABLED=0         # 폐기
KDB_CLAUDE_ADJUDICATE_ENABLED=0  # 폐기
KDB_MATCH_LLM_EXTRACT=0          # 폐기(단 kstory가 /v1/match 자유본문 쓰면 재검토 필요)
KDB_DISABLE_RSS_POLLING=1        # RSS 폴러 정지
KDB_SOURCE_EXPAND_ENABLED=1      # ★재활성화(Wikidata/iTunes 권위 채움 — 절대 끄지 말 것)
KDB_OTT_ENABLED=1                # ★재활성화(권위 채움)
KDB_LOCALFILL_ENABLED=1 · KDB_ENRICH_GROUND=1 · KDB_ENRICH_GROUND_STRICT=1   # 유지
KDB_NAVER_CLIENT_ID=... · KDB_NAVER_CLIENT_SECRET=...   # 네이버 검색 API(쿼터 1,000/일)
```
RSS 피드 전량 비활성(kwave_news_whitelist enabled=0). 복원 백업: `scripts/cleanup-20260614/whitelist_backup_20260703.csv`(활성 49개는 last_polled 최근값으로 식별).

## §3 — ai4 gemma 엔드포인트 (12B QAT Q4 dense)
- **호스트 aiin24**: SSH `atikar.com:38384`(외부) / `192.168.0.24`(내부), user `aiin`, pw `jdch0513!#`, **레거시 ssh-rsa 필요**. 세션 헬퍼 = `scratchpad/aish.sh`. **RTX 3060 12GB 1장뿐**.
- **구성**: `/home/aiin/gemma4-moe/docker-compose.yml`, 컨테이너 `gemma4-moe`(ghcr.io/ggml-org/llama.cpp:server-cuda), 포트 8080. 모델 `/home/aiin/gemma4-moe/models/gemma-4-12B-it-qat-UD-Q4_K_XL.gguf`(6.3GB) `--parallel 4 -c 16384 -fa on`. **원복**: `cp docker-compose.yml.q2-26b-vision.bak.20260703 docker-compose.yml && docker compose up -d`.
- **실측**: 단일 2.03s, 8동시 5.8s(~3×), VRAM 8/12GB. `enable_thinking:false`(chat_template_kwargs)로 직접출력. `model=gemma-4-26b-a4b`로 호출해도 200(llama.cpp가 model 무시) → KDB drop-in.
- **하드웨어 지형**: ai1·ai2·ai4 모두 게이트웨이 IP `175.198.102.238` 뒤. ai4=aiin24(내가 SSH 보유). **ai2=별도 백엔드**(부하시 5~10s로 느림, ai4가 이걸 덜어줌). **ai1=Ollama gemma4-moe(병렬 붕괴로 제외 유지)**. 3번째 엔드포인트 필요시 ai1 박스도 ai4처럼 llama.cpp 4bit dense로 개조.
- 24h 안정성 감시 스크립트 `scratchpad/ai4_watch.sh`(background). deadline 급증시 조기중단. ★"코드 팬아웃(Gatekeeper/Finalizer)" 계획은 감사머신 폐기로 **대체됨**(그 대상이 폐기됨). ai4 gemma는 on-demand gatekeeper + 새 검증기 gemma 판정용으로 유효.

## §4 — 전략 전환: 프로액티브 → 온디맨드 + ★실수 정정
- **폐기(프로액티브)**: RSS 키워드수집(느림)·상시 LLM 오염감사 루프(dataqa gpt-5.5·scope/contam review·Claude adjudicate). 라우트/테이블은 아카이브로 보존, 동작만 정지.
- **유지(온디맨드 서빙 경로)**: research 워커(15s·batch16·4워커, kstory 키워드 해소 엔진), API(/v1/lookup·match·prepare·corrections), enrich(bgEnrich·ResolveOnDemand), **권위 채움(Wikidata source-expand·iTunes·OTT)**, LocalFill/GroundEntity(검색 그라운딩), Disambiguator.
- ★**내 실수(교훈, [[feedback-authoritative-fill-sources]])**: 폐기 배치에 `KDB_SOURCE_EXPAND_ENABLED=0`(Wikidata/iTunes 권위 **채움**)을 잘못 포함. 오너 강한 지적("공신력 있는 걸 끄는 건 문제, 바로 채워야지"). 즉시 =1 복원 + OTT=1. **구분 원칙: 권위 데이터 채움=항상 ON / 느린 LLM 오염감사=폐기 대상.** langlink-upgrade·refill-anchored는 실행해보니 대부분 포화(0 upgrade) — 권위소스는 이미 거의 채워짐.
- locale-gaps 페이지 재구성: "백로그 783" → "커버리지 현황판"(채움가능=외부앵커 359 / 천장=앵커없음 424). 88~92% 이미 채워짐. 채움은 on-demand로 일원화.

## §5 — admin 메뉴 재설계 (기획서 + 1단계 완료 + 남은 것)
- **기획서 아티팩트**: https://claude.ai/code/artifact/c283aff8-6ab1-4fc8-b4f4-de169ab55278 (현행 19메뉴 전수진단 + 폐기6 + 신설5 + 새 네비트리).
- **네비 정의**: `internal/kdbadmin/router.go` `navItems()`(재작성됨). 라우트도 같은 파일.
- **✅ 1단계 완료·배포**(`dc2615f`):
  - 신설 `handlers_ondemand.go` + `templates/ondemand_queue.html`·`ondemand_consumers.html`:
    - **발굴 큐** `/admin/ondemand/queue`: research_queue+entities LATERAL 조인 → 요청 키워드별 처리결과(해소·active/후보·Inbox/기각/미생성) + 큐상태 필터. **오너 요구 '요청→처리 결과'의 답**.
    - **소비자 대시보드** `/admin/ondemand/consumers`: api_requests×api_consumers 집계(요청량·미스율·최근사용·주 엔드포인트).
  - 네비 재편: **온디맨드·kstory 섹션 신설**(발굴큐·소비자·요청로그·Inbox), 죽은 4메뉴 제거(whitelist·codex-runs·observations·candidates, 라우트는 아카이브 유지), 고아 승격(inbox·locale-gaps), **엔티티 필터 5→1**(entities_list.html 그룹 탭), 라벨 정정(소스→해소진단).
  - 대시보드 죽은 타일(Whitelist·Codex×2·RSS후보)→발굴대기·신규후보 타일, stale "read-only 뷰" 문구 수정.
  - `render_smoke_test.go`(신규 템플릿 렌더 검증) 통과.
- **❌ 남은 것 (2·3단계 — 신규 계측/키 필요)**:
  - 오염 검사 뷰 → **§6 증분2의 검증 뷰로 통합**(`/admin/quality/verification`).
  - **검색 헬스·쿼터** `/admin/ondemand/search-health`(네이버 쿼터 소진·429 가시화).
  - **백업·보안 상태** `/admin/ops/backup`(DB 자동백업 부재 — 오너 이전 지적, 26차 P0').
  - 잔여 정리: dashboard poll-cycle 카드(RSS 동결) 제거, `entity_sources.html` whitelist 블록 제거(제목만 '해소 진단'으로 바꿈), Hermes DISAMBIG 기본엔진 codex→gemma·RSS 잔재 step 정리, homonyms(dead 라우트미등록)·unclassified(0건) 코드 삭제, 검토큐 bumpable-only 축소, conflicts+homonyms 병합.

## §6 — 네이버 검색기반 오염판별 (증분1 완료 + ★증분2 설계 합의)
- **네이버 키 동작 확인**(검색 API 활성·로그인불요·쿼터 1,000/일). `.env`에 저장.
- **증분1(`1af5a06`, 프로덕션 미배선)**: `internal/kdb/naver/naver.go`(encyc/news 클라이언트 + `VerifyIdentity` 역할토큰 confirm) + CLI `kdb-app naver-verify [n]`.
- ★**실측으로 잡은 핵심 한계(배선 전 발견=다행)**: **encyc 단독 역할매칭은 너무 노이지** — 아이유→1위 "국제단위(IU)", 기생충→1위 "생물 기생충", 흔한 인물명→역사인물/정치인(선거벽보)이 상위 점령. 유명 엔티티도 오플래그. **news(+역할어) 쿼리가 훨씬 정확**(실측: news "기생충 영화"→"한국 영화 기생충(2019)" 정확). 저신뢰 샘플 15건: confirmed 5/review 9/no_entry 1(review에 정치인·역사인물 정확히 걸림=거름망은 작동).

### ★증분2 설계 (오너가 "속도+정확도" 기준 제게 판단 위임 → 합의)
**오염판별을 독립기능이 아니라 "1회 검증→캐시→빠른 서빙" 게이트로 설계.**
- **속도**: 검사를 요청마다(hot path) 안 돌림. **엔티티당 1회 검증→캐시** → kstory 요청은 DB 즉답. research 워커 15s로 miss도 곧 채움.
- **정확도·검증 3단계(싼→비싼)**:
  1. **결정론 권위확인(무료·즉시, ~80%)** — Wikidata QID·TMDb + ko-라벨 + charset. kstory가 묻는 유명 K-콘텐츠는 대개 여기서 확정. 네이버 안 씀.
  2. **검색근거+gemma 판정(권위소스 없는 소수만)** — SearXNG **+ 네이버 news(+역할어)** 를 **증거로 모아 gemma가 판정**(encyc 역할토큰 매칭 ✕). 결과 캐시.
  3. **동명이인** 결정론 disambig.
- **네이버 = 2단계 증거 조연**(판정은 gemma). 쿼터 1,000/일로 충분(권위소스가 대부분 거름).

### ✅ 증분2 구현 완료·배포·검증 (2026-07-04, PR#1: `7776612`·`9ec8ef6`·`3e428ff`)
- **마이그레이션 0084**: `kwave_entities`에 `verification_tier`/`verification_evidence`/`verified_tier_at` + 부분 인덱스. 라이브 적용됨.
- **`internal/kdb/verify`**:
  - `SweepDeterministic` — 전 active set-based 단일 UPDATE 분류(무료·무쿼터·<1s). **실측 authoritative 3125 / evidenced 770 / unverified 669**. tier 규칙: authoritative=권위앵커(wikidata/tmdb/kofic/kmdb/musicbrainz external_ref) / evidenced=wikipedia langlink·강한source(operator·local-usage·media-consensus 등)·conf≥0.75 / unverified=독립확증 없음.
  - `EvidencePass` — unverified 상위 n개를 네이버 news(+역할어)+gemma 판정으로 evidenced 승급. **실측: 5개 중 3개 승급**(하랑·찬호·정희, grounded reason). 결정론 스윕이 강등 못하게 evidence `search+gemma%` 보존. ★단명(mononym) 엔티티는 실재확인은 되나 특정 인물 매핑은 fuzzy → evidenced(약한티어)로 적정 헤지.
  - `ClassifyOne` — enrich 직후 단일 갱신용(CTE id 스코프, 미배선).
- **CLI**: `kdb-app verify-entities`(스윕) / `kdb-app verify-entities evidence [n]`(승급).
- **서빙**: `Entity.VerificationTier/Evidence` → `/v1/lookup`·`/v1/match` 응답 노출(캐시 즉답). **실측: 봉준호 → {verification_tier:authoritative, evidence:wikidata}**(프로브 소비자로 확인, 정리 완료).
- **주기 스윕**: 워커 루프 `verifyTicker` 기본 10분(`KDB_VERIFY_SWEEP_INTERVAL_SECONDS`), single-flight 가드 — 신규/재enrich stale tier 자동 최신화.
- **admin `/admin/quality/verification`**: tier 요약+검증율+유형별 분해(구성막대)+tier 필터(기본 unverified=리스크표면)+엔티티 목록. 네비 "검증 커버리지"(온더플라이 per-locale 집계)→"검증 tier·정체성"(서빙정렬 캐시) 재지정. 기존 `/admin/entities/trust`=legacy 라우트 유지. render_smoke_test 통과.

### 증분2 남은/후속
- **`verified_only` 소비자 게이트에 tier 반영**: 현재 tier 는 응답에 노출만. `verified_only=true` 시 `unverified` 엔티티 제외(또는 canonical_ko만) 게이트는 미적용 — kstory 실사용 개시 후 정책 확정 시 추가(api.go:1068 `applyLocaleVerifiedGate` 근처).
- **`ClassifyOne` 배선**: enrich/승급 완료 hook에서 호출하면 10분 주기 대기 없이 즉시 갱신(현재는 주기 스윕이 커버).
- **EvidencePass 스케줄/쿼터 계측**: 현재 수동 CLI. 온디맨드(kstory notable 키워드) 자동화 + 네이버 쿼터 소진 가시화(§5 검색헬스 페이지)와 연계.
- `KDB_MATCH_LLM_EXTRACT` 재검토(kstory가 자유본문 보내면 필요).

## §7 — 미결·후순위
- **DB 자동백업 부재**(26차 P0', 최대 리스크) — §5 백업 페이지 + pg_dump cron.
- kstory 실사용 개시 확인(현재 last_used_at NULL). 개시되면 §0 소비자 쿼리·발굴큐로 miss율 관찰.
- 카카오 API = 오너 "힘들 것" → 제외. 네이버 단독 + SearXNG로 설계(정체성=네이버, 외국어표기=SearXNG).
- PR #1 main 머지 여부(오너 판단).

연관: [[KDB.handoff-20260702.md]] [[reference-kdb-handoff]] [[feedback-authoritative-fill-sources]] [[reference-kdb-websearch]] [[reference-kdb-dataqa]].
