# KDB UUID·원본 연결·병합/분리 계약 — P0.03

버전 `identity-design-v1` · 2026-09-12 KST · 설계 계약, 운영 적용 아님.
기준: [분류 사전](KDB_CLASSIFICATION_RULES.md), [물리 구조](KDB_TARGET_SCHEMA.md), [실행 원장](KDB_INTEGRATION_TODO.md).

## 1. 실제 구현에서 재사용할 것

`internal/kdb/agents/disambiguator/merge.go`는 이미 SERIALIZABLE, UUID 순서의 부모 행 잠금,
입력 재확인, 외부 ID 충돌 차단, 취소 시 rollback을 사용한다. 이를 폐기하거나 Go를 재작성하지 않는다.
기존 `merge_integration_test.go`의 역방향 경쟁·늦은 실패·잠금·잘못된 계획 시험을 공통 모델에도 이식한다.
현재 경로는 kwave_entities 중심이고, 공통 crosswalk/관계/표기/준비/분리 원장 전체를 보장하는 것은 아니다.
`internal/kentity/mapping.go`의 동일인 연결 승인과 `tdb_mapping.go`의 후보 등록은 **데이터 사용권 승인과 별개**다.

## 1.1 가장 먼저 고정하는 규칙 — 한 사람 = 하나의 ID

현실의 한 사람에게 ID는 하나다. 그 ID가 그 사람에 관한 모든 값을 모으는 자리다.

**직업은 정체성이 아니다.** 한 사람이 배우이자 가수이자 MC일 수 있다. 이때 직업 행이 여럿일 뿐
ID는 하나이며, 겸업을 이유로 ID를 쪼개면 같은 사람이 두 UUID를 갖게 된다. 그 순간 소비자마다
저장한 ID가 갈리고, 근거·표기·정정이 두 곳으로 나뉘어 어느 쪽도 완전하지 않게 된다.
따라서 새 직업이 확인되면 새 ID를 만드는 것이 아니라 기존 ID에 직업을 더한다(I09).

**ID가 갈리는 유일한 이유는 다른 사람이라는 것이다.** 이름이 같아도 다른 사람이면 ID를 나눈다(I05).
이름은 키가 아니므로(I01) 같은 이름이 몇 명이든 각자 자기 ID를 갖는다 — 동명이인도, 동명 3인 이상도
이 규칙 하나로 풀린다. 구분은 직업이 아니라 **소속사·생년·대표작·외부 식별자·언어별 표기**로 한다.
겸업이 많아 직업 목록이 길다는 것은 다른 사람이 섞였다는 신호가 아니다.

운영 화면과 API는 이 규칙을 그대로 따른다. 동명 후보는 자동 선택 없이 전부 제시하고(M06),
한 기사에 같은 이름이 여러 번 나오면 각 위치를 각자의 ID에 따로 묶는다(M07).
물리적으로는 `kentity_person_roles`가 한 ID에 여러 직업을 담고([구조 §6.1](KDB_TARGET_SCHEMA.md#61-kentity_person_roles--신규)),
사람 사이의 병합/분리는 §5·§6의 검수 경로로만 이뤄진다.

## 1.2 흡수할 때 ID 와 구분값을 함께 부여한다

운영자 지시(2026-09-13): **"흡수할 때 id 등 부여하면서 카테고리, 즉 고유명사에서 명확하게
구분값을 주어야 추후 혼동이 없다."**

원본을 받아 새 대상을 만들 때, UUID 만 발급하고 분류와 구분값을 "나중에 채울 칸"으로 비워 두지
않는다. 비워 두면 그 사이에 이름이 사실상의 키가 되고, 같은 이름의 다른 대상이 한 UUID 로
합쳐진다. 실제로 그 경로가 열려 있었다 — 이름+유형이 유일 키라 두 번째 대상이 저장되지 못하고
진입 경로가 충돌을 "이미 있음"으로 접어 앞 대상의 UUID 를 돌려줬다.

흡수 시 각 대상이 반드시 명시값으로 갖는 것:

| 항목 | 없으면 |
|---|---|
| UUID | — (I01: 이름·직업·생년으로 만들지 않는다) |
| 유형 / 세부유형 | 검증된 분류를 만들 수 없고 복합 FK 가 거부한다 |
| **구분값** | 같은 이름의 다른 대상이 한 UUID 로 합쳐진다 |

구분값은 **그 대상을 실제로 구분하는 사실**로 만든다 — 유형·지역·소속·대표작·생년 같은 것.
구분되지 않으면 **지어내지 않고 검수로 보낸다.** 임의로 채운 구분값은 "구분했다"는 거짓 신호가
되며, 이름으로 판단하는 것과 같은 종류의 사고다.

원본 쪽 실측과 적용 범위는 [TDB 원본 조사 §8](KDB_P2_SOURCE_SURVEY.md)에 있다.

## 1.3 두 방향을 같이 못 박는다 — I13 (2026-09-20 운영자 확인)

운영자 지시: **"아리랑이라도 구분값을 주어 아이디를 별도로 부여해서 동명을 해결하면 되지.
하나는 노래, 하나는 식당, 하나는 호텔. 분류명만 제대로 설계되었다면 전혀 문제 안 되지.
사람도 고유명사의 하나의 아이디만 존재해 — 별명·가명·닉네임·실명이든 하나의 아이디로
모두 포함시키는 거지."**

이것이 정본이다. 축이 둘이고 서로 반대다.

| | 규칙 | 무엇이 가르나 | 지금 무엇이 강제하나 |
|---|---|---|---|
| **같은 이름, 다른 대상** | **각자 ID** | 이름이 아니라 **유형 + 구분값** | DB `UNIQUE (canonical_ko, entity_type, COALESCE(disambig,''))` |
| **한 대상, 여러 이름** | **하나의 ID** | — 실명·예명·별명·닉네임·로마자는 이름 변이다 | `ClaimsForName` + `SoleAliasOwner` (인입 경로 전부) + `TestEntityCreationPathsCheckAliases` |

**그래서 흡수량은 동명의 이유가 아니다.** 「아리랑」이 민요·식당·호텔로 셋이어도 각자
유형과 구분값을 갖고 각자 ID 로 앉는다. 원본을 더 받는다고 이 축이 망가지지 않는다 —
망가지는 유일한 경우는 **구분값 없이 받는 것**이고, 그건 §1.2 가 이미 금지한다.
(2026-09-16 로드맵이 "식당 14.5만을 넣으면 아리랑 판정이 망가진다"고 적었는데, 그 문장은
데이터 양을 이유로 든 점에서 틀렸다. 막아야 하는 것은 양이 아니라 **구분값 없는 적재**다.)

반대 축은 실제로 새고 있었다. 2026-09-20 활성 원장 실측 **307건**이 한 대상인데 두 ID 다:

```
위너 ↔ WINNER        하하 ↔ 하동훈       KCM ↔ 강창모
솔비 ↔ 권지안        예리 ↔ 김예림       예원 ↔ 정예원
```

원인은 인입 경로 셋 중 **둘이 `canonical_ko` 만 보고 별칭 칸을 안 본 것**이다
(`candidates.go`·`research/worker.go`. `demand_evidence.go` 는 보고 있었다 — 한 자리만
맞고 두 자리가 틀린, 이 저장소가 반복해 밟는 모양이다).

갈라지면 깨지는 것: 소비자마다 저장한 ID 가 달라지고, 근거·표기·정정이 두 곳으로 나뉘어
**어느 쪽도 완전하지 않게 된다.** 같은 사람에게 두 번 물어 두 답을 주는 상태다.

**I13 의 판정 규칙** (`SoleAliasOwner`) — 새 UUID 를 만들지 않는 경우는 하나뿐이다:

1. 그 이름을 **정본으로 쓰는 대상이 하나도 없고**,
2. **별칭으로 주장하는 대상이 정확히 하나**일 때.

정본 주장이 하나라도 있으면 만들지 않는 대신 붙이지도 않는다 — 「원희」가 brand_place
의 정본이면서 「아일릿 원희」의 별칭이면, 붙이는 순간 다른 대상을 삼킨다.
별칭 주장이 둘 이상이어도 붙이지 않는다. 어느 쪽인지는 **근거가 정하지 순서나
confidence 가 정하지 않는다**(I06).

이미 갈라진 307건은 `kdb-app name-split-audit [N]` 으로 요청 많은 순서로 본다.
**감사는 고치지 않는다** — 같은 대상이면 병합, 별칭이 틀렸으면 별칭 제거이고 처방이
반대라서 근거 없이 한쪽으로 몰면 D-37 을 반복한다.

## 2. 불변 조건 I01~I13

| 규칙 | 계약 |
|---|---|
| I01 | 이름·직업·생년·소속·분야는 UUID 생성/유일 키가 아니다. 새 현실 대상에 임의 UUID를 발급한다. |
| I02 | 기존 KDB Entity UUID를 보존한다. kwave_persons UUID는 이름 JOIN이 아니라 검수된 원본 연결로 대응한다. |
| I03 | TDB UUID는 원본 namespace 안에서 보존한다. 공통 UUID와 같다고 가정하지 않고 명시적으로 연결한다. |
| I04 | 후보 UUID 생성, 기존 대상과의 동일인 승인, 개별 값 승인, 외부 재사용 승인을 분리한다. |
| I05 | 가수 A와 배우 B가 동명이인이면 UUID·근거·이름·job을 분리한다. A의 배우 활동 발견이 B 병합의 근거는 아니다. |
| I06 | unknown, 외부 ID 충돌, distinct 판정, 잠금, 오래된 버전, 다른 writer 소유면 자동 병합하지 않는다. |
| I07 | 모든 값 쓰기는 대상 UUID·기대 revision·정책·근거를 검사한다. 모델은 제안만 만들고 직접 승인하지 않는다. |
| I08 | 같은 외부 ID도 잘못 연결됐을 수 있다. 공식 원천이라는 이유로 상충 유형/정체성 증거를 무시하지 않는다. |
| I09 | 개명·직업/소속/분야 변경은 기존 UUID의 변경이다. 작품·팀·회사의 별개 정체성 생성과 구별한다. |
| I10 | 병합은 제한된 승인 manifest 전체가 원자적으로 성공하거나 전부 rollback한다. 부분 성공을 완료로 보고하지 않는다. |
| I11 | 병합 전 ID·원본 연결·값의 출처·변경 이력을 보존한다. 잘못된 병합을 이름 기준으로 재분배하지 않는다. |
| I12 | 기사 시점의 사실과 지금의 사실은 다르다. 관측 시각을 재임 시작/생일로 넣지 않는다. |
| I13 | 한 대상의 실명·예명·별명·닉네임·로마자 표기는 **그 ID 의 이름 변이**다. 새 대상을 만들지 않는다. 반대로 같은 이름의 다른 대상은 유형+구분값으로 갈라 각자 ID 를 갖는다. 대상을 만드는 모든 경로는 만들기 전에 그 이름의 주인이 있는지 묻는다(§1.3). |

## 3. 원본 연결: 기존 crosswalk 보강

새 원본 마스터를 추가하지 않고 `kentity_crosswalks`를 재사용한다.
목표 자연 키는 `(source_system, source_table, source_id)`이고 `id uuid`도 부여한다.
현재 source_binding_id는 TDB shadow ID이므로 이름만 보고 범용 binding으로 사용하지 않는다.

| 원본 | source_system | source_table | source_id | 처리 |
|---|---|---|---|---|
| KDB 공통 투영 원본 | kdb | kwave_entities | 원본 UUID 문자열 | 공통 동일 UUID 유지, 이름 변경에도 연결 유지 |
| 별도 legacy 인물 | kdb | kwave_persons | 원본 UUID 문자열 | 검수 전 review. 위 테이블과 같은 UUID 문자열이어도 별개 원본 키 |
| TDB | tdb | tdb_places | 원본 UUID 문자열 | 전체 조사에 포함. QID 없는 기록도 수용 가능 |
| 향후 분야 원천 | 승인된 provider namespace | 실제 데이터셋/버전 namespace | 원천 불변 ID | URL/이름만으로 영구 키 구성 금지 |

기존 `source_system='legacy_person'`는 `(kdb,kwave_persons)`의 호환 입력이다.
기존 `source_system='tdb'`는 `(tdb,tdb_places)`로 해석한다. 새 API는 source_table을 명시한다.
호환 조회를 수정하기 전에 PK만 바꾸지 않는다. 동일 source_id가 다른 표에 있을 때 구 조회가 여러 행을 선택할 수 있기 때문이다.

연결 상태는 review/confirmed/conflict/rejected/withdrawn. 별도 `source_state`는 present/deleted/merged/unknown이다.
confirmed는 해당 원본이 그 현실 대상이라는 판정이며 이름/직업/분류/권리의 자동 승인값이 아니다.
source_fingerprint는 허용 필드의 정규 JSON SHA-256이다. 원천 PK·유형·이름·식별 속성·잠금·병합 대상·선택 필드 버전만 포함한다.
API 키/전체 raw payload/무관한 수집 시각은 fingerprint와 문서에 넣지 않는다.
`source_generation`은 원천 정체성 관련 입력이 바뀔 때 증가한다. 똑같은 재관측만으로 재검수 작업을 무한 생성하지 않는다.
정체성 입력 변경·원본 삭제/병합/잠금은 기존 승인을 stale로 판정해 공급을 재검수한다. 자동으로 다른 ID에 붙이지 않는다.
전체 스캔의 미발견은 스캔 완료 manifest를 확인하기 전 삭제로 해석하지 않는다.

멱등 요청 키는 `(owner_key, request_key)`, payload_hash가 다르면 409다.
동일 원본을 서로 다른 검수자가 동시에 등록해도 원본 키 UNIQUE가 후보 하나만 소유하게 한다.
다른 원본의 동명 후보는 별도 UUID를 가질 수 있다. 검증된 외부 ID 유일성 충돌 시 기존 후보를 반환하거나 검수로 보낸다.
원천이 같은 ID를 다른 대상으로 재사용하면 기존 binding을 덮어쓰지 않고 namespace 버전/정정 판정을 기록한다.

## 4. 외부 식별자와 같은/별개 판정

외부 키는 `(provider_namespace, external_id)`다. provider namespace는 기관/데이터셋·ID 정책을 포함한다.
Wikidata QID, 선수 등록 ID, 법인 식별 ID, 시설 ID를 서로 같은 namespace로 취급하지 않는다.
검증된 한 외부 키의 현재 소유자는 하나다. 예약/확정/철회·재배정은 전역 transaction-level identity lock 후 부모/원본 키 순서로 잠그고 두 표를 함께 재검사한다.
각 표의 UNIQUE 또는 잠금 없는 같은 transaction만으로 두 표 사이 소유권 경쟁을 막았다고 보지 않는다. 물리 제약/시험은 [구조 §14.4](KDB_TARGET_SCHEMA.md#144-외부-id-예약과-확정의-단일-소유--s04)에 연결한다.
외부 키가 다른 것은 항상 별개라는 뜻도 아니다. 중복 발급/합병/원천 정정 근거를 검수하되 자동 병합을 우회하지 않는다.

판정은 정렬된 UUID 쌍 `(left_id < right_id)`에 대해 possible_same/confirmed_same/distinct/withdrawn을 기록한다.
한 쌍의 현재 결정과 append-only 감사 이력을 분리한다. 양쪽 identity_revision, 판정자/시각, 근거, 이유가 필수다.
판정 근거는 각 endpoint의 증거 FK를 따로 가진다. A 소유 증거를 B 소유라고 참조하지 않는다.
confirmed_same은 병합 실행이 아니다. distinct는 자동 재조사 점수로 취소할 수 없다.
A=B이고 B=C를 병합할 때 A≠C 판정이 있으면 중지한다. 직접 두 행만 보고 연결 성분의 모순을 놓치지 않는다.
점수가 비슷하거나 근거가 부족한 경우 답은 ambiguous다. 첫 행/유명인/높은 인지도 자동 선택은 금지한다.

## 5. 공통 병합 트랜잭션

1차 공통 병합은 **검수된 소규모 쌍 단위 작업**으로 제한한다. 기존 자동 legacy merge의 확대 승인이 아니다.
처리량을 높이기 위한 수천 건 연결 성분 일괄 병합은 범위 밖이며 별도 격리 시험 없이는 활성화하지 않는다.

### 사전 계획

- survivor 선택은 현재 유효 서비스 ID/소비자 참조 보존을 우선한다. 이름 정렬/최신 생성/높은 모델 점수로 결정하지 않는다.
- 양쪽 identity_revision, source generation, 근거/권리/잠금 버전, 영향 참조 목록을 고정한 plan_hash를 생성한다.
- 각 이름/증거/직업/관계/binding/외부 ID에 keep/move/combine_evidence/hold를 명시한다.
- 충돌한 이름·직업을 무조건 더 많이 가진 쪽으로 덮어쓰지 않는다. 출처 사용권이 다르면 이름 이전도 보류할 수 있다.
- 이미 승인된 이름 기간 중첩·외부 ID 유일성·self relation 발생·단일 소속 추정 등을 미리 검증한다.
- 첫 파일럿 한도: 한 쌍, 변경 객체 500개 이하. 초과는 자동 쪼개지 않고 계획 검수로 보낸다.

### 잠금과 commit

1. 요청 권한/멱등 키/plan_hash 검증 후 SERIALIZABLE 트랜잭션을 시작한다.
2. 정체성 구조 변경용 단일 **transaction-level advisory lock**을 획득한다. 1차 병합/분리/쌍 판정/원본 대상 및 외부 ID 예약·확정 소유 변경을 직렬화한다.
   일반 검색·서로 다른 Entity의 표기 보충까지 전역 잠금으로 묶지 않는다.
3. legacy 소유가 섞이면 해당 legacy 부모들을 UUID 순으로, 다음 공통 부모들을 UUID 순으로 잠근다.
   연결 성분/redirect 대상까지 탐색해 동일 잠금 집합인지 재확인한다. 소유권/잠금 순서가 다른 옛 경로는 전환 전 차단해야 한다.
4. evidence·binding·외부 ID 예약·관계의 필요한 행을 정해진 PK 순서로 잠근다. 새 자식 쓰기도 해당 부모 잠금 규칙을 따른다.
5. 저장 시점의 모든 revision/권리/근거/잠금/distinct/유형을 다시 확인한다. 계획 후 변경됐으면 409와 재검수 사유를 반환한다.
6. 검수된 계획대로만 반영한다. 자식과 근거의 entity_id가 함께 이동해야 하는 FK는 제한된 작업에서 DEFERRABLE로 검증한다.
   복합 FK를 없애거나 트리거를 disable하지 않는다. 결합 시 원래 객체 ID→남는 객체 ID 매핑도 기록한다.
7. loser는 물리 삭제하지 않는다. retired + survivor redirect를 기록하고 source UUID/이전 소유권 이력을 보존한다.
8. 영향 Entity의 identity_revision/revision을 증가시키고 준비 결과를 stale로 만든다. 오래된 job의 generation/lease를 폐기한다.
9. 변경 전후 객체 목록·감사·무효화 outbox·성공 결과를 같은 트랜잭션에 기록한다. 실제 영향 행 수/FK 확인 후 commit한다.

초기 timeout은 기존 경로에 맞춰 lock 2초, statement 10초, 전체 15초로 제한한다. 부하 실측 후만 변경한다.
SQLSTATE 40001/40P01은 전 트랜잭션 rollback 후 최대 2회만 재시도한다. 매번 현재 상태를 다시 읽으며 옛 승인 계획이 달라졌으면 중단한다.
고유 키 충돌/권리 보류/잠금/정체성 모순은 기계적 재시도 사유가 아니다. DB 밖 검색/모델 호출은 잠금 보유 중 실행하지 않는다.
클라이언트 연결이 끊겨 commit 결과를 모르면 같은 멱등 키로 결과를 조회한다. 새 키로 동일 병합을 반복하지 않는다.

PostgreSQL의 [격리 수준](https://www.postgresql.org/docs/16/transaction-iso.html) 및
[잠금](https://www.postgresql.org/docs/16/explicit-locking.html) 규칙을 바탕으로 한 설계다. 실제 경쟁 시험을 대체하지 않는다.

## 6. redirect·분리·유형 정정

redirect는 별도 `kentity_redirects(from_id PK,to_id,operation_id,revision,created_at)`다.
양쪽 Entity FK, from!=to, 순환 금지, 새 연결은 최종 survivor로 정규화한다. 읽기는 최대 8hop을 넘으면 conflict로 중지한다.
redirect는 같은 endpoints의 applied merge만 참조한다. merge source=loser/target=survivor, split은 그 반대이며 reversal_of로 원 merge를 지정한다.
한 merge의 applied reversal은 최대 하나다. 단순 operation UUID FK로 planned/rejected/retype을 redirect 근거로 쓰지 못한다. 물리 검사는 구조 §14.5를 따른다.
옛 ID 조회는 requested_id와 resolved_id, identity_revision을 모두 반환한다. 옛 기사 snapshot의 ID를 조용히 다시 쓰지 않는다.
수정 요청에 옛 ID만 오면 자동으로 survivor를 수정하지 않고 새 대상/버전 확인이 필요하다고 409를 반환한다.

잘못된 병합 분리:

1. 해당 operation·양쪽 현재 UUID·후속 변경 목록을 읽어 분리 계획을 생성한다.
2. 위와 같은 전역 정체성 잠금/부모 잠금 순서를 적용한다. loser의 원래 UUID를 재활성화하고 임의 새 ID를 만들지 않는다.
3. 병합 후 변경되지 않은 객체는 operation before/after hash와 원래 소유자 기준으로 복원한다.
4. 병합 후 새로 생긴 값/기사 참조는 어느 대상의 것인지 새 근거가 필요하다. 이름으로 반씩 나누거나 모두 복제하지 않는다.
5. 후속 병합/복원 충돌은 hold 목록으로 남긴다. 범위가 커지면 자동 역순 UPDATE가 아니라 별도 복구 계획을 요구한다.
6. redirect 해제, pair distinct 또는 재검수, 원본 연결·근거·준비·캐시 무효화를 원자적으로 반영한다.
7. 외부에 이미 발행한 기사 정정은 outbox에 기록하되 이 트랜잭션이 CMS 발행 권한까지 갖지 않는다.

유형 정정은 병합과 다르다. UUID는 유지하되 잘못된 person 전용 직업/속성, 관계 predicate의 양끝 유형을 검사한다.
유형 변경 전에 충돌하는 현재 객체를 근거 있는 withdrawn/보관 이력으로 옮긴다. 남은 person FK를 CASCADE로 다른 유형에 바꾸지 않는다.
원본 데이터를 고쳤더라도 옛 writer가 재설치할 수 있으면 공통 차단/호환 writer 전환이 먼저다.

## 7. 기간·관계 계약

표현은 확인된 `valid_from/valid_until`(포함 마지막 날), 불완전한 연/월은 별도의 precision을 보존한다.
물리 표현은 구조 §14.7의 unknown/open/year/month/day와 year/month 구성요소다. year/month일 때 exact date는 NULL로 두고 중복 검사용 외피만 별도로 계산한다.
날짜를 모르면 NULL이다. NULL은 시점 사실의 긍정 증거가 아니며 겹침 방지에서만 보수적인 열린 경계로 취급한다.
직업 기간, 회사 재임 기간, 원천 관측 시각, 검수 시각을 서로 복사하지 않는다.
관계는 subject/object 두 UUID와 통제 predicate/role_code, 기간, 주체 소유의 관계 주장 근거를 가진다.

| predicate | subject 허용 | object 허용 | 주의 |
|---|---|---|---|
| member_of | person | organization/company/team | 정당·소속사·팀. 탈퇴가 직업 삭제를 뜻하지 않음 |
| holds_position | person | organization/company/team/league | 직책 코드와 기간 필수 검수. 대표이사=직업 코드가 아님 |
| operates | organization/company | location/team/league/brand | 기업과 점포/브랜드가 별개라는 관계 |
| located_in | location | location | 시설→지역. 근접 좌표만으로 동일 시설 승인 금지 |
| created_by | work | person/organization/company | 작품과 창작자 구분 |
| appears_in | person/character | work | 배우와 캐릭터 모두 별개 대상 |
| portrays | person | character | 캐릭터 이름을 배우의 공식명으로 복사 금지 |
| manufactured_by | product | company/organization | 상품과 제조 주체 구분 |
| branded_as | product/location | brand | 브랜드와 지점/상품 구분 |
| edition_of | event/work | event/work/league | 대회 회차·작품 회차. 허용 조합은 event→event/league, work→work만 |
| held_at | event | location | 개최 장소와 행사는 별개 |

predicate의 유형 조합은 통제 사전 행으로 검증한다. person/company 등 1차 코드만으로 임의 자유 관계를 승인하지 않는다.
동시 여러 소속을 현실 검증 없이 금지하지 않는다. 동일 주체·대상·predicate·직책의 중복 기간만 막는다.
정치 임기/선수 시즌 최신 여부는 원천 변경/재관측 정책으로 갱신하며 매일 새 인물을 생성하지 않는다.

## 8. 완료·검증 경계

P0.03 산출물은 이 계약과 물리 구조의 관련 컬럼 명세다. 이 계약만으로 병합/분리가 구현됐다는 뜻이 아니다.
P1은 M01~M14 중 UUID/동시성/원본 재수집/기간/분리 범위를 DB 통합 시험으로 확인해야 한다.
기존 merge 회귀 시험 재실행, 새 공통 객체 전체의 늦은 실패 rollback/복원 시험, 실제 데이터 동일인 인수는 별도다.
명확하지 않은 동일인·출처 사용권·분리 후 소유 판단은 검수자 결정 전까지 보류한다.
