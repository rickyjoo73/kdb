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
