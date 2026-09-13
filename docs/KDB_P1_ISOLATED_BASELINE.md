# P1.01 격리 복원 기준선 — 2026-09-13

G0 인수 직후 첫 P1 작업이다. 승인된 격리 DB 에 운영 백업을 복원하고, 코드·이미지·스키마·역할을 재확인하고,
0115~0122 기능을 재사용/보강/회귀 대상으로 나눈다. **운영 DB 는 읽기만 했고 DDL·데이터 변경은 없다.**
실행 상태 정본은 [KDB_INTEGRATION_TODO.md](KDB_INTEGRATION_TODO.md), 구조 대조는 [KDB_SCHEMA_DIFF_0115_0122.md](KDB_SCHEMA_DIFF_0115_0122.md)다.

## 1. 재확인한 기준점

| 항목 | 값 |
|---|---|
| 설계 브랜치 HEAD | `c10eb9a` (feat/kentity-platform-20260912) |
| 운영 checkout | `3f8af1b` + 미커밋 52 (보존 대상, 건드리지 않음) |
| 운영 이미지 | `kdb-app:entity-center-20260912-1`, healthy, restarts 0, 기동 2026-09-12T09:46Z |
| 헬스 version | `entity-center-20260912-1` (이미지 태그와 일치) |
| 운영 DB | PostgreSQL 16.14 / 472 MB / migration 72, 최신 `0122_kentity_tdb_crosswalk.sql` |
| 운영 객체 | 표 60 · 뷰 1 · 트리거 16 · kentity/kdb 함수 19 · 확장 pg_trgm·plpgsql |
| 로그인 역할 | `kdb` 하나 (superuser) — 변경 없음 |

## 2. 격리 복원본

| | |
|---|---|
| 컨테이너 | `kdb-p1-restore-db` (postgres:16-alpine, label `kdb.disposable=true`) |
| 격리 | **호스트 포트 미노출 0개**, 전용 volume `kdb-p1-restore-vol`, `--memory=1500m`, `--restart=no` |
| 원본 | `backups/kdb-20260913-120002.sql.gz` (91.9 MB, `gzip -t` OK, 전개 384 MB) |
| 결과 | exit 0, **ERROR 0 / FATAL 0** (로그 101줄은 시퀀스 `setval` 출력) |
| 크기 | 418 MB (운영 472 MB — 차이는 운영의 dead tuple) |
| 제거 | `docker rm -f kdb-p1-restore-db && docker volume rm kdb-p1-restore-vol` |

### 무결성 대조 — 구조 10/10 일치

표 60 · 뷰 1 · 시퀀스 19 · 인덱스 156 · 제약 156 · 트리거 16 · 함수 51 · enum 29 · 확장 2 · migration 원장 72. 전부 운영과 동일.

행수는 백업이 12:00 시점이라 그 뒤 worker 증가분만큼 운영이 많다. **감소한 표는 없다(손실 0).**

| 표 | 운영(現) | 복원(12:00) | 증가 |
|---|---|---|---|
| kentity_entities | 19,543 | 19,520 | +23 |
| kwave_entities | 19,539 | 19,516 | +23 |
| kentity_preparation_items | 321 | 182 | +139 |
| kentity_locale_readiness | 966 | 549 | +417 |
| kwave_kdb_enrich_attempts | 127,188 | 127,007 | +181 |
| kentity_crosswalks / names / corrections | 5,701 / 7 / 3,799 | 동일 | 0 |

## 3. D-항목 해소 결과

[KDB_SCHEMA_DIFF_0115_0122.md](KDB_SCHEMA_DIFF_0115_0122.md) §4 가 P1 로 넘긴 항목을 복원본에서 실측했다.

| ID | 결과 | 판단 |
|---|---|---|
| **D-01** | `kentity_evidence` 총 **1행**. 옛 키(entity_id,provider,source_record_id,source_url) 중복 **0**, 새 키(+claim_type,fingerprint,observation_hash) 중복 **0** | UNIQUE 를 **신규 추가**해도 정리할 데이터 없음. 위험 0 |
| **D-03** | `kentity_preparation_items` 182행 중 supplied 3 / resolved 112 / bound 112, **고아 UUID 0/0/0** | Entity FK 3개를 **그대로 추가 가능**. NULL 처리·감사 불필요 |
| **D-05** | `btree_gist` **1.7 설치 성공**. §6.1+§14.7 EXCLUDE 시제품 6/6 통과 (아래 §4) | 설계대로 구현 가능 |
| **D-07** | `kentity_entities.status` 기본값 **없음** 확인 | `SET DEFAULT 'candidate'` — 기존 행 무영향 |
| **D-11/12/13** | crosswalks(entity_id) · candidate_requests(entity_id) · id_reservations(native_owner) 인덱스 **전부 없음** 확인 | 인덱스 3개 추가 |
| **D-02** | (완화안 설계는 P1.02) 현행 CHECK `kentity_tdb_binding_required` + `qid ~ '^Q[1-9][0-9]*$'` 유지 중 | P1.02 에서 완화안 작성·시험 |

## 4. §6.1 EXCLUDE 실증 — 6/6

격리본 `p1proto` 스키마에 시제품 표를 만들어(운영 표 미접촉) 검증했다.
`possible_validity daterange GENERATED ALWAYS AS (...) STORED` + `EXCLUDE USING gist (entity_id =, role_code =, possible_validity &&) WHERE status='verified'`.

| 시나리오 | 기대 | 결과 |
|---|---|---|
| 같은 사람 + 같은 직업 + 겹치는 기간(verified) | 거부 | ✅ 거부 |
| 같은 사람, 다른 직업(singer+actor) | 허용 | ✅ 허용 |
| **다른 사람(동명이인), 같은 직업·같은 기간** | 허용 | ✅ 허용 — **M01 만족** |
| unverified | 허용(부분 인덱스 밖) | ✅ 허용 |
| 연도만 아는 2024 + 겹치는 day 기간 | 보수적 거부 | ✅ 거부 |
| unknown(무한 경계) + 임의 기간 | 보수적 거부 | ✅ 거부 |

핵심 확인: 연도만 아는 행은 `valid_from` 이 **NULL 그대로**이고 `possible_validity` 만 `[2024-01-01,2025-01-01)` 로 계산된다.
**가짜 1월 1일을 저장하지 않으면서** 중복 검사는 보수적으로 동작한다 — §14.7 S07 의 요구가 물리적으로 성립한다.
생성열 표현식이 IMMUTABLE 조건을 만족함도 함께 확인됐다(STORED 생성 성공).

## 5. DB 세션 귀속 — 미확인 항목 해소

[KDB_WRITER_AUTHORITY.md](KDB_WRITER_AUTHORITY.md) §미확인 사항의 "idle 세션이 어느 binary 인지 미확인"을 좁혔다.

- 운영 `kdb` DB 의 비-psql 세션 **10개 전부 `client_addr=172.21.0.4`**, 이 IP 는 컨테이너 **`kdb-app` 하나**다.
- `application_name` 은 여전히 빈 값이라 **컨테이너 안의 어느 코드 경로인지는 미확인**이다. pool 이 여러 개인지도 아직 모른다.
- 따라서 권한 매트릭스의 `kdb_api`/`kdb_worker`/`kdb_admin` 분리는 **한 바이너리 안에서 pool 을 나눠야** 한다. 별도 프로세스 가정은 틀렸다.
- 다음: P1.02 에서 pgx pool 별 `application_name` 을 붙여 경로를 가른다. 운영 권한 회수는 그 관측 이후(P5).

## 6. 0115~0122 재사용 / 보강 / 회귀 대상

트리거 16개 중 11개가 0115~0122 산이고 5개(fill-hash 계열)는 0104/0105 산이다.

| 트리거 | 표 | 출처 | 분류 | P1.02 영향 |
|---|---|---|---|---|
| kentity_legacy_owner | kentity_entities | 0116→0118 | **회귀** | D-07 status DEFAULT 가 같은 표 → BEFORE 트리거 재확인 필요 |
| kentity_legacy_identity | kwave_entities | 0116→0118 | 재사용 | legacy 동기화. P5 전환 때 재검토 |
| kentity_verified_name | kentity_names | 0116→0118 | **보강** | §12.3 새 컬럼·UNIQUE 교체 시 함수 조건 확장 |
| kentity_automatic_name_guard | kentity_names | 0121 | **보강** | 동일 |
| kentity_evidence_observation | kentity_evidence | 0117 | **회귀** | D-01 UNIQUE 추가가 같은 표 |
| kentity_evidence_invalidation | kentity_evidence | 0117→0118 | **회귀** | 동일 |
| kentity_dependent_evidence_invalidation | kentity_evidence | 0121 | **회귀** | 동일 |
| kentity_evidence_dependency_guard | kentity_evidence_dependencies | 0121 | **보강** | §12.2 PK 를 (evidence_id,depends_on_id) 로 확장 |
| kentity_ownership_decision | kentity_ownership_decisions | 0118 | 재사용 | 표 보존 |
| kentity_legacy_external_id_reservation | kwave_entity_external_refs | 0117→0118 | **보강** | §10.1 native_owner→entity_id 호환 변경 |
| kentity_tdb_binding_invalidation | kentity_tdb_shadows | 0122 | 재사용 | D-02 완화 시 재확인 |
| trg_kdb_fill_hash_ins/upd/refs/person | kwave_* | 0104/0105 | 재사용 | 범위 밖 |
| trg_kdb_attempts_stamp_hash | kwave_kdb_enrich_attempts | 0105 | 재사용 | 범위 밖 |

표 22개는 [KDB_SCHEMA_DIFF_0115_0122.md](KDB_SCHEMA_DIFF_0115_0122.md) §2 의 분류를 그대로 쓴다: 기존 보강 17 · 재사용 5(readiness_events, candidate_requests, ownership_decisions, tdb_shadows, tdb_shadow_events) · legacy adapter 뷰 1.

**회귀 시험이 반드시 필요한 것: evidence 3개 + entities 1개 = 4 트리거.** P1.02 가 건드리는 표에 걸려 있다.
**보강 필요: 4개** (names 2 · dependencies 1 · external_refs 1). **그대로 재사용: 8개.**

## 7. P1.02 로 넘기는 forward migration 초안 범위

D-항목 중 데이터 위험이 0 으로 확인된 것만 1차로 묶는다.

1. `CREATE EXTENSION btree_gist` (검증 완료)
2. `kentity_evidence` 새 UNIQUE 7컬럼 추가 (중복 0 확인)
3. `kentity_preparation_items` Entity FK 3개 (고아 0 확인)
4. `kentity_entities.status SET DEFAULT 'candidate'`
5. 인덱스 3개: crosswalks(entity_id), candidate_requests(entity_id), id_reservations(native_owner)
6. D-02 완화안은 **별도 검토 후** — CHECK 를 바꾸면 QID 없는 TDB 수용 범위가 열리므로 M05/M08 시험과 함께 간다

위 1~5 는 기존 행에 영향이 없다(실측). 6 은 의미 변경이므로 시험을 먼저 붙인다.
적용은 **격리본에서만** 하고, 운영 적용은 P3.02 승인 범위 안에서 별도 판단한다.
