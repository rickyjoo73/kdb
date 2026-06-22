# KDB handoff — 17차 (2026-06-22): local-usage 2단계 확정-승급 (LOCAL_USAGE 설계 3단계)

이전 세션의 미완 흐름(미커밋 `source_priority.go` 의 `SourceLocalUsage` stub)을 이어서 완성.
설계 SSOT = `docs/KDB_LOCAL_USAGE_DESIGN.md` 의 3단계(Gemma 검색 보강). 오너 승인 = **2단계 확정-승급 모델**.

## §0 — 30초 상태 체크 (새 세션 첫 명령, 2026-06-23 갱신)
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
# 자율 flag (HERMES/FILLVERIFY/MATCH_LLM_EXTRACT/LOCALFILL 모두 1 이어야 정상)
docker exec kdb-app sh -c 'echo HERMES=$KDB_HERMES_ENABLED FILLVERIFY=$KDB_FILLVERIFY_ENABLED MATCH=$KDB_MATCH_LLM_EXTRACT LOCALFILL=$KDB_LOCALFILL_ENABLED'
# SQL↔Go source priority parity (local-usage=1·media=2·local-search=7·codex-fallback=8)
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'lu='||kdb_source_priority('local-usage')||' media='||kdb_source_priority('media-consensus')||' ls='||kdb_source_priority('local-search')||' codex='||kdb_source_priority('codex-fallback');"
# local-usage/local-search 산출 추이 (LocalFill 자율가동 → 신규 엔티티 채우며 증가)
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('local-usage','local-search') GROUP BY s;"
# SearXNG(자체 메타검색) 헬스 + 차단엔진 (brave/google unresponsive 면 부하 열화 — 시간 지나면 복구)
docker exec kdb-app sh -c "curl -s -m 8 'http://kdb-searxng:8080/search?q=test&format=json'" | head -c 80; echo
# in-place 감독 run rows (LocalFill/Finalizer/DataQA/Extractor 가 ok 로 찍혀야)
docker exec kdb-db psql -U kdb -d kdb -P pager=off -c "SELECT role,status,items_out,to_char(created_at,'MM-DD HH24:MI') t FROM kwave_kdb_hermes_runs WHERE role IN ('LocalFill','Finalizer','FillVerifier') ORDER BY created_at DESC LIMIT 6;"
```
**배포**: 코드만 `docker restart kdb-app` / **env(.env) 변경 시 `docker compose -f docker-compose.kdb.yml up -d kdb-app`**(recreate). SearXNG는 `docker compose ... up -d kdb-searxng`. 빌드/테스트: `docker exec -w /app kdb-app sh -c 'GOFLAGS=-buildvcs=false go build ./internal/kdb/... ./cmd/...'`(테스트는 `env -u KDB_LLM_PROVIDER -u KDB_GEMMA_BASE_URL -u KDB_GEMMA_API_KEY`). 수동 검색보강: `docker exec kdb-app /tmp/kdb-app localfill 20 [--dry]`.

## §1 — 이번 세션 완료 (라이브, 영향 0)

### 배경 (정직하게)
- LOCAL_USAGE 설계 1·2단계는 이미 완료: TMDb 정상화(`tmdb-refresh`, alt_titles, `e459099`), Wikidata aliases 영속화(`b85d4f4`).
- 3단계 "Gemma 검색 보강"의 **빈칸-채움 부분은 기존 QA gap-fill 엔드포인트(`/v1/qa/work`·`/v1/qa/result` → `applyQAFills`, `source=local-search`)로 이미 구현**돼 있었으나, **라이브 `local-search`=0** — 서버22 QA/gap-fill 워커가 실제 값을 생산하지 않는 상태(미가동 또는 미적용). ★다음 세션이 서버22 워커 가동 여부부터 확인할 것.
- 미커밋 `SourceLocalUsage`(`local-usage`) 상수만 남아 있었음(이전 세션 중단분). 주석은 tier 1(권위)인데 설계문서의 검색보강 tier 5(=`local-search`)와 충돌 → **2단계 모델로 정합**.

### 2단계 확정-승급 모델 (구현 완료)
- **약한 증거** → `local-search`(tier 99, 빈칸만). 권위소스가 나중에 업그레이드. (= 기존 동작 유지)
- **강한 증거**(다회투표 만장일치 + grounding) → `local-usage`(**tier 1, operator-locked 동급**). 빈칸 채움 + 하위신뢰 슬롯 교체. operator-locked/operator·operator_locked 엔티티는 절대 불가. 교체값은 `kwave_kdb_dataqa_log`(verdict=`localusage-promote`)에 스냅샷 → revert 가능. 같은 우선순위(이미 local-usage) 값 충돌은 핑퐁 방지로 덮지 않고 `[kdb:lu-drift]` 운영자 플래그.

### /admin/hermes 페이지 반영 (B10)
- B10 "QA 교환" Detail 을 일반 "gap-fill" → **"2단계 확정-승급(약→local-search 빈칸, 강=만장일치+grounding→local-usage 권위 tier1)"** 로 갱신. MetricKey `localusage` 신설 → 라이브 지표 **`local-usage N · local-search M`** 표시(현재 둘 다 0 = 서버22 워커 미생산 갭 정직 노출). `handlers_hermes_workflow.go`: workflowStats 에 LocalUsage/LocalSearch + collectWorkflowStats 1-scan 쿼리 + workflowMetric case 추가. 빌드 clean, 배포(admin 200).
- ⚠️ 운영자 로그인 필요 페이지라 에이전트는 렌더 HTML 직접 확인 불가(크리덴셜 미조회). 대신 페이지가 실행하는 지표 쿼리·빌드·라우팅(302→login)으로 검증. 오너가 URL 로그인해 육안 확인 가능.

### 변경 파일
- `internal/kdb/source_priority.go`: `SourceLocalUsage` → Priority 1(operator-locked 동급), Mark `★`, MarkClass `bg-indigo-600 text-white`, SourcesByPriorityAsc 편입. **ShouldReplace 코드 변경 없음** — Priority 만으로 정확히 동작(검증됨: lu>media replace, media>lu keep, operator-locked 보호, 동일 prio drift).
- `migrations/0078_kdb_local_usage_source.sql`(신규): `kdb_source_priority` 에 `local-usage→1` 추가. 0076 대체. **라이브 적용 완료**(`docker exec -i kdb-db psql ... < migrations/0078_*.sql`).
- `internal/kdb/source_priority_test.go`: 파싱 path→0078, exact 목록·Priority ordering·Mark·ShouldReplace 케이스 추가. **전부 PASS**(SQL↔Go parity 포함).
- `internal/kdbapi/qa.go`: `QAFill` 에 증거필드(`agree`/`total`/`grounded`) + `strong()`. `applyQAFills` 2단계 분기. 신규 `promoteLocalUsage`(ShouldReplace 가드 + revert 스냅샷 + drift flag). **하위호환**: 증거필드 없는 구 워커 페이로드 → 약함 → local-search(기존과 동일).

### 검증
- 컨테이너 `go build`(kdb/kdbapi/cmd) clean, `go test`(env -u 로 codexcli 보호) 전부 PASS.
- 라이브 SQL: local-usage=1·media=2·tmdb=4·codex=7·local-search=99. can_replace_canonical: lu>media=t, media>lu=f, operator-locked 보호=f, locked=f.
- kdb-app restart 후 api/admin 200, 에러 0. **`local-usage`=0·`local-search`=0 → 라이브 영향 0**(producer 미가동, 안전 배포). 서버22가 evidence 보내기 시작하면 자동 발동.

## §2 — 서버22 워커 계약 (다음 세션, 서버22 레포 필요)
- KDB 쪽은 준비 완료. 서버22 QA/gap-fill 워커가 `/v1/qa/result` 의 `fills[]` 에 **증거필드를 추가로 보내면** 자동으로 강/약 분기:
  ```json
  {"locale":"ja","value":"…","kind":"native","agree":3,"total":3,"grounded":true}
  ```
  - `agree==total>=3 && grounded==true` → local-usage(확정·tier 1 승급).
  - 그 외 / 증거필드 생략 → local-search(빈칸만, 잠정).
- **선결 과제**: 현재 `local-search`=0 = 서버22 워커가 애초에 fills 를 안 보내고 있음. 워커 가동/접근부터 확인(검색=DDG lite·소량throttle, gemma 로컬:8080, 검색어=이름+영문+역할+대표작, 5라운드 튜닝 프롬프트는 설계문서 C 참조).

## §3 — 롤백/안전
- local-usage 승급 이상 시: `kwave_kdb_dataqa_log WHERE verdict='localusage-promote'` 의 `old_value`/`old_source` 로 revert.
- 강한 증거가 안 들어오는 한 producer inert(기존 local-search 동작과 동일). 마이그레이션은 함수 재정의뿐(데이터 무변).
- `SourceLocalUsage`(tier 1)는 **매체합의도 교체 가능**(오너 승인). 단 강한 증거 + revert 로그 + operator 잠금 보호로 방어. 매체합의 교체가 과하다고 판단되면 `promoteLocalUsage` 에 `kdb_source_priority(curSrc) >= 4` 가드 추가로 보수화 가능(매체·rss 보호).

## §3.5 — 16차 §3 백로그 처리 (이번 세션 추가, 전부 라이브)
- **A8 MatchMissExtractor** (commit cb030f0, flag `KDB_MATCH_LLM_EXTRACT=1` ON): `/v1/match` 자유본문이 0건 매칭이면 본문에서 K-콘텐츠 한글명을 gemma 추출 → research 큐 적재(ContextHint=match-miss, 비동기·핫패스 보호·**응답 불변**). lookup-miss enqueueDiscovery 와 동등. `handler.matchExtractor`(gemma 라우팅). 배포: MATCH=1.
- **#7 자가복구 표시** (commit 0ffee09): `/admin/hermes` heartbeat 아래 "자가복구 상태" 패널. codex breaker open→codex-role gemma 인계 / gemma incident→CLASSIFY·FILL codex 폴백 / 평소 초록. heartbeat stall 수·라벨 경고. `selfHealStatus`(kdb.BreakerIsOpen·GemmaHealthy 동일 소스).
- **#6 감독 in-place 편입** (commit e610e2b): autopilot 30m cycle 밖 4종 루프를 registry 미편입(cadence 보존) 제자리 감독. **`hermes.RecordRun(ctx,pool,RunRecord)` 신규**(Supervisor 불필요). 추출(SweeperTick→SweepStats→runFast 기록)·dataqa(runDataQATick)·정정검증(DrainWikidataVerified applied>0)·finalizer(RunTail→Report.TailActions). ⚠️ kdb→hermes import 는 hermes 테스트 경로(hermes test→agents/enricher→kdb)서 cycle → 호출부(cmd/kdb)에서 기록. **라이브 검증: Extractor run row 기록 확인**(status=ok,in=1,out=1,self_check=t). DataQA 20m·Finalizer 다음 cycle·CorrectionDrain applied>0 시 기록.

## §3.6 — 자체 웹검색 백엔드 (server22 의존 완화, 라이브)
오너 지적("벌크 말고 필요할 때마다, 대안 찾아라") + 실측 결과:
- **실측(KDB IP)**: Google News RSS=**503**(기존 site_search·news_search 가 전부 의존 → KDB 자체검색 죽어있었음). DDG=단일 200·~5연속후 202 차단지속. **Bing/Mojeek/Sogou(zh)/Coccoc(vi)=200**. (상세·권고 = `docs/KDB_WEBSEARCH_BACKENDS.md`.)
- **구현(commit 2f9a2dd)**: 신규 `internal/kdb/websearch` provider chain(Bing 주력·DDG fallback) + 전역 throttle(버스트차단 방지) + 차단 시 provider cooldown. Bing b_algo 파싱 + ck/a base64 URL 디코딩. news_search·site_search 를 이 체인으로 전환(503 해결). site_search 는 Searcher 주입식(fake 테스트). env `KDB_WEBSEARCH_PROVIDERS`(기본 bing,ddg)·`_MIN_INTERVAL_MS`·`_COOLDOWN_MIN`.
- **결론**: on-demand·소량이면 **server22 없이 KDB 자체 검색 가능**.
- **정식 API 불가(오너): 전부 카드 필수**(Bing API 는 2025-08 은퇴됨·410). → **SearXNG 자체 호스팅으로 해결**:
  `docker-compose.kdb.yml` 에 `kdb-searxng`(searxng/searxng, 내부망 expose 8080, `searxng/settings.yml`
  JSON on·limiter off·secret_key=gitignore). websearch `searxng` provider 추가, **기본 체인 `searxng,bing,ddg`**
  (searxng 1순위·메타검색이라 견고). env `KDB_SEARXNG_URL`(기본 `http://kdb-searxng:8080`). 라이브 검증
  완료(JSON 집계 결과). 무료·무카드·무키. 한계=같은 IP egress(대량 시 일부 throttle, on-demand 소량 유지).
  현지엔진(Sogou zh·Coccoc vi)·정식 API(카드 가능 시)는 provider 1개 드롭인으로 확장.

## §3.7 — 현지엔진 + local-usage 자체 가동 (server22 불요)
- **현지엔진(commit 0f33f2f 후속)**: SearXNG provider 에 locale→engines 힌트. zh=`baidu,google,brave`(직접 Baidu 는 302지만 SearXNG 내장 baidu 파서가 처리 — 라이브 검증: zh 쿼리가 "鱿鱼游戏" 현지표기 반환). 손수 스크래퍼 대신 SearXNG 내장 엔진 활용.
- **LocalFill(commit a3be14f)**: 설계 §C(검색보강)를 KDB 자체로. `internal/kdb/localfill.go` + `agents.RoleLocalFill`. 빈 locale 엔티티 → 이름+영문+역할+대표작 쿼리 websearch → **gemma 다회투표(N=3)** 추출(native/latin·동음이의차단·grounding) → `/v1/qa/result` POST(**applyQAFills 2단계 재사용** — 만장일치+grounded=local-usage 승급). CLI `kdb-app localfill [n] [--dry]`. **dry-run 검증 통과**(키사다파라다이스·황현우 id/es/pt_br 3/3 만장일치+grounded). codex 미사용.
  - ★실가동: `kdb-app localfill 20`(쓰기). 우선 `--dry` 로 결과 관찰 권장. autopilot 자동편입은 flag 게이트로 추후(자율 tier-1 쓰기라 관찰 후).
  - 의미: **server22 워커 없이 KDB 자체로 현지표기 검색보강 가능**(server22 의존 제거 경로 완성).

## §3.8 — local-usage 10라운드 자율 수렴 + 개선 + 자율 가동 (2026-06-23)
오너 지시(무질문 자율·10회+ 관찰·개선·지속작동)대로 localfill 10라운드 실행:
- **수렴**: 빈-locale 모집단(active·非locked)은 **97 엔티티**뿐(추정 1800 오류)→7라운드만에 전량 처리. **local-usage 0→110 + local-search 8**. 남은 빈칸(zh55·zhh55·vi36 등)은 현지표기 부재 무명꼬리(정당 빈칸). 값 품질 우수(유마 zh=田悠真, 채연 ja=チョン・チェヨン 등 실제 현지문자).
- **품질**: 10라운드 내내 한글누출 0·zh라틴 0·ja라틴canonical 0(charset 가드가 영문제목은 alias로 라우팅). 동음이의/오염 무.
- **개선 2건(관찰 중 발견)**: ① per-entity 7d 쿨다운(commit 25429f1) — charset 거부 locale 재검색 낭비 차단. ② `local-search(7)>codex-fallback(8)` 우선순위 정정(commit 81883c0) — 검색그라운드>LLM합성.
- **SearXNG 부하 열화**: 1.5s 버스트 검색이 상위엔진(brave/google) rate-limit 유발(baidu 등은 작동). → 자율은 기본 throttle(2.5s) 유지.
- **자율 가동(지속)**: autopilot cycle(30m)마다 `runAutonomousLocalFill`(flag `KDB_LOCALFILL_ENABLED=1`·`KDB_LOCALFILL_BATCH=10`, hermes RecordRun 감독). 쿨다운이 재검색 막아 신규 엔티티 위주. 강증거만 local-usage 승급. **.env flag ON, 배포됨.**

## §4 — git / 커밋 (이번 세션, 전부 main push 완료)
- `4fe0d52` local-usage 2단계 (source_priority·qa.go·migration 0078·hermes B10, +16차 동반)
- `cb030f0` A8 MatchMissExtractor · `0ffee09` #7 자가복구 · `e610e2b` #6 in-place 감독
- `2f9a2dd` websearch(Bing·DDG) · `0f33f2f` SearXNG provider · `+α` 현지엔진(zh=baidu)
- `a3be14f` LocalFill · `25429f1` 쿨다운 · `81883c0` source priority(local-search 7>codex 8, migration 0079) · `264b5ed` autopilot 자율편입
- push: gh CLI `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`. (오너 main 직접 push 승인.)
- migration: 0078(local-usage), 0079(local-search/codex) 적용됨. **수동 적용**: `docker exec -i kdb-db psql -U kdb -d kdb -v ON_ERROR_STOP=1 < migrations/00NN.sql`.
- .env(gitignore): `KDB_MATCH_LLM_EXTRACT=1`·`KDB_LOCALFILL_ENABLED=1`·`KDB_LOCALFILL_BATCH=10` 추가. FILLVERIFY/HERMES/DATAQA=1·GEMMA_TIMEOUT=240000 유지.
- **신규 인프라**: `docker-compose.kdb.yml` 에 `kdb-searxng`(내부망 메타검색). 설정 `searxng/settings.yml`(secret·gitignore) — 재배포 시 `docs/KDB_WEBSEARCH_BACKENDS.md` 절차로 재생성.
- 신규 파일: `internal/kdb/websearch/`, `internal/kdb/localfill.go`, `migrations/0079_*`, `docs/KDB_WEBSEARCH_BACKENDS.md`.

## §5 — 다음 세션 (이어서 할 것)
**★해결됨(이번 세션)**: 서버22 의존 제거 — local-usage 가 **KDB 자체**(SearXNG 메타검색 + LocalFill gemma 다회투표 + 2단계 승급)로 흐름(0→110). 서버22 워커 불요.
**자율 진행 중(관찰만, §0 으로 확인):**
- **LocalFill**(autopilot 30m, flag ON) — 신규 엔티티 빈 locale 자동 채움. **7d 쿨다운 만료(~2026-06-30) 후 기존 97건 재시도** → 검색 복구분 추가 채움 기대. local-usage 증가·`LocalFill` run row 확인.
- **FillVerifier** codex-fallback 하락. **SearXNG 상위엔진(brave/google) 부하 복구** 확인.
**다음 후보(우선순위):**
1. 현지엔진 확장 — 현재 zh=baidu 만. vi=Coccoc 등 `searxngEngines()` 추가(`docs/KDB_WEBSEARCH_BACKENDS.md`).
2. 정식 Search API(카드 가능 시 Brave 무료2k·Mojeek) → websearch provider 1개 드롭인(스크래핑 차단 영구 제거). 발급법=본 세션 대화 + 백엔드 문서.
3. scope:review **192건** 운영자 검토(자동 reject X) + QA 설계 S1정화/S3검증 미구현 + 오너 확정 3종(통신·자동화·우선순위).
4. 포미닛=4Minute 등 **그룹의 person 오분류** 소수(en-copy 점검서 발견) — classify 도메인.
**이전 deferred(15차):** Gemma fill source 라벨(7곳), WF8 교정값손실 1, 레거시 동일QID active쌍~18, codexcli env테스트 2.
