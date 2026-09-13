# 선택 흡수 필드·writer·기존 API 대조 — P0.05/P0.06 진행 기록

2026-09-12 KST. **부분 조사 완료, 전체 조사/고객 계약 인수 미완료.** 실행 상태 정본은 [TODO](KDB_INTEGRATION_TODO.md).

## 1. 이번에 고정한 입력

- [schema inventory](KDB_TDB_SCHEMA_INVENTORY.json): 20:40 KST KDB 24표/305컬럼, TDB 8표/82컬럼.
  컬럼/기본값/제약/인덱스/트리거만 읽었고 실제 원천 payload·자격증명은 포함하지 않았다.
- [원본 필드 매핑](KDB_TDB_FIELD_MAPPING.json): 선택된 KDB 6표+TDB 8표의 **185개 컬럼 각각**에 처리 방식을 부여했다.
  최초 v1은 포함19/조건부133/보관만32/업무 DB 제외1이었다. 후속 v2는 원본 추적과 target 채택을 분리해 포함28/조건부122/보관만34/업무 DB 제외1이다.
  PK component 18개는 원본 추적 필수이며 common UUID/코드/표기 승인은 별도 target_adoption으로 검수한다. 선택 표 범위 미매핑0, 중복 매핑0은 전체 시스템 인수와 다르다.
- 이것은 전체 원본 시스템 테이블/출처/실행 경로를 전부 조사했다는 뜻이 아니다. 나머지 표와 트리거 함수의 영향도 조사가 남아 있다.

포함은 키/상태/원본 추적을 보존한다는 뜻이고 자동 승인·외부 공급 허가가 아니다.
조건부는 정체성/출처 권리/정확 locale/형식/분류 근거를 확인한 뒤 변환한다.
보관만/업무 DB 제외는 현재 원본을 삭제한다는 뜻이 아니다. 기존 공통 DB에 이미 투영된 유효 값을 삭제하라는 뜻도 아니다.
archive_only인 원본 관측 시각은 증분 후보 탐색에 사용할 수 있지만 사실 유효일·새 검수 시각으로 복사하지 않는다.
raw payload는 원천/복구 보관 범위에 남기고 일반 운영 Entity에 복제하지 않는다.

## 2. 읽기/쓰기 경로 대조

| 시스템/경로 | 현행 책임 | 목표 처리 |
|---|---|---|
| KDB internal/kdbapi/api.go | legacy Entity/person/match/lookup/prepare/corrections 및 공통 catalog/preparations | 현재 외부 계약 보존, common adapter와 새 버전 계약으로 단계 전환 |
| KDB internal/kentity/store.go, catalog.go | 11유형/8분야 검증·공통 등록/검색 | 통제 사전/복수 직업/13유형으로 호환 확장 |
| KDB internal/kentity/mapping.go | legacy_person 원본→Entity 판정 | source_table namespace 포함, 이름 UNIQUE 연결 제거 |
| KDB internal/kentity/tdb_mapping.go | 제한 TDB shadow 후보 등록/원본 연결 | 전체 원본 binding과 필요한 필드만 일반화. shadow 10건을 전량 이전으로 계산 금지 |
| KDB internal/kdb/agents/disambiguator/merge.go | legacy 병합과 외부 ID/잠금/입력 보호 | 기존 시험 유지, 공통 operation/분리/파생값·소유 보호 추가 |
| KDB internal/kdb/readiness/* | 요청 준비·공통 이름 보충 | exact locale/복수 근거/provider 일반화·비용·철회 확장 |
| KDB migrations/0116,0118의 legacy trigger | 기존 writer가 common 투영/소유권 보호 | legacy 부모→common 부모 잠금 순서 보존, 전환된 ID에 옛 writer 재쓰기 차단 |
| TDB internal/source/wikidata.go installName | 표기 위생·우선순위·잠금 설치 | 공통 단일 writer로 규칙 재사용, 잠금 후 재검사 |
| TDB internal/match/persist.go promoteTx | installName 외 직접 names 쓰기 | 이 우회 경로까지 gate/소유권 전환 대상 |
| TDB source/ondemand.go, rulefill.go, alias_learn.go, complete.go, regions.go | 후보/규칙/별칭 직접 쓰기 | 각 호출점/트리거와 실제 writer 권한 추가 조사. 모두 자동 통합 완료로 보지 않음 |
| TDB source/namehygiene.go, latinfix.go, shopfix.go, seoul.go 등 | 소급 정정/강등/재승격 | 정정·잠금·차단 원장 및 재유입 방지 보존 |
| TDB internal/api/pipeline.go | prepare 원천 보충, correction 설치+원장 | 요청 접수·작업·결과를 분리하되 기존 클라이언트의 동기 응답 계약 유지 검증 |
| TDB internal/api/server.go | 장소 상세/변경/NDJSON export | 원본 UUID·지역 힌트·언어형식·캐시 계약을 호환 adapter로 검증 |

직접 쓰기 경로를 검색해 발견한 것은 목록화의 시작이다. 모든 호출/트리거/서비스 계정을 인수했다는 의미는 아니다.
현재 TDB 사용자 변경 파일(internal/match/*, quality/watch, 0086/0087 등)은 수정하지 않았다.

## 3. 실제 호출 증거 — 기존 API 종료 금지

20:56 KST KDB 요청 원장의 **최근 5,000행**을 bounded READ ONLY 집계했다.
기간은 표본상 9/6~9/12이며 전체 일/월 사용량이 아니다. query/body/키/고객 식별 내용은 출력하지 않았다.

| 경로 | 표본 호출 수 | 서로 다른 non-NULL 소비자 ID |
|---|---|---|
| POST /v1/entities/match | 2,036 | 1 |
| POST /v1/lookup/bulk | 1,415 | 1 |
| POST /v1/prepare | 1,180 | 2 |
| POST /v1/corrections | 280 | 2 |
| POST /v1/lookup | 55 | 1 |

그 밖에 QA/공통 preparations/catalog와 교정 조회가 있다. NULL consumer_id는 env key/내부 시험 등이 섞일 수 있어 고객 0명이라고 해석하지 않는다.
최근 호출에는 실제 연동과 시험이 섞일 수 있으므로 고객 책임자 확인이 필요하다.
TDB 소비자 원장은 15계정, 14계정 사용 시각 보유, 최신 last_used_at=2026-08-20 23:09:22 KST였다.
이 시각만으로 지금 사용하지 않는다고 단정하거나 15계정을 삭제/종료하지 않는다.
현재 사용하는 서비스 목록과 전환 확인 담당자를 사용자에게 요청했다. 답변 전 고객별 전환 승인을 가정하지 않는다.

## 4. 호환 계약표

| 기존 계약 | 확인한 내용 | 전환 전 필수 확인 |
|---|---|---|
| KDB 인증 | X-KDB-Key / Bearer, write scope 구분 | 키·scope·rate limit·owner 별 실제 고객 계약. 인증정보를 다른 DB에 무단 복제하지 않음 |
| KDB /api/* | /v1/*로 rewrite하는 alias 존재 | /api 경로 소비자도 포함해 replay |
| KDB Entity/person UUID | 별도 persons와 entity 조회가 공존 | 검수된 crosswalk로 대응, 이름 JOIN 금지, requested/resolved UUID 구분 |
| KDB 준비 | /prepare와 /preparations 별도 | 동기형 기존 응답과 실제 준비 원장을 혼동하지 않고 각 계약 유지 |
| KDB locale | readiness.Normalize에 zh/zh-cn/zh-hans 등 legacy alias | 새 exact locale API와 호환 변환을 분리하고 기존 결과 필드 회귀 확인 |
| TDB 인증 | X-TDB-Key / Bearer, consumer별 rate limit | 실제 client 식별/전환, 기존 키 인증 유지 범위 |
| TDB lookup/match | 기사 text/명시 이름·locale·region_hint | 같은 문자열의 지역/유형/동명 구분과 인지도 scan 기준 replay |
| TDB prepare | 최대40개 후보, 원천 조회 포함 동기 처리·최대5분 context | 새 비동기 job을 기존 준비 완료 응답으로 가장하지 않음 |
| TDB correction | place_id 또는 ko 기반 해소 후 media 설치·outcome 반환 | 잘못된 대상 ID·잠금·locale/권리·실제 저장결과 검증, 후보 접수/확정 구분 |
| TDB detail/export | place_id,place_type,ko,지역·names, NDJSON snapshot | UUID/canonical/aliases/원천·형식/권리·미승인 기록 처리 계약 |
| TDB changes | updated_at > since, ORDER BY updated_at, LIMIT1000, next_since | 같은 timestamp 1000초과 경계에서 누락 위험. 새 커서 (updated_at,id)와 삭제/철회·이름변경 전파를 별도 시험 |

changes의 커서 문제는 코드에서 확인한 **잠재 누락 경계**다. 실제 누락 사고를 재현했다고 주장하지 않는다.
클라이언트가 timestamp만 저장하는 경우 새 tuple cursor로 바꾸기 전에 소비자 협의/호환 replay가 필요하다.
readiness/metadata만 보고 TDB 전체 export를 common 준비 표기만으로 교체하면 데이터 양·형식이 달라질 수 있다.
미확인 이용권·잘못된 원본 링크·확정되지 않은 동명은 별도 보류하고 실제 사용 가능 범위를 명시해야 한다.

## 5. 남은 인수 자료

1. 실제 고객 서비스/담당자·필수 경로/locale·저장 UUID/커서·fallback 요구 목록.
2. 선택 원천별 이용권과 공통 재배포 허용 범위. AI Hub 포함 미확인은 보류 유지.
3. 나머지 원천/파생 표·직접 writer·트리거/계정별 영향 인벤토리, 실제 source→target 전후값.
4. 19개 TDB 실데이터 유형 및 M01~M14의 독립 정답/검수자 판정. 입력 원천을 자기 정답으로 사용하지 않음.
5. manifest 스냅샷/증분/복구 인수, 승인 batch의 정확 UUID 목록. 미검증 0123 초안은 아직 실행하지 않음.

이 자료가 채워지기 전 P0.05/P0.06/G0를 완료로 표시하지 않는다. 현재는 기존 API/서비스/DB/worker를 유지한다.

## 6. Sol high 병렬 검토와 추가 조사 — 22:33 KST 이후

- [추가 원천 조사](KDB_SOURCE_SUPPLEMENT.json): 기존 inventory 밖의 13표/161컬럼 metadata 및 컬럼별 선별 제안. 원천 행·키·config/raw payload는 읽지 않았다.
  이는 앞선 snapshot과 다른 시각이며 161컬럼의 최종 서비스 DB 매핑이 완료된 것은 아니다. 현재 효력의 권리/정정/철회 보호를 빠뜨리지 않기 위한 projection이다.
- [저장·증분 계약](KDB_WRITER_DELTA_CONTRACT.md): W01~W10 writer 경계, 잠금 후 재검사·감사 원자성·source별 delta/삭제 및 X01~X12 시험 명세.
  TDB canonical 교체 전 잠금 조회, source_code를 안 바꾸는 재승격, KDB child/외국어 표기만 변경되는 경계를 원 코드로 대조했다.
- [API 호환 시험 명세](KDB_API_COMPATIBILITY_CASES.md): 기존 client의 UUID/fallback 필드 유실·에러 봉투 불일치, route alias, locale/prepare/correction/delta 차이와 R01~R12.
  실제 Go/API replay는 아직 실행하지 않았다. 외부 고객이 어느 client/필드/커서를 사용하는지는 계속 미확인이다.
- [목표 구조 draft-v3 §14](KDB_TARGET_SCHEMA.md): 요청-준비-job UUID/버전 FK, typed evidence/no_form, policy revision/만료 차단,
  외부 ID 소유 경쟁, applied merge/redirect/reversal 결합, 부분 날짜, 반증 판정 상태 8건을 설계 보정했다.

기존 14표/185컬럼 매핑은 보존한다. 추가 정책/guard projection의 최종 target PK/FK/NULL과 전체 writer/DB 역할 대조가 남아 P0.02/P0.05는 닫지 않는다.
모든 원천 로그/수집기/메뉴를 통째로 흡수하는 안으로 돌아가지 않는다. 검수에 필요한 현재 보호 정보만 선별한다.

## 7. 원장·필드 책임·권한 및 전체 표 이름 대조

- [이관 제어 3표의 물리 명세](KDB_MIGRATION_CONTROL_SCHEMA.md): runs/records/source_guards + 기존 audit FK.
  원본 전체 typed PK·새 UUID 생성 계획·현재 기각 대상·중지/재개·승인자와 실행자 분리·복구 after-hash 보호를 정의했다.
- [필드 writer 레지스트리](KDB_FIELD_WRITER_MAP.json): 14표의 원천 경로와 분야별 callback 책임. 기존 185필드 전부 trace/target_adoption/update_owner/writer_ref/validation_cases를 가진다.
  잘못 묶인 lock/source_state·관측시각/사실시각·source deleted/master 삭제를 분리했다. 분류별 no_form 집계는 Entity의 no_form 근거가 아니다.
  kwave_entity_relations의 실제 writer는 아직 미확인이며 경로가 안 보인다고 불변으로 가정하지 않는다.
- [DB 권한 관측](KDB_WRITER_AUTHORITY.md): 23:36 조사 및 23:45 별도 확인. 관측 연결은 DB별 단일 superuser owner role, 실제 API/worker binary 귀속은 미확인.
  migration 이름/수량은 KDB72/TDB87, user trigger는16/13. migration byte 대조·실제 GRANT negative test는 아직 없다.
- [전체 table-name 범위 원장](KDB_TABLE_SCOPE.json): 23:53 READ ONLY catalog에서 KDB60/TDB27 public 일반표 이름을 확인했다.
  최초 추가 판단10표 중 8표의 코드 용도를 후속 대조해 3표는 기존 운영 유지로 분류했다. 현재 선택 원천27/기존 common21/기존 운영 유지25/기존 복구 snapshot7/추가 판단7이다. 모든 컬럼·writer·실데이터 PK 조사를 완료했다는 뜻이 아니다.
  추가 판단7에는 현행 수요3표, legacy 의존 미확인2표, 아직 대조하지 않은 kdb_audit_suspects/tmp_ld가 있다. 원본 삭제 승인이나 자동 제외가 아니다.
  tdb_source_candidates/tdb_invariants/kwave_kdb_enrich_attempts는 기존 운영을 유지하며 관련된 현재 정책·안전 조건만 따로 검토한다. 레인/정찰/과거 시도 엔진을 복제하지 않는다.
- 검사: 27개 선택 원천의 실제 PK metadata를 사용한 합성 키 예제, bigint 정밀도·text 선행0·namespace 분리 및 9개 오류 입력 거부.
  [제어 명세 검사](checks/validate-kdb-control-design.cjs)는 DB를 호출하지 않는다. 실제 FK/transaction/GRANT/race 시험은 P1 이후다.

남은 기술 인수는 source 정책 namespace/locale·우선순위, 추가 판단 원천의 현재 효력과 필요한 필드, 실행 pool별 writer 권한 및 실제 고객 replay 기준이다.
원장/필드 책임 문서 작성과 실제 운영 흡수 완료를 합산하지 않는다. P0.02/P0.05/P0.06/G0는 아직 열려 있다.

## 8. 2026-09-13 — §5 "남은 인수 자료"의 처리 결과

| §5 항목 | 처리 | 근거 | 남은 것 |
|---|---|---|---|
| 1. 고객 서비스·필수 경로·저장 UUID·커서·fallback | **인수** — 소비자 4곳(동일 운영자)·5 route·UUID 저장 확정·cursor 소비 0 | [KDB_API_COMPATIBILITY_CASES.md](KDB_API_COMPATIBILITY_CASES.md) §실사용 확인 (P0.06) | TDB 측 R03/R09/R10 → P1.01 |
| 2. 원천별 이용권·재배포 범위 | **기본 차단으로 고정** — source_policies 전부 unreviewed, 검토 대상 3군, aihub 차단 유지 | [KDB_ACCEPTANCE_LIMITS.md](KDB_ACCEPTANCE_LIMITS.md) §5.5 (P0.09) | approved 는 새 version INSERT 시점에 |
| 3. 원천/파생 표·writer·트리거·계정 인벤토리 | **인수** — 87표 범위 확정, 22표+뷰 차이표, 트리거 16·함수 20·역할 1 관측, 권한 매트릭스 | [KDB_SCHEMA_DIFF_0115_0122.md](KDB_SCHEMA_DIFF_0115_0122.md), [KDB_WRITER_AUTHORITY.md](KDB_WRITER_AUTHORITY.md) §물리 권한 매트릭스 (P0.02/P0.05) | kwave_entity_relations writer 미확인(0행), idle 세션 binary 귀속 → P1.01 application_name |
| 4. 19유형·M01~M14 독립 정답/판정자 | **인수** — 정답은 구성(M) 또는 2 자율 판정기 합의+근거 provider 분리(T). 운영자는 escalation | [KDB_ACCEPTANCE_FIXTURES.md](KDB_ACCEPTANCE_FIXTURES.md) (P0.08) | 실행 P1.04/05·P2.07 |
| 5. manifest·증분·복구 인수, 승인 batch UUID | 계약은 **인수**(제어 3표), UUID 목록은 P2.08 산출물 | [KDB_MIGRATION_CONTROL_SCHEMA.md](KDB_MIGRATION_CONTROL_SCHEMA.md) | 0123 초안 채택/폐기 → P2.03 |

§5 의 "이 자료가 채워지기 전 P0.05/P0.06/G0 를 완료로 표시하지 않는다"는 조건은 위 표로 충족됐다. 남은 것은 전부 P1 이후 실행 항목이며 P0 설계 의존이 아니다.
