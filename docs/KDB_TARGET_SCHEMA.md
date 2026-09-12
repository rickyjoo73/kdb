# KDB 목표 물리 구조 — P0.02 핵심 데이터 사전 초안

작성: 2026-09-12 KST · 버전: `target-schema-draft-v4`.
기준: [통합 기획안](KDB_TDB_INTEGRATION_BLUEPRINT.md), [분류 사전](KDB_CLASSIFICATION_RULES.md), [실행 TODO](KDB_INTEGRATION_TODO.md).
상태: **핵심 Entity/분류 구조 상세 설계 진행 중. P0.02 전체 완료 아님. 운영 적용 SQL 아님.**
초기 미결 목록은 §9, 후속 상세는 §10~§15다. 전체 원본/고객/권한 대조와 최종 인수는 아직 남아 있다.
테이블 이름만 정해진 영역은 물리 설계 완료로 계산하지 않는다.
후속 상세: [정체성 계약](KDB_IDENTITY_CONTRACT.md), [표기·준비 계약](KDB_NAME_READINESS_CONTRACT.md).
§10~§13은 두 계약에 필요한 물리 구조 차이를 추가한다. 전체 P0.02 인수는 원본 필드/소비자 계약 대조 후다.

## 1. 조사 기준과 설계 범위

2026-09-12 20:21 KST KDB/TDB PostgreSQL 16.14의 READ ONLY metadata와
KDB `migrations/0115~0122`, `internal/kentity/store.go`, `catalog.go`, `tdb_types.go`를 대조했다.
0115~0122에서 구현된 UUID/근거 귀속/소유권/보충 보호를 보강한다. 원본 DB를 합쳐 새로 구축하는 작업이 아니다.

| 확인된 현행 | 목표 변경 | 이유 |
|---|---|---|
| common entity_type 11값 CHECK, Go 허용목록 | 기존 코드 보존, 사전 FK + brand/character 호환 확장 | 명시적 분류, 잘못된 person/work 투영 방지 |
| subtype text NOT NULL DEFAULT '' | subtype NULL 허용 + (type,subtype) FK | 미확인과 잘못된 조합을 구별 |
| legacy person primary_role/secondary_roles | 공통 person_roles N:M | 복수 직업과 UUID별 값 소유 |
| kwave_persons.name_ko UNIQUE | 새 공통 인물 연결에 승계 금지 | 같은 이름의 다른 사람 수용 |
| names: 단일 evidence_id, 자체 잠금 없음 | 복수 근거 연결·행 잠금·기간 보호 보강 예정 | 동명 간 증거 복사/수동 정정 덮어쓰기 방지 |
| crosswalk source_system/source_id + TDB shadow 결합 | source table을 포함한 binding 계약 보강 예정 | legacy 두 인물 테이블, QID 없는 TDB 원본도 구분 |
| locale fill의 qid NOT NULL | provider 중립 입력/정체성 버전 보강 예정 | QID 없는 유효 대상도 보충 가능해야 함 |

현재 evidence.claim_type의 CHECK는 identity/name/relation만 허용한다. 아래 분류 근거용 참조를
실제 적용하기 전에 P0.04에서 classification/occupation 등 주장 범위와 연결 구조를 확정해야 한다.
기존 identity 근거 하나로 모든 직업/분야를 증명한 것으로 처리하지 않는다.

### 최소화 기준

새 직업/분류 사전·연결은 필수다. 정치인/기업인/배우/선수별 별도 profile DB는 만들지 않는다.
회사·팀·상품은 공통 Entity + 이름 + 외부 ID + 관계를 먼저 쓴다.
인물 생년/장소 주소·좌표 등 동명 구별에 필요한 속성만 별도 최소 구조를 검토한다.
TDB 전체 raw payload/에이전트/레인/정찰 포털/과거 로그·통계는 운영 테이블로 복제하지 않는다.
흡수 manifest·복구 보관과 서비스 master를 구분한다. 원본 삭제는 본 설계 범위가 아니다.

## 2. 핵심 ERD

```text
types ───────────┐        role_types (얕은 부모/자식)
  └─ subtypes ──┤                   │
                ▼                   ▼
             entities ─────── person_roles ─── evidence
                │
       ┌────────┼─────────┬──────────┬────────────┐
       ▼        ▼         ▼          ▼            ▼
 entity_domains names  external_ids relations   source bindings
       │        │         │          │            │
    domains   evidence  evidence   대상 Entity  원본 namespace/ID

identity decisions / redirects / audit
    → UUID 판정·병합·분리 이력 (상세는 후속 P0.03)
preparations / readiness / fill jobs
    → 정확 UUID·locale·정책·revision (상세는 후속 P0.04)
```

이는 논리 관계도다. 아래 §4~§6만 현 단계에서 컬럼 단위로 정리했다.
names/relations/binding 등 화살표가 있다고 해당 영역 설계를 완료한 것은 아니다.

## 3. 컬럼 표기·공통 책임

- `NN`: NOT NULL, `?`: NULL 가능. `—`: DB 기본값 없음, 호출자가 명시한다.
- UUID 신규 기본값은 `gen_random_uuid()`다. 기존 KDB Entity UUID는 보존한다.
- `created_at/updated_at`: timestamptz, 관측/검수/유효 기간과 별개다.
- `revision`: bigint > 0. writer가 기대 revision 확인 후 1 증가시킨다. 자동 updated_at만으로 동시성 보호를 대체하지 않는다.
- 명칭/이유/코드는 btrim 후 비어 있지 않아야 한다. 이름은 전역 UNIQUE가 아니다.
- 사전 PK 코드는 ASCII 소문자/숫자/밑줄, 길이 1~64. 타입·분야·직업 코드 변경은 migration 검토 대상이다.
- 원본·증거·판정 FK는 기본 ON DELETE RESTRICT, ON UPDATE NO ACTION이다. 원본 삭제와 Entity 물리 삭제를 연결하지 않는다.
- 근거의 같은 대상 보호는 `(evidence_id, entity_id) → evidence(id, entity_id)` 복합 FK로 수행한다.
  evidence UUID 존재만 확인하는 단일 FK로 약화하지 않는다.
- 다른 행/테이블의 승인 상태는 CHECK 함수로 조회하지 않는다. FK/UNIQUE/EXCLUDE로 표현할 수 없는 조건은
  보호된 writer와 부모 행 잠금, 필요 시 지연 제약 트리거로 양쪽 변경을 검사한다.
  PostgreSQL의 [제약 문서](https://www.postgresql.org/docs/16/ddl-constraints.html)가 설명하는 CHECK의 행 간 제한을 따른다.
- 앱의 일반 worker는 임의 분류/근거 승인·사전 변경 권한을 갖지 않는다. 승인과 제안 저장 API를 분리한다.

## 4. 분류 사전: 신규 3개 + 기존 분야 사전 보강

### 4.1 `kentity_types` — 신규

사용처: 등록 유형 선택, DB FK, API 허용값. 갱신 책임: 검토된 schema/사전 migration.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| code | text NN | — | PK; 분류 사전의 13코드 |
| label_ko | text NN | — | 표시명 |
| enabled | boolean NN | false | 승인된 seed만 true. 신규 코드 자동 노출 금지 |
| sort_order | integer NN | 0 | >=0 |
| revision | bigint NN | 1 | >0 |
| created_at | timestamptz NN | now() | 생성 시각 |
| updated_at | timestamptz NN | now() | 변경 시각 |

추가 인덱스 없음. 13행 규모를 위해 불필요한 인덱스를 만들지 않는다.
unknown은 검수 화면에서 사용할 수 있지만 활성 서비스 검증에서는 거부한다.

### 4.2 `kentity_subtypes` — 신규

사용처: 유형에 맞는 세부유형만 등록/검색. 갱신 책임: 사전 migration.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| entity_type | text NN | — | FK types(code) |
| code | text NN | — | PK(entity_type,code) |
| label_ko | text NN | — | 표시명 |
| parent_code | text ? | NULL | FK(entity_type,parent_code)→subtypes(entity_type,code); 1차 모두 NULL |
| enabled | boolean NN | false | 비활성 신규 부여 거부 |
| sort_order | integer NN | 0 | >=0 |
| revision | bigint NN | 1 | >0 |
| created_at | timestamptz NN | now() | 생성 시각 |
| updated_at | timestamptz NN | now() | 변경 시각 |

자기 부모/다른 유형 부모 금지. 계층 추가 시 사전 변경 트랜잭션을 직렬화해 순환·깊이를 검사한다.
기존 자유 subtype은 선매핑 결과가 있어야 FK 검증 단계로 넘어간다. 충돌을 해결하려고 사전에 원문 문자열을 전부 넣지 않는다.

### 4.3 `kentity_role_types` — 신규

사용처: 인물 직군/직업과 하위 포함 필터. 갱신 책임: 사전 migration.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| code | text NN | — | PK |
| label_ko | text NN | — | 표시명 |
| parent_code | text ? | NULL | FK role_types(code), 자기 부모 금지 |
| enabled | boolean NN | false | 비활성 신규 부여 거부 |
| sort_order | integer NN | 0 | >=0 |
| revision | bigint NN | 1 | >0 |
| created_at | timestamptz NN | now() | 생성 시각 |
| updated_at | timestamptz NN | now() | 변경 시각 |

상위→하위 포함은 작은 사전의 recursive 조회/버전별 메모리 캐시로 처리한다. closure table은 1차에 만들지 않는다.
부모 변경은 전체 사전 잠금 안에서 순환/최대 3레벨을 검증한다. 단순 자기 참조 CHECK만으로 순환 방지 완료라고 하지 않는다.

### 4.4 `kentity_domains` — 기존 보강

사용처: 공통 복수 분야 필터. 갱신 책임: 사전 migration. 현재 code/label_ko를 보존한다.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| code | text NN | — | 기존 PK |
| label_ko | text NN | — | 기존 표시명 |
| enabled | boolean NN | false | 신규; 기존 8코드는 검증 후 true |
| sort_order | integer NN | 0 | 신규 >=0 |
| revision | bigint NN | 1 | 신규 >0 |
| created_at | timestamptz NN | now() | 신규; 과거 실제 생성 시각이라고 주장하지 않음 |
| updated_at | timestamptz NN | now() | 신규 |

계층/분야별 workflow 설정 테이블은 만들지 않는다. 분야의 준비도는 실제 데이터/locale 결과에서 집계한다.

## 5. `kentity_entities` — 기존 보강, 공통 UUID 원본

사용처: 모든 조회/등록/판정/보충의 ID 기준. 갱신 책임: **현재 write_owner에 맞는 보호된 writer**.
원본 adapter는 소유권 전환 전에도 자동 승인 권한이 없고, 전환 후에는 공통 writer를 우회하지 못해야 한다.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| id | uuid NN | gen_random_uuid() | 기존 PK. 변경/이름 기반 재생성 금지 |
| entity_type | text NN | — | 기존 CHECK를 types FK로 호환 전환 |
| subtype | text ? | NULL | 기존 ''→선매핑/NULL, 복합 FK(entity_type,subtype) |
| canonical_ko | text NN | — | 기존, 비어 있지 않은 관리/호환 대표 표시값. 그 자체는 검증 증거가 아님 |
| origin_system | text NN | — | kdb/native/tdb. 최초 유입 기록, writer 권한과 별개 |
| write_owner | text NN | native | kdb/native/tdb; 전환은 승인된 소유권 API만 |
| status | text NN | candidate | active/candidate/rejected/retired; 목표 신규 기본값 명시 |
| classification_status | text NN | pending | 신규: pending/conflict/verified |
| classification_reason | text NN | awaiting_classification | 신규, 사유 코드/설명. 승인 시에도 판정 이유 보존 |
| classification_evidence_id | uuid ? | NULL | 신규, 같은 Entity 근거 복합 FK |
| classification_policy_version | text NN | classification-design-v1 | 신규, 구현 채택 시 실제 배포 정책 버전으로 seed 변경 |
| classified_by | text ? | NULL | 신규, 검수/승인 정책 주체 |
| classified_at | timestamptz ? | NULL | 신규, 분류 판정 시각. 원천 관측 시각 복사 금지 |
| operator_locked | boolean NN | false | 기존. 자동 작업 차단, 검수자도 명시적 해제 절차 필요 |
| revision | bigint NN | 1 | 기존 >0. 의미 있는 분류/잠금/표기/근거 영향 때 증가 |
| created_at | timestamptz NN | now() | 기존 |
| updated_at | timestamptz NN | now() | 기존 |

PK(id) 외에 UNIQUE(id,entity_type)를 추가해 person 전용 자식 FK를 지원한다.
`(entity_type,subtype) → subtypes(entity_type,code)`는 subtype NULL이면 후보 수용을 허용한다.
verified는 type!=unknown, subtype 존재, 근거/판정자/판정 시각 존재가 행 자체의 필수 조건이다.
분야 1개 이상·인물 직업 완성·근거 유효성은 아래 연결 변경과 함께 부모 잠금 안에서 다시 검증한다.
최초 전환에서 legacy active 전체를 즉시 새로운 CHECK로 강제해 서비스 장애를 만들지 않는다.
P2에서 분류/호환 범위를 판정하고 P5에서 신규 활성 기준 적용 대상을 전환한다.

인덱스:

- 기존 (canonical_ko,entity_type)는 **비유일**로 유지하고 실제 사용 계획 확인 후 조정.
- 목록: (status,updated_at,id), 유형 필터: (entity_type,subtype,status,id).
- 분류 검수함: (classification_status,updated_at,id) WHERE classification_status!='verified'.
- 이름 정규화/다국어 검색 인덱스는 names 설계와 묶는다. canonical_ko의 substring 검색만으로 최종 동명 검색을 완성하지 않는다.

canonical_ko는 기존 호환 표시 캐시다. 승인된 한국어 이름이 있으면 같은 트랜잭션에서 투영한다.
후보 관리용 canonical_ko가 있다고 한국어 strict-ready를 반환하지 않는다.
source 정정/원본 이름 변경도 원천 binding 없이 이름으로 Entity를 찾아 덮어쓰지 않는다.

## 6. 분류 연결

### 6.1 `kentity_person_roles` — 신규

사용처: 복수 직업·동명 비교·직업별 검색. 갱신 책임: 공통 분류 writer/승인 경로.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| id | uuid NN | gen_random_uuid() | PK |
| entity_id | uuid NN | — | FK entities(id), 같은 대상 evidence 참조 기준 |
| entity_type | text NN | person | CHECK='person', FK(entity_id,entity_type)→entities(id,entity_type) |
| role_code | text NN | — | FK role_types(code) |
| status | text NN | unverified | unverified/verified/withdrawn/blocked |
| evidence_id | uuid ? | NULL | FK(evidence_id,entity_id)→evidence(id,entity_id) |
| valid_from | date ? | NULL | 근거로 확인된 직업 기간 시작 |
| valid_until | date ? | NULL | 포함되는 마지막 날. 종료일>=시작일 |
| assigned_by | text NN | — | 제안/판정 주체 |
| reason | text NN | — | 판정 이유 |
| policy_version | text NN | — | 분류 정책 버전 |
| verified_by | text ? | NULL | 승인 주체 |
| verified_at | timestamptz ? | NULL | 승인 시각 |
| operator_locked | boolean NN | false | 자동 변경 차단 |
| revision | bigint NN | 1 | >0 |
| created_at | timestamptz NN | now() | 생성 시각 |
| updated_at | timestamptz NN | now() | 변경 시각 |

verified이면 evidence_id/verified_by/verified_at 필수. evidence 자체가 유효한지도 잠금 후 재확인한다.
UNIQUE(id,entity_id)는 후속 복수 근거 연결/필드 잠금이 같은 대상인지 참조할 때 사용한다.
검증된 동일 직업의 중복 기간은 `entity_id =, role_code =, possible_validity &&` (§14.7의 보수적 기간)
부분 EXCLUDE(status='verified')로 차단하는 안을 채택한다. `btree_gist` 제공/설치 가능 여부는 P1 격리 DB에서 검증한다.
이는 PostgreSQL의 [범위 중첩 배제 방식](https://www.postgresql.org/docs/16/rangetypes.html#RANGETYPES-CONSTRAINT)에 따른다.
NULL 날짜는 중복 방지에서는 열린 경계로 취급해 보수적으로 충돌시킨다. **생애 전체에 그 직업이었다는 증거로 해석하지 않는다.**
완전히 모르는 기간의 같은 직업 제안은 기존 행에 근거를 보강하거나 검수한다. 임의 날짜로 쪼개 EXCLUDE를 우회하지 않는다.
서로 다른 직업(singer,actor)은 동시에 허용한다. 서로 다른 UUID이면 같은 직업/이름/기간도 정상이다.
날짜는 유한한 0001-01-01~9999-12-31 범위로 제한하며 월/연도만 알려진 자료에서 일자를 만들어 넣지 않는다.
불완전 날짜의 저장/시점 판정과 관계 기간은 §14.7의 공통 표현을 사용한다.

인덱스: PK, UNIQUE(id,entity_id), (entity_id,status), (role_code,entity_id) WHERE status='verified',
evidence 철회용 (evidence_id,entity_id) WHERE evidence_id IS NOT NULL.
유형을 person 이외로 바꿀 때 자식 복합 FK가 남은 직업 행을 거부한다. 자동 ON UPDATE CASCADE로 우회하지 않는다.
유형 정정 시 직업 이력의 감사 보존/격리 이동 절차는 P0.03에서 설계해야 한다.

### 6.2 `kentity_entity_domains` — 기존 보강

사용처: 복수 분야 연결·필터·분류 완성 확인. 갱신 책임: 공통 분류 writer.

| 컬럼 | 타입/NULL | 기본값 | 키/규칙 |
|---|---|---|---|
| entity_id | uuid NN | — | 기존 FK entities(id) |
| domain | text NN | — | 기존 FK domains(code), PK(entity_id,domain) |
| assigned_by | text NN | — | 기존 |
| reason | text NN | — | 기존 |
| created_at | timestamptz NN | now() | 기존 |
| status | text NN | unverified | 신규: unverified/verified/withdrawn/blocked |
| evidence_id | uuid ? | NULL | 신규, 동일 대상 evidence 복합 FK |
| policy_version | text NN | — | 신규, 실제 writer 정책 버전 필수 |
| verified_by | text ? | NULL | 신규 |
| verified_at | timestamptz ? | NULL | 신규 |
| operator_locked | boolean NN | false | 신규 |
| revision | bigint NN | 1 | 신규 >0 |
| updated_at | timestamptz NN | now() | 신규 |

verified의 근거/판정자/시각 필수. 기존 assigned_by/reason이 있다는 이유로 소급 승인하지 않는다.
분야는 이 표의 현재 판정 1행 + audit 전후 이력으로 관리한다. 직업/재임처럼 모든 분야에 기간 테이블을 늘리지 않는다.
인덱스: 기존 PK + (domain,entity_id) WHERE status='verified', evidence 철회용(evidence_id,entity_id).
마지막 승인 분야 철회 시 Entity 분류 상태를 같은 트랜잭션에서 pending/conflict로 낮추고 준비 결과를 무효화한다.
구체 철회 전파 순서와 캐시 버전 계약은 P0.04와 함께 검증한다.

## 7. 쓰기 순서와 필수 DB 시험 — 설계 요구사항

단일 Entity 분류 쓰기의 최소 순서:

1. 인증/분류 권한 확인 → Entity UUID로 행 잠금 → 기대 revision/write_owner/operator_locked 재확인.
2. 코드 존재·활성/상하 관계, 같은 Entity의 근거·권리·판정 상태를 확인한다.
3. 변경 전후를 UUID 기준으로 기록하며 직업/분야/분류를 갱신한다. 유형 변경은 영향 참조를 먼저 검사한다.
4. 분류 완성 조건을 다시 계산 → revision 증가 → 감사 기록 → 관련 준비/캐시 무효화 원장 갱신 → commit.
5. 실패하면 전부 rollback. 성공 응답은 영향 행 수/새 revision을 검증한 후 보낸다.

동일인 병합·분리처럼 여러 UUID를 잠그는 계약은 P0.03에서 별도로 정의한다.
사전 비활성화/근거 철회도 동일한 승인 조건을 무효화할 수 있으므로 해당 역방향 변경을 검증해야 한다.
‘자동 조회 후 INSERT’만으로 교차 트랜잭션 경쟁이 해결됐다고 하지 않는다.

P1 필수 시험:

- 같은 이름 A/B, 각각 singer/actor 삽입 성공. A의 근거를 B의 직업 FK로 저장하면 실패.
- A 하나에 singer+actor 성공, A의 동일 singer 중첩 기간 승인 실패, B의 singer 승인은 성공.
- company에 person_role 삽입 실패, person 역할이 남은 채 company로 유형 수정 실패.
- person+restaurant subtype 실패. unknown/NULL subtype은 후보로 수용하되 분류 완료 실패.
- 직업 부모 순환/없는 부모/비활성 코드 신규 부여 실패. 하위 검색에서 rapper→singer→entertainer 포함.
- 별칭/직업/정당 변경으로 UUID가 바뀌지 않음. 분류 불명은 검수에 남고 일반 기타로 승인되지 않음.
- 마지막 분야/근거 철회와 직업 승인 경쟁 후 불가능한 verified 상태가 남지 않음.

위 시험은 **아직 미실행**이다. 문서 내 코드 존재 검사와 실제 PostgreSQL 제약 시험을 구분한다.

## 8. 기존 자산 재사용/보강 범위

| 기존 테이블/기능 | 방향 | 아직 확정할 것 |
|---|---|---|
| kentity_names / evidence / evidence_dependencies | 재사용·보강 | 복수 주장별 근거, 자체 잠금, 버전/기간/권리 철회 (§9, P0.04) |
| kentity_relations | 재사용·보강 | predicate 통제, 양끝 유형 FK, 직책/종목/기간, 증거 귀속 (P0.03) |
| kentity_external_ids / id_reservations | 재사용·보강 | namespace별 유일성·충돌, Wikidata 전용 예약 제거 범위 (P0.03) |
| kentity_crosswalks / tdb_shadows | 원본 binding 계약 보강, shadow는 이전 호환 | 현재 source_binding_id는 TDB shadow FK. 범용 원천 binding으로 오인 금지 |
| kentity_ownership_decisions | 소유권 승인 이력 재사용 | 전체 writer 인벤토리와 전환 전제 (P0.05/P0.06) |
| kentity_candidate_requests | 요청 멱등 원장 재사용 | owner_key+request_key, payload hash 불일치 거부 유지 |
| kentity_resolution_jobs | 후보 정체성 조사 재사용 | entity_revision/lease/generation/cancel 보호 유지 |
| kentity_locale_fill_jobs | 결손 보충 재사용·일반화 | qid 필수 의존 제거, 정확 locale/provider/입력 버전 |
| kentity_preparations/items/locale_readiness | 실제 요청 준비 원장 재사용 | bound UUID/ordinal/owner 범위, 이름 증거/버전, fallback 분리 |
| kentity_audit_events | 감사 재사용 | 행별 before/after와 정정·병합·분리 역추적 계약 |
| kwave_entity_person_details / kwave_persons | legacy adapter/읽기 호환 | 후자는 이름 UNIQUE. 임의 이름 JOIN으로 공통 인물을 만들지 않음 |
| tdb_places / names / links | 필요한 필드 선택 흡수 | 주소·좌표·행정코드·원천 ID·이름·잠금의 개별 전후 매핑 |
| tdb_sources / src_records | 필요한 근거·정책만 | 전체 설정/자격증명/raw payload 복사 금지. 권리 미확인은 차단 |

## 9. 초기 미결 목록 — 후속 §10~§13과 함께 대조

이 목록은 상태를 따로 관리하는 TODO가 아니다. 실행 상태 정본은 `KDB_INTEGRATION_TODO.md`다.

| 남은 설계 묶음 | 필요한 구체 산출물 | 연계 task |
|---|---|---|
| 인물/장소 최소 속성 | 생년의 정밀도, 역할 해당 없음 판정, 주소/좌표/행정코드, 필드별 근거·잠금·소유권 전체 컬럼 | P0.02/P0.03/P0.05 |
| 관계·정체성 | predicate/type 허용표, 원본 namespace+table+ID binding, pair decision/redirect/분리 변경 원장 전체 컬럼 | P0.02/P0.03 |
| 표기/근거/권리 | exact locale 사전, 복수 근거 연결·검수 범위·부분 기간·잠금, 철회 의존 그래프 | P0.02/P0.04 |
| 작업/준비 | 기존 0115~0122 표별 최종 컬럼 차이, provider 중립 fill, owner/entity/revision/lease/cache 제약 | P0.02/P0.04 |
| 이전/고객 계약 | 운영 DB에 둘 manifest 최소 필드와 보관 분리, legacy UUID/API 호환·tenant override 사용처 | P0.05/P0.06 |
| 전체 물리 대조 | 신규/재사용 모든 대상 표의 키·NULL·기본값·인덱스·writer/삭제·철회 경로 누락 검사 | P0.02/P0.10 |

위 목록 중 정체성/관계/프로필/표기·준비는 후속 §10~§13으로 구체화했다.
남은 원본/고객/manifest 및 전체 역방향 제약 대조를 마쳐야 한다. **P0 내부 설계 의존을 해소하는 작업이며 P1 구현 게이트를 건너뛰는 것이 아니다.**
전 영역 컬럼과 필수 계약이 채워지고 대조 검증되기 전에는 P0.02나 G0를 완료 표시하지 않는다.

## 10. 정체성 계약의 물리 구조

현행 컬럼/기본값/FK/인덱스/트리거 정본은 20:40 KST 읽기 전용으로 작성한
[32테이블·387컬럼 inventory](KDB_TDB_SCHEMA_INVENTORY.json)다. 아래 기존 표는 그 컬럼을 보존하고 명시한 차이만 적용한다.
본 문서의 공통 NN/NULL/기본값/삭제 규칙을 동일 적용한다. 새 표의 아래 목록은 전체 컬럼이다.

### 10.1 entities/crosswalk/external_ids 보강

| 표 | 추가·변경 컬럼 (타입, NULL, 기본값) | 키·인덱스·갱신 책임 |
|---|---|---|
| kentity_entities | identity_revision(bigint NN,1), dependency_epoch(bigint NN,1) | 모두 >0. identity/권리·의존 영향 writer만 증가. 일반 정체성 변경과 무관한 job 관측으로 올리지 않음 |
| kentity_crosswalks | id(uuid NN,gen_random_uuid()), source_table(text NN,명시), source_state(text NN,'unknown'), source_merged_into(text ?,NULL), source_observed_at(timestamptz ?,NULL), mapping_policy_version(text NN,명시), target_identity_revision(bigint NN,0) | id UNIQUE, PK를 (source_system,source_table,source_id)로 전환. confirmed일 때 identity revision>0. source_state present/deleted/merged/unknown |
| kentity_crosswalks | 기존 status에 withdrawn 추가, source_binding_id 이름을 legacy_shadow_id로 호환 전환 | 기존 TDB shadow FK 유지. source_generation/source_fingerprint/target_revision 보존. 새 source_table이 없는 구 API는 namespace별 adapter로 해석 |
| kentity_external_ids | revision(bigint NN,1), observed_at(timestamptz NN,now()), operator_locked(boolean NN,false), policy_version(text NN,명시) | verified(provider,external_id) UNIQUE 유지. 같은 UUID evidence 복합 FK 유지. provider는 승인된 namespace만 허용 |
| kentity_id_reservations | native_owner를 entity_id로 호환 변경, revision(bigint NN,1), updated_at(timestamptz NN,now()) | PK(provider,external_id), entity_id FK entities. 예약이 이름/권리 승인이라는 뜻은 아님. 공통 정체성 writer만 변경 |

crosswalk 추가 컬럼의 기존 행 backfill은 확정된 source_system 매핑만 사용한다. 공통 기본 source_table='places'를 넣지 않는다.
FK 인덱스: crosswalk(entity_id), (status,source_system,source_table,updated_at,id), 외부 ID(evidence_id,entity_id).
삭제된 원본의 ID 추적을 위해 crosswalk를 삭제하지 않는다. fingerprint/정체성 변경은 승인 무효화와 같은 트랜잭션이다.
원본 전체 공급이 원래 하나의 외부 ID UNIQUE로 표현되지 않는 provider는 alias namespace를 추가 승인하기 전 verified로 등록하지 않는다.

### 10.2 `kentity_identity_decisions` — 신규, 현재 쌍 판정

전체 컬럼: left_id(uuid NN), right_id(uuid NN), decision(text NN,'possible_same'),
left_identity_revision(bigint NN), right_identity_revision(bigint NN), left_evidence_id(uuid ?), right_evidence_id(uuid ?),
actor(text NN), reason(text NN), policy_version(text NN), revision(bigint NN,1),
created_at(timestamptz NN,now()), updated_at(timestamptz NN,now()).
PK(left_id,right_id), CHECK(left_id<right_id), 양쪽 Entity FK, endpoint별 (evidence_id,endpoint_id) 복합 FK.
decision은 possible_same/confirmed_same/distinct/withdrawn. confirmed_same/distinct는 양쪽 현재 정체성 버전과 근거 필수.
writer는 정체성 검수 경로만. 바뀐 판정은 audit에 전후값을 남기며 이전 distinct를 무근거 갱신할 수 없다.
인덱스 (right_id,left_id), (decision,updated_at). 연결 성분 순환/충돌은 identity advisory lock 안에서 검사한다.

### 10.3 `kentity_identity_operations` — 신규, 병합/분리/유형 정정 결과

전체 컬럼: id(uuid NN,gen_random_uuid()), owner_key(text NN), request_key(text NN), payload_hash(text NN),
operation(text NN), source_id(uuid NN), target_id(uuid NN), status(text NN,'planned'),
plan(jsonb NN), plan_hash(text NN), actor(text NN), reason(text NN), reversal_of(uuid ?),
created_at(timestamptz NN,now()), applied_at(timestamptz ?), revision(bigint NN,1).
PK(id), UNIQUE(owner_key,request_key), source/target FK entities, reversal_of FK same table.
operation merge/split/retype, status planned/applied/rejected. retype만 source_id=target_id 허용.
plan은 최대 1MiB·최대 500변경 객체의 버전이 지정된 허용 schema이며 비밀값/raw payload 포함 금지.
applied_at은 applied일 때만 존재한다. 같은 요청의 hash 변경은 거부한다.
실패 기록과 실제 성공 기록을 구분한다. 처리 중 DB rollback이면 applied 기록도 남지 않는다.
인덱스(source_id,created_at),(target_id,created_at),(reversal_of). 정체성 writer만 갱신.

### 10.4 `kentity_redirects` — 신규

전체 컬럼: from_id(uuid NN), to_id(uuid NN), operation_id(uuid NN), revision(bigint NN,1), created_at(timestamptz NN,now()).
PK(from_id), 양쪽 Entity FK, operation_id FK identity_operations, CHECK(from_id<>to_id), 인덱스(to_id).
정체성 writer만 삽입/분리 시 제거하며 변경 전후는 audit에 보존한다. from Entity 자체를 삭제하지 않는다.
from retired/to 유효성·순환·깊이는 병합 트랜잭션에서 검사한다. 이름/직업 FK를 redirect만으로 자동 이동시키지 않는다.

### 10.5 감사/복원 원장 재사용

`kentity_audit_events`에 operation_id(uuid ?,FK identity_operations), object_table(text NN,''),
object_key(jsonb NN,'{}'), before_hash(text ?), after_hash(text ?)를 추가한다.
기존 before_value/after_value/actor/action/reason/entity_id/created_at 보존.
operation_id가 있으면 허용 대상 표/완전한 PK/전후 hash를 요구한다. 임의 SQL/테이블을 plan에서 실행하지 않는다.
인덱스(operation_id,id). 일반 앱은 과거 audit UPDATE/DELETE 불가.
분리는 operation의 객체별 전후 hash·원래 entity_id를 대조한다. 후속 변경된 객체는 자동 복원 대신 검수한다.

### 10.6 관계 통제

새 `kentity_relation_types` 전체 컬럼: code(text NN PK), label_ko(text NN), enabled(boolean NN,false), revision(bigint NN,1).
새 `kentity_relation_type_pairs`: predicate(text NN FK relation_types), subject_type(text NN FK types), object_type(text NN FK types),
PK(predicate,subject_type,object_type). [정체성 계약 §7](KDB_IDENTITY_CONTRACT.md)의 정확 조합만 seed한다.
새 `kentity_position_types`: code(text NN PK), label_ko(text NN), enabled(boolean NN,false), revision(bigint NN,1).
1차 직책 코드: ceo/executive/legislator/mayor/minister/chairperson/head_coach/coach/player/member.
위 사전 writer는 migration뿐이며 신규 코드가 없으면 role_label 자유 문자열을 자동 승인하지 않는다.

기존 relations에 subject_type(text NN), object_type(text NN), position_code(text ? FK position_types),
operator_locked(boolean NN,false), revision(bigint NN,1), created_at/updated_at(timestamptz NN,now()) 추가.
FK(subject_id,subject_type) 및 (object_id,object_type)→entities(id,entity_type),
FK(predicate,subject_type,object_type)→relation_type_pairs. predicate도 relation_types FK.
기존 role_label은 호환 표시값이며 position_code의 대체 승인 키가 아니다. holds_position 승인 시 position_code 필수.
기존 같은 주체 evidence FK 유지. 중복 기간은 동일 양끝/predicate/position별로 승인 행의 범위 배제 제약을 사용한다.
position_code NULL도 중복 검사에서 같은 값으로 다뤄야 한다. 저장 generated key COALESCE(position_code,'') 또는 동등한 격리 검증된 표현을 사용한다.
대상 subtype 변경/근거 철회가 관계를 무효화하면 동일 writer에서 재검수하고 파생 준비도 무효화한다.

## 11. 최소 프로필 — 직업별 전문 DB를 늘리지 않음

### 11.1 `kentity_person_profiles` — 신규

전체 컬럼: entity_id(uuid NN PK), entity_type(text NN,'person',CHECK='person'),
birth_year(smallint ?), birth_month(smallint ?), birth_day(smallint ?), birth_evidence_id(uuid ?),
role_state(text NN,'pending'), role_reason(text NN,'awaiting_role_evidence'), role_evidence_id(uuid ?),
birth_locked(boolean NN,false), role_locked(boolean NN,false), revision(bigint NN,1), updated_at(timestamptz NN,now()).
FK(entity_id,entity_type)→entities, 각각 evidence 복합 FK. 월은 연도가, 일은 월이 있을 때만 허용한다.
연도 1~9999, 월1~12, 일은 실제 달력 유효성 검사. 없는 월/일을 1월1일로 채우지 않는다.
role_state pending/verified/not_applicable/conflict. not_applicable에는 대상 주장 근거/이유 필수.
직업 verified는 실제 person_roles 승인값이 있는지 부모 잠금 안에서 검사한다. 정보가 없어서 해당 없음으로 만들지 않는다.
성별·연락처·거주지·주민 식별번호·가족관계는 통합 목적상 기본 수집/흡수하지 않는다.
직업과 소속은 각각 person_roles/relations에 저장한다. 이름/직군별 프로필 테이블은 만들지 않는다.
인덱스(birth_year,entity_id)는 실제 동명 후보 질의의 계획을 본 뒤 채택한다. 공개 근거·필요성이 있는 생년만 검수한다.

### 11.2 `kentity_location_profiles` — 신규

전체 컬럼: entity_id(uuid NN PK), entity_type(text NN,'location',CHECK='location'),
latitude(double precision ?), longitude(double precision ?), coordinate_precision_m(double precision ?), geo_evidence_id(uuid ?),
address_ko(text ?), address_en(text ?), address_evidence_id(uuid ?),
admin_namespace(text ?), sido_code(text ?), sigungu_code(text ?), ldong_code(text ?), admin_evidence_id(uuid ?),
geo_locked(boolean NN,false), address_locked(boolean NN,false), admin_locked(boolean NN,false),
revision(bigint NN,1), updated_at(timestamptz NN,now()).
FK(entity_id,entity_type)→entities와 모든 evidence 동일 Entity 복합 FK.
좌표 둘 다 NULL 또는 둘 다 유효한 유한값. 위도[-90,90]/경도[-180,180], 정확도>=0, datum WGS84만 1차 수용.
다른 좌표계는 검증된 변환 후 별도 변환 근거를 남긴다. 좌표/주소만으로 동일 시설을 승인하지 않는다.
행정 코드가 있으면 admin_namespace가 필수다. 한국 법정동/행정동/타국 지역 코드를 혼합하지 않는다.
주소·좌표·행정 그룹마다 evidence/lock을 별도로 둔다. 필드 업데이트는 값+근거+잠금+revision을 원자적으로 검사한다.
행사 좌표는 event를 location으로 만들지 않고 검수된 held_at 관계로 보존한다.
heritage_no/시설 공식 ID는 external_ids의 namespace로 보존한다. 새로운 문화재 전용 master를 만들지 않는다.
지역 필터 인덱스(admin_namespace,sido_code,sigungu_code,entity_id). 좌표 범용 GIS/폴리곤 엔진은 이 흡수의 필수 범위가 아니다.

## 12. 이름·근거·locale 구조 차이

### 12.1 locale/source policy 사전

새 `kentity_locales` 전체 컬럼: code(text NN PK), label_ko(text NN), enabled(boolean NN,false),
sort_order(integer NN,0), revision(bigint NN,1). 13개 합집합 코드와 활성 범위는 표기 계약 §2를 따른다.
하나의 자동 fallback FK를 두지 않는다. fallback은 요청 정책의 명시적 순서이며 strict-ready와 분리한다.

새 `kentity_source_policies` 전체 컬럼: id(uuid NN,gen_random_uuid()), provider(text NN), version(text NN),
license_code(text NN,'unreviewed'), storage_allowed(boolean NN,false), verification_allowed(boolean NN,false),
name_export_allowed(boolean NN,false), excerpt_export_allowed(boolean NN,false),
status(text NN,'unreviewed'), terms_url(text ?), reviewed_by(text ?), reviewed_at(timestamptz ?),
valid_until(timestamptz ?), conditions(text NN,''), created_at(timestamptz NN,now()).
PK(id), UNIQUE(provider,version), status unreviewed/approved/blocked/expired.
허용 플래그는 approved였던 검토자/시각/조건 근거가 있어야 설정한다. license/허용 플래그/조건은 불변이고 변경 허가는 새 버전으로 검토한다.
approved에서 blocked/expired로의 제한적 철회만 같은 행에서 허용한다. 현재 허가는 status와 만료도 함께 만족해야 하며 과거 true 플래그만으로 공급하지 않는다.
철회는 §14.3의 policy revision/읽기 gate/정책 outbox로 즉시 차단하고 Entity별 무효화는 나누어 처리한다.
사전 운영자만 변경하고 정책 확정의 책임은 권리 담당자다. 원천 token/config는 이 표에 저장하지 않는다.

### 12.2 evidence 재사용

현행 evidence 컬럼에 revision(bigint NN,1), source_policy_id(uuid ? FK source_policies),
claim_fingerprint(text NN,명시), source_observation_hash(text NN,명시),
claim_payload(jsonb NN,'{}'), independent_origin(text NN,명시)를 추가한다.
claim_type에 occupation/classification/profile/no_form 추가. claim_payload는 타입별 허용 필드만이며 16KiB 제한.
기존 UNIQUE(entity_id,provider,source_record_id,source_url)는 하나의 문서 내 여러 주장을 막으므로
UNIQUE(entity_id,provider,source_record_id,source_url,claim_type,claim_fingerprint,source_observation_hash)로 전환한다.
기존 UNIQUE(id,entity_id)와 근거 귀속 FK 유지. verified일 때 주장 범위/정체성/현재 권리와 검수 시각을 재검사한다.
정책 미확인 legacy는 자동 verified backfill 금지. export_allowed는 정책+주장의 공급 가능성을 반영하는 호환 캐시다.
실제 외부 공급은 policy의 현재 유효성도 확인한다. 인덱스(source_policy_id,entity_id),(entity_id,claim_type,status).

dependencies의 PK를 (evidence_id,depends_on_id)로 보강해 필수 부모 여러 개를 허용한다.
기존 entity_id/created_at과 양쪽 동일 Entity 복합 FK 유지, 자기 참조/순환 금지.
동일 Entity 부모 잠금 안에서 DAG 변경을 검사한다. 일반 worker는 임의로 기존 필수 의존을 삭제할 수 없다.

### 12.3 이름 재사용 + 복수 근거 연결

names에 normalized_value(text NN,명시), normalization_version(text NN,명시),
operator_locked(boolean NN,false), verification_method(text ?), policy_version(text NN,명시)를 추가한다.
locale FK locales(code), UNIQUE(id,entity_id) 추가.
기존 출처별 UNIQUE는 실제 중복을 대조한 뒤
UNIQUE NULLS NOT DISTINCT(entity_id,locale,value,kind,form,valid_from,valid_until)로 대체한다.
대표명 선택은 동일 Entity/locale에서 verified canonical의 possible_validity(§14.7) 겹침을 배제한다. 출처가 많다고 대표명 여러 개를 승인하지 않는다.
이름 자체의 의미상 form이 충돌하면 별도 주장을 검수한다. source_code는 초기 원천 호환값이지 유일한 진실 출처가 아니다.
인덱스(locale,normalized_value,entity_id), (entity_id,locale,status), evidence 철회 역조회 유지.

새 `kentity_name_evidence`: name_id(uuid NN), entity_id(uuid NN), evidence_id(uuid NN), stance(text NN,'supports'),
created_at(timestamptz NN,now()), PK(name_id,evidence_id).
FK(name_id,entity_id)→names(id,entity_id), FK(evidence_id,entity_id)→evidence(id,entity_id), stance supports/contradicts.
인덱스(evidence_id,name_id). 같은 대상이어도 locale/표기/기간이 다른 주장의 supports 승격은 writer가 거부한다.
names.evidence_id가 있으면 연결된 유효 supports여야 한다. 역방향 철회·연결 삭제도 부모 잠금 안에서 재검증한다.

## 13. 준비·보충 표의 목표 차이

다음은 inventory의 기존 전체 컬럼을 보존한 최소 보강이다. 날짜/카운터/상태 등을 이름이 비슷한 새 queue 표로 복제하지 않는다.

| 표 | 추가/변경 컬럼 | 제약·책임 |
|---|---|---|
| kentity_preparations | as_of(timestamptz ?), source_hash(text NN,''), request_form_policy(text NN,'strict-recorded'), article_scope(text NN,'') | owner는 인증에서 도출. source_hash/article_scope는 P7 이벤트 계약과 일치해야 함. 요청 idempotency UNIQUE 유지 |
| kentity_preparation_items | bound_identity_revision(bigint ?), bound_entity_revision(bigint ?), span_start(integer ?), span_end(integer ?) | UUID들에 공통 Entity FK(구 namespace는 adapter), bound UUID가 있으면 두 revision 필수. span 양끝 둘 다 존재·0<=start<end. 단위는 Unicode scalar offset, 기사 source_hash에 고정 |
| kentity_locale_readiness | name_id(uuid ? FK names), entity_id(uuid ? FK entities), identity_revision(bigint ?), name_revision(bigint ?), dependency_epoch(bigint ?), fallback_locale(text ? FK locales), usable_value(text NN,''), usable_form(text ?), proof_policy_version(text NN,'') | state에 no_form/stale 추가, locale FK, name_id와 entity_id 같은 대상 복합 FK. ready이면 전체 proof 포인터/버전과 값 필수 |
| kentity_locale_fill_jobs | qid NULL 허용, provider(text NN,명시), source_reference(text NN,명시), entity_revision(bigint NN,명시), identity_revision(bigint NN,명시), scope_key(text NN,'global'), purpose(text NN,'locale_fill'), model_version(text ?), prompt_version(text ?), input_tokens(bigint NN,0), output_tokens(bigint NN,0), changed_names(integer NN,0), newly_ready(integer NN,0), started_at/finished_at(timestamptz ?) | 공통 Entity/locale FK, UNIQUE(entity_id,locale,input_fingerprint,policy_version,scope_key). 수치>=0. state cancelled 추가, lease/generation 보호 유지 |
| kentity_resolution_jobs | identity_revision(bigint NN,명시), scope_key(text NN,'global'), provider_policy_version(text NN,명시), model_version/prompt_version(text ?), input_tokens/output_tokens(bigint NN,0) | 후보 정체성 조사, current revision+lease 재확인. name fill 승인권 없음. 비용>=0 |
| kentity_readiness_events | 기존 모든 컬럼 보존 | snapshot은 허용 필드/비밀값 제외. append-only, preparation+ordinal/locale 조회 |
| kentity_candidate_requests | 기존 모든 컬럼/PK 보존 | owner_key/request_key hash 멱등성, Entity 승인과 분리 |
| kentity_ownership_decisions | 기존 모든 컬럼 보존 | source hash/기대 revision/선택 타입/분야/검수 근거. 과거 판정 UPDATE 금지 |

새 `kentity_fill_waiters`: preparation_id(uuid NN), ordinal(integer NN), locale(text NN), job_id(uuid NN),
created_at(timestamptz NN,now()), PK(preparation_id,ordinal,locale,job_id),
FK(preparation_id,ordinal,locale)→locale_readiness, FK job_id→fill_jobs, 인덱스(job_id).
한 요청 취소는 waiter만 종료/제거하고 다른 요청의 공통 job을 취소하지 않는다. 과거 요청 추적은 readiness_events로 보존한다.

새 `kentity_invalidation_outbox`: id(bigint GENERATED ALWAYS AS IDENTITY PK), entity_id(uuid NN FK entities),
dependency_epoch(bigint NN), reason(text NN), created_at(timestamptz NN,now()),
processed_at(timestamptz ?), attempts(integer NN,0), next_attempt_at(timestamptz NN,now()),
UNIQUE(entity_id,dependency_epoch), 인덱스(next_attempt_at,id) WHERE processed_at IS NULL.
DB 변경 트랜잭션이 삽입하고 전용 invalidator가 처리한다. 재전달 허용, consumer는 epoch로 멱등 처리한다.
읽기 gate는 DB epoch를 확인하므로 outbox 지연이 곧 잘못된 strict 공급을 허용하지 않는다.

증거/이름 정책 변경으로 job 상태만 complete로 바꾸지 않는다. 실제 names 저장/선택 결과와 readiness 재계산까지 검증한다.
신규 `source_reference`는 인증된 원천 ID 또는 검수된 공개 URL이며 비밀 토큰/기사 본문을 넣지 않는다.
원천이 없는 job은 provider를 빈 문자열로 채우지 말고 후보 조사/보류로 보낸다.

§10~§13로 P0.03/P0.04의 설계 필드를 연결했다. 실제 최종 migration·인덱스 계획·역방향 제약·호환 adapter는 P1에서 시험한다.
P0.02를 닫기 전에는 원본 필드 매핑 및 실제 API/권한·이전 manifest의 최종 범위를 대조해야 한다.

## 14. 독립 검토로 보강한 교차행·교차표 제약

§10~§13의 추가 차이다. **설계 보정 8건이며 아직 실제 SQL/DB 시험 PASS가 아니다.**
검수 결과를 기능을 늘리는 방향이 아니라 잘못된 UUID/주장/기간/권리가 저장·공급되지 않게 하는 최소 제약으로 반영한다.
공통 writer/원천 증분과 실행 전 X01~X12 시험은 [저장·증분 계약](KDB_WRITER_DELTA_CONTRACT.md)에 연결한다.

### 14.1 준비 항목과 readiness의 대상 일치 — S01

items에 UNIQUE(preparation_id,ordinal,bound_entity_id,bound_identity_revision,bound_entity_revision)를 추가한다.
readiness에 entity_revision(bigint ?,NULL), scope_key(text NN,'global')를 추가하고,
FK(preparation_id,ordinal,entity_id,identity_revision,entity_revision)→위 items 키를 DEFERRABLE INITIALLY IMMEDIATE로 둔다.
ready/no_form이면 위 UUID와 두 revision이 모두 NOT NULL이고 revision>0인 상태 CHECK를 둔다.
미해소/ambiguous item에는 UUID를 만들어 넣지 않는다. readiness의 이름과 item의 bound UUID를 각각 독립 FK만으로 검사하지 않는다.
재해소 때 old waiter 제거/취소 이력·readiness stale·새 bound 버전은 같은 transaction에서 반영하고 FK는 commit 전에 검증한다.
정체성/일반 Entity의 현재 revision이 proof와 같은지는 읽기 gate에서도 확인한다. 과거 proof를 보관하려고 현재 Entity revision에 FK를 걸지는 않는다.
시험: A item에 B 이름으로 ready 저장 거부; 같은 A의 오래된 bound revision 거부; 미해소 item을 ready로 만드는 NULL 우회 거부.

### 14.2 주장 종류와 no_form 근거 — S02

같은 Entity 복합 FK 외에 지연 제약 트리거로 **참조 위치별 claim_type과 주장 payload 일치**를 검사한다.
대응: Entity type/domain=classification, person_roles=occupation, person birth/location 속성=profile,
person role_state=occupation, external ID/동일·별개 판정/confirmed crosswalk·source binding=identity, relations=relation, names/name_evidence=name.
confirmed binding의 source namespace/전체 원본 키와 claim payload도 일치해야 하며 근거 철회 때 역방향 확인을 한다.
같은 UUID의 identity 근거를 singer 직업이나 영어 이름 근거로 쓰는 것은 실패해야 한다.
주장의 locale/value/role_code/관계 양끝·predicate/기간 등도 일치해야 한다. claim_type만 맞으면 승인되는 것이 아니다.
이미 참조된 claim_type/claim_payload/fingerprint는 직접 수정하지 않는다. 새 주장·새 근거로 검수하며 옛 근거는 철회 이력을 보존한다.
근거 status/권리 철회와 링크 삭제는 역방향 재평가 대상이다. 상태/값을 검증하는 트리거는 허용 writer의 부모 잠금 규칙과 함께 시험한다.

readiness에 no_form_evidence_id(uuid ?,NULL), FK(no_form_evidence_id,entity_id)→evidence(id,entity_id)를 추가한다.
state=no_form이면 이 ID와 entity_id는 NN, name_id는 NULL이며 typed claim=no_form·정확 locale/form/as_of·현재 권리/독립 근거를 검사한다.
그 외 state에서는 no_form_evidence_id=NULL이다. no_form은 strict-ready가 아니며 모델이 이름을 못 찾았다는 응답으로 만들지 않는다.
시험: 같은 사람의 identity→occupation 교차 주장 거부; 다른 ID/locale/철회 근거로 no_form 저장 거부.

### 14.3 권리 정책의 버전·만료·철회 — S03

source_policies 추가 컬럼: revision(bigint NN,1,CHECK>0), changed_by(text ?,NULL), changed_at(timestamptz ?,NULL), change_reason(text ?,NULL).
initial revision=1 생성 후 동일 행에서 허용하는 변경은 approved→blocked/expired의 단방향 차단뿐이다.
최초 승인도 검수자/시각/범위/조건을 갖춘 **새 version/id를 approved로 INSERT**하는 경로를 선택한다. 기존 unreviewed 행을 UPDATE로 승격하지 않는다.
unreviewed에 연결됐던 근거는 자동 승계하지 않고 새 정책에 따른 재검수를 거쳐야 한다. 일반 worker의 approved INSERT 권한은 없다.
변경 시 revision을 1 증가시키고 changed_by/at/nonempty reason을 요구한다. 과거 license/flags/conditions/valid_until은 수정하지 않는다.
차단 해제·허가 확대·만료 연장은 새 version/id 검토로 처리하며 기존 evidence를 새 정책에 자동 재연결하지 않는다.

readiness에 policy_proof(jsonb NN,'[]')를 추가한다. ready/no_form에는 선택 근거와 필수 의존들의 모든 정책 (policy_id,revision)을 중복 없이 고정한 비어 있지 않은 배열이 필요하다.
각 항목의 UUID·양수 revision 및 실제 evidence가 참조한 정책 집합 일치는 보호된 writer에서 검증한다. 임의 JSON self-assertion을 승인으로 쓰지 않는다.
공급 gate는 이 proof뿐 아니라 현재 정책 status/revision/허용 범위와 valid_until을 직접 검사한다. valid_until 경과는 outbox가 없어도 즉시 차단한다.
복수 supports 중 어느 독립 근거를 사용했는지 고정하고, 그 정책이 차단됐으면 대안 근거로 재평가하기 전 옛 proof를 공급하지 않는다.

§13의 invalidation_outbox를 복제하지 않고 다음처럼 확장한다:
entity_id와 dependency_epoch를 NULL 허용으로 바꾸고 source_policy_id(uuid ? FK source_policies), policy_revision(bigint ?)를 추가한다.
CHECK는 정확히 하나의 쌍만 NN이고 양수: (entity_id,dependency_epoch) XOR (source_policy_id,policy_revision).
기존 UNIQUE(entity_id,dependency_epoch)는 Entity 이벤트의 부분 UNIQUE로, 정책 이벤트는 UNIQUE(source_policy_id,policy_revision) WHERE source_policy_id IS NOT NULL로 둔다.
policy 철회와 정책 outbox는 같은 transaction이다. 대규모 Entity fan-out을 그 transaction에 넣지 않는다.
invalidator는 정책 이벤트를 bounded batch로 재개하고 각 Entity의 dependency_epoch/후속 outbox를 갱신한다. 중간 중단에도 policy read gate가 차단을 유지한다.
정책 이벤트용 fanout_after_entity_id(uuid ?,NULL), fanout_started_at(timestamptz ?,NULL), fanout_completed_at(timestamptz ?,NULL)를 같은 outbox에 추가한다. Entity 이벤트에서는 모두 NULL이다.
처리자는 정책 이벤트 행을 먼저 잠그고 현재 source_policy_id 참조의 distinct Entity UUID를 cursor 이후 오름차순으로 한 batch만 처리한다.
해당 Entity별 epoch/outbox와 cursor 전진을 같은 transaction에 commit한다. crash 후에는 commit된 cursor부터 재개하므로 동일 batch의 epoch를 이중 증가시키지 않는다.
정책 변경 producer는 이 이벤트 행과 Entity 행을 역순으로 함께 잠그지 않는다. 차단 정책의 새로운 유효 근거 참조는 writer/read gate가 거부하며, 기존 근거를 다른 Entity로 옮기는 정체성 writer는 양쪽 epoch를 직접 무효화한다.
전체 후보를 소진하고 source policy 참조/차단 gate를 최종 대조한 뒤만 fanout_completed_at/processed_at을 기록한다. 부분 batch 성공을 전체 처리로 표시하지 않는다.
추가 시험: 세 batch 분량에서 첫 commit 직후 crash→재시작/중복 실행에도 미계상0·완료 전 processed_at 없음.
시험: 철회 commit 후 invalidator 지연·캐시 hit에서 old proof 공급0; 만료 후 supply 차단; 권한 없는 정책 해제 거부.

### 14.4 외부 ID 예약과 확정의 단일 소유 — S04

두 표 각각의 UNIQUE로는 부족하다. reservation/verified owner 생성·교체·철회는 §정체성 계약의 전역 transaction-level identity lock 범위에 포함한다.
그 후 legacy 부모→common 부모 UUID 순서→예약/외부 ID 키 순서로 잠그고 **두 표의 현재 owner**를 함께 재검사한다.
서로 다른 UUID가 같은 (provider,external_id)를 reserve/verify할 수 없다. direct DML을 허용하지 않고 역방향 소유 변경도 제약 트리거로 검사한다.
일반 이름 저장에는 이 전역 잠금을 적용하지 않는다. 검수 없는 외부 ID 재배정은 불가하다.
시험: A reserve/B verify 동시 실행에서 최대 하나만 commit; 철회·재배정 역순에도 현재 owner 하나.

### 14.5 redirect와 병합·분리 결과 결합 — S05

merge operation의 source=loser, target=survivor로 고정한다. split은 source=해당 survivor, target=원래 loser이고 reversal_of가 원 merge ID다.
redirect(from,to,operation_id)는 같은 endpoints의 status=applied/operation=merge만 참조하도록 지연 제약 트리거로 검사한다.
identity_operations에 CHECK(reversal_of IS NULL OR reversal_of<>id), UNIQUE(reversal_of) WHERE status='applied' AND reversal_of IS NOT NULL을 추가한다.
reversal_of는 split에만 NN, merge/retype에는 NULL이며 applied split은 applied merge와 반대 endpoints를 요구한다.
operation을 applied로 확정할 때와 redirect INSERT/UPDATE/DELETE 모두 역방향 검사한다. 같은 transaction의 합법적인 merge/split 중간 상태는 commit 시 검증한다.
후속 병합으로 현재 endpoints/객체가 달라졌으면 자동 split하지 않고 별도 검수한다. 과거 applied operation은 불변 이력이다.
시험: planned/rejected/retype·불일치 endpoints의 redirect 거부; 같은 merge의 이중 applied reversal 거부.

### 14.6 waiter와 job의 UUID·locale·scope 일치 — S06

fill_waiters에 entity_id(uuid NN), identity_revision(bigint NN), entity_revision(bigint NN), scope_key(text NN)를 추가한다. 새 컬럼 기본값 없음, revision>0.
readiness에 UNIQUE(preparation_id,ordinal,locale,entity_id,identity_revision,entity_revision,scope_key),
fill_jobs에 UNIQUE(id,entity_id,locale,identity_revision,entity_revision,scope_key)를 추가한다.
waiter는 양쪽으로 각각 대응 전체 컬럼 복합 FK를 둔다. ready/no_form뿐 아니라 job을 기다리는 bound readiness도 대상/버전을 채워야 한다.
UUID가 없는 대상은 resolver 단계이며 무작위 fill job에 붙이지 않는다.
scope는 global 또는 인증된 owner에 귀속된 tenant scope만 허용한다. readiness scope 설정/변경 시 인증 owner를 검사하며 클라이언트 입력을 신뢰하지 않는다.
재해소·scope 변경 때 old waiter를 명시 종료하고 새 키로 연결한다. 한 waiter 취소가 다른 요청/global job을 취소하지 않는다.
시험: A/ko waiter에 B/en job 및 오래된 revision job 연결 실패; cross-owner 전용 scope 실패; global waiter 하나 취소 후 다른 요청 유지.

### 14.7 부분 날짜의 손실 없는 표현 — S07

person_roles/relations/names 세 표에 같은 6컬럼을 추가한다:
valid_from_precision/valid_until_precision(text NN,'unknown'),
valid_from_year/valid_until_year(smallint ?,NULL), valid_from_month/valid_until_month(smallint ?,NULL).
precision은 unknown/open/year/month/day다. 기존 valid_from/valid_until(date ?)는 **day일 때만** exact date를 저장한다.
unknown/open은 year/month/date 모두 NULL, year는 1~9999의 year만, month는 year와 1~12의 month만,
day는 실제 유한 date와 그 date의 year/month가 모두 일치해야 한다. 없는 일자를 1일로 채우지 않는다.
unknown과 근거 있는 open은 다르다. open도 원천 근거가 있어야 하며 정보 없음에서 자동 승계하지 않는다.

가능한 기간의 외피를 계산하는 possible_validity(daterange STORED generated)로 중복 EXCLUDE를 한다.
year/month의 시작은 해당 연/월 첫날, 종료는 마지막 날을 **중복 검사 계산에만** 쓰고 관측한 날짜로 표시하지 않는다.
unknown/open은 충돌 검사에서 무한 경계로 보수적으로 다룬다. 확실히 역전된 경계는 거부하고, 순서를 확정 못 하는 상충 기간은 승인 보류한다.
정확 as_of 공급은 불확실한 경계가 해당 시점을 포함하는지 확정할 수 없으면 보류한다. unknown 외피가 시점을 덮는다고 사실로 공급하지 않는다.
names의 UNIQUE NULLS NOT DISTINCT에는 기존 키와 함께 위 6컬럼을 포함한다. 모두 NULL date인 서로 다른 알려진 연도를 같은 행으로 접지 않는다.
기존 exact date는 day로, NULL은 unknown으로만 보수적 backfill한다. 과거 자료의 기간 승인을 자동 확대하지 않는다.
시험: 2024년만 아는 입력 왕복 시 가짜 1월1일 없음; unknown/open 구별; 불법 날짜/역전 거부; 보수적 중복 검출.

### 14.8 반증의 해결 상태 — S08

name_evidence에 resolution(text NN,'unresolved'), revision(bigint NN,1), decided_by(text ?), decided_at(timestamptz ?), decision_reason(text ?)를 추가한다.
resolution은 unresolved/upheld/dismissed/withdrawn. unresolved만 판정 3컬럼 NULL, 나머지는 판정 주체/시각/비어 있지 않은 이유 필수다.
supports는 upheld일 때만 유효 지원으로 사용한다. contradicts는 unresolved/upheld이면 ready 차단, upheld이면 name verified 유지도 거부한다.
dismissed/withdrawn 반증도 물리 삭제하지 않는다. 반증의 근거 자체가 철회됐다고 검수 없이 dismissed로 바꾸지 않는다.
같은 Entity/주장/locale 일치 및 근거 유효성을 확인하고, 검수 writer가 name/entity revision·dependency_epoch·outbox를 원자적으로 갱신한다.
names.evidence_id는 현재 upheld supports여야 한다. 링크의 resolution/stance 변경과 evidence 철회에 대한 역방향 제약을 검사한다.
시험: 미해결 반증에서 ready 거부; 근거 있는 dismissed 후 재평가; upheld 반증과 verified 이름 동시 유지 거부; 철회 경쟁에서 옛 proof 차단.

S01~S08의 FK/NULL/trigger/race 구현·실행은 P1.03~P1.09에서 한다. 현재 문서 검사만으로 이 제약의 DB 동작을 증명하지 않는다.

## 15. 선택 흡수 원장과 현재 유효 보호 판단

[이관 제어 물리 설계](KDB_MIGRATION_CONTROL_SCHEMA.md)의 데이터 사전을 본 구조의 일부로 사용한다.
신규는 migration_runs/migration_records/source_guards 3표이며 기존 audit에 migration_record_id FK 하나를 추가한다.
TDB 과거 로그/에이전트/수집기 전체를 복제하지 않고 원본 PK별 처리·복구와 현재 유효한 기각/철회/수동 정정만 관리한다.

원본 추적은 전체 typed PK를 보존하고, 서비스 Entity의 값 채택은 별도 승인으로 분리한다.
계획 UUID가 아직 생성되지 않은 경우와 기존 Entity FK를 분리해 신규 인물·장소 계획을 잘못된 이름 JOIN으로 해소하지 않는다.
같은 source 기록의 특정 후보 기각은 rejected_entity_id를 명시한다. 동명이인 전체를 일괄 기각하거나 다른 검수된 후보 연결을 막는 것으로 확대하지 않는다.

현재 DB 역할·트리거·적용 migration 및 제한 권한 전환안은 [쓰기 권한 조사](KDB_WRITER_AUTHORITY.md)에 있다.
runtime owner/superuser를 그대로 두고 공통 writer 이름만 바꾸는 것은 DB 권한 경계가 아니다.
권한은 격리된 실제 Go 호출·DDL/DML 거부 시험 후 단계 전환한다. 이 설계 작업에서는 운영 GRANT/REVOKE나 접속 계정을 변경하지 않는다.

남은 최종 구조 인수: source_code→정책 namespace/우선순위·locale 범위, 기존 필드 target 채택의 물리 컬럼/주장 종류,
전체 writer/실행 pool 역할 및 독립 정답·실사용 API 계약이다. §15를 썼다고 P0.02/P0.05/G0를 완료로 보지 않는다.
