# 공통 저장·증분·재유입 방지 계약

2026-09-12 KST · `writer-delta-design-v1` · P0.02/P0.05 보강.
상태 정본: [TODO](KDB_INTEGRATION_TODO.md). **설계 및 읽기 조사다. 운영 수정·경쟁 재현·이관 완료가 아니다.**
관련: [정체성](KDB_IDENTITY_CONTRACT.md), [표기/준비](KDB_NAME_READINESS_CONTRACT.md), [물리 구조](KDB_TARGET_SCHEMA.md),
[추가 원천 컬럼·코드 기준](KDB_SOURCE_SUPPLEMENT.json).
후속 물리 명세: [이관 원장·현재 guard](KDB_MIGRATION_CONTROL_SCHEMA.md), [현재 DB 역할/권한](KDB_WRITER_AUTHORITY.md).

## 1. 필요한 보호만 흡수

TDB 레인/에이전트/과거 통계 전체를 복제하지 않는다. 다만 현재 유효한 차단·정정·철회·잠금은 단순 로그가 아니다.
이것을 빼고 이름만 가져오면 이미 기각한 값이 다시 설치된다. 원본 원장은 보존하고, **현재 효력이 있는 보호 판단과 근거 참조만** 변환 대상으로 조사한다.

22:33 KST READ ONLY metadata로 기존 조사 밖의 TDB 5표/63컬럼, KDB 8표/98컬럼을 추가 확인했다.
기존 32표/387컬럼 snapshot을 덮어쓰지 않는다. 추가 13표/161컬럼은 다른 시각의 관측이며 하나의 일관된 데이터 snapshot이 아니다.
원천 행 값·인증키·config·raw payload를 읽지 않았다. 컬럼별 처리 제안은 정답·권리 승인이나 최종 물리 매핑 완료가 아니다.

| 필요한 원천 | 남길 최소 내용 | 복제하지 않을 내용 |
|---|---|---|
| tdb_sources | 원본 code/provider, 차단 상태, 교체 우선순위, locale/생성 여부, 권리 검토 입력 | api_key/key_param/config, 수집 cursor/운영 통계. 원본 비밀 저장소는 그대로 유지 |
| tdb_decisions/holds/review_queue/corrections | 원본 복합키·대상/후보 ID·현재 보류/철회·정정 outcome·근거 참조 | 전량 과거 판정, 모델 점수 기반 승인, 큐 UI/재시도 엔진 복제 |
| kwave_media_observations/news_whitelist | 대상 ID별 표기 관측, 검수 가능한 출처, 모회사/원문 계열 구분 | RSS 수집 통계·본문·수집 설정 |
| kwave_entity_resolution_attempts | 필요한 재시도 억제·판정 참조 | 과거 시간/비용 집계의 master 복제 |
| KDB correction/identity correction/dataqa/recheck/adjudication 원장 | 유효 정정·기각·철회·원복 이전값의 제한 참조 | 전체 모델 응답·raw snapshot·과거 queue 체계 |

원본에서 place_id/entity_id가 NULL인 판정을 이름으로 자동 귀속하지 않는다. 미해소 원본 키로 보류한다.
원본의 active/live/enabled/generated/confidence는 공통 verified/strict-ready의 승인이 아니다.
free text의 정정 사유/근거는 필요한 주장만 추출하며 비밀값·개인정보·본문을 일반 audit에 통째로 넣지 않는다.

## 2. 코드에서 확인한 writer 경계

경로의 KDB는 KDB 소유 개발 clone, TDB는 기존 TDB 저장소다. 정확한 파일 bytes의 SHA-256은 추가 원천 JSON에 고정했다.
HEAD만으로 사용자 dirty 파일을 동일 코드라고 간주하지 않는다. 아래는 **코드 근거**, 운영에서 실패한 건수의 측정이 아니다.

| ID | 현행 경계/근거 | 목표 책임 및 인수 시험 |
|---|---|---|
| W01 | TDB internal/source/wikidata.go:460~499: 잠금/우선순위 조회 후 transaction 시작, 강등 시 재검사 없음 | 공통 name writer가 부모 잠금 후 재판정; X01/X02 |
| W02 | TDB internal/match/persist.go:35~94: 직접 승격, canonical 조회에 FOR UPDATE 없음 | source adapter는 제안만, 같은 name writer로 합류; X01/X03 |
| W03 | TDB migrations/0044_block_disabled_sources.sql:23 및 namehygiene.go:326, latinfix.go:183, match/shoprepair.go:234: source_code 미변경 승격 | alias→canonical/상태/근거 변경에도 현재 source policy 검사; X04 |
| W04 | TDB internal/api/pipeline.go:127~145: 정정 설치와 원장 기록 분리 | 실제 저장·감사·요청 결과를 한 transaction으로; X05 |
| W05 | TDB internal/match/master.go:170~209, source/aihub_promote.go:249~278, source/deleted_recover.go:54 | 장소 생성/선택→이름→link 전체 identity/name writer 소유; 삭제 원천 자동 부활 금지; X06/X11 |
| W06 | KDB internal/kdb/observations.go:95~148: parent_org 독립성 계산 후 표기/판정 쓰기 | 관측은 name 주장별 근거, 공통 source 정책과 독립성 재검사; X07 |
| W07 | KDB corrections/review.go:241~275, dataqa/dataqa.go:324~351, migrations/0090_identity_correction_audit.sql:3 | correction/rollback도 같은 소유·잠금·현재값 비교. 기각/철회 재설치 차단; X05/X11 |
| W08 | KDB migrations/0104_kdb_fill_input_hash_materialized.sql:100~121: child 변경은 부모 fill_input_hash만 갱신 | entity.updated_at 단독 delta 금지. child별 PK/변경·삭제를 대조; X08 |
| W09 | KDB migrations/0118_kentity_write_ownership.sql:50~51: 외국어 canonical만 바뀌면 common revision 그대로 | legacy 외국어 표기/출처/잠금 변경도 별도 source digest·delta로 포착; X09 |
| W10 | TDB decisions/holds는 embedded_rollback.go:33와 match/persist.go:120~138의 실제 판단 입력 | 과거 로그 전량 대신 현재 효력의 guard projection과 원장 reference; X11 |

W01~W10은 이번에 고정한 핵심 경로다. 모든 writer·트리거·DB role의 전수 조사 완료를 뜻하지 않는다.
특히 direct UPDATE를 grep한 목록만으로 우회가 막혔다고 판정하지 않는다. 호출점→실행 프로세스→DB 역할→허용 함수까지 P1/P5에서 연결한다.

## 3. 공통 name writer의 단일 저장 계약

1. 외부 원천 조회/모델 호출/표기 정규화는 transaction 밖에서 한다. 입력은 정확 Entity UUID, locale, 주장/관측 digest, 기대 버전, 요청 키다.
2. 짧은 transaction을 시작한다. legacy 보호가 필요한 경우 legacy 부모 UUID 오름차순 → common 부모 UUID 오름차순을 따른다.
   이름 행이 없는 경우에도 **존재하는 Entity 부모 행**을 잠가 동일 슬롯의 동시 생성을 직렬화한다. 일반 표기 쓰기에 전역 identity advisory lock은 쓰지 않는다.
3. 부모 잠금 후 write_owner/ownership epoch, identity_revision, 요청 revision, 전체/필드 잠금, current canonical과 source 우선순위를 다시 읽는다.
   그 전에 읽은 값은 승인 조건으로 재사용하지 않는다. 같은 표기 문자열이라는 이유로 검증 시각/신뢰도를 올리지 않는다.
4. 현재 source policy와 필요한 근거를 고정된 순서로 검사한다. 정책 차단과 경쟁하는 쓰기는 정책 행 잠금 또는 동등한 직렬화로 보장한다.
   정책 철회는 policy 변경만 짧게 commit하고 부모를 잡은 채 policy를 잡는 경로와 역순으로 Entity fan-out을 하지 않는다.
   철회 전 commit된 값도 외부 read gate가 현재 정책을 검사하므로 철회 후 사용할 수 없어야 한다. 대량 무효화는 outbox로 재시도한다.
5. source binding/관측/locale/형식/시점·동명 소유와 현재 유효한 기각/철회 guard를 검사한다. priority가 높아도 이 검사를 우회하지 못한다.
6. 검수된 변경만 수행한다. canonical 강등/새 값 설치/supports/감사/멱등 결과/epoch·outbox는 원자적이다.
   DB trigger가 쓰기를 조용히 무시하는 경우도 있으므로 반환 성공이나 RowsAffected 하나만 믿지 않고 **실제 저장된 값·근거·잠금**을 재조회한다.
7. 기대 저장 결과가 다르면 전체 rollback하고 not_applied/conflict를 반환한다. 요청 접수/후보 생성/값 설치/요청 locale 준비를 서로 다른 결과로 기록한다.

잠금 획득/상태 변경 시 일반 writer도 동일 부모 순서를 따른다. legacy 우회 writer가 이 프로토콜 밖에 남아 있으면 해당 ID의 전환은 차단한다.
단일 writer란 함수 이름이 하나라는 뜻이 아니다. 일반 worker의 직접 DML 권한을 제한하고 허용 경로 밖 갱신이 거부되는지 시험해야 한다.
서버 DB 역할 변경은 P5의 승인된 전환 단계에서만 한다. 현행 계정을 지금 변경하지 않는다.

## 4. 초기 흡수·증분 선택

**1차 파일럿은 일관된 격리 복원본의 승인 UUID 목록을 읽는 방식**으로 제한한다. 실행 중인 두 DB의 서로 다른 READ ONLY 결과를 단일 snapshot으로 부르지 않는다.
온라인 변경 수집이 아직 검증되지 않았으므로 현재 source updated_at/API timestamp cursor만으로 무손실 이관을 약속하지 않는다.

| 원천 변경 | 탐색/검증 계약 | 금지 |
|---|---|---|
| Entity 부모 | 완전 PK + 선택 필드 digest; 관측 시각은 후보 탐색만 | updated_at이 같다는 이유로 같은 버전 취급 |
| person_details/external_refs | 해당 child PK별 snapshot/delta + entity 귀속, 삭제 tombstone/최종 PK 대조 | fill_input_hash만으로 외부 ID 이동·삭제 이력 복원 |
| 이름/별칭/출처/잠금 | names PK 또는 legacy entity+locale 필드 projection의 별도 digest | common identity revision 하나로 대표 |
| 링크/binding/병합·삭제 | 원본 namespace/완전 PK, 이전·다음 대상 ID, state 및 원천 변경 순서 | 사라진 행을 이름으로 재생성·다른 대상에 재연결 |
| 정책/현재 유효 guard | 정책 key/version 및 유효 guard 집합 digest; 변경 시 영향 사용 차단 | enabled=true만으로 권리 승인, 과거 기각/철회 생략 |

온라인 전환 시 필요한 최소 옵션은 P1/P2에서 하나를 검증해 확정한다.
기본 후보는 **승인 cohort의 원천 쓰기 동결·작업 배출 → 최종 source PK/digest 대조 → common writer 전환**이다.
동결이 불가능하면 별도 승인한 transaction-consistent change capture/삭제 기록이 필요하다. 이벤트 sequence 할당 순서는 commit 순서가 아니므로 단순 MAX(id) cursor를 무손실 근거로 쓰지 않는다.
마지막 대조 이후 source write가 가능하면 cutover를 진행하지 않는다. 서비스 중단이나 DB 트리거 설치를 이번 설계가 승인하지 않는다.
증분 목적 때문에 기존 KDB updated_at 의미를 전역 변경하거나 TDB 전체 source payload를 KDB로 복제하지 않는다.

## 5. manifest에 반드시 남길 값

서비스 Entity 필드와 별도의 이관 제어 산출물이다. P0에서는 파일 계약이며 운영 제어 테이블이 이미 있다는 뜻이 아니다.

- 실행 header: format_version/run_id, DB별 snapshot/복원 식별자, 양쪽 코드 HEAD+사용 파일 digest, schema/migration/trigger 정의 digest, mapper/policy 버전, 승인 cohort 목록 digest, 시작/종료 시각.
- record key: source_system/source_table/**원본 PK 전체의 typed JSON**. 복합키·bigint는 손실 없는 문자열 표현. JSON 속성 순서/NULL/문자 정규화가 고정된 fingerprint_version을 사용한다.
- 분모/상태: include/conditional/archive_only/exclude_operational과 accepted/held/rejected/failed를 별개 필드로 기록. 선택 제외와 처리 실패를 합치지 않는다.
- 변환: 원본 selected_projection_hash, 이전/이후 target UUID+완전 객체 PK, expected_identity/entity/ownership/policy 버전, 필드 disposition·변환 규칙·검증 case ID.
- 보호 projection: 원본 guard 복합키, guard_kind(rejected_binding/withdrawn_name/operator_correction/hold/empty_slot), 대상 Entity는 해소된 경우만, exact locale/주장 fingerprint, 현재 효력/사유 코드, 원본 원장 reference와 검수 버전. 원천 전체 차단은 기존 source_policies로만 관리한다.
- 결과: expected/actual object count, applied row hash, 오류/보류 이유, 멱등 요청 키, 완료 transaction/result 식별자, 제한 전후 diff와 복구 reference.

전량 원장 JSON을 guard 하나에 넣지 않는다. 미해소 Entity는 source key로 보류하며 이름 기반 배정을 금지한다.
원본이 변했거나 guard의 현재 효력을 판별하지 못하면 승인 결과를 재사용하지 않는다. 검수 없이 timestamp 최댓값 하나로 상충 판정을 덮어쓰지 않는다.
정확 PK나 복구 reference가 없는 레코드는 적재 성공으로 계상하지 않는다. 운영 audit에는 비밀/raw 본문을 저장하지 않는다.
후속 이관 제어 명세에서 runs/records/source_guards 및 기존 audit 연결을 물리 컬럼으로 작성했다.
전체 P0.02 인수에는 남은 source policy namespace/서비스 채택·writer 권한과 실제 API 대조가 필요하다.

## 6. 반드시 구현해서 실행할 회귀 시험

아래는 **시험 명세**다. 문서 검사는 이 ID/참조의 존재만 검사하며 DB 경쟁 시험 PASS를 대신하지 않는다.

| ID | 재현할 순서 | 필수 결과 | 연결 |
|---|---|---|---|
| X01 | 자동 교체 판단 뒤 운영자가 잠금, 그 다음 자동 저장 | 자동 저장 거부; 운영자 값/잠금 보존 | W01/W02, P1.07, M13 |
| X02 | 동일 Entity/locale에 canonical 없는 상태에서 두 writer 동시 시작 | 대표명 최대1, loser는 현재 상태 재판정, 강등만 남는 부분 commit 없음 | W01, P1.07, M14 |
| X03 | 기대 source/identity revision 뒤 다른 동명으로 binding 정정 | 오래된 제안 저장0, 다른 ID 이름/근거 불변 | W02, P1.04, M01/M13 |
| X04 | 기존 alias 출처를 blocked로 변경 후 kind만 canonical로 승격 | 승격 거부; 별칭 보관과 공급 허가 분리 | W03, P1.07, M12 |
| X05 | 이름 쓰기 후 감사/결과 저장 직전에 실패 + 재요청 | 모두 rollback 또는 같은 완료 결과 재조회, 성공 이중계상0 | W04/W07, P1.07, M12 |
| X06 | source→place 선택 후 link/name 실패, 삭제 원본 재수집 | 부분 유효 master/잘못된 link 없음; deleted 자동 부활 없음 | W05, P2.06, M10 |
| X07 | 동일 모회사 도메인 두 개가 같은 표기를 공급 | 독립근거2개로 승인 금지, 출처/권리 검수 | W06, P1.07, M14 |
| X08 | 부모 timestamp 그대로 child 수정/삭제/다른 Entity로 귀속 변경 | 이전/다음 ID와 삭제 모두 manifest 계상 | W08, P2.06, M10 |
| X09 | legacy 외국어 표기/출처만 수정, common revision 불변 | source digest 차이로 감지; 구 준비값 공급 금지 | W09, P2.06, M13 |
| X10 | snapshot 뒤 삭제·동일 timestamp 수정·역순 commit 발생 | 최종 PK/digest 대조 또는 검증된 change capture로 누락0 | W08/W09, P2.06, M10 |
| X11 | 철회/수동 정정된 표기를 원천·repair·old worker가 재제안 | 현재 guard/ownership 검사로 재설치 차단 | W05/W07/W10, P1.07/P5.02, M12 |
| X12 | trigger가 쓰기를 무시하거나 동일 요청 commit 후 연결 끊김 | 실제 저장 재조회, 접수≠설치≠ready, 멱등 결과 유지 | W01/W04, P1.07, M13 |

## 7. 이번 범위와 남은 일

확인: 24개 source 파일 digest, 추가 13표/161컬럼 metadata/컬럼별 처리 제안, W01~W10 저장 경계, X01~X12 실행 전 시험 명세.
미완료: 모든 원천/writer/trigger/role 목록, 유효 guard 실데이터 판정, 전체 필드의 최종 target FK/NULL/갱신 책임 연결, 격리 DB 경쟁·재실행 시험.
P0.05 및 G0는 계속 열린 상태다. 기존 API/원본 DB/worker/Gemma 일정은 유지한다.
