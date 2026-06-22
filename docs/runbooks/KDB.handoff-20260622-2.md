# KDB handoff — 17차 (2026-06-22): local-usage 2단계 확정-승급 (LOCAL_USAGE 설계 3단계)

이전 세션의 미완 흐름(미커밋 `source_priority.go` 의 `SourceLocalUsage` stub)을 이어서 완성.
설계 SSOT = `docs/KDB_LOCAL_USAGE_DESIGN.md` 의 3단계(Gemma 검색 보강). 오너 승인 = **2단계 확정-승급 모델**.

## §0 — 30초 상태 체크 (16차 §0 동일 + 아래)
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
# SQL↔Go source priority parity (local-usage=1·media=2·local-search=99 이어야 정상)
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT kdb_source_priority('local-usage'), kdb_source_priority('media-consensus'), kdb_source_priority('local-search');"
# local-usage/local-search 산출 추이 (서버22 워커가 증거 보내기 시작하면 증가)
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('local-usage','local-search') GROUP BY s;"
# local-usage 승급으로 교체된 값(revert 가능) 감사
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT count(*) FROM kwave_kdb_dataqa_log WHERE verdict='localusage-promote' AND reverted_at IS NULL;"
```

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
- **결론**: on-demand·소량이면 **server22 없이 KDB 자체 검색 가능**. 단 DC IP 스크래핑은 본질 취약 → **정식 Search API 키(Brave 무료2k/Bing/Mojeek)가 견고한 종착점**(provider 1개 드롭인). 현지엔진(Sogou zh·Coccoc vi)도 provider 추가로 확장 가능.

## §4 — git / 커밋 (이번 세션, 전부 main push 완료)
- `4fe0d52` local-usage 2단계 (source_priority·qa.go·migration 0078·hermes B10) — 16차와 함께 `2295ac7..4fe0d52 HEAD→main`.
- `cb030f0` A8 MatchMissExtractor · `0ffee09` #7 자가복구 · `e610e2b` #6 in-place 감독 · (+이 핸드오프).
- push: gh CLI `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`. 오너 "모두 처리" 지시로 push 진행.
- .env(gitignore): `KDB_MATCH_LLM_EXTRACT=1` 추가(A8). FILLVERIFY/HERMES/DATAQA=1 유지.

## §5 — 남은 작업 / 막힌 항목 (정직)
**막힘(오너·서버22 필요):**
- 서버22 QA 워커 미가동(어제 16:06 1회 후 정지) — KDB 서버서 SSH·Gemma 도달 불가. 워커 재가동 + evidence 필드 송신은 서버22 환경 필요. = local-usage/local-search 가 흐르려면 선결.
- scope:review 플래그 **192건** 운영자 확인 대기(자동 reject 안 함). QA 설계 S1정화/S3검증 미구현 + 오너 확정 3종(통신·자동화·우선순위).
**자율 진행 중:** FillVerifier(codex-fallback 39%·12,4xx 하락·canary ok). A8/#6 신규 run row 누적 관찰.
**이전 deferred(15차):** Gemma fill source 라벨(7곳), WF8 교정값손실 1, 레거시 동일QID active쌍~18, codexcli env테스트 2.
