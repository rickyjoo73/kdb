# KDB/TDB writer 권한 경계 조사

상태: **read-only 관측 및 잠정 개선안**. 이 문서는 role, grant, 함수, trigger 또는 배포 상태를 변경했다는 기록이 아니다. 실제 변경은 금지되어 있으며, 운영 runtime 권한은 격리 환경에서 호출 경로와 rollback을 검증하기 전에 회수하면 안 된다.

관련 기준은 [writer delta contract](KDB_WRITER_DELTA_CONTRACT.md)와 [integration TODO](KDB_INTEGRATION_TODO.md)다.

## 관측 범위와 안전 조건

- 관측 시각: `2026-09-12 23:36:11 KST` 부근. KDB가 반환한 시각은 `2026-09-12 23:36:11.12095+09`, TDB가 반환한 시각은 `2026-09-12 14:36:11.140423+00`로 같은 시점이다.
- 별도 재확인: `2026-09-12 23:45 KST` bounded read-only 조회에서도 KDB `kdb`와 TDB `tdb` 각각 `SUPERUSER`, `BYPASSRLS`, `CREATEROLE`, `CREATEDB`, `REPLICATION`이 true였고, 비시스템 login role은 DB별 1개였다. 관측 세션은 KDB 11개, TDB 4개로 모두 해당 단일 role이었다. 이 재확인은 role 핵심값만 대상으로 했고 비밀값은 조회하지 않았다.
- 두 DB 모두 PostgreSQL `16.14`이며, 컨테이너 이미지는 `postgres:16-alpine`이었다.
- 모든 SQL은 `BEGIN READ ONLY`와 transaction-local `statement_timeout = '6s'` 안에서 실행했다.
- password, `pg_authid`, 환경변수, raw config, API key, 원천 row 및 raw payload는 읽지 않았다. DB, 서비스, 파일, migration을 변경하거나 배포하지 않았다.
- 세션의 `application_name`이 비어 있어 관측된 idle 연결을 API, worker, cron 등 특정 runtime binary에 귀속할 수 없다. 아래 pool 설계는 현재 프로세스 귀속을 입증하지 않는다.

## 현재 관측값

| 항목 | KDB | TDB | 의미와 한계 |
|---|---|---|---|
| 현재/유일한 비시스템 login role | `kdb` | `tdb` | 둘 다 `SUPERUSER`, `CREATEROLE`, `CREATEDB`, `REPLICATION`, `BYPASSRLS`, `LOGIN`, `INHERIT`다. |
| 관측 세션 | `kdb`: idle 10, 조사 psql active 1 | `tdb`: idle 3, 조사 psql active 1 | idle 세션의 `application_name`이 없어 실제 runtime binary는 미확인이다. |
| `public` relation owner | `kdb`: table 60, sequence 19, view 1 | `tdb`: table 27, sequence 7, view 1 | 현재 role은 owner이자 superuser라 직접 DML/DDL이 가능하다. |
| table grant / RLS | 별도 non-owner/PUBLIC table grant 없음, public RLS/FORCE RLS 0 | 동일 | grant 부재가 경계가 아니다. owner/superuser가 모두 우회한다. |
| `public` schema | owner `pg_database_owner`; PUBLIC `USAGE` | owner `tdb`; PUBLIC usage 없음 | TDB의 PUBLIC 함수 EXECUTE는 schema 접근이 추가되면 활성화될 수 있는 잠재 권한이다. |
| migration metadata | DB 72행, clone SQL 72개, latest `0122_kentity_tdb_crosswalk.sql` | DB 87행, repo SQL 87개, latest `0087_merge_exact_master_duplicates.sql` | KDB table에는 checksum column이 없다. TDB에는 checksum이 있으나 이번 조사에서 87개 전체를 source와 대조하지 않았다. 따라서 count/latest 일치는 byte identity 증명이 아니다. |
| public non-internal trigger | 16개, 모두 enabled | 13개, 모두 enabled | trigger function 중 SECURITY DEFINER는 0개다. trigger는 현 superuser/owner에 대한 권한 경계가 아니다. |
| 조사 대상 writer function | 19개, 전부 SECURITY INVOKER, function-local `search_path` 없음 | 21개, 동일 | 현재 role과 PUBLIC에 기본 EXECUTE가 있다. KDB는 PUBLIC schema usage도 있어 즉시 접근 가능하다. |

KDB의 최근 migration 적용 시각은 `0122` 기준 `2026-09-12 17:31:56+09`, TDB는 `0087` 기준 `2026-08-25 02:24:40+00`이었다. 이 값도 migration 내용 동일성을 보장하지 않는다.

KDB의 16개 trigger는 legacy owner/name guard, evidence/dependency/observation guard와 invalidation, ownership adoption, TDB shadow invalidation, legacy sync, fill-hash, external reference reservation, enrichment stamp를 포함한다. TDB의 13개는 `tdb_place_names` 11개와 `tdb_places` 2개다. `sources`, `src_records`, `links`, `decisions`, `holds`, `corrections`에는 user trigger가 관측되지 않았다.

현재 `kdb`/`tdb`는 trigger disable, DDL, 직접 DML, `session_replication_role` 변경 및 RLS 우회가 가능하다. 따라서 trigger는 데이터 규칙일 뿐 현재의 권한 경계가 아니다. 또한 TDB blocked-source trigger는 `INSERT OR UPDATE OF source_code`에만 걸려 kind-only alias→canonical 변경에는 발화하지 않으며, canonical script guard는 요청을 실패시키는 대신 `NEW.kind = 'alias'`로 되돌릴 수 있다. 성공/RowsAffected만으로 승격 완료를 판정해서는 안 되고 저장 후 상태를 확인해야 한다.

## 소스 근거

- `docs/KDB_WRITER_DELTA_CONTRACT.md:29-48`: W01–W10 writer inventory; `:50-67`: single-writer 계약; `:69-86`: delta; `:104-126`: negative test와 role inventory 요구.
- `docs/KDB_INTEGRATION_TODO.md:82-108`: P0.05 권한 조사와 P1.01/P1.07/P1.09 후속 경계.
- KDB clone `migrations/0116_kentity_core.sql:102-184`: legacy owner/name guard와 legacy sync.
- KDB clone `migrations/0118_kentity_write_ownership.sql:9-60,85-135`: write owner, adoption, external reference reservation, evidence invalidation.
- KDB clone `migrations/0121_kentity_common_fill.sql:12-44`, `migrations/0122_kentity_tdb_crosswalk.sql:13-31`: dependency/automatic-name guard와 TDB shadow invalidation.
- KDB clone `internal/kdb/observations.go:95-148`, `internal/kdb/corrections/review.go:241-275`, `internal/kdb/dataqa/dataqa.go:324-351`: 함수 경계를 우회할 수 있는 현 direct writer 후보.
- TDB `migrations/0044_block_disabled_sources.sql:10-24`, `migrations/0073_canonical_promotion_guard.sql:33-48`: blocked source와 canonical promotion guard의 실제 발화/재작성 의미.
- TDB `internal/source/wikidata.go:460-499`, `internal/match/persist.go:35-103`, `internal/api/pipeline.go:127-147`: source, match, correction split direct writer 후보.

## 잠정 최소권한 모델

다음 capability는 검토 단위이지 곧바로 설치할 8개 서비스 또는 8개 login role 목록이 아니다. 실제 pool 수는 호출 경로 조사 후 최소화하고, object owner는 login pool과 분리한다.

| 논리 capability | 허용 범위 | 잠정 login pool 묶음 | 금지 범위 |
|---|---|---|---|
| `catalog_read` | 승인된 view/table SELECT | `api_pool`, `admin_pool` | DML, DDL, writer EXECUTE |
| `request_intake` | 제한된 intake 함수 호출 | `api_pool` | canonical 직접 변경, owner/tenant 임의 선택 |
| `source_propose` | source observation/proposal 기록 | `worker_pool` | canonical promotion/withdrawal 승인 |
| `canonical_apply` | 검증된 좁은 함수로 canonical 반영 | 필요 시 `worker_pool`의 별도 capability | table 직접 DML, arbitrary actor 승인 |
| `review_decide` | authenticated reviewer 결정 함수 | `admin_pool` | 대상 owner/tenant 우회, 원시 DML |
| `tdb_observe` | 허용된 TDB read/view | `observer_pool` 또는 필요한 기존 pool | TDB writer와 DDL |
| `audit_read` | append-only audit 조회 | 감사가 필요한 pool | audit 수정/삭제 |
| `migration_ddl` | migration 시점 DDL/ownership | 일회성 migrator | 상시 runtime login |

`kentity_owner`/`tdb_owner` 같은 object-owner role은 `NOLOGIN` 후보이며, runtime pool에는 소유권과 superuser를 부여하지 않는다. 그러나 이는 아직 적용안이 아니다. 현재 어떤 binary가 어떤 SQL 경로를 사용하는지 매핑하기 전 `kdb`/`tdb`의 운영 권한을 revoke하면 장애 또는 부분쓰기 위험이 있다.

## SECURITY DEFINER를 선택할 때의 최소 계약

SECURITY DEFINER는 자동으로 안전한 writer가 아니다. 좁은 함수가 필요한 경우에만 다음을 동시에 만족해야 한다.

1. 함수 owner는 `NOLOGIN` 전용 role이고 함수는 private schema에 둔다. `SET search_path = pg_catalog, <고정 private schema>, pg_temp`를 선언하며 모든 relation/function을 schema-qualified로 참조한다. 임시 schema는 명시적으로 마지막에 두고 private schema CREATE 권한을 일반 caller에 주지 않는다.
2. 함수 생성·`REVOKE ALL ON FUNCTION <정확한 signature> FROM PUBLIC`·필요 capability에만 EXECUTE 부여를 한 transaction으로 수행해 PUBLIC 실행 가능 구간을 만들지 않는다. trigger function의 불필요한 PUBLIC EXECUTE도 회수한다.
3. 함수 내부에서 target UUID, tenant/scope, 현재 `write_owner`, expected revision, source 상태, withdrawal/tombstone 및 idempotency를 다시 검증한다.
4. 앱이 자유롭게 전달한 `actor`/`reviewer` 문자열을 DB role이 승인 근거로 받아서는 안 된다. 권한은 인증된 server-side principal과 session/pool capability에서 도출하고, 공유 pool이라면 변조 불가능하게 검증된 서버 context를 사용한다. actor 문자열은 감사 metadata일 뿐 authorization이 아니다.
5. SECURITY DEFINER 함수만 추가한 채 superuser pool과 direct DML을 유지하면 경계가 생기지 않는다. 호출 경로 전환, 실제 저장상태 검증, 관측 및 rollback 후에만 direct grant를 단계적으로 회수한다.

고정 search_path의 pg_temp 순서와 함수 생성/권한 부여의 원자성은 [PostgreSQL 16 공식 함수 보안 지침](https://www.postgresql.org/docs/16/sql-createfunction.html#SQL-CREATEFUNCTION-SECURITY)의 기준을 따른다. 설계 원칙 확인이지 실제 역할 시험 통과가 아니다.

## 최소 negative cases

| ID | 반드시 실패하거나 보존해야 할 조건 |
|---|---|
| A01 | read/intake 전용 pool이 canonical table의 INSERT/UPDATE/DELETE, DDL, trigger disable 또는 `session_replication_role` 변경을 시도하면 거부된다. |
| A02 | intake/source-propose caller가 canonical을 직접 바꾸거나 임의 owner/tenant/target UUID를 선택하면 거부된다. 허용된 함수만 정확한 signature로 실행된다. |
| A03 | canonical apply가 잘못된 UUID, `write_owner`, expected revision, blocked source 또는 withdrawn evidence를 받으면 원자적으로 거부되고 부분쓰기하지 않는다. |
| A04 | client-controlled `actor`/`reviewer` 값만 바꿔 review 권한을 얻을 수 없다. cross-tenant/cross-owner 요청은 거부되고 감사 actor는 인증된 서버 context에서 나온다. |
| A05 | PUBLIC 및 미승인 role은 writer/trigger 함수를 실행할 수 없다. 임시/공격자 schema object로 고정 `search_path`와 schema-qualified lookup을 가로챌 수 없다. |
| A06 | runtime pool은 migration DDL을 수행할 수 없다. TDB kind-only blocked-source 변경과 silent alias rewrite는 반환값이 아닌 저장된 최종 상태로 검증하며, 권한 cutover 실패 시 검증된 rollback으로 기존 writer를 복구한다. |

## 단계 경계

| 단계 | 이 문서에서 허용되는 결과 | 금지/exit 조건 |
|---|---|---|
| P0 / P0.05 | read-only role·owner·ACL·trigger·migration inventory, 호출 경로와 capability 설계 | role 생성, grant/revoke, SECURITY DEFINER 전환, 운영 배포 없음 |
| P1 | 격리 restore에서 owner/login 분리, 함수/role 생성, `SET ROLE` A01–A06, Go 통합 test, binary→pool 매핑과 `application_name`, rollback 연습 | 실제 runtime 경로가 모두 매핑·통과하기 전 운영 revoke 없음 |
| P5 / 전환 | 승인된 cohort에서 capability 함수 선배포, pool별 이동, replay/metrics 관측 후 direct DML을 단계적으로 회수 | 누락 writer, 저장상태 불일치, rollback 미검증이면 중단하고 회수하지 않음 |

## 미확인 사항과 배포 금지

- 현재 idle DB 세션이 어느 API/worker/cron binary 및 어느 코드 경로에서 생성됐는지는 미확인이다.
- KDB 72개 migration에는 DB checksum 근거가 없고, TDB 87개 checksum도 이번 조사에서 source와 전수 대조하지 않았다.
- direct writer 후보 목록은 실제 트래픽 소비자 전수 목록이 아니다. 동적 SQL, 운영 job, 수동 runbook과 외부 consumer는 별도 확인이 필요하다.
- 이 문서는 role/grant/함수 DDL의 배포 승인서가 아니다. P1 격리 검증과 실제 소비자 매핑 없이 운영 superuser/direct-DML 권한을 회수하거나 SECURITY DEFINER 함수를 배포하지 않는다.

## 물리 권한 매트릭스 — P0.02 설계 (미적용)

위 "잠정 최소권한 모델"의 capability 를 실제 표·함수에 대응시킨 목표안이다. 2026-09-13 카탈로그 기준 현행은
login role `kdb` 하나(superuser, 표 60·함수 20 owner)이고, 함수 20개는 SECURITY INVOKER·PUBLIC EXECUTE 기본·`search_path` 미고정이다.
이 절은 role 생성·GRANT/REVOKE 를 수행하지 않는다. P1 격리 복원본에서 아래를 생성해 A01~A06 을 시험하고, 운영 전환은 P5 다.

### role 구성 (5 + owner)

| role | LOGIN | 용도 | application_name |
|---|---|---|---|
| `kentity_owner` | NO | 모든 kentity_*/kwave_* 객체·함수 owner. superuser 아님 | — |
| `kdb_migrator` | YES(배포 시만) | `SET ROLE kentity_owner` 후 DDL. 상시 접속 금지 | `kdb-migrate` |
| `kdb_api` | YES | catalog_read + request_intake | `kdb-api` |
| `kdb_worker` | YES | source_propose + canonical_apply(함수 EXECUTE 만) | `kdb-worker:<lane>` |
| `kdb_admin` | YES | review_decide + audit_read | `kdb-admin` |
| `kdb_observer` | YES | TDB read-only view | `kdb-tdb-observer` |

기존 `kdb` superuser 는 P5 전환 완료·rollback 검증 후 회수하며, 그 전에는 세션마다 `application_name` 만 먼저 붙여 binary→pool 매핑을 관측한다(현재 idle 세션 10개 미확인).

### 표 등급별 권한

| 등급 | 표 | kdb_api | kdb_worker | kdb_admin | 비고 |
|---|---|---|---|---|---|
| 사전 | types, subtypes, role_types, domains, locales, relation_types, relation_type_pairs, position_types | SELECT | SELECT | SELECT | 쓰기는 migrator 만 |
| canonical | entities, names, name_evidence, evidence, evidence_dependencies, external_ids, id_reservations, crosswalks, relations, person_roles, entity_domains, person_profiles, location_profiles, identity_decisions, identity_operations, redirects | SELECT | **직접 DML 없음** — writer 함수 EXECUTE | 직접 DML 없음 — review 함수 EXECUTE | 모든 변경은 부모 잠금·revision·감사를 포함한 함수 경로 |
| 원장·큐 | preparations, preparation_items, locale_readiness, fill_waiters, locale_fill_jobs, resolution_jobs, candidate_requests, readiness_events, invalidation_outbox | INSERT/SELECT/UPDATE(자기 owner_key 행) | SELECT/UPDATE(lease·state) | SELECT | owner_key 는 인증 context 에서 도출, 클라이언트 입력 아님 |
| 이관 제어 | migration_runs, migration_records, source_guards | — | records/guards 실행 함수 EXECUTE | runs 승인 함수 EXECUTE | 승인 capability 와 실행 capability 분리(제어 설계 §3) |
| 감사 | audit_events, tdb_shadow_events | — | INSERT(함수 내부) | SELECT | UPDATE/DELETE 는 owner 포함 누구에게도 GRANT 하지 않음 |
| 소유권 | ownership_decisions | — | — | 승인 함수 EXECUTE | 과거 판정 UPDATE 금지 |
| TDB 관측 | tdb_shadows | — | tdb_observer 만 INSERT/UPDATE | SELECT | |
| legacy | kwave_entities, kwave_entity_external_refs, kwave_entity_person_details, kwave_persons | SELECT(뷰 경유) | 전환 전: 기존 lane 이 직접 DML(현행 유지) | SELECT | P5 에서 write_owner 전환 순서대로 회수 |

### 함수 강화 (20개 전부)

1. `REVOKE ALL ON FUNCTION <정확 signature> FROM PUBLIC` — 트리거 함수 포함. 필요한 role 에만 EXECUTE.
2. 트리거 함수는 SECURITY INVOKER 유지 + `SET search_path = pg_catalog, public, pg_temp` 고정(pg_temp 마지막).
3. canonical writer 함수만 SECURITY DEFINER 후보이며, 위 "SECURITY DEFINER 를 선택할 때의 최소 계약" 5조건을 동시에 만족해야 한다. owner 는 `kentity_owner`.
4. 함수 생성·REVOKE·GRANT 는 한 트랜잭션.

### A01~A06 대응

A01 → kdb_api 로 canonical INSERT/DDL/`session_replication_role` 시도 거부. A02 → kdb_api 가 함수 우회 직접 UPDATE 거부. A03 → writer 함수에 잘못된 revision/withdrawn evidence 전달 시 원자 거부. A04 → actor 문자열 변조로 review 권한 획득 불가(권한은 role 에서). A05 → PUBLIC 의 함수 EXECUTE 0, pg_temp 객체로 search_path 가로채기 불가. A06 → kdb_worker 의 DDL 거부, cutover 실패 시 `kdb` 유지로 rollback.

### 배포 금지 경계

이 절은 설계다. 운영 `kdb` 권한 회수, role 생성, GRANT/REVOKE, SECURITY DEFINER 전환은 P1 격리 시험(A01~A06 PASS)과 P5 전환 승인 전에는 수행하지 않는다.
