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

---

# P1.02~P1.06 격리 구현·검증 — 2026-09-13

산출물: [p1/p1_structure.sql](p1/p1_structure.sql)(forward migration, 1,239줄), [p1/p1_tests.sql](p1/p1_tests.sql)(제약 시험 69건).
**두 파일은 `migrations/` 밖에 둔다.** `deploy.yml` 은 `ls migrations/*.sql` 로만 원장을 채우므로 하위 경로는 잡히지 않고,
혼동을 없애려 아예 `docs/p1/` 에 뒀다. 운영 승격은 P3.02 승인 범위에서 번호를 붙여 옮긴다.

## 8. 적용 결과 — 처음부터 재현 가능

`DROP DATABASE → 복원(ERROR 0) → 구조 적용 → 시험` 순서를 그대로 3회 재현했다.

| | 복원 직후 | P1.02 적용 후 |
|---|---|---|
| 표 | 60 | **80** (신규 20) |
| 제약 | 156 | **420** |
| 인덱스 | 156 | 229 |
| 트리거 | 16 | **19** |
| 확장 | 2 | 3 (btree_gist) |

사전 seed: 유형 13(활성 11) · 세부유형 61(59) · 직군 26 · 분야 8 · locale 14(9) · predicate 11 + 조합 30 · 직책 10 · **source_policies 0(기본 차단)**.
직군 3레벨(`rapper→singer→entertainer`) 재귀 조회 동작 확인.

데이터 처리: legacy subtype 18,042건을 `classification_reason` 에 보존하고 NULL 로 내림(원문 손실 0),
기존 `ready` readiness 171건을 `stale` 로 강등(`first_ready_at` 172건 보존), crosswalk 5,701건 `source_table` backfill,
Entity 총계 19,520 불변.

## 9. 제약 시험 — 69/69 PASS

| 영역 | 건수 | 내용 |
|---|---|---|
| P1.03 | 18 | 동명 각자 직업 / 교차 근거 거부 / 겸업 / company 에 직업 거부 / 유형변경 거부 / 잘못된 subtype / NULL 후보 수용 / 사전 밖 코드 / 순환 부모 / `other` 코드 거부 / 이름 비유일 / 근거 없는 verified 거부 / 생일 형태 |
| M01~M05 | 16 | 동명 UUID 분리, 같은 직업·같은 기간 허용, possible_same 보류, 관계 허용표, 역/상점 QID 충돌 |
| M08~M14 | 30 | 외부 ID 단일 소유, 부분 날짜, unknown pending, 병합/분리/guard, waiter/outbox, locale 정확 태그, 정책 승인 |
| P1.06 | 5 | 멱등 키, migration record truth table, 승인 없는 apply 거부, audit 객체 키 |

## 10. 시험이 드러낸 것 — D-16 ~ D-26

설계 문서만으로는 보이지 않던 충돌이다. 전부 격리본에서 재현했다.

| ID | 발견 | 처리 |
|---|---|---|
| **D-16** | `kentity_sync_legacy_identity()` 가 `subtype = NEW.entity_type::text` 로 legacy enum 을 계속 덮어쓴다. subtype 을 NULL 로 내려도 legacy 쓰기 한 번이면 되돌아오고 그 순간 사전 FK 가 깨져 **legacy 쓰기 자체가 실패**한다 | 트리거에서 subtype 투영 제거. legacy 유형은 `kwave_entities.entity_type` 에 그대로 있어 손실 없음 |
| **D-17** | `kentity_names` 에 모호한 legacy 태그 `zh` 1행. 표기 계약은 "zh 를 무조건 Hans 로 바꾸지 않는다" | 값을 고치지 않고 `enabled=false` 비활성 호환 코드로 사전에 추가. 정확 태그 판정은 검수(P2) |
| **D-18** | 기존 `ready` 171건에 S01 의 bound UUID·revision 도 S03 의 policy_proof 도 없다 | 없는 증명을 만들지 않고 `stale` 로 강등. `first_ready_at` 보존 |
| **D-19** | 부분 날짜 CHECK 가 **NULL 전파로 뚫렸다.** `year BETWEEN 1 AND 9999` 가 year NULL 이면 NULL 이고 CHECK 는 NULL 을 통과로 본다 → `precision='month'` + year NULL 이 저장돼 `possible_validity` 가 `(,)` 무한대가 됐다 | `COALESCE(CASE…, false)` + 분기마다 `IS NOT NULL`. person_roles·relations·names 3표 모두 적용 |
| **D-20** | redirect 가 `planned`/`rejected` operation 에도 붙었다. S05 의 "applied merge 만" 은 FK 로 표현할 수 없다 | `DEFERRABLE INITIALLY DEFERRED` 제약 트리거 추가. 같은 transaction 안의 redirect→apply 순서는 허용(M12j), 끝까지 applied 가 아니면 거부(M12k) |
| **D-21** | Entity UUID 불변(I01/I02)을 **DB 가 전혀 강제하지 않았다.** 참조 없는 행은 PK 를 그냥 바꿀 수 있었다 | BEFORE UPDATE 트리거로 차단 |
| **D-22** | `normalized_value` 를 writer 11곳이 제각기 채우는 구조 + `DEFAULT ''` 가 nonempty CHECK 와 충돌 | 정규화는 value 의 순수 함수이므로 트리거 한 곳에서 찍는다 |
| **D-23** | `kentity_adopt_rejected_legacy()` 가 `subtype=''` 를 쓰고 entity_domains 에 policy_version 을 안 준다. P1.01 에서 이 트리거를 "재사용"으로 분류한 건 **오분류**였다 | subtype NULL + policy_version 명시로 보강 |
| **D-24** | `kentity_reserve_legacy_external_id()` 가 `native_owner` 를 읽는다. Go 에는 없어 grep 으로 안 잡혔고 `kwave_entity_external_refs` INSERT 시 런타임에서만 터졌다 | 트리거를 `entity_id` 로 갱신 |
| **D-25** | crosswalk PK 가 3컬럼으로 넓어졌는데 `kentity_invalidate_tdb_binding()` 은 `source_id` 만으로 갱신한다. 지금은 원천 표가 하나라 사고가 안 났을 뿐 | 갱신 조건에 `source_table` 추가 |
| **D-26** | 공통 readiness writer 가 **S03 의 policy_proof 없이 `ready` 를 만든다.** `source_policies` 가 기본 차단(seed 0)이므로 승인된 정책 없이는 ready 가 성립할 수 없다 | **P1.07 로 이월.** writer 가 proof 를 기록하거나 `policy_blocked` 를 내야 한다 |

D-16·D-23·D-24·D-25 는 전부 **트리거**다. P1.01 의 "회귀 4 / 보강 4 / 재사용 8" 분류에서 재사용으로 본 2개가 실제로는 보강 대상이었다.
설계 문서 대조만으로는 트리거 본문의 컬럼 참조를 잡을 수 없다는 뜻이며, 같은 이유로 P5 권한 전환 전에 트리거 본문 전수 대조가 필요하다.

## 11. Go 계층 변경 (P1.02 범위)

스키마를 바꾸면 Go 도 같이 바꿔야 한다. 회귀 테스트가 4종을 잡아냈다.

| 변경 | 파일 | 이유 |
|---|---|---|
| `SELECT e.subtype` → `COALESCE(e.subtype,'')` | store.go(2), catalog.go, tdb_mapping.go | subtype 이 NULL 허용이 돼 스캔이 깨졌다 |
| INSERT 시 `NULLIF($3,'')` | store.go | 빈 문자열은 사전 코드가 아니다 → 미확인은 NULL |
| TDB 원천 유형을 subtype 에 넣지 않음 | tdb_mapping.go | `tdb:person` 은 사전 코드가 아니다(D-16 같은 뿌리) |
| crosswalk 에 `source_table`·`mapping_policy_version` 명시 | tdb_mapping.go(2) | PK 3컬럼 전환 |
| confirm 시 `target_identity_revision` 고정 | tdb_mapping.go | confirmed 는 대상 정체성 버전을 붙들어야 한다(§10.1) |
| `policy_version` 명시 | store.go, tdb_mapping.go, resolution_approve.go + 테스트 3 | entity_domains/external_ids 신규 NOT NULL |
| `native_owner` → `entity_id` | 8파일 | 컬럼 rename |

**배포 순서 주의**: 이 Go 변경은 P1.02 스키마를 전제한다. `deploy.yml` 은 migration 을 먼저 적용하고 컨테이너를 뒤에 띄우므로 순서는 맞지만,
**코드만 먼저 나가면 운영이 깨진다.** P3.02 에서 두 가지를 한 배포로 묶어야 한다.

## 12. 검증 결과

| 검사 | 결과 |
|---|---|
| SQL 제약 시험 | **69/69 PASS** (처음부터 3회 재현) |
| `go build ./...` | PASS |
| `go vet ./...` | PASS |
| `go test ./...` | **31 ok / 0 FAIL** / 17 no-test |
| `go test -race ./internal/...` | **29 ok / 0 FAIL** |
| 격리본 회귀(`-run Restored`) | disambiguator·kdbadmin·kentity **ok**, readiness 만 FAIL(원인 D-26 하나) |
| 운영 영향 | kdb-app healthy·restarts 0, migration 72·표 60·확장 `pg_trgm,plpgsql` 그대로. **DDL·데이터 변경 0** |

## 13. P1 잔여

- **P1.04/P1.05 부분**: M01~M05·M08~M14 는 DB 수준에서 시험했다. **M06**(문맥 없는 이름 → ambiguous 응답)과 **M07**(기사 span linking)은 API 경로라 P1.08 이 필요하다. **M10**(원본 재수집 멱등)은 dry-run 변환기가 있어야 해 P2 로 간다.
- **P1.06 부분**: 멱등 키·truth table·승인 게이트는 시험했다. 직렬화 충돌 재시도·부분 rollback 같은 **동시성 시험은 미실행**이다.
- **P1.07 미착수**: 공통 writer 재사용·우회 쓰기 차단. D-26 이 여기 속한다.
- **P1.08 미착수**: API·최소 UI 를 격리 데이터에 연결.
- **P1.09 부분**: build/vet/test/race 는 통과. **기존 API replay·390/1440px·권한/CSRF·조회 성능은 미실행**.
- **P1.10 미착수**: G1 인수.

---

# P1.07 부분 — 원천 정책 게이트와 정책 증명 (D-26 해소), 2026-09-13

운영자 지시: **wikidata 정책을 먼저 승인**하고 writer 가 그 증명을 기록하게 한다.

## 14. 원천 정책 승인 — 운영자 결정

P0.09 §5.5 는 모든 provider 를 `unreviewed`(차단)로 두고, approved 는 "검토자·시각·terms_url·조건을 갖춘
**새 version INSERT**" 만 허용했다. 그 경로로 첫 승인을 넣었다.

| provider | version | license | status | 허용 | 차단 |
|---|---|---|---|---|---|
| wikidata | 2026-09-13 | CC0-1.0 | approved | storage · verification · **name_export** | **excerpt_export** |

근거는 Wikidata 구조화 데이터(레이블·별칭·설명·statement)가 CC0 1.0 이라는 점이다.
조건을 명시해 범위를 좁혔다 — **구조화 표기만 해당하고, 링크된 Wikipedia 본문(CC BY-SA)은 제외**한다.
이 행은 격리본의 결정 기록이며 운영 반영은 P3.02 에서 별도 판단한다.

## 15. 게이트 구현 — 두 경로 모두

**공통 경로**(`evaluateCommon`)
- 정체성 근거를 받치는 **현재 승인된** 정책을 함께 읽는다. 승인 정책이 없는 정체성 근거가 하나라도 있으면 `policy_blocked`.
- 선택한 이름의 근거 provider 가 approved + `name_export_allowed` 가 아니면 `policy_blocked`.
- `ready` 일 때 정체성 정책 + 이름 정책을 중복 없이 합쳐 `policy_proof` 에 고정한다.

**legacy 경로**(`evaluate`)
- `canonical_<loc>_source` 를 `ProviderForSourceCode()` 로 provider namespace 로 옮긴다. 매핑 근거는
  [KDB_SCHEMA_DIFF_0115_0122.md](KDB_SCHEMA_DIFF_0115_0122.md) §9 의 8단계 표다. 코드 문자열 자체는 보존하고 조회에만 쓴다.
- 출처 등급(`qualifiedSource`)을 통과해도 그 provider 정책이 **지금** 승인돼 있어야 `ready` 가 된다.
- 아니면 `policy_blocked` + 사유에 원천 코드를 남긴다. 값을 지어내지 않는다.

두 경로 모두 "과거에 true 였던 플래그"가 아니라 **현재 `status` 와 `valid_until` 을 직접 확인**한다(S03).

## 16. 구현하며 드러난 것 — D-27, D-28

| ID | 발견 | 처리 |
|---|---|---|
| **D-27** | `writeLocale` 이 `state='ready'` 를 먼저 쓰고 `policy_proof` 를 **다음 문장**에서 썼다. CHECK 는 문장 단위로 평가되므로 그 사이에서 제약이 터진다 | state·proof·bound UUID·두 revision 을 **한 UPDATE** 로 묶었다 |
| **D-28** | S01 의 `ready_shape` 는 bound UUID 와 두 revision 을 요구하는데, 아무도 `bound_identity_revision`/`bound_entity_revision` 을 채우지 않았다 | item 바인딩 시 Entity 의 현재 두 revision 을 함께 고정하고, readiness 는 `UPDATE … FROM items` 로 같은 값을 가져와 복합 FK 를 만족시킨다 |

D-27 은 "제약을 문장 단위로 만족시켜야 한다"는 일반 규칙이다. 지연 제약이 아닌 CHECK 를 쓰는 한
여러 컬럼이 서로를 요구하면 **한 문장에서 함께 써야 한다**. 이후 writer 설계에 그대로 적용된다.

## 17. 검증

| 검사 | 결과 |
|---|---|
| SQL 제약 시험 | **69/69 PASS** |
| `go vet` | PASS |
| `go test ./...` | **31 ok / 0 FAIL** |
| `go test -race ./internal/...` | **29 ok / 0 FAIL** |
| 격리본 회귀(`-run Restored`) | **4/4 패키지 ok** — disambiguator · readiness · kdbadmin · kentity |

회귀 테스트 중 `common_restored_test`(en 이 `ready` 여야 함)와 `common_fill_restored_test`(ja 가 `ready` 여야 함)가
통과한다는 것이 곧 **정책 증명을 갖춘 ready 가 성립한다**는 증거다. 시험 fixture 에는 합성 provider 의 정책을 함께 만들었다 —
시험 대상은 준비 기제이지 권리가 아니기 때문이다.

운영 무해: `kdb-app` healthy·restarts 0, migration 72 · 표 60 · `kentity_source_policies` 없음(격리본에만 존재).

## 18. 이 결정이 남기는 것

`qualifiedSource` 가 통과시키는 legacy 출처는 15개인데 지금 승인된 정책은 **wikidata 하나**다.
따라서 legacy 경로에서 `ready` 가 되는 값은 `wikidata-label` 출처뿐이고, 나머지는 `policy_blocked` 로 검수에 남는다.
`wikidata-label` 은 현행 최대 tier(약 67,879칸)라 승인 효과가 즉시 크지만, 나머지는 정책 검토를 기다린다.

다음 승인 후보(운영자 결정 필요): `tmdb` · `musicbrainz` · `itunes` · `kofic` · `kmdb` · `discogs` ·
`netflix` · `disney` · `naver-people`(공식·카탈로그, 각각 이용약관 확인) 그리고
`operator` · `correction` · `media-consensus`(운영자 내부 근거라 외부 이용권 문제가 없다).
후자 3개는 지금 바로 승인 가능한 성격이다.

## 19. P1.07 잔여

정책 게이트·증명·S01 바인딩은 구현했다. P1.07 의 나머지 — 표기 위생·우선순위·잠금·수동 정정을
**공통 writer 하나로 모으고 우회 쓰기 경로를 차단**하는 작업은 남아 있다. 우회 차단의 DB 측 근거는
[KDB_WRITER_AUTHORITY.md](KDB_WRITER_AUTHORITY.md) §물리 권한 매트릭스이며, 실제 role 분리는 P5 다.

---

# P1.07 — 원천 정책 전면 승인과 공통 writer 보호선, 2026-09-13

## 20. 승인한 원천 정책 13 provider

운영자 결정(2026-09-13). 외부 카탈로그 9종은 **이용 가능함을 운영자가 확인**했고 일부는 API key 로 접근 중이다.

| 갈래 | provider | license_code | 비고 |
|---|---|---|---|
| 공개 데이터 | wikidata · musicbrainz | CC0-1.0 | 구조화 데이터가 CC0 |
| 운영자 내부 근거 | operator · correction · media-consensus | internal-operator-review | 외부 이용권 문제 없음 |
| 외부 API | tmdb · itunes · kofic · kmdb · discogs · naver-people | operator-confirmed-api-terms | API key 보유분 포함 |
| 공식 카탈로그 | netflix · disney | operator-confirmed-official-page | 공식 페이지 작품 표기 |

전부 `storage` · `verification` · **`name_export`** 허용, **`excerpt_export` 는 차단**이다.
이번에 연 것은 **표기(레이블) 공급**뿐이고 본문 발췌는 요청 범위가 아니어서 열지 않았다 — 필요해지면 provider 별 새 version 을 검토한다.
provider 별 표기(attribution) 의무는 각 행의 `conditions` 에 남겼다.

**여전히 차단**: `gtranslate` · `codex-fallback`(기계번역, tier 8), `romanization` · `opencc` · `kana-rule`(규칙 생성, tier 7),
`rss-observation`(도메인별 미검토). 이들은 `form` 자체가 `generated`/`translated` 라 strict-ready 대상이 아니다(표기 계약 §2).

결과: `qualifiedSource` 가 통과시키는 legacy 출처 15개 중 **13개가 정책을 얻었다.** 남은 것은 `local-usage` 와 `naver-people` 계열 변형뿐이며,
`wikidata-label`(약 67,879칸)·`tmdb`(8,657)·`correction-verified`(2,155)·`operator-locked`(772)·`musicbrainz`(742)·`itunes`(518) 등
현행 검증 가능 표기의 대부분이 공급 가능해졌다.

## 21. 공통 name writer 보호선 — DB 에 둔다

Go 경로는 우회될 수 있으므로(WRITER_AUTHORITY A01/A02) 규칙 자체를 `kentity_names` 의 BEFORE 트리거로 둔다.
**우선순위 판정은 legacy 가 쓰던 `kdb_source_priority()` 를 그대로 재사용한다 — 두 벌을 만들지 않는다.**

| 규칙 | 내용 | 대응 |
|---|---|---|
| 운영자 잠금 | `operator_locked` 표기는 자동 출처가 값·상태·종류·형식을 바꿀 수 없다. 운영자 출처(`operator`·`operator-locked`·`correction-verified`)만 가능 | X01 |
| 출처 우선순위 | 검증된 대표명을 **더 낮은 등급** 출처로 교체할 수 없다 | X03 |
| 현재 효력 guard | `withdrawn_name`·`operator_correction` 은 **그 주장 하나**를 막는다(슬롯 전체가 아니다). `empty_slot` 은 대표명 자동 승격만 막고 별칭은 허용한다 | X11 |

주장 지문은 `kentity_name_claim_fingerprint(locale, value, kind, form)` 로 계산하며, `source_guards.claim_fingerprint` 와 같은 규칙이다.
제어 설계 §5 의 "withdrawn_name/operator_correction 은 exact claim_fingerprint 가 필수" 를 물리로 옮긴 것이다.

## 22. 검증 — 83/83

| 검사 | 결과 |
|---|---|
| SQL 제약·인수 시험 | **83/83 PASS** (P1.03 18 · M 46 · P1.06 5 · **P1.07 14**) |
| `go vet` | PASS |
| `go test ./...` | **31 ok / 0 FAIL** |
| `go test -race ./internal/...` | **29 ok / 0 FAIL** |
| 격리본 회귀(`-run Restored`) | **4/4 패키지 ok** |
| 운영 | `kdb-app` healthy·restarts 0 · migration 72 · 표 60 · 정책 표 없음 |

새 시험 중 확인 가치가 큰 것:
- **X11c** 철회 guard 가 같은 슬롯의 **다른** 주장은 막지 않는다 — 슬롯 전체 봉쇄가 아님을 실증.
- **X11e** 비운 슬롯이어도 별칭은 허용 — `empty_slot` 이 대표명 승격만 막음을 실증.
- **X03c** 등급 비교가 실제 tier 를 쓴다: `operator`(1) < `correction-verified`(4) < `gtranslate`(8).
- **X00b** 미승인 provider 5종이 여전히 승인 0임을 확인 — 승인이 새는지 감시한다.

## 23. P1.07 잔여

- **표기 위생**: `kdb.IsValidSpellingForLocale` 은 두 evaluate 경로에서 이미 호출된다. DB 보호선으로 옮길지는 Go 함수 의존이라 별도 판단이 필요하다.
- **실제 저장 결과 검사(X12)**: `writeLocale` 이 `RETURNING … Scan` 으로 영향 행을 확인하므로 "접수 ≠ 설치" 는 이미 구분된다. 나머지 writer 에도 같은 패턴을 넓히는 작업이 남았다.
- **우회 쓰기 경로 차단의 나머지**: 이름 외 표(external_ids·person_roles·entity_domains)의 잠금·우선순위 보호선과, 궁극적으로 role 분리(P5).

---

# P1.08 부분 — API·최소 UI 를 격리 데이터에 붙인다 (M06/M07), 2026-09-13

## 24. 무엇을 확인하기로 돼 있었나

원장 P1.08: "필수 검색/준비/등록/검수 API와 최소 UI를 격리 데이터에 연결한다. **동명 후보 두 명의 선택·저장 대상 ID·별도 표기·직업 필터**를 확인한다."
인수 사례로는 M06(문맥 없는 이름 → ambiguous+후보)과 M07(한 기사에 동명 span 2개)이 여기서 풀린다.

## 25. 먼저 고정한 규칙 — 한 사람 = 하나의 ID

구현 방향을 잡기 전에 정체성 규칙을 [식별 계약 §1.1](KDB_IDENTITY_CONTRACT.md) 로 올렸다.

한 사람이 배우이자 가수이자 MC 면 직업 행이 여러 개일 뿐 **ID 는 하나**다. 겸업을 이유로
ID 를 쪼개면 같은 사람이 두 UUID 를 갖게 되고, 근거·표기·정정이 두 곳으로 갈려 어느 쪽도
완전하지 않게 된다. **ID 가 갈리는 유일한 이유는 다른 사람이라는 것이다.** 동명이인도 동명
3인 이상도 이 규칙 하나로 풀리며, 구분은 직업이 아니라 소속사·생년·대표작·외부 식별자·
언어별 표기로 한다(I01/I05/I09, 구조 §2 "복수 직업과 UUID별 값 소유").

이 규칙이 바로 구현을 바꿨다. 처음 넣은 직업 필터는 legacy `primary_role` **하나**만 봐서
겸업자가 부직업으로 거르면 사라졌다 — 규칙대로라면 남아야 한다. 다중 role 로 고쳤다.

## 26. match 의 ambiguous — 추가 필드로만 붙인다

`/v1/entities/match` 는 지금까지 **flat entity 배열**만 돌려줬고, 소비자는 관행적으로
`entities[0]` 을 정답처럼 저장했다. 신뢰도 1위는 "더 유명한 쪽"이지 "맞는 쪽"이 아니다
(식별 계약 §4 "첫 행·유명인·높은 인지도 자동 선택 금지"). TDB 쪽 match 에만 있던
found/ambiguous map 을 KDB 로 흡수하는 것이 R03 의 의도다.

| 항목 | 결정 |
|---|---|
| 응답 모양 | `entities` 그대로 + `status`/`candidates` 를 `omitempty` 로 **추가만** |
| 발동 조건 | 요청 본문 **전체**가 후보 표기와 정확히 같고(=문맥 없음) 그 이름이 2건 이상으로 갈릴 때 |
| 비발동 | 기사 본문처럼 문맥이 붙은 요청은 종전 동작 — 여기까지 ambiguous 로 바꾸면 번역 핫패스가 통째로 막힌다 |
| entity_type | 보지 않는다. person "X" 와 work "X" 가 같이 걸려도 문맥 없는 요청자는 못 고른다 |
| bulk | 같은 규칙 적용. 단건만 막으면 bulk 로 자동 선택이 되살아난다 |

기존 소비자 4곳은 `entities` 만 읽으므로 회귀가 없다. 시험이 두 방향을 다 건다 —
이름만 보내면 `status=ambiguous` + 후보 2건 + 최상위 `id` 없음, 문맥을 붙이면 `status` 자체가 없음.

## 27. 동명 후보 화면 — 404 로 죽어 있었다

`entityHomonyms`/`entitySetDisambig` 핸들러는 있는데 **라우터 등록이 빠져 있었다.** 같은 이름
후보를 나란히 놓고 고르는 화면은 이것뿐인데 접근 경로가 없었고, 그래서 `needs_disambig`
플래그 1,839건이 처리 경로 없이 쌓여 있었다.

- 인증 그룹에 등록해 `sessionAuth`+`csrfProtect` 를 실제로 걸었다. 폼 2개에 없던 `_csrf` 를 넣었다.
- **저장 대상 ID**: 후보별 UUID 를 그대로 노출한다(링크만 있고 값이 안 보였다).
- **별도 표기**: `global ●/○` 불리언을 EN/JA/ZH-Hant 실제 값으로 바꿨다. 동명이인은 한국어만
  같고 다른 언어 표기가 갈리는 경우가 많아 이게 실제 판별 근거다. 비어 있으면 "아직 없음"이지 "같다"가 아니다.
- **직업 필터**: 그 사람의 **모든** 직업(`secondary_roles` + 공통 `kentity_person_roles`)을 본다.
  거르는 대상은 구성원이 아니라 **그룹**이다 — 한쪽만 남기면 비교가 불가능해져 화면 목적이 사라진다.
  `kentity_person_roles` 는 P1.02 구조에만 있으므로 `to_regclass` 로 존재를 확인하고 없으면 legacy 만 본다.

## 28. S01 은 지금까지 한 번도 실행된 적이 없었다

`p1_tests.sql` 에 준비·readiness 픽스처가 아예 없어서 §14.1 의 복합 FK 가 미실행이었다.
설계가 지정한 시험 3종을 그대로 걸고 7건으로 늘렸다.

| 시험 | 기대 | 결과 |
|---|---|---|
| M07a | ordinal 0 을 묶인 UUID·버전으로 확정 | accept |
| **M07b** | **A 의 readiness 에 B 의 이름 붙이기** (§14.1 "A item 에 B 이름으로 ready 저장 거부") | reject |
| M07c | ordinal 1 에 다른 ordinal 의 대상 UUID | reject |
| **M07d** | **묶이지 않은 revision** (§14.1 "오래된 bound revision 거부") | reject |
| M07e | ordinal 1 을 자기 대상으로 확정 | accept |
| M07f | 두 span 이 서로 다른 UUID·서로 다른 이름 (교차오염 0) | accept |
| **M07g** | **미해소 항목을 ready 로 만드는 NULL 우회** (§14.1) | reject |

## 29. kdbapi 최초의 격리 복원 DB 시험

이 패키지의 DB 시험은 전부 합성 축약 스키마(`testdb.New`)였다. 즉 "복원 DB 통합" 근거로 쓸 수
없었고, 실제로 `kwave_entities_homonym_key` 같은 운영 제약은 합성 스키마에 없어서 안 걸렸다.
`testdb.Restored` 를 쓰는 첫 HTTP 시험 2건을 넣었다.

## 30. 새로 드러난 것 — D-29

| # | 내용 |
|---|---|
| **D-29** | **이름+유형이 정체성 키로 쓰이고 있다.** legacy `kwave_entities_homonym_key` 는 `(canonical_ko, entity_type, coalesce(disambig,''))` UNIQUE 다. 라벨을 아직 모르는 두 번째 동명인은 저장 자체가 안 된다. 그런데 `internal/kdb/research/worker.go:288` 은 이 23505 를 잡아 **기존 행의 UUID 를 재사용**한다 — 같은 이름의 *다른 사람*이 앞사람 ID 로 합쳐진다. I01("이름은 UUID 유일 키가 아니다")·I05 정면 위반이며, 소비자는 틀린 UUID 를 받아 저장한다 |

실측(운영, READ ONLY): 엔티티 13,192건 중 **동명 그룹 3개(6행)**. 한국 인명에서 동명이 3쌍뿐일
수는 없다. 같은 DB 에 `needs_disambig=true` 가 **1,839건** 쌓여 있는 것과 맞물린다 —
`candidates.go` 의 RSS 경로는 동명 다수를 만나면 누적을 보류하고 플래그만 켜서 homonym-safe
하지만(그래서 플래그가 쌓였다), `worker.go` 의 온디맨드 lookup-miss 경로는 충돌을 "이미 있음"
으로 접어 같은 ID 를 돌려준다. 화면이 404 였으니 쌓인 플래그를 처리할 경로도 없었다.

고치는 것은 운영 동작 변경이라 이 단계에서 하지 않는다. 범위: UNIQUE 자체를 공통 모델로
옮기는 것은 P3, 진입점의 ID 재사용 차단은 P2.05(알려진 오연결)와 함께 본다. **P1.10 에서
설계 반영 대상으로 올린다.**

## 31. 검증 — 90/90

| 검사 | 결과 |
|---|---|
| SQL 제약·인수 시험 | **90/90 PASS** (P1.03 18 · M 53 · P1.06 5 · P1.07 14) |
| `go build` / `go vet` | PASS / PASS |
| `go test ./...` | **31 ok / 0 FAIL** |
| `go test -race ./internal/...` | **29 ok / 0 FAIL** |
| 격리본 회귀(`-run Restored`) | **4/4 패키지 ok** (kdbapi 2건 신규 포함) |
| 정적 검사 5종 | 전부 PASS |
| 운영 | `kdb-app` healthy · restarts 0 · 표 60 · `kentity_source_policies` 없음 |

재현 파이프라인에서 고친 것 하나: 시험 파일을 **호스트** `/tmp` 에 복사하고 psql 은 **컨테이너**
`/tmp` 를 읽고 있었다. 그래서 새 시험 7건이 조용히 빠진 채 83/83 PASS 가 나왔다. `docker cp` 로
바꿨다. "통과했다"가 "그 파일로 통과했다"를 뜻하지 않을 수 있다는 사례다.

또 하나: 시험의 `t.Cleanup` 을 삽입 **뒤에** 등록했더니, 삽입 중 `t.Fatal` 이 나자 Cleanup 이
등록조차 되지 않아 행이 남았고 다음 실행의 후보 집합에 끼어들어 엉뚱하게 실패했다. 등록을 앞으로 옮겼다.

## 32. P1.08 잔여

- **span linking 자체는 P7**. 인수 픽스처가 "P1 tests bound-UUID fixture only" 로 명시했고,
  `span_start`/`span_end` 컬럼과 CHECK 는 이미 P1.02 에 있다.
- **등록/검수 API 의 나머지 화면**(`/admin/kentity` 검색·등록)은 이미 연결돼 있고 P1.09 의
  권한/CSRF·390/1440px 검사 대상이다.

---

# P1.07 후속 — 트리거 캐스케이드 전수 감사, 2026-09-13

## 33. 왜 여기를 다시 봤나

P1.02 에서 찾은 충돌 13건 중 **4건이 트리거 본문**이었고, 그 넷은 문서 대조로는 보이지 않고
격리 실행에서만 드러났다. 같은 종류가 더 있는지 트리거 18종을 전수로 훑고, 의심 건을 그대로
믿지 않고 **격리본에서 재현**해 확인했다.

드러난 공통 패턴 하나: 제약은 P1 에서 촘촘해졌는데 **트리거 본문은 P1 이전 스키마를 가정한 채**
남아 있었다. 제약 시험은 "한 문장이 걸리는가"만 보므로, 트리거가 다른 트리거를 부르는
**캐스케이드** 경로는 한 건도 시험된 적이 없었다.

## 34. 재현으로 확인한 것 — D-30 ~ D-34

| # | 내용 | 재현 |
|---|---|---|
| **D-30** | **안전장치가 안전장치를 막았다.** 근거 철회 전파는 `kentity_names.status` 를 `blocked` 로 내리는데, P1.07 이름 보호선 ①이 "status 가 바뀌었고 출처가 자동 출처"라는 이유로 이를 덮어쓰기로 오인해 예외를 던졌다. 결과: **운영자가 잠근 이름이 하나라도 있으면 그 근거를 철회하는 트랜잭션 전체가 중단**된다 | TRG01 |
| **D-31** | 철회 전파가 이름·외부ID 에서 멈춘다. P1 이 "검증은 근거 필수"를 직업·분야·분류·이름근거까지 넓혔는데 전파 대상은 0117 시절 두 표 그대로라, 철회된 근거를 가리키는 `verified` 행이 남는다 | TRG03 |
| **D-32** | 의존 guard 가 자식 근거를 `claim_type='name'` 으로 하드코딩한다. claim_type 을 7종으로 넓히고 PK 를 넓혀 다중 부모를 허용해 놓고, 직업·분류·프로필 근거는 의존 등록조차 안 된다 | TRG04 |
| **D-33** | "승인 근거는 불변"의 비교 튜플이 10컬럼에서 멈춰 있다. P1 이 추가한 `claim_fingerprint`/`source_observation_hash` 는 새 UNIQUE(`kentity_evidence_claim_key`)의 구성 컬럼인데 감시 밖이라 **주장의 정체성이 조용히 바뀔 수 있었다** | TRG05 |
| **D-34** | provider 당 승인 정책이 하나임을 아무것도 강제하지 않는다. UNIQUE 는 `(provider, version)` 뿐이라 같은 provider 의 승인 정책이 둘 이상 공존할 수 있고, 읽기 쪽은 `DISTINCT ON (provider)` 로 하나를 고른다 — **어느 권한이 적용될지를 결정이 아니라 ORDER BY 가 정한다** | X00 |

D-30 은 P1.07 에서 **내가 넣은 보호선**이 만든 것이다. 보호선을 넣을 때 "무엇을 막을까"만 보고
"무엇이 이 표를 정당하게 내리는가"를 보지 않았다.

## 35. 고친 방식 — 교체와 회수를 구분한다

D-30 의 핵심은 **주장을 바꾸는 것**과 **근거가 사라져 내리는 것**이 다르다는 것이다.
값·종류·형식이 그대로이고 뒤를 받치던 근거가 더는 쓸 수 없는 상태라면 그것은 교체가 아니라 회수다.
회수를 막으면 근거 없는 이름을 계속 서빙하게 되는데 그쪽이 더 나쁘다("빈칸 > 틀린값").
근거가 여전히 멀쩡하면 이 면제는 걸리지 않으므로 **운영자 잠금 보호는 그대로 남는다.**

D-34 는 갱신을 겹치기가 아니라 **인계**로 정의해 풀었다. 새 버전을 승인할 때 옛 버전을 같은
transaction 에서 `expired` 로 내린다. 내릴 때 허용 플래그도 함께 꺼야 한다 — 만료된 정책이
권한만 들고 남으면 "승인 없이 허용"이 되고, 그건 이미 CHECK 가 막고 있다(M14h 로 시험).

## 36. 시험 자체의 결함 — 조용히 공회전하던 단언 12개

TRG02 가 통과하면 안 되는 상황에서 통과했다. 원인: 조건절만 쓰는 단언(`WHERE <조건>`)은
조건이 거짓이면 **0행일 뿐 오류가 아니다.** `p1_try` 는 "문장이 성공했는가"만 보므로 거짓 단언이
조용히 PASS 로 적힌다. 이 모양의 단언이 **12곳** 있었다 — M01a·M03a·M04a·M05a·M05c·M07f·
X00·X00b·X03c·X22 등, 전부 "성립함을 확인한다"는 종류다.

거짓이면 예외를 던지는 `p1_must(boolean)` 으로 12곳 전부 바꿨다. 바꾸자마자 **X00 이 즉시 실패**했다 —
승인 정책이 13이 아니라 14였고, 그게 D-34 의 발견 경로였다. 공회전하던 단언이 실제로 결함을
가리고 있었던 것이다.

## 37. 검증 — 97/97

| 검사 | 결과 |
|---|---|
| SQL 제약·인수 시험 | **97/97 PASS** (P1.03 18 · M 56 · P1.06 5 · P1.07 14 · **TRG 5**) |
| `go test ./...` | **31 ok / 0 FAIL** |
| `go test -race ./internal/...` | **29 ok / 0 FAIL** |
| 격리본 회귀(`-run Restored`) | **5/5 패키지 ok** |
| 운영 | 무변경 |

## 38. 남은 의심 건 (미재현)

전수 감사가 SUSPECT 로 남긴 것들이다. 확실하지 않으므로 시험을 먼저 만들고 판단한다.

- `kentity_sync_legacy_identity` 의 유형 전환이 subtype FK·verified_classification CHECK·
  person_roles FK 와 충돌할 수 있다(P2.02 선매핑 이후 노출). **P2.02 전에 시험을 만든다.**
- `kentity_adopt_rejected_legacy` 가 `subtype=NULL` 로 내리면서 `classification_status` 를 안 내린다.
- `kentity_reserve_legacy_external_id` 의 INSERT 가 `entity_id` 를 채우지 않아 첫 RAISE 가 도달 불가.
- 이름 보호선 ②가 legacy 소스 어휘 전체에 대해 fail-open(`kdb_source_priority` 의 `ELSE 99`).
- `claim_fingerprint` 에 형식 CHECK 가 없어 md5/sha256 혼용 시 guard 가 조용히 빗나갈 수 있다.
