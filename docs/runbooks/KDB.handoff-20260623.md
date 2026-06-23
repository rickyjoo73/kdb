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

연관: [[reference-kdb-handoff]] [[reference-kdb-websearch]] [[reference-kdb-gemma-discovery]].
