# KDB 통합 검증 분모·파일럿·중지 기준 — P0.09 설계안

2026-09-12 · `acceptance-design-v1`. 수치는 **설계상의 시험 조건**이며 실측 처리량/실제 품질 결과가 아니다.
실행 상태: [TODO](KDB_INTEGRATION_TODO.md). 기존 M01~M14/G0~G7을 대체하거나 축소하지 않는다.

## 1. 분모와 집계 계약

| 지표 | 고정 분모 / 분자 | 주의 |
|---|---|---|
| 조사 계상 | snapshot manifest의 모든 원본 PK / 포함·조건부·보관·제외 중 하나로 설명된 PK | 대기/오류를 분모에서 빼지 않음 |
| 실제 흡수 | 승인된 batch의 객체 PK / 목표 값·원본 binding·근거 대조가 끝난 객체 | 원장에 ID만 있다는 것은 흡수 완료가 아님 |
| 분류 완성 | 합의한 활성 서비스 대상 / 유형·subtype·분야·인물 직업/해당 없음이 검증된 UUID | candidate/unknown 수와 사유도 별도 공개 |
| 정체성 오류 | 독립 판정한 linking 표본 / 잘못 연결된 건 | 표본 0건을 전체 오류0으로 표현하지 않음 |
| 언어 준비 | 실제 유효 요청의 item×requested_locale / strict-ready 조건을 모두 충족한 칸 | 테스트·취소·fallback·generated·translated 별도 집계 |
| 보충 수율 | 해당 회차 실제 원천/모델 호출 / 새 검증 이름 및 새 준비 칸 | 동일 결과 재기록·job 종료를 성과로 계산 금지 |
| 지연/비용 | 실제 요청/작업 표본 / p50·p95·p99·최장 대기·호출/토큰·오류 원인 | 미계측은 0이 아님. 기사 길이·언어·모델 분포 함께 기록 |

snapshot 기준 시각/schema/선별 정책/mapper 버전/조회 범위를 manifest에 고정한다.
원본 총수는 계속 변하므로 시각이 다른 집계를 더하거나 이전 수량을 현재 수량이라고 보고하지 않는다.
제외·보류는 사유 코드+책임자+재검토 조건을 반드시 기록한다. 원본 삭제는 이 기준에 포함하지 않는다.

## 2. 단계별 시험 범위

- P1: M01~M14 DB/API 범위와 모든 새 FK/유일성/기간/직업/동명/권리·late job/rollback 사례.
  합성 데이터는 안전성 정답을 통제하는 용도다. 실제 인물 분류 품질의 정답으로 사용하지 않는다.
- P2: 실제 TDB 데이터가 있는 19유형별 최소10개(있으면)/전체가10개 미만이면 전수와 M01~M14 경계 표본.
  source/locale/form/권리와 정상·상충·other·QID 없음·원본 삭제/병합을 층화한다. 한 대량 출처만으로 전체를 대표하지 않는다.
- 필수 한정 정답 세트는 실제 검수자가 입력 원천과 독립된 근거로 확정한다. 애매한 표본은 억지 정답을 만들지 않고 보류다.
- 첫 운영 batch 제안: 독립 검수·권리·분류가 통과한 최대100 Entity, 계획상 변경 객체 최대1,000개.
  고위험 병합은 별도 한 쌍/500객체 제한이며 첫 흡수 batch에 자동 섞지 않는다.
- 다음 확대는 최대1,000 Entity씩 하되 이전 batch 계상/오류/호환/복구를 통과한 경우만 허용한다.
- P6: 최소24시간 + 실제 정규 worker/야간 주기. 관련 주기가24시간보다 길면 그 주기까지 관측한다.
  시간 경과 없이 로그를 복사하거나 테스트 clock으로 실제 운영 관측을 대체하지 않는다.
- P7: 실제 기사 길이/언어/피크 분포, 1대 장애, 중복/수정/취소·구버전 반환/정정/E2E.
  현재 서버당6동시/30초 가정은 부하 시험 전 보장값이 아니다.

## 3. 통과·중지 기준

즉시 중지/격리: 동명 간 값/근거 교차 저장 1건, 잘못된 source binding 1건, 권리 차단 우회 1건,
설명 없는 원본 손실/중복 처리 1건, 롤백 실패 1건, 오래된 job이 새 UUID/버전을 덮어쓴 사례 1건,
기존 고객의 승인되지 않은 응답/ID/커서 변경 1건.
경고를 누적해 평균으로 통과시키지 않는다. 원인→재현→보정→해당 전체 회귀 시험 후에만 확대한다.

운영 부하 중지 제안: 관련 API 5xx가 5분 동안 기준 대비 1%p 이상 증가하거나
p95가 같은 질의/표본 조건의 사전 기준 대비 20% 이상 악화하면 새 batch를 멈춘다.
표본이 적어 판정 불가하면 통과가 아니라 추가 관측이다. 절대 SLO는 실제 고객 계약 확인 후 별도 고정한다.
DB lock timeout/장기 대기, 디스크 부족, invalidation 적체가 발생하면 원인 확인 전 적재를 늘리지 않는다.

원천 보충 파일럿은 provider별 동시1/초당1호출 이하, 하루100호출을 상한 제안으로 둔다.
원천 약관·기존 허용 한도가 더 낮으면 더 낮은 값이 우선한다. 첫 실행 전에 실제 예산/스케줄 승인 범위를 기록한다.
모델 호출은 정책/정답/비용 계측이 준비되지 않은 provider에 자동 활성화하지 않는다.
테스트 때문에 현재 Gemma 야간 스케줄/동시성을 변경하지 않는다.

## 4. 역할·보류 책임

| 판단/실행 | 책임 | 지금 상태 |
|---|---|---|
| 코드/스키마/격리 시험·회귀·계상 | 개발 담당 | 상세 설계/조사 진행 |
| 실제 동일인/별개·공식 표기·19유형 정답 | 사용자 지정 데이터 검수자 | 판정자/실표본 인수 필요 |
| 원천 이용권/재배포 범위 | 사용자 지정 권리 책임자 | AI Hub 등 미확인 출처 보류 |
| 고객 API/ID/커서 전환·자동 발행 | 각 서비스 책임자 | 사용 서비스/담당자 확인 요청 |
| batch 대상/복구·배포·운영 인수 | 운영 책임자 | G0~G2 및 정확 manifest 인수 이후 |

작업자는 기술적 시험 결과를 기록할 수 있지만 타인의 권리·고객 승인·실제 운영 관측을 대신 완료 선언하지 않는다.
아직 책임자가 정해지지 않은 판단은 자동승인 기본값을 만들지 말고 보류한다.
P0.09 설계 문서 작성 완료와 수치·담당자의 실제 운영 인수는 구분한다.

## 5. 고정값 — 2026-09-13 (P0.09)

§1~§4 는 계약과 규칙이고, 이 절은 운영 실측으로 채운 **숫자와 범위**다. 출처는 운영 KDB/TDB READ ONLY 집계(요청 로그 최근 30일, 카탈로그, 행수)이며 DDL·데이터 변경은 없다.
실행 상태 정본은 [KDB_INTEGRATION_TODO.md](KDB_INTEGRATION_TODO.md), fixture 는 [KDB_ACCEPTANCE_FIXTURES.md](KDB_ACCEPTANCE_FIXTURES.md), 소비자 계약은 [KDB_API_COMPATIBILITY_CASES.md](KDB_API_COMPATIBILITY_CASES.md) §실사용 확인이다.

### 5.1 분모·기준선

| 지표 | 분모 / 기준선 (2026-09-13 실측) | 출처 |
|---|---|---|
| API 지연 (30일, ms) | lookup/bulk n=10,518 p50 49 · p95 472 · p99 1,011 / entities/match n=6,811 p50 1,306 · p95 3,346 · p99 5,155 / prepare n=6,497 p50 453 · p95 2,500 · p99 4,781 / corrections n=1,682 p50 339 · p95 1,002 · p99 1,495 / lookup n=311 p50 309 · p95 930 · p99 1,133 | kwave_kdb_api_requests |
| 5xx 율 (30일) | corrections 0.119 %, 그 외 4 route 0.000 % | 동일 |
| 요청량 | 일 평균 873건, 소비자 4곳 | 동일 |
| 언어 준비 분모 | 최근 30일 legacy prepare 6,497 요청 · 15,936 term → item×locale(주요 8) ≈ 127,488 칸. 신경로 kentity_preparations 60건(review 56) | request_terms, kentity_preparations |
| 분류 완성 분모 | kentity_entities active 12,652 (work 6,435 · person 5,068 · organization 1,149), candidate 526, rejected 6,340 | kentity_entities |
| 정체성 오류 분모 | kentity_crosswalks 5,701 = legacy_person review 5,700 + tdb review 1, confirmed 0 | kentity_crosswalks |
| 조사 계상 분모 | TDB tdb_places 536,329 (19유형) / KDB kwave_entities 19,515 / kentity_entities 19,519 | 카탈로그 |
| 보충 수율 분모 | 현행 채워진 locale 칸 중 tier 7·8(생성·기계번역) ≈ 63,500칸은 strict-ready 제외 — 회귀 아님 | KDB_SCHEMA_DIFF §9 |
| 용량 | DB 470 MB · backups/ 1.2 GB(03:00/12:00 자동) · /data 440 G 사용 / 515 G 여유(47 %) | 서버 |
| 비용 상한 | Gemma 00~05 KST, gemma4:26b, 동시 12 — **변경 금지**. gtranslate 월 400,000자 cap, 당월 3,021자 | RESUME, gtranslate.go |

### 5.2 파일럿 범위 — G0 승인 범위 제안

| | 파일럿 A (KDB 내부) | 파일럿 B (TDB 최소 앵커) |
|---|---|---|
| 대상 | active person 5,068 의 person_roles 채움 (원천 kwave_entity_person_details, 운영자 소유) | tdb_places.admin_region 249행 **전수** (QID 249/249) |
| 1차 batch | ≤100 Entity · ≤1,000 변경 객체 (§2). QID 보유 3,561 중 primary_role≠other 3,353 우선 | 249 전수 (단일 batch, ≤1,000 객체) |
| 채우는 것 | person_roles(verified 는 근거 있을 때만), classification_status | crosswalk(tdb,tdb_places,id) + location_profiles admin 코드 + external_ids(wikidata) |
| 제외 | 이름·표기 변경 없음, 병합 없음 | 이름 텍스트(tourapi 등) — 정책 승인 전 제외. identity·코드만 |
| 검증 | M01 M03 M09 M11, X03 | M05 M10, T19, D-02 완화 확인 |
| 권리 | 외부 원천 없음 | wikidata QID(정책 검토 후), 행정코드(공공 표준) |

**1차 제외**: aihub_tour 링크 행(권리 미확인, 링크 1,397,918), `other` 106,602, `restaurant` 145,433(qualifier_ko·guards 가동 전), TDB `person` 31,378(identity_decisions 가동 전), 모든 병합 작업(§2 별도 한 쌍/500객체).
**확대 순서 제안**: admin_region → legal_dong 4,912 → cultural_facility 5,943 → heritage 13,888(QID 1,675) → … QID 비율·규모 순. batch 당 ≤1,000 Entity, 이전 batch 계상·복구·호환 통과 후만(§2).

### 5.3 표본 수

- M01~M14: 14 fixture 전부, P1.04/P1.05 (단언 2개 이상씩, 합성).
- T01~T19: 층 5 × 층당 10(미만이면 전수) × 19 ≤ 950 + T02 `other` 50 → **최대 약 1,000행**, P2.07.
- 정체성 linking 독립 판정: **200건** = legacy_person 199(층화) + tdb 1(전수). 5,701 의 3.5 %. 오류 **1건이면 즉시 중지**(§3) — 표본 0건을 전체 0으로 표현하지 않는다.
- 언어 준비: 최근 30일 실요청 6,497건 전수를 P6 24h 관측의 대조군으로 쓴다.

### 5.4 확대·중지 기준 — 수치화

§3 의 즉시 중지 목록은 그대로다. 운영 부하 기준을 5.1 기준선으로 고정한다.

| 조건 | 중지 수치 |
|---|---|
| 5xx (5분 창) | corrections ≥ 1.12 %(0.119 + 1.0 p), 그 외 route ≥ 1.0 % |
| p95 (+20 %) | lookup/bulk > 566 ms · entities/match > 4,015 ms · prepare > 3,000 ms · corrections > 1,202 ms · lookup > 1,116 ms |
| 저장 | /data 여유 < 200 GB, 또는 백업 1회 실패 → 적재 중지 |
| 무효화 적체 | invalidation_outbox 미처리 > 1,000 또는 최고령 > 24 h → 중지 |
| 잠금 | lock wait > 30 s 1건 → 원인 확인 전 확대 금지 |
| 외부 호출 | provider 동시 1 · 초당 1 · 일 100 (§3). 약관이 더 낮으면 그 값 |
| 모델 | Gemma 스케줄·동시성, gtranslate cap 변경 없음 |

### 5.5 보류 책임자·권리 차단 기본값

- **책임 구조**: 판정은 자율 판정기(fixture §1 의 7 경로)가 하고, 두 판정기가 불일치하거나 근거가 상충할 때만 운영자가 결정한다. 4 소비자 사이트·운영·권리의 결정 주체는 **운영자 단일**이다(P0.06 실측: 동일 호스트·동일 운영자). 외부 담당자 대기 항목은 없다.
- **권리 차단 기본값**: `kentity_source_policies` 는 seed 0. 모든 provider 는 `status=unreviewed`, `storage_allowed/verification_allowed/name_export_allowed/excerpt_export_allowed = false` → **공급 차단이 기본**. approved 는 검토자·시각·terms_url·조건을 갖춘 **새 version INSERT** 만(구조 §12.1, S03). unreviewed 행 UPDATE 승격 금지.
- **1차 검토 대상(승인 아님)**: wikidata(CC0 정책 문서 확인 후 approved INSERT), operator/correction/media-consensus(운영자 내부 근거), TDB 운영자 소유 행의 identity·행정코드.
- **차단 유지**: aihub_*(이용권 미확인), rss-observation 7 도메인(도메인별 미검토), tier 7 검색·규칙 변환(namuwiki·baidu-baike·gemini-search 등), tier 8 기계번역은 `translated` 로만 보존.
- 보류 판단은 TODO 원장 §8 과 source_policies 행에 남긴다. 자동승인 기본값은 어디에도 없다.

### 5.6 §4 역할표의 현재 상태

| 판단/실행 | 책임 | 2026-09-13 상태 |
|---|---|---|
| 코드/스키마/격리 시험 | 개발 담당 | P0 설계 인수, P1 대기 |
| 동일인/별개·공식 표기·19유형 정답 | 자율 판정 2경로 합의 + 운영자 escalation | **P0.08 인수** |
| 원천 이용권/재배포 | 운영자 | **기본 차단 고정**, 검토 대상 목록 5.5 |
| 고객 API/ID/커서 전환 | 운영자(4 사이트 동일 운영) | **P0.06 실측 인수** |
| batch 대상/복구·배포 | 운영자 | G0~G2 후 5.2 범위에서 승인 |

이 절의 숫자는 2026-09-13 기준선이다. P1 격리 시험·P2 dry-run 결과로 갱신하되, 갱신 전 값은 이 절에 남긴다.
