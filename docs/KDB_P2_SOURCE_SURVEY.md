# TDB 원본 조사 — P2.01

버전 `p2-survey-v1` · 2026-09-13 KST · **읽기 전용 조사.** 원본·운영 DB 에 쓰기 없음.
기준: [실행 원장](KDB_INTEGRATION_TODO.md), [물리 구조](KDB_TARGET_SCHEMA.md), [식별 계약](KDB_IDENTITY_CONTRACT.md).

## 1. 고정한 기준점 (snapshot)

| 항목 | 값 |
|---|---|
| 원본 | `tdb-db` / database `tdb` (같은 호스트, 별도 컨테이너) |
| 대상 표 | `tdb_places` |
| 행 수 | **536,329** (`status='active'` 536,322 · `rejected` 5 · `merged` 2) |
| 최종 갱신 | `2026-09-11 20:50:52+00` |
| 최초 생성 | `2026-07-31 04:19:30+00` |
| 조사 시각 | 2026-09-13 KST |
| mapper 버전 | `tdb-places-mapper-v1` (P2.03 에서 구현) |
| 선택 정책 버전 | `tdb-selection-v1` (본 문서 §6) |
| canonicalization 버전 | `nfc-v1` (구조 §12.3 과 동일) |

이 값들은 `kentity_migration_runs` 의 `mapper_version` / `selection_policy_hash` /
`canonicalization_version` / `cohort_hash` 에 그대로 들어간다.

## 2. 완전한 PK

원장이 "완전한 PK를 기록한다"고 요구한 이유는, 공통 crosswalk 가 `source_id` **한 컬럼**으로
원본을 가리키기 때문이다. 원본 키가 두 컬럼이면 한 컬럼으로는 지목할 수 없다.

| 원본 표 | 실제 PK | 공통 crosswalk 로 옮길 때 |
|---|---|---|
| `tdb_places` | `(id)` uuid | `source_id = id::text` — 1:1 |
| `tdb_place_names` | `(id)` bigint | 이름은 대상 Entity 에 귀속. 별도 crosswalk 없음 |
| **`tdb_place_links`** | **`(source_code, external_id)`** | **두 컬럼.** 외부 ID 는 `kentity_external_ids(provider, external_id)` 로 간다 |
| **`tdb_src_records`** | **`(source_code, external_id)`** | **두 컬럼.** 원천 관측은 `kentity_evidence(provider, source_record_id)` 로 간다 |
| `tdb_sources` | `(code)` | 원천 정책 `kentity_source_policies(provider, version)` 로 |
| `tdb_type_locale_forms` | `(place_type, locale)` | 표기 형식 규칙 — P4 |

`kentity_crosswalks` 의 PK 는 `(source_system, source_table, source_id)` 다. `source_table` 을
P1.02 에서 추가했기 때문에 `tdb_places` 와 `tdb_src_records` 를 같은 네임스페이스에서 구분할 수 있다.
다만 `tdb_src_records`/`tdb_place_links` 를 연결 대상으로 삼으려면 `source_id` 를
`source_code + '/' + external_id` 로 합성해야 하며, **그 합성 규칙을 mapper 버전에 고정한다.**

## 3. 19유형 분모

| place_type | 전체 | active | 구분자 없음 |
|---|---|---|---|
| restaurant | 145,433 | 145,433 | 0 |
| **other** | **106,602** | 106,602 | 30 |
| district | 83,897 | 83,897 | 973 |
| tourist_spot | 36,981 | 36,981 | 0 |
| **person** | **31,378** | 31,378 | **31,378 (전부)** |
| accommodation | 26,569 | 26,568 | 0 |
| shopping | 25,509 | 25,504 | 0 |
| leisure_sports | 20,796 | 20,796 | 0 |
| nature | 19,664 | 19,664 | 0 |
| heritage | 13,888 | 13,887 | 0 |
| cultural_facility | 5,943 | 5,943 | 0 |
| legal_dong | 4,912 | 4,912 | 0 |
| work | 3,822 | 3,822 | 0 |
| festival_event | 3,322 | 3,322 | 0 |
| food | 2,774 | 2,774 | 2,774 |
| organization | 2,557 | 2,557 | 2,557 |
| education | 1,122 | 1,122 | 1,122 |
| transit | 911 | 911 | 911 |
| admin_region | 249 | 249 | 17 |
| **합계** | **536,329** | 536,322 | 39,802 |

## 4. locale 분모

| locale | 표기 수 | 대상 수 | 운영자 잠금 |
|---|---|---|---|
| ko | 561,116 | 536,319 | 0 |
| en | 560,546 | 506,783 | 0 |
| ja | 534,103 | 465,091 | 0 |
| zh-Hans | 444,532 | 417,749 | 1 |
| zh-Hant | 415,043 | 386,001 | 3 |
| ru | 130,717 | 129,082 | 0 |
| es | 14,123 | 14,008 | 0 |
| fr | 11,205 | 11,048 | 0 |
| de | 9,094 | 8,999 | 0 |
| id | 3,130 | 3,123 | 0 |
| vi | 1,642 | 1,639 | 0 |

`ru`·`fr`·`de` 는 KDB 의 locale 사전에 없다(구조 §2 seed 14종). 흡수 시 **사전에 없는 locale 은
저장할 수 없다**(M14a 로 시험됨). 처리는 P2.02 에서 정한다 — 사전 확장이냐 보관만이냐.

## 5. 원천·권리 분모

| 라이선스 | 대상 수 | 연결 수 | 성격 |
|---|---|---|---|
| **AI Hub 이용약관** | **327,010** | 1,397,918 | 표준 공개 라이선스 아님. 재배포 조건 확인 필요 |
| 공공누리 제1유형 | 201,445 | 329,201 | 출처표시 조건, 상업적 이용·변형 허용 |
| CC0 | 17,915 | 17,999 | wikidata |

원천별 상위: `aihub_tour` 1,397,918 · `ngii_gazetteer` 106,571 · `tourapi_ko` 68,993 ·
`seoul_dict` 38,688 · `tourapi_en` 18,340 · `wikidata` 17,999 · `heritage_khs` 17,969.

표기 출처 상위: `aihub_tour` canonical 1,499,119 · `ngii_gazetteer` 348,114 · `rule` 236,100 ·
`wikidata` 134,140. **`rule` 은 생성 표기**이므로 `form='generated'` 로 들어가야 하고
strict-recorded 를 요구하는 소비자에게 공급되면 안 된다(M14 계약).

> **권리 판단은 이 문서의 범위가 아니다.** 측정값만 적는다. `kentity_source_policies` 는 기본
> 차단이므로, 승인되지 않은 원천의 표기는 읽기 게이트에서 자동으로 막힌다(S03). AI Hub 조건은
> 운영자 확인 대상으로 올린다.

## 6. QID 없는 기록 — 96.7%

| 항목 | 수 | 비율 |
|---|---|---|
| QID 연결 있는 대상 | **17,915** | 3.3% |
| **QID 없는 대상** | **518,414** | **96.7%** |

이 수가 흡수 방식을 바꾼다.

- 자동 병합 게이트는 공유 QID 를 요구한다(`merge.go`). QID 가 없는 96.7% 는 **애초에 자동
  병합 대상이 아니다.** 그리고 그 게이트 자체가 S04 와 양립하지 않는다([D-36](KDB_P1_ISOLATED_BASELINE.md#45-d-36--두-규칙을-합치면-자동-병합-경로가-도달-불가가-된다)).
- `kentity_external_ids` 로 고정할 anchor 가 없으므로, 96.7% 의 정체성 근거는 **원천 관측
  자체**(`tdb_src_records`)가 된다. 그 관측이 `kentity_evidence` 의 `identity` 주장이 되고,
  원천 정책 승인(S03)이 공급 가부를 정한다.
- 따라서 **이름으로 동일성을 판단할 유혹이 가장 큰 구간이 여기다.** I01 이 금지하는 바로 그것이다.

## 7. 동명·구분값 — 흡수의 실제 위험

### 7.1 TDB 내부

| 항목 | 수 |
|---|---|
| 같은 `(name_ko, place_type)` 가 둘 이상 | 21,103 그룹 / 93,167 행 (17.4%) |
| 구분자를 가진 대상 | 496,527 / 536,329 (92.6%) |
| `(name_ko, place_type, disambiguator)` 충돌 | **445 그룹 / 저장 못 할 행 478** |
| 그중 구분자가 비어서 충돌 | **0** |
| 19유형이 한 유형으로 접힐 때(최악 상한) | 718 그룹 / 저장 못 할 행 755 |

TDB 는 구분자를 잘 채워 왔다. 내부 충돌은 478건(0.089%)뿐이고, 그것도 구분자 누락이 아니라
**같은 구분자를 쓴 서로 다른 대상**이다(heritage 202 · district 191 · nature 46 상위).

### 7.2 KDB 와 합칠 때 — 여기가 진짜 문제다

| 항목 | 수 |
|---|---|
| KDB 와 이름이 겹치는 TDB 키 | **5,171** |
| 그 TDB 행 수 | **5,172** |
| 겹치는 KDB 엔티티 수 | **4,215** |

유형별:

| place_type | 겹치는 키 | 행 | **구분자 없음** |
|---|---|---|---|
| **person** | 3,604 | 3,604 | **3,604 (전부)** |
| district | 803 | 804 | 0 |
| restaurant | 402 | 402 | 0 |
| other | 87 | 87 | 0 |
| accommodation | 64 | 64 | 0 |
| nature | 55 | 55 | 0 |
| shopping | 43 | 43 | 0 |
| cultural_facility | 31 | 31 | 0 |
| **transit** | 16 | 16 | **16** |

**TDB `person` 31,378건은 구분자가 하나도 없다.** TDB 안에서는 동명이 0건이라 문제가 없었다.
그런데 KDB 에는 이미 13,191개 엔티티가 있고, 그중 4,215개와 이름이 겹친다.

KDB 의 legacy UNIQUE 는 `(canonical_ko, entity_type, coalesce(disambig,''))` 이다. 구분값 없이
넣으면 **두 번째 사람이 저장되지 못하고, 진입 경로는 그 충돌을 "이미 있음"으로 접어 앞사람의
UUID 를 돌려준다**([D-29](KDB_P1_ISOLATED_BASELINE.md#30-새로-드러난-것--d-29)).
같은 이름의 다른 사람이 한 ID 로 합쳐진다.

## 8. 그래서 흡수의 규칙 — ID 와 구분값을 함께 부여한다

> **운영자 지시(2026-09-13): "흡수할 때 id 등 부여하면서 카테고리, 즉 고유명사에서 명확하게
> 구분값을 주어야 추후 혼동이 없다."**

이 지시는 위 실측이 가리키는 곳과 정확히 같다. 흡수 시 각 대상은 **UUID 하나**와 함께 다음
세 가지를 **반드시 명시값으로** 갖는다. 나중에 추론하도록 비워 두지 않는다.

| 부여할 것 | 무엇으로 채우나 | 비우면 생기는 일 |
|---|---|---|
| `entity_type` | 19 place_type → KDB 13유형 선매핑 (P2.02) | 유형 미상이면 검증된 분류를 만들 수 없다(구조 §3 CHECK) |
| `subtype` | 61 세부유형 사전 코드 | 사전 밖 값은 복합 FK 가 거부한다 |
| **`disambig`(구분값)** | TDB `disambiguator`, 없으면 **흡수 시 생성** | 같은 이름의 다른 대상이 한 UUID 로 합쳐진다 |

구분값 생성 규칙(P2.02 에서 확정, 초안):

1. TDB `disambiguator` 가 있으면 그대로 쓴다 (496,527건).
2. 없으면 **그 대상을 실제로 구분하는 사실**로 만든다 — 유형·지역(`addr_ko` 의 시/군/구)·
   소속·대표작·생년. 순서는 대상 유형별로 고정한다.
3. 그래도 구분되지 않으면 **구분값을 지어내지 않는다.** 검수 대기로 보낸다 —
   임의 값은 "구분했다"는 거짓 신호가 되고, 그건 이름으로 판단하는 것과 같은 사고다.
4. 이름이 KDB 쪽과 겹치는 5,172건은 **동일인/동일대상 여부를 먼저 판정**한다. 같으면 하나의
   ID(기존 KDB UUID 유지), 다르면 각자 UUID + 서로 다른 구분값. 이름만으로 어느 쪽도
   자동 결정하지 않는다(I01/I04/I05).

person 3,604건이 4번 경로의 대부분이다. 이건 **이름 일치를 근거로 병합해서도, 자동으로
분리해서도 안 되는** 구간이라 P2.05 의 위험 목록과 P3 의 승인 batch 범위에 직접 들어간다.

## 9. 선택 정책 `tdb-selection-v1` (초안)

| 처분 | 대상 | 근거 |
|---|---|---|
| `include` | 구분값이 이미 있고 KDB 와 이름이 겹치지 않는 active 행 | 충돌 위험 없음 |
| `conditional` | KDB 와 이름이 겹치는 5,172건 | 동일성 판정 선행 |
| `conditional` | 구분자 없는 39,802건 중 §8-2 로 구분값을 만들 수 있는 것 | 생성 규칙 적용 후 재평가 |
| `archive_only` | `rule` 로 생성된 표기 | `form='generated'` — strict 소비자에 공급 금지 |
| `archive_only` | 사전에 없는 locale(ru·fr·de) 표기 150,016건 | locale 사전 확장 결정 전까지 |
| `exclude_operational` | `status<>'active'` 7건 | 원본에서 이미 제외 |
| **보류** | `other` 106,602건 | 카테고리 불명확 — P2.02 재조사 대상 |

`include` 예상 규모는 P2.02 선매핑 뒤에 확정한다. 지금 단계에서 숫자를 못 박지 않는다 —
유형 접힘에 따라 충돌 수가 달라진다(§7.1 최악 상한 755).

## 10. 이 조사가 바꾼 것

- 96.7% 가 QID 없음 → 정체성 근거는 외부 ID 가 아니라 **원천 관측**이다. 원천 정책 승인이
  공급 가부를 정하는 유일한 통로가 된다.
- `person` 31,378건 구분자 0 → **구분값 부여를 흡수의 필수 단계로 올린다.** 나중에 채우는
  값이 아니다.
- 완전한 PK 가 두 컬럼인 표가 둘 → `source_id` 합성 규칙을 mapper 버전에 고정한다.
- AI Hub 조건이 61% 를 덮는다 → 운영자 확인 대상. 기본 차단이라 확인 전에는 공급되지 않는다.
