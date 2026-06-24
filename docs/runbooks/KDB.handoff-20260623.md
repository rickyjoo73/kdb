# KDB handoff — 18차 (2026-06-23): enrich 흐름 병목(codex-fallback firehose) 봉인

오너 지시("전체적으로 흐름이 막혀 빨리 진행 못하는 구간 진단·대책")로 운영 전 구간을 실측 진단 →
최대 품질부채인 **codex-fallback 누수**를 enrich 파이프라인에서 2단계로 봉인하고, 5차에 걸친
실시간 모니터링으로 **증가→감소 역전**을 확인했다.

## §0 — 30초 상태 체크 (새 세션 첫 명령, 2026-06-23 갱신)
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
# 자율 flag — HERMES/FILLVERIFY/MATCH/LOCALFILL/REGROUND/ESCALATE + 신규 ENRICH_GROUND/STRICT 모두 1
docker exec kdb-app sh -c 'echo HERMES=$KDB_HERMES_ENABLED FILLVERIFY=$KDB_FILLVERIFY_ENABLED MATCH=$KDB_MATCH_LLM_EXTRACT LOCALFILL=$KDB_LOCALFILL_ENABLED REGROUND=$KDB_LOCALFILL_REGROUND ESCALATE=$KDB_LOCALFILL_ESCALATE GROUND=$KDB_ENRICH_GROUND STRICT=$KDB_ENRICH_GROUND_STRICT'
# ★누수차단 핵심지표: codex-fallback 은 이제 순감소해야 정상(strict 봉인 + reground 드레인)
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('codex-fallback','local-usage','local-search') GROUP BY s ORDER BY s;"
# enrich 인라인 그라운딩 발동 누적 + 빈칸(부작용) 추이
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'enrichground='||count(*) FROM kwave_kdb_enrich_attempts WHERE field='enrichground';"
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'active_empty_foreign='||count(*) FROM kwave_entities WHERE status='active' AND (canonical_ja=''OR canonical_vi=''OR canonical_id=''OR canonical_es=''OR canonical_pt_br=''OR canonical_zh=''OR canonical_zh_hant=''OR canonical_en='');"
# cycle 건강(1800s 이하·skip 없어야) + hermes 전역할 ok
docker exec kdb-db psql -U kdb -d kdb -P pager=off -c "SELECT to_char(ran_at,'MM-DD HH24:MI') t, duration_ms/1000 sec, classified, enriched FROM kwave_kdb_autopilot_log ORDER BY ran_at DESC LIMIT 4;"
```
**배포**: 코드만 `docker restart kdb-app` / **env(.env) 변경 시 `docker compose -f docker-compose.kdb.yml up -d kdb-app`**(recreate). 빌드/테스트: `docker exec -w /app kdb-app sh -c 'GOFLAGS=-buildvcs=false go build ./internal/kdb/... ./cmd/...'`(테스트는 `env -u KDB_LLM_PROVIDER -u KDB_GEMMA_BASE_URL -u KDB_GEMMA_API_KEY`). 단건 enrich 진단: `docker exec -e KDB_ENRICH_GROUND=1 -e KDB_ENRICH_GROUND_STRICT=1 kdb-app /tmp/kdb-app enrich-test "<ko>"`(layers 에 `ground` 나오고 `codex-fallback` 안 나오면 strict 정상).

## §1 — 병목 진단 (실측, 정직)
운영 전 구간 실측 결과 **흐름이 막힌 핵심 구간 = locale 채움의 신뢰축이 시간순으로 거꾸로**:
- enrich `orchestrator.go` 가 **L3(Wikidata) 직후 바로 L4(codex-fallback, 신뢰축 최하위 LLM 합성)**
  으로 빈칸을 즉시 메꿈 → 검색-그라운딩 레이어가 hot-path 에 **없음**.
- 별도 비동기 LocalFill 재그라운딩 캠페인(`localfill:rg`)이 codex-fallback 을 좋은 값으로 업그레이드
  하지만, **생산(firehose) ≫ 배수**라 구조적으로 못 따라잡음.
- 실측: 6h 기준 enrich_attempts `gpt-5.5`=385 vs `localfill`=135(생산이 ~3×). active locale 신규 민팅
  codex-fallback ~1,295 vs local-usage 124(~10×). codex-fallback 누적 12,604→12,691→**12,698**(순증),
  재그라운딩 모집단 2,937→2,950(오히려 증가) = 캠페인이 **수렴이 아니라 발산**.
- codex-fallback 은 전체 locale 값의 ~39%(최대 출처) — 어떤 자율도구도 *생산*을 못 막던 최대 품질부채.

## §2 — 대책 ① enrich L3.5 인라인 검색-그라운딩 (누수 firehose 차단)
codex 합성(L4) **앞에** 같은 SearXNG+gemma 다회투표 그라운딩을 끼워 신규 codex-fallback 을 생산지점에서 차단.
- **신규**(`internal/kdb/localfill.go`): `GroundEntity(ctx,pool,entityID,perEntity)` + `loadGroundEntity`.
  단일 엔티티의 빈 locale 을 `localFillOne`(기존 LocalFill 추출기) 으로 그라운딩 → `postQAResult`
  (applyQAFills 2단계 가드 재사용: 강증거 3/3+grounded→local-usage(tier1), 약증거→local-search(빈칸)).
  esc=nil(빈칸-채움은 codex 미투입, 오너 방침). 7d 쿨다운(field `enrichground`, 빈칸/재그라운딩 쿨다운과 독립).
- **배선**(`internal/kdb/enrich/orchestrator.go`): L3 Wikidata 와 L4 codex 사이에 `kdb.GroundEntity` 호출.
- **게이트**: flag `KDB_ENRICH_GROUND=1`(off 면 완전 no-op·기존 동작 보존). package 방향 `enrich→kdb`
  는 허용(역방향만 cycle)이라 직접 호출 가능.
- **canary(LIVE, 전역 무영향 단일 프로세스)**: 설계자[id]=`The Plot` 3/3→**local-usage**, 승산있습니다[pt_br]
  =`There's a Chance of Winning` 3/3→**local-usage**(codex-fallback 아님). 김종수(동명이인)·섬총각(무명)은
  강증거 미달→안전하게 codex 폴백. charset 백스톱 정상(`열혈농구단` 한글→es 거부→빈칸).

## §3 — 대책 ② strict "빈칸 > 틀린값" (잔여 누수 봉인, 오너 승인)
①배포 후 모니터링서 **잔여 누수** 포착(codex-fallback flat, 순감소 안 됨): grounding 이 검색 무신호
(동명이인·무명) 로 못 채운 locale 을 enrich 가 **여전히 codex-fallback 로 합성**. 오너 결정 = 빈칸>틀린값.
- **구현**: `GroundEntity` 가 `handled`(이번 턴 grounding 실행 OR 7d 쿨다운=이미 담당) 반환. `loadGroundEntity`
  가 쿨다운을 WHERE 가 아니라 컬럼(EXISTS)으로 노출 → 쿨다운중이어도 handled=true. orchestrator 는
  `groundHandled && kdb.EnrichGroundStrict()` 면 **L4 codex 스킵** → 검색 무신호 locale 은 codex 추측값
  대신 **빈칸 유지**. reground 캠페인·쿨다운 만료 시 재방문.
- **게이트**: flag `KDB_ENRICH_GROUND_STRICT=1`(off 면 grounding 후에도 codex 폴백=① 단독 동작으로 완화).
- **canary**: 김종수(쿨다운=handled)→`layers=[musicbrainz wikidata]`(codex-fallback **사라짐**), zh 빈칸 유지.
  이사샤(신규·무명)→`layers=[]` codex 스킵, 빈칸 유지. build/test PASS, recreate 배포(api/admin 200).

## §4 — 모니터링 결과 (5차, 2026-06-23 18:35~21:55)
오너 지시("이제 막 시작했으니 전체적으로 모두 모니터링")대로 50~90분 주기 자율 모니터:
- **순감소 역전 확립**: codex-fallback 추이 12,698(①baseline)→12,699→12,697→12,693→12,702(잔여누수 포착)
  →**②strict 배포(21:03)**→12,691(-11/52분). **배포 전 +14/h → 현재 ~-12/h 방향 역전**. local-usage 226→236.
- **부작용 없음**: foreign-locale 빈칸 보유 active **82건**(전체 ~2%, 하드테일 정당빈칸). exhausted 안정.
- **cycle 건강**: strict 로 codex 생략 → 오히려 빨라짐(221~298s). skip overlapping tick 0(18:24 1943s 1회는
  배포 직전 대량 poll 1회성·자가회복, grounding 무관 — enrichground 2/30m 라 latency 미미).
- **전 구간 그린**: api/admin 200, gemma·codex breaker 정상, SearXNG(brave 간헐 rate-limit·체인 흡수),
  hermes 전역할 ok, candidate~54, 한글누출 0, panic/fatal 0.

## §5 — 롤백 / 안전
- 누수차단 끄기: `.env` 에서 `KDB_ENRICH_GROUND_STRICT=0`(strict 만 끔, ① 그라운딩은 유지·codex 폴백 복원)
  또는 `KDB_ENRICH_GROUND=0`(L3.5 자체 off, 완전 원복) 후 recreate. 데이터 무손상(읽기/빈칸뿐, 교체 없음).
- grounding 쓰기는 전부 기존 `/v1/qa/result` 가드(charset·suppress·operator 보호·revert) 통과. strict 는
  codex *호출을 안 함*(쓰기 자체가 없음)이라 부작용은 "빈칸 유지"뿐 — 가장 보수적.
- local-usage 승급 이상 시: `kwave_kdb_dataqa_log WHERE verdict='localusage-promote'` 로 revert(기존과 동일).

## §6 — 다음 작업
- **codex-fallback 장기 드레인 관찰**: strict 로 생산 정지됐으니 reground 캠페인(`localfill:rg`)이 기존
  12,691 을 수일~수주에 걸쳐 순감. §0 추이 쿼리로 추적. 정체 시 reground batch/주기 조정 검토.
- **16차 §3 잔여 후보(이번 세션 미착수)**: ②LocalFill 을 30m cycle 에서 분리한 상시 저속 드레이너(동일
  순간부하·유휴갭 제거로 throughput↑), ③exhausted(10,620) 감사(codex-fallback 음역 복구가능분 비율 측정).
- **기존 품질부채**: `zh_latin_leak`=650(중국어 locale 라틴값, 일부는 정당 브랜드명 — 선별 필요),
  샘 킴/샘 김 등 철자중복 잠복 dup(14차 dedup 방식 준용).
- 그룹의 person 오분류 잔여(QID 없는 person 794 미검증, 17차 §3.11).

## §7 — 정정: 제2 codex-fallback 생산자 봉인 (06-24, commit f8725c7)
**honest visibility**: §4 의 "순감소 확립"은 성급했다. 익일 아침 모니터에서 codex-fallback 이
12,681→12,718(+37/h) 재증가(역추세). 끝까지 추적한 근본원인 = **codex-fallback 생산자가 둘**:
- §2 가 고친 `internal/kdb/enrich/orchestrator.go` 는 한 경로일 뿐.
- **`internal/kdb/agents/enricher/`**(hermes "Enricher" 역할·매 cycle items_in 17~19 도는 **주력 배치
  enricher**)가 자체 `cascadeLocales` L4(gpt-5.5, layers.go)로 codex-fallback 을 직접 생산 — L3.5/strict
  미적용. 적발 증거: 신규 person 박재웅이 vi/pt/zhh=codex-fallback 인데 canonical_* enrich_attempt·
  enrichground 가 0(=orchestrator 가 아닌 agents/enricher 가 채움).
- 왜 §4 에서 안 보였나: 저녁엔 신규 유입이 적어 agents/enricher 가 mint 할 게 없었고 reground 가
  net-감소시킴. 아침 대량 candidate(poll 1106, 1044건)가 active 로 풀리며 제2 경로가 +37 mint.

**봉인**(commit f8725c7): `agents/enricher/layers.go` L4(gpt-5.5) **앞**에 동일 패턴 — ① `kdb.GroundEntity`
검색-그라운딩, ② `kdb.EnrichGroundStrict()` 면 grounding 담당(실행/7d쿨다운) 엔티티의 L4 codex 스킵.
`enrichground` 쿨다운은 orchestrator 와 **공유**라 같은 엔티티 중복검색 없음. `localfill.go` 에 nil-pool
방어 가드(테스트 nil pool panic 방지). build·test PASS, `docker restart kdb-app`(소스 재빌드) 배포.
**검증(모니터 8차)**: codex-fallback +37/h→**flat**(12,718→12,717), Enricher cycle 정상(grounding 미부풀림,
205~254s), api/admin 200. **다음 cycle 신규유입 표본으로 완전 확립 확인 예정**(성급선언 재발 방지).

### §7.1 — 3차 정정: candidate 단계 grounding 누락 (06-24, commit e742539)
f8725c7 후에도 codex-fallback 재증가 지속(모니터 9차 12,717→12,742). 신규 lookup-miss 발굴
person(양희은 등)이 enrichground 없이 codex-fallback locale 획득. **진짜 근본원인 = "제3 생산자"가
아니라 grounding 의 status 필터 누락**: research/discovery 워커(`research/worker.go:179,201`)가 엔티티를
**`status='candidate'` 로 생성→그 상태로 `Orch.Enrich`**(codex-fallback 민팅)→Wikidata 통과 시 active
승급. 그런데 `loadGroundEntity` 가 `WHERE status='active'` 라 candidate 단계에선 grounding handled=false
→ strict 미적용 → codex 가 candidate 에 codex-fallback 을 박고 active 로 승급. 양희은 enrich_attempts=0
인 이유: 핵심은 candidate-stage enrich(orchestrator)였고 satisfied locale 은 attempt 행 삭제됨.
- **봉인**: `loadGroundEntity` status 필터 `'active'` → `IN ('active','candidate')`. 두 enricher 모두
  `kdb.GroundEntity`→`loadGroundEntity` 경유라 한 곳 수정으로 candidate-stage 양 경로 커버. canary:
  candidate '도둑들' enrich→`layers=[]`(codex-fallback 없음)·enrichground 기록·빈칸 유지.

★교훈: ① codex-fallback 같은 source 라벨은 **여러 생산자**가 쓸 수 있다 — 봉인 시 `grep -rn "SourceCodexFallback\|'codex-fallback'" internal/` 로 *모든* writer 확인(enrich 서브시스템이 둘: `enrich/` vs `agents/enricher/`). ② enrich 는 **candidate 단계**에도 돈다(research 발굴) — grounding/strict 가드는 status='active' 만 보면 안 됨. ③ 모니터링서 **신규 유입 표본**(최근 created + 출처별)을 봐야 누수 경로가 드러난다(저부하 시간엔 가려짐).

## §8 — codex-fallback 드레인 속도/품질 규명 + TMDb 재귀속 (06-24)
오너 질문("드레인 속도 못 높이나 / 퀄리티 괜찮나")으로 실측 규명.
- **속도 ≠ 병목, 소스 가용성이 병목**: 벌크 reground(검색) 캠페인은 방문 10×인데 promote 동일(~4/h, 변환율 ~2%). drain-fillverify(신규 CLI `7454163`)로 QID 1,427 전수 시도했으나 24건만 acted(1.7%) — **Wikidata 에 해당 locale 라벨 부재가 대부분**(표본 ja=없음). 남은 codex-fallback(~12,700)은 권위소스(Wikidata/웹/TMDb)에 데이터 없는 하드테일이며 **값은 대체로 정확**(仮面の女王·The Roundup 5 등). codex-fallback 라벨 = "출처 미검증"이지 "오류" 아님.
- **TMDb 작동 확인**(앞 세션 "키 없음"은 변수명 오독 — 실제 `KDB_TMDB_API_TOKEN` 정상): 매칭가능 작품은 이미 현지화(오징어게임 pt=Round 6). 남은 작품 cf 다수가 TMDb 미수록 locale(es) 또는 무명/예능.
- **TMDb 재귀속 구현**(`42e3deb`): `tmdb.AllTitles`(영문복사 포함 전체 공식제목)+`TitleMatches`, `orchestrator.reattributeTMDb` — codex-fallback 값이 TMDb 공식제목과 일치하면 source 만 tmdb 승급(값 무변). `RefreshVideoTitles` 반환에 reattributed 추가. 영향 소폭(TMDb 가 es 자주 결여).
- **품질 감사(결정론, 권위 대조)**: ① person local-usage 12개 중 Wikidata 대조로 2건 교정(지성 zh_hant 地成→池晟, 김종구 金钟求→金鍾求). ② 작품 라틴 local-usage 영문복사 24건 중 TMDb 대조로 **실제 오류 1건**만 발견·교정(범죄도시5 pt_br The Roundup 5→Força Bruta 5). 나머지는 TMDb 도 영문이거나 미수록=정상. revert 로그 `verdict IN ('localusage-audit-fix','localusage-tmdb-fix')` 3건.
- **결론**: 커버리지 92.6%·charset 위반 0·실오류 3건뿐 = **품질 양호**. 전체 품질 향상의 핵심 지표는 codex-fallback 의 *자연 감소*(신규 권위데이터 유입 시)이며, 인위적 가속은 소스 한계로 비효율. ★교훈: env 키 변수명 정확히 확인(`KDB_TMDB_API_TOKEN`), codex-fallback 카운트는 오류율 아닌 출처분포 지표로 해석.

## §9 — 모니터링 인계 (새 세션이 이어서 실행, 2026-06-24 16:12 갱신)
**오너 방침: 모니터링 지속.** 새 세션은 이 섹션의 쿼리를 주기적으로 돌려 추세·헬스 확인하고,
이상 시에만 개입(honest visibility로 원인분석·대책). 전부 그린이면 한 줄 보고 후 주기 유지.

### 자율 운영 상태 (전 flag ON, .env)
`HERMES=1 FILLVERIFY=1 MATCH=1 LOCALFILL=1 REGROUND=1 ESCALATE=1 GROUND=1 STRICT=1`
autopilot 30m cycle + 자율 reground/FillVerifier/L3.5 grounding 가동 중. 누수 3경로 전부 봉인.

### Baseline (2026-06-24 16:12 KST) — 이후 추세 비교 기준
- **codex-fallback=12,726** · local-usage=272 · local-search=12 (codex-fallback 은 **자연 감소**가 정상,
  급증 시 누수 회귀 의심 → §7/§7.1 의 enrich 경로·신규유입 표본 추적)
- active=4,173 · fully_filled(8/8)=3,848(92%) · empty_foreign=84(정상범위) · candidate=73 · unknown=0
- enrichground_attempts=138 · audit-fix revert 3건

### 모니터링 1-shot (복붙 실행)
```sh
docker exec kdb-db psql -U kdb -d kdb -At -c "
SELECT 'cf='||count(*) FILTER (WHERE s='codex-fallback')||' lu='||count(*) FILTER (WHERE s='local-usage')||' ls='||count(*) FILTER (WHERE s='local-search') FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t;
SELECT 'empty_foreign='||count(*) FILTER (WHERE canonical_ja=''OR canonical_vi=''OR canonical_id=''OR canonical_es=''OR canonical_pt_br=''OR canonical_zh=''OR canonical_zh_hant=''OR canonical_en='')||' candidate='||(SELECT count(*) FROM kwave_entities WHERE status='candidate') FROM kwave_entities WHERE status='active';
SELECT 'hangul_leak='||(SELECT count(*) FROM kwave_entities WHERE status='active' AND (canonical_es~'[가-힣]'OR canonical_pt_br~'[가-힣]'OR canonical_id~'[가-힣]'OR canonical_en~'[가-힣]'));"
curl -s -o /dev/null -w 'api=%{http_code} ' http://127.0.0.1:9100/v1/health; curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'roles_ok='||count(*) FILTER (WHERE status='ok')||'/'||count(*)||' nonok='||count(*) FILTER (WHERE status<>'ok') FROM kwave_kdb_hermes_runs WHERE created_at>now()-interval '40 min';"
docker exec kdb-db psql -U kdb -d kdb -P pager=off -c "SELECT to_char(ran_at,'HH24:MI') t, duration_ms/1000 sec FROM kwave_kdb_autopilot_log ORDER BY ran_at DESC LIMIT 3;"
docker logs kdb-app --since 40m 2>&1 | grep -ciE 'panic|fatal' | sed 's/^/panic40m=/'
docker exec kdb-app sh -c "curl -s -m 8 'http://kdb-searxng:8080/search?q=test&format=json'" | python3 -c "import sys,json;d=json.load(sys.stdin);print('searxng=%d unresp=%s'%(len(d.get('results',[])),[e[0] for e in d.get('unresponsive_engines',[])]))"
```

### 정상 vs 경보
| 지표 | 정상 | 경보(원인분석) |
|---|---|---|
| codex-fallback | 횡보~완만감소 | **급증**(시간당 +10↑) = 누수 회귀 → 신규유입 표본(created 최근, notes/enrich_attempts/enrichground) 추적, `grep -rn SourceCodexFallback internal/` 로 writer 재확인 |
| empty_foreign | ~80±20 | **급증** = strict 과적용/소스 장애 |
| hangul_leak | **0** | >0 = charset 백스톱 회귀 |
| cycle duration | <1800s, skip 0 | >1800s 또는 skip 다발 = grounding/poll 과부하 → perEntity 하향 |
| hermes roles | ok(transient incident는 자가복구 확인) | 비복구 err = 역할 점검 |
| api/admin | 200 | 비200 = `docker logs kdb-app` |

### 이상 시 대응 원칙
- 역추세/회귀는 **숨기지 말고 표본으로 원인규명**([[feedback-honest-visibility]]). codex-fallback 은 *출처 미검증*지표지 오류율 아님(품질감사 §8).
- drain 가속은 소스 한계라 비효율(§8) — 인위적 캠페인 자제, 자연 감소 관찰.
- 데이터 교정은 권위 대조(Wikidata/TMDb) + revert 로그(`dataqa_log`) 필수. env 키는 `KDB_TMDB_API_TOKEN`(변수명 주의).

연관: [[reference-kdb-handoff]] [[reference-kdb-websearch]] [[reference-kdb-gemma-discovery]].
