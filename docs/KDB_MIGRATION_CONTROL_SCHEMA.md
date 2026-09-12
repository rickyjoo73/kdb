# 선택 흡수 제어 원장·현재 보호 판단 — 물리 설계

2026-09-12 KST · `migration-control-design-v1` · P0.02/P0.05.
관련: [전체 구조](KDB_TARGET_SCHEMA.md), [저장·증분 계약](KDB_WRITER_DELTA_CONTRACT.md), [TODO](KDB_INTEGRATION_TODO.md).
**운영 migration SQL이 아니라 구현 전 데이터 사전이다.** 현재 테이블 생성/데이터 이관/이관 승인 여부와 혼동하지 않는다.

## 1. 추가 범위는 3표로 제한

| 대상 | 필요한 이유 | 넣지 않을 내용 |
|---|---|---|
| kentity_migration_runs | 승인 범위·snapshot·mapper·중지·완료 분모 고정 | 별도 수집 스케줄/에이전트 관리 |
| kentity_migration_records | 원본 PK별 처리/보류/제외·멱등성·복구 추적 | 전체 원본 payload/과거 로그 복제 |
| kentity_source_guards | 현재 유효한 원본 연결 기각·표기 철회·수동 정정·빈 슬롯 보호 | 모든 과거 판정·모델 응답·별도 검수 포털 |

이름/직업/속성/관계/근거는 기존 목표 Entity 표로 간다. 이 3표가 세 번째 고유명사 DB가 되지 않는다.
원천 전체의 이용권/차단은 source_policies를 쓴다. block_source를 별도 guard에 중복 운영하지 않는다.
현재 source_policies에 대응하지 못한 원천은 unreviewed/보류다. enabled나 priority만으로 허용 정책을 만들지 않는다.

공통 표기: NN=NOT NULL, ?=NULL 허용, —=기본값 없음. UUID 기본값 gen_random_uuid(), 시각 timestamptz.
모든 FK는 ON DELETE RESTRICT/ON UPDATE NO ACTION, 실제 row/guard revision은 bigint>0이다. create 계획의 expected_* 두 컬럼만 0 sentinel을 허용하며 §4의 mode별 규칙을 적용한다. 다른 Entity에 값을 옮기는 CASCADE는 없다.
일반 앱/collector는 이 표에 직접 DML할 권한이 없다. 허용 importer/검수 writer의 감사된 경로로만 변경한다.

## 2. 원본 PK 표현 — 이름과 해시만으로 연결하지 않음

source_system은 kdb/tdb 또는 승인된 원천 namespace, source_table은 승인 manifest에 있는 실제 표/데이터셋이다.
source_pk는 **해당 원본 PK의 모든 컬럼**을 담는 typed JSON object이며 크기 512 UTF-8 bytes 이하를 1차 범위로 한다.
PK가 없거나 이 한도를 넘는 원천은 임의로 이름/해시를 PK로 만들지 않고 보류 후 별도 키 계약을 검토한다.

```json
{
  "source_code": {"type": "text", "value": "fixture_source"},
  "external_id": {"type": "text", "value": "000123"}
}
```

- 컬럼 키는 실제 PK와 정확히 같아야 한다. 누락/추가/NULL/중복 JSON key를 거부한다.
- type은 이 단계에서 uuid/text/int2/int4/int8만 허용한다. integer도 JSON number가 아니라 10진 문자열이다.
- text의 앞뒤 공백·대소문자·선행 0을 임의 정규화하지 않는다. uuid는 유효 UUID의 소문자 표준 표현, integer는 범위 내 canonical 10진 표현으로 고정한다.
- 원본 PK의 ordinal과 무관하게 컬럼 이름을 코드포인트 순서로 정렬해 canonical encoding을 생성한다. hash는 검색/변경 검출용이지 원본 PK를 대체하는 유일성 키가 아니다.
- PK metadata와 canonicalization_version을 snapshot 기준에 고정한다. PK 타입이 달라지면 재매핑한다. bigint를 JavaScript Number로 왕복하지 않는다.
- 실제 비밀키 컬럼은 원본 식별 PK로 수용하지 않는다. 아카이브/제외 필드는 값 복사 대신 승인된 원본 reference와 필요한 비가역 digest만 남긴다.

512-byte 한도는 원본 삭제/포기 기준이 아니라 초기 importer의 명시적 보류 범위다. 두 JSON이 같은 digest여도 실제 PK가 다르면 같은 원본으로 처리하지 않는다.

## 3. kentity_migration_runs — 신규

전체 컬럼:

| 컬럼 | 타입·NULL·기본값 | 제약/의미 |
|---|---|---|
| id | uuid NN, gen_random_uuid() | PK |
| owner_key / request_key | text NN / text NN, — | 인증된 운영 주체/멱등 요청, 각 <=200 bytes; UNIQUE(owner_key,request_key) |
| request_hash | text NN, — | lowercase SHA-256, 같은 요청 다른 payload 거부 |
| mode | text NN, — | dry_run/apply; dry_run 성공을 apply 성공으로 바꾸지 않음 |
| state | text NN, planned | planned/running/paused/completed/failed/cancelled |
| mapper_version / canonicalization_version | text NN / text NN, — | 검토된 버전, 비어 있지 않음 |
| source_basis | jsonb NN, — | DB별 snapshot/복원 참조·코드/적용 migration/trigger digest; 허용 schema, <=32KiB, 비밀값 없음 |
| cohort_hash / selection_policy_hash | text NN / text NN, — | 정확 원본 PK 목록·선별 규칙 SHA-256; 첫 실행 후 불변 |
| expected_records / expected_entities / max_changed_objects | bigint NN / bigint NN / bigint NN, — | >=0, 승인 분모·상한; 서비스 Entity 외 정책/guard도 record 분모에 포함 |
| approved_by / approved_at / approval_ref | text ? / timestamptz ? / text ?, NULL | apply running에는 셋 모두 필수. 대상/cohort/version에 결합한 승인 reference |
| revision | bigint NN, 1 | CAS·중지/재개 보호 |
| created_at / started_at / finished_at | timestamptz NN now() / timestamptz ? / timestamptz ? | terminal state에만 finished_at 필수 |
| last_error_code | text ?, NULL | 통제 오류코드; 원천 body/SQL/키 출력 없음 |

인덱스: PK, 멱등 UNIQUE, (state,created_at,id). 대시보드 처리 수는 records에서 상태별 집계한다. 별도 완료 카운터를 원본보다 먼저 올리지 않는다.
planned→running 후 source_basis/cohort/mapper/승인 payload를 바꾸지 않는다. 변경 계획은 새 run/idempotency key다.
paused에서 재개는 같은 snapshot/cohort/hash 및 현재 권한을 다시 검사한다. revoked approval이나 변경된 원천을 무시해 이어서 쓰지 않는다.
취소는 이미 반영한 기록을 삭제/자동 원복한다는 뜻이 아니다. 처리된/미처리된 분모를 각각 보존한다.
첫 파일럿 상한은 [인수 기준](KDB_ACCEPTANCE_LIMITS.md)의 승인된 범위에서만 선택한다. 설계 문서 자체를 approved_by로 넣지 않는다.
planner/importer capability는 approved_by/approved_at/approval_ref를 설정할 수 없다. 승인용 capability와 실행용 capability를 분리한다.
승인 함수만 인증된 책임자와 정확 cohort/request_hash/mapper/snapshot을 결합한 승인 reference를 기록한다. 실행 함수는 현재 principal의 execute 권한과 그 승인 내용을 내부에서 재검사한다.
임의 actor 문자열·request JSON을 승인 reference로 신뢰하지 않으며 runtime 직접 table DML은 금지한다. 정확 signature별 EXECUTE만 허용하는 P1 격리 권한 시험에 포함한다.

## 4. kentity_migration_records — 신규

전체 컬럼:

| 컬럼 | 타입·NULL·기본값 | 제약/의미 |
|---|---|---|
| id | uuid NN, gen_random_uuid() | PK |
| run_id | uuid NN, — | FK runs(id) |
| source_system / source_table | text NN / text NN, — | 각각 <=64 ASCII bytes, 승인된 source namespace/표 |
| source_pk | jsonb NN, — | §2 typed PK, 실제 PK 보존 |
| source_fingerprint | text NN, — | 허용 projection+guard/원천 버전 SHA-256; raw/config 제외 |
| source_state | text NN, unknown | present/deleted/merged/unknown; master 상태와 별개 |
| source_observed_at | timestamptz ?, NULL | 원천 관측 시각. 검수 시각/사실 유효일이 아님 |
| disposition | text NN, — | include/conditional/archive_only/exclude_operational |
| state | text NN, planned | planned/held/excluded/validated/applied/failed/reverted |
| target_mode | text NN, none | none/existing/create; 계획 대상 종류 |
| planned_target_id | uuid ?, NULL | existing/create이면 NN인 계획 UUID. 아직 생성되지 않은 대상도 계획할 수 있어 이 컬럼에는 Entity FK를 두지 않음 |
| target_entity_id | uuid ?, NULL | FK entities(id), 검수된 연결일 때만 지정. 정책/미해소 기록에는 NULL |
| expected_entity_revision / expected_identity_revision | bigint ? / bigint ?, NULL | existing이면 둘 다 NN이고 >0, create이면 둘 다 0(존재하지 않아야 함), none이면 NULL |
| expected_owner | text ?, NULL | existing이면 현재 write_owner, create이면 승인된 최초 owner; none이면 NULL. 소유권 판정 자체는 기존 ownership 원장 사용 |
| policy_proof | jsonb NN, [] | 사용 근거·필수 정책 id/revision의 제한 proof; 자가 승인 JSON 아님 |
| plan_hash | text NN, — | 허용 전후 diff·대상 객체·source/target 버전의 SHA-256 |
| expected_object_count / actual_object_count | integer NN 0 / integer NN 0 | >=0; applied면 일치, run 승인 상한 검사 |
| reason_code / recovery_ref | text NN / text ?, — / NULL | 사유코드, 운영자 접근통제 복구 bundle reference; 비밀 토큰/공개 raw URL 금지 |
| applied_result_hash | text ?, NULL | applied/reverted 이력에서 확인할 실제 객체 diff digest |
| attempts / revision | integer NN 0 / bigint NN 1 | attempts>=0; CAS, 무제한 자동 재시도 아님 |
| created_at / updated_at / applied_at | timestamptz NN now() / timestamptz NN now() / timestamptz ? | 적용 실제 시각, 이후 reverted여도 원 적용시각 보존 |

UNIQUE(run_id,source_system,source_table,source_pk). 한 run의 같은 원본 key/fingerprint에서 두 결과를 만들지 않는다.
인덱스: (run_id,state,id), (target_entity_id), (source_system,source_table,source_pk). PK JSON은 원본 형식 그대로가 아니라 §2 검증된 구조다.
run_id/원본 key/fingerprint/plan_hash는 처리 중 불변이다. 수정된 원본은 새 run의 새 판정이며 기존 applied row를 덮어쓰지 않는다.
target_mode=existing의 target_entity_id는 planned_target_id와 같고 실재 FK를 만족한다. create는 계획 단계 target_entity_id=NULL이며 applied transaction에서 계획 UUID의 Entity를 생성·연결한다.
create인데 그 UUID/원본 binding이 먼저 생겼으면 이름/높은 점수로 덮어쓰지 않고 conflict로 재계획한다. none의 두 target ID는 NULL이다.
기존 source의 신분이 미해소됐다는 이유로 create를 승인하지 않는다. 원본 식별·분류/권리 검토와 정확 UUID 계획을 거쳐야 한다.

| target_mode | planned_target_id | target_entity_id | expected_entity_revision / expected_identity_revision | expected_owner |
|---|---|---|---|---|
| none | NULL | NULL | 둘 다 NULL | NULL |
| existing | NN | 같은 UUID, 기존 Entity FK | 둘 다 >0 | NN |
| create | NN | applied/reverted이면 같은 UUID FK; 그 전에는 NULL | 둘 다 정확히 0 | NN |

위 truth table을 하나의 상태별 CHECK로 고정한다. create의 기대 버전 0은 실제 Entity revision=0을 허용하는 뜻이 아니다.
record를 applied로 전이하는 순간에는 mode=apply, state=running인 유효 run을 잠금 후 확인하고 actual count/proof/recovery_ref/hash/applied_at을 갖춰야 한다.
paused에서는 새 apply를 허용하지 않는다. 승인·snapshot 재검사와 재개 CAS로 running이 된 뒤에만 진행한다.
이는 전이 시점 제약이다. 완료된 applied 기록의 parent run은 후속 집계/중지에 따라 completed/paused 등으로 바뀔 수 있다. run 완료 전환은 마지막 batch commit 이후 별도 transaction에서 검사한다.
dry_run에서 승인된 변환 검사 결과는 validated다. apply/applied를 가장하지 않는다. archive/exclude는 서비스 객체를 수정하지 않고 excluded로 계상한다.
최종 apply run에는 validated/planned/failed가 남으면 completed로 전환하지 않는다. approved held/excluded는 별도 분모로 남기고 ‘전체 사용 가능’으로 보고하지 않는다.
run terminal 상태와 record 변경의 경쟁은 run 행 잠금→기존 legacy/common 부모 순서→record ID 순서로 직렬화한다. 병합/import는 섞지 않고 identity merge는 별도 기존 operation으로 검수한다.
Entity 생성/원본 binding 변경이 포함되면 run 잠금 다음에 기존 transaction-level identity lock을 취한 뒤 부모를 잠근다. 정체성 writer는 거꾸로 run을 요구하지 않는다.

실제 값 변경/guard 반영/감사/record applied는 **같은 KDB transaction**이다. importer가 결과 응답을 못 받아도 동일 record를 조회해 결과를 복구한다.
기존 `kentity_audit_events`에 migration_record_id(uuid ?,NULL,FK records(id))만 추가하고 인덱스(migration_record_id,id)를 둔다.
객체별 기존 before_value/after_value/object_table/object_key/hash를 재사용한다. audit UPDATE/DELETE 및 원본 payload 전량 삽입은 금지한다.
복구는 이 record의 기대 after-hash와 현재 실제값을 비교한 새 감사 작업이다. 후속 사용자 변경이 있으면 자동 원복 대신 보류한다.
terminal run의 일반 record 변경은 금지한다. 유일한 예외인 applied→reverted는 승인된 복구 writer가 run 잠금/CAS·after-hash 대조·새 감사/복구 reference를 같은 transaction에 남길 때만 허용한다.
원래 applied_at/actual_object_count/hash와 적용 감사는 보존한다. run completed는 원래 승인 batch의 처리 완료이지 복구 뒤 현재 서비스 데이터가 그대로라는 뜻이 아니다.

## 5. kentity_source_guards — 신규, 현재 효력만

전체 컬럼:

| 컬럼 | 타입·NULL·기본값 | 제약/의미 |
|---|---|---|
| id | uuid NN, gen_random_uuid() | PK |
| origin_system / origin_table / origin_pk | text NN / text NN / jsonb NN, — | 판단의 원본 전체 PK, §2; 과거 logs 원문을 복사하지 않음 |
| scope_key | text NN, — | <=768 UTF-8 bytes, 아래 정해진 scope의 canonical encoding; 이름 문자열 key 금지 |
| scope_kind | text NN, — | source_binding/entity/name_slot |
| subject_system / subject_table / subject_pk | text ? / text ? / jsonb ?, NULL | source_binding일 때만 셋 모두 NN, source 원본 전체 PK |
| entity_id / identity_revision | uuid ? / bigint ?, NULL | FK entities(id), entity/name_slot일 때 NN·revision>0; source_binding은 NULL |
| rejected_entity_id | uuid ?, NULL | FK entities(id), rejected_binding일 때만 NN. 해당 원본→특정 대상 연결 기각을 표현 |
| locale | text ?, NULL | FK locales(code), name_slot일 때만 NN, exact tag |
| claim_fingerprint | text ?, NULL | withdrew/rejected 표기의 정확 주장 digest; 전체 슬롯 잠금/hold는 NULL 가능 |
| guard_kind | text NN, — | rejected_binding/withdrawn_name/operator_correction/hold/empty_slot |
| state | text NN, proposed | proposed/active/released/superseded |
| basis_record_id | uuid ?, NULL | FK migration_records(id), 이관에서 온 경우 필수; 일반 수동 검수는 감사로 식별 |
| evidence_id | uuid ?, NULL | entity scope에 있으면 (evidence_id,entity_id)→evidence(id,entity_id) 복합 FK |
| reason_code | text NN, — | 통제 사유. 자유 원문·모델 응답 아님 |
| source_fingerprint | text NN, — | 판단 원본의 현재 효력을 검토한 digest |
| decided_by / decided_at / decision_ref | text ? / timestamptz ? / text ?, NULL | active/released/superseded에 필수, 증빙·검수 reference |
| revision | bigint NN, 1 | >0, 모든 효력 변경 시 증가 |
| created_at / updated_at | timestamptz NN now() / timestamptz NN now() | 운영 관측 |

UNIQUE(origin_system,origin_table,origin_pk,scope_key). scope_key는 scope 종류+Entity UUID/정체성 버전 또는 source typed key+기각 대상 UUID+exact locale+필요 주장 digest를 포함한다.
같은 이름의 A/B는 항상 다른 scope다. source_binding의 동일 원본이 여러 후보 중 누구인지 미정이면 후보 전체를 무근거 기각하지 않고 hold한다.
인덱스: (entity_id,locale,state) WHERE entity_id IS NOT NULL, (subject_system,subject_table,subject_pk,state) WHERE subject_pk IS NOT NULL,
(state,updated_at,id), (basis_record_id). 단일 name_slot에서 서로 다른 원천의 active 판단이 충돌하면 자동 priority로 해제하지 않고 보류한다.

guard_kind/scope 허용 조합:

- rejected_binding: source_binding만, rejected_entity_id 필수. 해당 원본→특정 UUID 연결만 기각한다. 다른 검수된 후보 연결과 다른 원천 동명 인물 전체를 기각하지 않는다.
- withdrawn_name/operator_correction: name_slot만. exact claim_fingerprint가 필수다. 정정할 새 값 자체는 names/근거/감사에 저장한다.
- empty_slot: name_slot만, claim_fingerprint=NULL. 운영자가 의도적으로 비운 대표명 슬롯을 별칭 자동 승격으로 채우지 못하게 한다.
- hold: source_binding/entity/name_slot. 근거가 부족한 상태를 격리할 뿐 ‘별개 사람임’이나 ‘정답 없음’을 주장하지 않는다.

active는 **차단 효력 승인**이지 표기·정체성의 verified 승인이 아니다. 미해소 guard를 임의 UUID에 배정해 해제하지 않는다.
효력 없는 과거 판정을 이름순/최신 timestamp 하나로 active로 수입하지 않는다. 현재 유효성 검토·상충 판단이 필요한 건 state=proposed로 남기며, 검수된 차단 보류는 guard_kind=hold/state=active로 구분한다.
entity/name_slot 변경은 같은 부모 잠금 안에서 Entity revision/dependency_epoch·준비 stale·outbox·감사와 원자적이다.
source_binding guard 생성/해제/대상 해소는 정체성 transaction lock과 공유한다. source binding writer는 그 lock 후 현재 guard를 검사한다.
identity_revision이 달라진 guard를 조용히 무시하지 않는다. 현재 대상 재검토가 끝날 때까지 보류하며 재해소·병합·분리 계획에 포함한다.
guard 해제는 해당 책임자의 기대 revision/근거/사유가 필요하다. source 재수집이나 retry TTL만으로 released로 바꾸지 않는다.
기각 대상이 병합/redirect/유형 정정돼도 guard를 건너뛰지 않고 원 정체성 작업에서 포함·재검수한다. 미해소된 redirect를 다른 후보 승인 근거로 쓰지 않는다.
source false lock을 기존 true 잠금의 해제로 해석하지 않는다. active guard/정책 철회는 이름/Entity 잠금과 독립적으로 공급을 차단할 수 있다.

## 6. 추가 원천 projection의 목표 정리

| 추가 원천의 논리 projection | 물리 목적지/승인 조건 |
|---|---|
| source_key/source_recovery_reference | migration_records(source_system,source_table,source_pk,source_fingerprint,recovery_ref); PK/key 자체가 name 승인 아님 |
| guard target/현재 outcome | source_guards의 scope/guard_kind/state/reason/reference. 해당 이름/외부 ID는 별도 검수 후 기존 names/external_ids로만 |
| retry_context/retry_not_before | 현재 효력이 있는 hold만 source_guards, 기존 resolver의 next_attempt_at 갱신은 별도 검수. 과거 재시도 큐 전량 복제 없음 |
| source_policy review_input | existing target source_policies의 검토 입력/원본 reference. 코드별 policy namespace/우선순위·locale 범위는 정책 검토 후 확정; 자동 허가 없음 |
| evidence name_claim | kentity_evidence의 typed name claim + name_evidence. 원본 Entity 귀속/locale/원문 계열·권리 확인 필요 |
| restricted actor reference | 기존 audit.actor에 검수된 내부 식별자로 변환. 외부 고객 키/주소를 일반 UI에 노출하지 않음 |

source_policies의 provider는 무조건 TDB provider 문자열을 복사하지 않는다. 같은 adapter 아래 서로 다른 dataset/locale/이용조건을 하나의 허가로 합치지 않는다.
이 부분의 최종 source_code→policy namespace와 locale/우선순위 컬럼 계약이 확정되기 전 해당 source-policy 필드는 conditional로 유지한다.
원천 161컬럼을 이 3표의 JSON 한 칸에 몰아 넣는 이관은 금지한다. 선택된 최소 필드/참조 이외에는 기존 접근통제 원본에 남긴다.

## 7. 구현 전 명세 검사와 구현 후 필수 시험

| ID | 검사할 입력/경쟁 | 필수 결과 | 단계 |
|---|---|---|---|
| C01 | source_code만 있고 external_id가 없는 source PK | 전체 PK 누락 거부 | P0 명세/P1 importer |
| C02 | bigint 9007199254740993 또는 text 000123 | precision/선행 0 보존, 숫자 JS 변환 금지 | P0 명세/P1 importer |
| C03 | 같은 source PK, 다른 table/system 및 같은 이름 다른 UUID | 별개 원본/대상 유지; hash/name 기준 합치기 금지 | P0 명세/P1·P2 |
| C04 | 같은 run/source key 재실행, 다른 fingerprint/plan | 같은 완료 결과 재조회 또는 conflict; 덮어쓰기0 | P1·P2 |
| C05 | importer 값 쓰기 후 audit/record 실패 | 전부 rollback; applied 없는 부분 서비스 쓰기0 | P1·P2 |
| C06 | run 중지와 batch commit 경쟁 | run lock/CAS, 승인 범위 밖 반영0, 처리 분모 보존 | P1·P2 |
| C07 | empty_slot/철회 guard, source false lock, 동명 stale response | 자동 승격·잠금 해제·다른 UUID 공급0 | P1·P4 |
| C08 | guarded source binding을 동시에 생성/해제/해소 | 잠금 후 현재 guard 판정; 미해소 값 자동 확정0 | P1·P5 |
| C09 | after-hash 다른 사용자 수정 뒤 원복 | 자동 원복 거부, 변경 보존·검수 | P1·P5 |
| C10 | dry_run validated/held를 운영 완료로 집계 | 적용0과 검증·보류 분모 분리 | P0 명세/P2·P6 |

P0 검사에서는 typed PK/필드 계약과 C01~C03의 합성 예제를 검증한다. C04~C09의 실제 DB 원자성/경쟁은 실행 전이며 문서 PASS로 대체하지 않는다.
P0.02 전체 완료에는 나머지 source policy·서비스 필드 최종 매핑/권한 계약도 필요하다. 이 문서의 3표 작성만으로 G0를 닫지 않는다.
