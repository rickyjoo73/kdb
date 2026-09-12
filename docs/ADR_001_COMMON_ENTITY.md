# ADR 001 — KDB 안의 공통 Entity 마스터와 단계적 쓰기 전환

2026-09-12. 상태: 설계 확정, 구현/검증 단계. TDB 전체 이관 완료가 아님.

## 확인한 제약

- KDB kwave_entities는 UUID, 고정 locale 컬럼, 연예 전용 enum/worker를 사용한다.
- kwave_persons는 이름 UNIQUE이며 현재 5,700개 UUID가 Entity UUID와 전혀 겹치지 않는다.
  같은 이름의 활성·후보 Entity가 여럿인 레거시 이름 3개를 확인했다(시점 관측).
- TDB는 이름이 아닌 UUID, 행 기반 이름, 좌표/주소, generated/source policy를 갖고 있다.
  이용 조건 미결인 aihub_tour를 공유 자산으로 자동 내보내지 않는다.

## 선택

별도 정치 DB를 만들지 않고 KDB PostgreSQL 안에 정규화된 공통 Entity 레이어를 추가한다.
기존 UUID를 유지하고, 신규 일반 분야는 연예 전용 worker의 kwave_entities 선정 대상에
즉시 넣지 않는다. 운영 중인 고정 locale API는 그대로 유지한다.

- `kentity_entities`: 공통 UUID, 유형, 대표명, 상태, 쓰기 원천, 버전/잠금.
- `kentity_entity_domains`: 복수 분야. 분야는 사람의 정체성이나 단일 직업이 아니다.
- `kentity_names`: 행 기반 이름/별칭/음역, locale, 출처, 검증/생성 구분, 유효 기간.
- `kentity_relations`: 사람→기관/팀/직책 등 기간 있는 관계와 근거.
- `kentity_evidence`: 출처 URL/원천 레코드/권리 정책/근거 관측. 비밀키를 넣지 않는다.
- `kentity_external_ids`: 외부 식별자 주장과 검증 상태. 충돌은 검수하며 자동 병합하지 않는다.
- `kentity_crosswalks`: source system+기존 ID→공통 UUID. 이름은 매핑 키가 아니다.
- `kentity_audit_events`: 변경자, 이유, 전후 값, 버전.

## 단일 쓰기 책임

0118 보완: 최초 출처와 현재 쓰기 책임은 다를 수 있다. `origin_system`은 불변이고
`write_owner`로 현재 writer를 구분한다. 연예 범위 밖이라는 이유로 기각된 기존 UUID를
검토 후 공통 candidate로 넘길 수 있으며, 기존 KDB 레코드는 그대로 보존한다.
정확한 조건·잠금·롤백은 [쓰기 책임 전환](ENTITY_OWNERSHIP_20260912.md)을 따른다.
아래 기존 KDB writer 설명은 **아직 전환하지 않은 행**에 적용한다.

전환기 KDB 기존 행의 정체성·고정 언어값은 **기존 KDB가 쓴다**. 공통 정체성은 DB 트리거로
동일 UUID에 투영하고, 공통 이름 조회는 레거시 컬럼을 읽는 view로 제공하여 이중 쓰기하지 않는다.
공통 레이어의 새 분야·관계는 별도 행으로 관리하며 기존 worker가 덮어쓰지 않는다.
신규 native Entity의 이름/상태는 공통 서비스만 쓴다. 새 데이터는 처음에 candidate다.
TDB는 검증 전까지 장소/이름의 쓰기 책임을 유지한다. 이관 adapter는 허용된 필드와
출처만 snapshot/crosswalk로 수신한다. 최종 쓰기 전환은 shadow 대조와 고객 전환 후 한다.

같은 UUID에 서로 다른 원천이 들어오면 실패시킨다. 동일 이름이나 동일 분야만으로
기존 ID를 재사용하지 않는다. 정치인이자 운동선수인 한 사람은 분야/관계가 여러 개인 한 Entity다.

## 안전성 / 이관 게이트

legacy 인물 연결은 review→confirmed/rejected 상태와 근거를 남긴다. 이름 일치만으로 confirmed를
만들지 않는다. ID별 person_details를 우선하며, 명시적 연결 없는 legacy profile 복사를 제거한다.
초기 공통 기능 활성화 시 기존 PersonExtractor의 이름 기반 선정/승격/복사와 Sweeper의
이름 기반 양방향 mirror를 중지한다. UUID별 빈 person_details 생성과 기존 Enricher는 유지한다.
`/admin/kentity/mappings`에서 기존 ID와 모든 이름 일치 후보의 프로필/출처를 비교한다.
운영자만 명시적 동일인 근거·HTTPS URL·확인 사실을 남겨 승인/보류할 수 있다.
SERIALIZABLE 트랜잭션에서 매핑 revision, source fingerprint, 대상 profile fingerprint,
양쪽 잠금을 재검사한다. 동시 결정은 한 번만 commit하고 감사 이벤트를 함께 저장한다.
승인 근거는 내부 정체성 연결에만 사용하며 export_allowed=false, license=unreviewed를 유지한다.
이름 일치만으로 자동 승인하지 않으며 승인 버튼도 프로필/표기를 복사하지 않는다.
레거시 프로필 자체를 삭제하지 않으며 기존 인물 화면은 보존한다. 검증된 UUID 매핑으로
프로필 보충을 재개하는 경로는 별도 후속 구현이다. 이 기능 제한을 통합 완료로 표시하지 않는다.
KDB 기존 전체 데이터를 "검증 완료"로 소급 승격하지 않는다. 기존 출처 라벨은 기록의 provenance다.
공통 이름이 native/evidenced인지, generated인지, 아직 unverified인지 별도로 표시한다.
TDB의 rule/llm/translated/no_form/blocked 정책을 common ready로 단순 변환하지 않는다.

스키마 복원→새 구조의 동명/기간/출처/취소/권한 테스트→제한 반영→shadow 비교를 통과하기 전
구형 테이블/API를 삭제하지 않는다. rollback은 신규 기능을 끄고 기존 쓰기 경로를 유지한다.
