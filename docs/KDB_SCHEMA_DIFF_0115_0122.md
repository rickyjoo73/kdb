# 현행 물리 구조(0115~0122) vs 목표 구조 draft-v4 차이표 — P0.02 대조

기록일: 2026-09-13 KST. 현행은 운영 KDB 카탈로그(`pg_catalog`/`information_schema`) READ ONLY 조회 결과이고,
목표는 [KDB_TARGET_SCHEMA.md](KDB_TARGET_SCHEMA.md) §4~§15다. 이 문서는 §9 미결 6묶음 중
"기존 0115~0122 표별 최종 컬럼 차이"(그룹4)와 "전체 물리 대조·누락 검사"(그룹6)의 산출물이며,
그룹1(최소 속성 원천 대조)·그룹2·3(seed 목록)·그룹5(이전/고객 계약 위치)를 §5~§8에 함께 고정한다.
기존 설계 결정은 바꾸지 않는다. 카탈로그와 설계가 어긋난 지점만 D-번호로 남기고 처리 단계를 지정한다.
실행 상태 정본은 [KDB_INTEGRATION_TODO.md](KDB_INTEGRATION_TODO.md)다. 여기서 발견한 D-항목은 P1 시험 항목이지 운영 변경이 아니다.

## 1. 대조 기준

- 현행: 표 22 + 뷰 1(`kentity_name_catalog`) + legacy `kwave_entities`(48컬럼)·`kwave_entity_external_refs`·`kwave_entity_person_details`·`kwave_persons`(5,700행, `name_ko` UNIQUE). 트리거 16, 사용자 함수 20(전부 SECURITY INVOKER, `search_path` 미고정, PUBLIC EXECUTE 기본), 소유자 `kdb` 단일(superuser). 확장: `pg_trgm` 설치, `btree_gist` **미설치**(available 1.7).
- 목표: §4~§6 컬럼 단위 확정 5표, §10~§13 보강·신규, §14 S01~S08 교차 제약, §15 이관 제어 3표.
- 표기: `NN`=NOT NULL, `?`=NULL 허용. "보존"=컬럼·제약 변경 없음. 인덱스는 PK/UNIQUE 외 보조 인덱스만 언급.

## 2. 표별 차이 — 기존 표 (0115~0122)

| 표 | 현행 요약 | 목표 변경(§) | 분류 | writer 책임 |
|---|---|---|---|---|
| kentity_entities | 11컬럼. `subtype NN ''`, `status NN` 기본값 없음, CHECK entity_type 11값(brand/character 없음), `write_owner` kdb/native/tdb. 인덱스 (canonical_ko,entity_type),(status,updated_at) | §5: subtype `?`+복합 FK, status 기본 'candidate', classification_status/reason/evidence_id/policy_version/classified_by/classified_at 추가, UNIQUE(id,entity_type), 인덱스 3종; §10.1: identity_revision·dependency_epoch | 기존 보강 | 공통 보호 writer(현재 write_owner별) |
| kentity_domains | code,label_ko 2컬럼. 8행 = 분류 규칙 §5 코드와 일치 | §4.4: enabled/sort_order/revision/created_at/updated_at | 기존 보강 | 사전 migration만 |
| kentity_entity_domains | 5컬럼(entity_id,domain,assigned_by,reason,created_at) | §6.2: status/evidence_id/policy_version/verified_by/verified_at/operator_locked/revision/updated_at, 인덱스 2종 | 기존 보강 | 공통 분류 writer |
| kentity_names | 14컬럼. UNIQUE(entity_id,locale,value,kind,source_code), 부분 UNIQUE (entity_id,locale) WHERE canonical&verified, 트리거 2(automatic_name_guard, verified_name) | §12.3: normalized_value/normalization_version/operator_locked/verification_method/policy_version, locale FK, UNIQUE(id,entity_id), UNIQUE NULLS NOT DISTINCT 로 교체; §14.7: 부분 날짜 6컬럼+possible_validity EXCLUDE | 기존 보강 | 공통 name writer |
| kentity_evidence | 13컬럼. PK(id), UNIQUE(id,entity_id), 비유일 인덱스 (entity_id,provider,source_record_id). 트리거 3 | §12.2: revision/source_policy_id/claim_fingerprint/source_observation_hash/claim_payload/independent_origin, claim_type +occupation/classification/profile/no_form, 새 UNIQUE 7컬럼 | 기존 보강 | 근거 writer/검수 |
| kentity_evidence_dependencies | PK(evidence_id) 단일, (evidence_id,entity_id)·(depends_on_id,entity_id) 복합 FK | §12.2: PK→(evidence_id,depends_on_id) | 기존 보강 | 근거 writer |
| kentity_external_ids | 5컬럼. PK(entity_id,provider,external_id), 부분 UNIQUE (provider,external_id) WHERE verified | §10.1: revision/observed_at/operator_locked/policy_version | 기존 보강 | 정체성 writer |
| kentity_id_reservations | 3컬럼(provider,external_id,native_owner) | §10.1: native_owner→entity_id 호환, revision/updated_at | 기존 보강 | 정체성 writer |
| kentity_crosswalks | 16컬럼. PK(source_system,source_id), `source_binding_id`→tdb_shadows FK, CHECK tdb면 binding NN | §10.1: id/source_table/source_state/source_merged_into/source_observed_at/mapping_policy_version/target_identity_revision, PK→(system,table,id), status +withdrawn, binding 컬럼명 legacy_shadow_id 호환 | 기존 보강 | 정체성 writer |
| kentity_relations | 9컬럼, 0행. 인덱스 (subject,predicate,valid_from),(object,…) | §10.6: subject_type/object_type/position_code/operator_locked/revision/created_at/updated_at + 사전 FK 3종; §14.7 부분 날짜 6컬럼 | 기존 보강 | 관계 writer |
| kentity_audit_events | 8컬럼, bigserial | §10.5: operation_id/object_table/object_key/before_hash/after_hash; §15: migration_record_id | 기존 보강 | append-only(INSERT만) |
| kentity_preparations | 15컬럼. UNIQUE(owner_key,idempotency_key), status 4값 | §13: as_of/source_hash/request_form_policy/article_scope | 기존 보강 | api intake |
| kentity_preparation_items | 10컬럼. PK(prep,ordinal). supplied/resolved/bound_entity_id 에 **FK 없음** | §13: bound_identity_revision/bound_entity_revision/span_start/span_end; §14.1 S01 UNIQUE 5컬럼; UUID 들에 공통 Entity FK | 기존 보강 | api intake/resolver |
| kentity_locale_readiness | 13컬럼. PK(prep,ordinal,locale), state 8값(pending,ready,ambiguous,no_evidence,policy_blocked,failed,unverified,cancelled) | §13: name_id/entity_id/identity_revision/name_revision/dependency_epoch/fallback_locale/usable_value/usable_form/proof_policy_version; S01 entity_revision/scope_key; S02 no_form_evidence_id; S03 policy_proof; state +no_form/stale | 기존 보강 | readiness writer |
| kentity_locale_fill_jobs | 17컬럼. `qid NN`, UNIQUE(entity,locale,input_fingerprint,policy_version), state 7값(…,stale) | §13: qid `?`, provider/source_reference/entity_revision/identity_revision/scope_key/purpose/model·prompt_version/tokens/changed_names/newly_ready/started·finished_at, UNIQUE +scope_key, state +cancelled | 기존 보강·일반화 | fill worker |
| kentity_resolution_jobs | 17컬럼. UNIQUE(entity_id,entity_revision,policy_version), attempts≤4, running이면 lease NN | §13: identity_revision/scope_key/provider_policy_version/model·prompt_version/tokens | 기존 보강 | resolver worker |
| kentity_readiness_events | 8컬럼, bigserial | 보존(append-only) | 재사용 | readiness writer |
| kentity_candidate_requests | 5컬럼. PK(owner_key,request_key), FK DEFERRABLE | 보존 | 재사용 | api intake |
| kentity_ownership_decisions | 10컬럼. UNIQUE(entity_id), reason≥10자, source_url https 강제. 트리거 adopt_rejected_legacy | 보존(과거 판정 UPDATE 금지) | 재사용 | 소유권 승인 API |
| kentity_tdb_shadows | 24컬럼. UNIQUE(tdb_id,qid), qid ~ ^Q[1-9][0-9]*$, state 5값 | 보존(이전 호환 binding) | 재사용 | tdb observer |
| kentity_tdb_shadow_events | 7컬럼, bigserial | 보존 | 재사용 | tdb observer |
| kentity_name_catalog (뷰) | kwave_entities 의 8 locale 컬럼을 write_owner='kdb' 일 때 form='unknown'/status='legacy' 로 투영 ∪ kentity_names | 호환 뷰 유지. names 가 정본이 되면 legacy 분기 축소(P5) | legacy adapter | 읽기 전용 |

## 3. 신규 표 — 목표에만 있음 (20)

| 표 | 정의 위치 | 전체 컬럼 확정 | writer |
|---|---|---|---|
| kentity_types / kentity_subtypes / kentity_role_types | §4.1~4.3 | ✓ | migration |
| kentity_person_roles | §6.1 (+§14.7 6컬럼) | ✓ | 분류 writer |
| kentity_identity_decisions / identity_operations / redirects | §10.2~10.4 (+S05) | ✓ | 정체성 writer |
| kentity_relation_types / relation_type_pairs / position_types | §10.6 | ✓ | migration |
| kentity_person_profiles / location_profiles | §11.1~11.2 | ✓ | 프로필 writer |
| kentity_locales / source_policies | §12.1 (+S03) | ✓ | migration / 권리 운영자 |
| kentity_name_evidence | §12.3 (+S08) | ✓ | name writer |
| kentity_fill_waiters / invalidation_outbox | §13 (+S06/S03) | ✓ | readiness writer / invalidator |
| kentity_migration_runs / migration_records / source_guards | [KDB_MIGRATION_CONTROL_SCHEMA.md](KDB_MIGRATION_CONTROL_SCHEMA.md) §3~§5 | ✓ (42 컬럼행) | 승인·실행 capability 분리 |

## 4. 카탈로그 대조에서 드러난 불일치·누락 — D-항목

각 항목은 설계를 바꾸는 것이 아니라 **설계가 전제한 현행이 실제와 다른 지점**이거나 **설계가 아직 지정하지 않은 자리**다. 처리 단계를 명시한다.

| ID | 발견 | 근거 | 처리 |
|---|---|---|---|
| D-01 | §12.2 는 "기존 UNIQUE(entity_id,provider,source_record_id,source_url)를 전환"한다고 썼지만 **현행 kentity_evidence 에 그 UNIQUE 가 없다.** 있는 것은 PK(id), UNIQUE(id,entity_id), 비유일 인덱스 (entity_id,provider,source_record_id)뿐 | pg_constraint | P1.02 migration 은 "전환"이 아니라 **신규 추가**로 작성. 기존 중복 행 유무를 P1.01 복원본에서 먼저 계수 |
| D-02 | 현행 CHECK `kentity_tdb_binding_required`(source_system='tdb' → source_binding_id NN) + shadows.qid `^Q…` 강제 → **QID 없는 TDB 원본은 지금 crosswalk 될 수 없다.** 정체성 계약 §3 은 "QID 없는 기록도 수용" | pg_constraint ×2 | §10.1 의 source_table 도입 시 이 CHECK 를 `legacy_shadow_id IS NOT NULL OR mapping_policy_version <> ''` 류로 완화하는 안을 P1.02 에서 시험. 완화 전엔 QID 없는 TDB 는 review 로만 |
| D-03 | preparation_items.supplied/resolved/bound_entity_id 세 UUID 컬럼에 **Entity FK 가 없다.** §13 은 "UUID 들에 공통 Entity FK"를 요구 | pg_constraint(items 는 FK 1개뿐) | P1.02 에서 FK 추가. 기존 181행 중 kentity_entities 에 없는 UUID 계수 후 NULL 처리·감사 |
| D-04 | TDB `tdb_places.disambiguator` 와 legacy `kwave_entities.disambig` 의 **공통 자리 미지정.** kentity_entities 에 해당 컬럼 없음 | 컬럼 목록 | **P0.02 결정**: kentity_entities 에 `qualifier_ko text ?`(호환 표시 한정어, 정체성 키 아님, canonical_ko 와 별개)를 §5 컬럼에 추가. 검색 UNIQUE 에 넣지 않으며 동명 구별의 근거는 profiles/relations 가 맡는다 |
| D-05 | §6.1/§14.7 의 uuid+daterange EXCLUDE 는 `btree_gist` 필요. **미설치**(available 1.7) | pg_available_extensions | P1.01 격리 DB 에 `CREATE EXTENSION btree_gist` 후 EXCLUDE 시험. 운영 설치는 P3.02 |
| D-06 | state 목록 차이: fill_jobs 현행에 `stale` 존재(§13 미언급), readiness 현행에 `unverified`,`cancelled` 존재(§13 미언급) | CHECK 정의 | §13 목록을 현행 합집합으로 읽는다. 값 제거 없음. P1.02 CHECK 는 현행+추가값 |
| D-07 | entities.status 현행 **기본값 없음**(NN). §5 는 기본 'candidate' | column_default | P1.02 `SET DEFAULT 'candidate'`. 기존 행 영향 없음 |
| D-08 | 시퀀스 스타일 혼재: 기존 3표 bigserial, §13 outbox 는 IDENTITY | pg_depend | 규약 통일은 하지 않음(기존 보존). 신규만 IDENTITY |
| D-09 | 함수 20개 전부 PUBLIC EXECUTE 기본·`search_path` 미고정·owner=kdb(superuser) | pg_proc | [KDB_WRITER_AUTHORITY.md](KDB_WRITER_AUTHORITY.md) §물리 권한 매트릭스. P1 격리에서 REVOKE/SET search_path 시험, 운영 적용은 P5 |
| D-10 | name_catalog 뷰가 legacy 분기에서 form='unknown'/status='legacy' 를 하드코딩 | pg_get_viewdef | 호환 뷰의 의도된 표시. P5 에서 names 정본 전환 시 legacy 분기 제거 |
| D-11 | kentity_crosswalks.entity_id FK 에 **인덱스 없음**(5,701행) | pg_indexes | §10.1 에 이미 (entity_id) 인덱스 명시. P1.02 |
| D-12 | kentity_candidate_requests.entity_id FK 에 인덱스 없음(4행) | pg_indexes | 소규모. P1.02 에서 함께 추가 |
| D-13 | kentity_id_reservations.native_owner FK 에 인덱스 없음(5행) | pg_indexes | §10.1 entity_id 호환 변경 시 인덱스 추가 |
| D-14 | Go `supportedTypes` 11값 = 현행 CHECK 11값. §4.1 사전 13코드(brand,character) 와 **2코드 차이** | store.go:62 | 설계대로: 사전 FK 전환 시 Go 허용목록을 사전 조회로 교체(P1.02). 그때까지 brand/character 는 API 미노출 |
| D-15 | legacy `kwave_entity_type` enum 13값 → 공통 11값 매핑에서 term→work, brand_place→work, channel_outlet→work, event_tour→work 로 접힘(kentity_legacy_type) | LEGACY_TYPE_MAP | 분류 규칙 §6 이 이미 후보 매핑 13/13 을 정의. P2.02 선매핑에서 subtype 으로 복원(brand_place→location/company, event_tour→event 등). 현행 접힘은 후보 상태로만 |

D-01/02/03/07/11/12/13 은 P1.02 forward migration 초안 항목, D-05 는 P1.01 선행, D-04 는 이 문서로 결정, D-14/15 는 P1/P2 코드·선매핑 항목이다.

## 5. 원천 → 최소 속성 대조 (§9 그룹1)

`tdb_places`(22컬럼) 의 속성이 §11.2 `kentity_location_profiles` 로 가는 자리:

| tdb_places | 목표 | 비고 |
|---|---|---|
| lat, lon | latitude, longitude (+coordinate_precision_m, geo_evidence_id, geo_locked) | WGS84 만 1차. 둘 다 NULL 또는 둘 다 유한 |
| addr_ko, addr_en | address_ko, address_en (+address_evidence_id, address_locked) | |
| sido_code, sigungu_code, ldong_code | 동명 컬럼 (+admin_namespace NN 조건, admin_evidence_id, admin_locked) | 한국 법정동 namespace 명시 |
| heritage_no | kentity_external_ids (provider=문화재 namespace) | 전용 master 없음 |
| disambiguator | kentity_entities.qualifier_ko (D-04) | 정체성 키 아님 |
| name_ko + tdb_place_names(11컬럼: locale,name,kind,source_code,confidence,operator_locked,verified_at) | kentity_names + name_evidence | kind/source_code 는 초기 호환값. form 은 recorded 로 시작, 승격은 근거 필요 |
| tdb_place_links(7: source_code,external_id,place_id,method,score,linked_at) | kentity_external_ids(status unverified) + crosswalk 근거 | score 는 승인 근거가 아님 |
| merged_into, status, operator_locked | crosswalk.source_state(merged)/source_merged_into, 원본 잠금은 source_guards | source false ≠ 해제 |
| prominence, confidence, notes, source_urls | archive_only(매핑 v2) | 서비스 채택 없음 |

인물 최소 속성(§11.1)의 legacy 원천은 `kwave_entity_person_details`(birth_year int, gender char, primary/secondary_roles, groups, agency, notable_works) 와 `kwave_persons`(5,700행, name_ko UNIQUE). birth_year→birth_year(precision year), gender 는 기본 미흡수(§11.1), roles→person_roles 선매핑(분류 규칙 §7 16/16), groups/agency→relations(member_of), notable_works→relations(appears_in/created_by) 후보. 이름 UNIQUE 는 승계하지 않는다.

## 6. 사전 seed 목록 (§9 그룹2·3) — P1.02 migration 입력

| 사전 | 건수 | 출처 | 활성 기준 |
|---|---|---|---|
| kentity_types | 13 | [KDB_CLASSIFICATION_RULES.md](KDB_CLASSIFICATION_RULES.md) §2 | 현행 11 = enabled true, brand/character = false(P1 코드 전환 후 true), unknown = 검수용 |
| kentity_subtypes | 61 | 동 §3 | parent_code 전부 NULL, enabled true |
| kentity_role_types | 26 | 동 §4 | 3레벨(entertainer→singer→rapper). enabled true |
| kentity_domains(보강) | 8 | 동 §5 = 현행 8행 | enabled true |
| kentity_locales | 13 | [KDB_NAME_READINESS_CONTRACT.md](KDB_NAME_READINESS_CONTRACT.md) §2 | 실사용 en/ja/vi/es/zh-Hans + ko/zh-Hant/id/pt-BR = enabled true; de/fr/ru/th = false(TDB th disabled) |
| kentity_relation_types + pairs | 11 predicate / pairs 는 [KDB_IDENTITY_CONTRACT.md](KDB_IDENTITY_CONTRACT.md) §7 정확 조합 | member_of, holds_position, operates, located_in, created_by, appears_in, portrays, manufactured_by, branded_as, edition_of, held_at | enabled true |
| kentity_position_types | 10 | 목표 구조 §10.6 | ceo/executive/legislator/mayor/minister/chairperson/head_coach/coach/player/member |
| kentity_source_policies | 0 seed | §12.1 | 승인은 검토자·시각·조건을 갖춘 INSERT 만. unreviewed 자동 승격 금지 |

## 7. 이전/고객 계약의 물리 위치 (§9 그룹5)

- manifest 최소 필드: `kentity_migration_records` 의 source_pk(typed)/source_fingerprint/plan_hash/policy_proof/applied_result_hash 만 운영 DB 에 둔다. 원천 raw payload 와 복구 bundle 은 `recovery_ref` 가 가리키는 운영자 접근통제 보관소로 분리한다(제어 설계 §4).
- legacy UUID 호환: 고객이 KDB UUID 를 저장한다(corrections 3,799/3,799 동반, [KDB_API_COMPATIBILITY_CASES.md](KDB_API_COMPATIBILITY_CASES.md) §실사용 확인). 따라서 `kentity_redirects`(§10.4) 와 UUID 불변(§5)이 호환의 물리 근거다. 기존 5 route 는 `kentity_name_catalog` 뷰와 legacy 어댑터로 읽는다.
- tenant override: 현재 사용처 **0** — 소비자 4곳 모두 global scope 이며 tenant 전용 표기 표는 없다. `scope_key`(readiness/fill_jobs/waiters, §13/S06) 는 미래 tenant 를 위한 자리이고 지금 기본값 'global' 만 쓴다. 별도 override 표는 만들지 않는다.

## 8. 검증 경계

이 문서는 카탈로그 조회와 설계 문서의 정적 대조다. FK/UNIQUE/EXCLUDE 의 실제 동작, 기존 행의 제약 충족 여부(D-01/D-03 계수), btree_gist 설치는 P1.01~P1.03 에서 격리 DB 로 확인한다. 운영 DDL·GRANT 변경은 없었다.

## 9. source_code → 정책 namespace·우선순위·locale 물리 계약 (P0.05)

현행 우선순위는 `kdb_source_priority(text)` 함수(IMMUTABLE, 8단계·약 55코드)이고, 값은 `kwave_entities.canonical_*_source` 8컬럼에 문자열로 있다.
목표는 `kentity_source_policies(provider, version)`(§12.1) + `kentity_names.form/source_code` + `kentity_locales` 다. 우선순위 숫자는 **정책 검토의 입력**이지 자동 verified 근거가 아니다.

| legacy tier | 대표 코드 (운영 건수) | 목표 provider namespace | names.form | strict-ready 가능 |
|---|---|---|---|---|
| 1 운영자 | operator-locked 772, operator, local-usage 2,494 | `operator` (정정 근거 evidence.claim_type=name, correction-verified 는 별도 `correction`) | recorded | ✓ 검수 근거 있을 때 |
| 2 매체 합의 | media-consensus 534 | `media-consensus` (독립 근거 2개 규칙 X07) | recorded | ✓ |
| 3 매체 관측 | rss-observation:<domain> 7개 도메인 2,100+ | provider=`rss-observation`, 도메인은 evidence.independent_origin/source_url | recorded | ✓ 정책 approved 도메인만 |
| 4 공식·카탈로그 | tmdb 8,657, musicbrainz 742, itunes 518, kofic 116, discogs 107, correction-verified 2,155, 기타 OTT/음원 | 코드 그대로 namespace 1:1 (`tmdb`,`musicbrainz`,…). 각각 license/valid_until 은 정책 행에서 검토 | recorded | ✓ approved 시 |
| 5 위키데이터 | wikidata-label 67,879 | `wikidata` (external_ids QID 와 같은 namespace) | recorded | ✓ |
| 6 위키백과 | wikipedia-langlinks 796, wikipedia-sitelink 62, wikipedia-zh-variant 2,349 | `wikipedia`(langlinks/sitelink), zh-variant 는 **translated**(변환) | recorded / translated | zh-variant ✗ |
| 7 검색·규칙 변환 | romanization 24,304, opencc 5,191, kana-rule 1,952, local-search 373, namuwiki, baidu-baike, gemini-search … | `generated:<rule>` (romanization/opencc/kana) 와 `search:<engine>` 분리 | generated / recorded(search 는 근거 검수 필요) | ✗ generated |
| 8 기계번역 | gtranslate 25,610, codex-fallback 8,404 | `mt:gtranslate`, `mt:codex` | translated | ✗ |
| 99 미분류 | (함수 ELSE) | 정책 없음 → `unreviewed`, 공급 차단 | unknown | ✗ |

규칙:
- 이전 시 `canonical_<loc>_source` 값은 names.source_code 에 **그대로 보존**(호환값)하고, 위 표로 provider namespace 를 파생해 source_policies 를 조회한다. 코드 자체를 재작성하지 않는다.
- tier 7·8 은 form 이 generated/translated 라 strict-ready 에서 제외된다(표기 계약 §2). 현행 커버리지의 큰 몫(romanization 24,304 + gtranslate 25,610 + codex 8,404 + opencc 5,191 ≈ 63,500칸)이 여기 속하므로, **P5 전환 후 strict-ready 수치는 현행 "채워진 칸" 수보다 크게 낮게 나온다.** 이는 정직한 분리이며 회귀가 아니다. 인수 기준의 "언어 준비" 분모 정의와 일치한다.
- locale 태그 alias: legacy `translate_cache.term_type='mtraw:zh-CN'`(5,507) 은 `zh-Hans` 로, `zh` 단독은 호환 adapter 안에서만 `zh-Hans` 로 해석하고 응답에 실제 locale 을 표시한다(R05). `kentity_locales` 에는 `zh-Hans`/`zh-Hant` 만 존재한다.
- `kentity_names.source_code` 현행 3값(wikidata-label 3, operator-candidate 3, legacy-scope-review 1)은 위 표에 흡수된다. `operator-candidate` 는 tier 1 의 미검수 상태, `legacy-scope-review` 는 tier 99.
- `kdb_source_priority` 함수는 P5 까지 legacy 경로에서 유지하고, 공통 경로는 정책 행의 status/valid_until/허용 플래그를 읽는다. 숫자 tier 를 신규 함수로 복제하지 않는다.

## 10. pending 7표 판정 결과 (P0.05)

[KDB_TABLE_SCOPE.json](KDB_TABLE_SCOPE.json) 에 2026-09-13 기준으로 기록했다(87표·selected 27 불변, pending 0).

| 표 | 판정 | 근거 | 후속 |
|---|---|---|---|
| kwave_entity_research_queue | operational_retained (현재 수요 원장) | 26,608행, 최근 30일 4,338건 유입, R27/W8 | 행 이전 없음. 공통 후속은 candidate_requests/resolution_jobs |
| kwave_kdb_request_terms | operational_retained | 18,917행, 30일 prune 작동, 소비자 귀속 | locale 컬럼 없음 → locale 수요 추정 금지 유지 |
| tdb_name_misses (TDB) | operational_retained (TDB 운영) | 88행, last_seen 08-20, zh-Hans 61 | KDB 이전 없음. 이름 바인딩 금지 |
| kwave_person_research_queue | recovery_only | 562행 전부 done, 05-27 이후 쓰기 0, 읽기 1 | 보존, 재수입 없음 |
| kwave_entity_candidates | recovery_only | 60 pending, 05-25 이후 정지 | 보존, 중복 큐 금지 |
| kdb_audit_suspects | recovery_only + **현재 효력 인계** | 코드 참조 0. MISLINK 213 중 168 이 지금도 QID 연결 유지 | 168건을 P2.05 source_guards hold/rejected_binding 제안으로 인계(자동 해제 아님) |
| tmp_ld (TDB) | recovery_only | 법정동 임시표 5,067행(code,name,sgg), 참조 0 | 행정코드 공식 원천은 P4.02 별도 승인. 사전 승격 금지 |

어느 표도 selected_source 가 되지 않으므로 필드 매핑 v2(14표/185필드)는 변경 없다. 원본 삭제 승인은 전부 false 유지.
