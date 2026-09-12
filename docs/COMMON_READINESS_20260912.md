# 공통 Entity의 실제 요청 언어 준비 원장

상태: 2026-09-12 15:42 KST 운영 반영. `b885a03`, `kdb-app:common-ready-20260912-1`, 0119.
정상 운영 로그인과 실제 후보 UUID의 요청 생성→조회→취소 확인. 미검증 후보 ready=0 유지.

## API 계약

기존 `POST /v1/preparations`에서 `catalog`를 생략하면 기존 정책을 유지한다.
새 공통 정책은 명시적으로 `"catalog":"common"`을 요청한다.

```json
{
  "catalog": "common",
  "terms": [{"ko": "검수한 한국어 명칭", "type": "organization", "entity_id": "검수한 Entity UUID"}],
  "locales": ["en", "ja", "zh-Hans", "zh-Hant", "pt-BR"],
  "article_id": "고객 기사 ID",
  "article_version": "원문 버전"
}
```

`common-reviewed-names-v1` 정책을 원장·멱등키 payload hash·근거 snapshot에 고정한다.
기존 고객 격리, 취소, 기사 버전, 최초 준비 관측 이력을 재사용한다. 기존 요청을 새 정책으로
소급 변경하거나 동일 멱등키로 다른 정책 요청을 덮어쓰지 않는다.

## 실제 준비 조건

- 명시된 UUID, 일치하는 한국어 명칭(또는 현재 검증된 한국어 별칭), 일치하는 유형.
- common writer, active 정체성, 재사용 가능한 verified identity evidence, 공통 잠금 없음.
- 정확한 locale의 현재 유효한 canonical/recorded/verified 이름이 정확히 1개.
- 해당 이름의 근거가 같은 Entity에 속하고 verified/export_allowed이며, 사용권 미검토가 아님.
- HTTPS 출처와 해당 locale의 문자 검사를 통과해야 한다.
- 이름 ID·버전, Entity ID·버전, 정체성 및 이름 근거 ID·출처·권리 정책을 `proof`에 기록한다.

`pt`≠`pt-BR`, `zh`≠`zh-Hans`≠`zh-Hant`≠`zh-TW`다. 원천에 일반 언어 label만 있으면
해당 지역/문자별 요청은 미준비다. 영어 대체값은 별도 필드이며 준비 수에 포함하지 않는다.
정체성 UUID를 주지 않은 이름 검색은 후보 탐색일 뿐이다. 결과가 1개여도 자동 정체성 확정으로
세지 않는다. 기사 문맥 기반 UUID 연결은 후속 linker가 별도로 책임진다.

기간은 DB의 현재 날짜(운영 UTC) 기준이다. 과거 기사 시점의 직책/기관 명칭 선택까지 지원한다는
뜻은 아니다. 기간이 겹치는 복수 승인 대표명이 있으면 검수로 보류한다. 생성·번역·미검증·만료
표기는 ready가 아니다. 기존 wide-column 표기도 공통 근거 검수로 자동 승격하지 않는다.

## 일관성과 취소

공통 요청 GET은 SERIALIZABLE 트랜잭션 안에서 현재 Entity/이름/근거를 다시 관측한다.
이전에 ready였어도 근거 철회/사용권 회수/잠금/기간 만료 시 값과 proof를 제외한다.
최초 ready 시각은 역사로 보존하고, 현재 ready 시각은 현재 입력 버전 기준으로 관리한다.
재검증 시각은 매번 갱신하며 변경 판정 이력에는 실제 DB 저장 시각을 기록한다.
동시 수정으로 직렬화 충돌이 나면 오래된 값을 대신 반환하지 않고 오류로 재시도를 요구한다.

반환값은 해당 트랜잭션의 관측이며 미래 발행까지 유효하다는 보장이 아니다. Glossary/발행자는
사용 전 다시 검사하고 버전을 고정해야 한다. 그 발행 경로의 구현은 별도 TODO다.
취소는 현재 표기·proof를 제거하고 historical first_ready_at 및 감사 이력을 보존한다.
빈 `value`/`source`/`fallback_value` 및 `ready_at: null`은 JSON에도 명시한다.
필드 생략 때문에 클라이언트의 재사용 객체에 철회된 값이 남는 것을 방지한다.

구형 언어 보충 worker는 공통 writer로 전환된 UUID를 덮어쓸 수 없다. 공통 조회에서 없는
언어를 구형 filler에 넘기지도 않는다. 공통 승인 뒤 추가 언어 근거를 자동 보충하는 전문 경로는
아직 미완료다. 기존 범용 Entity 조사 결과/검수 화면에서 얻은 표기를 승인하면 이 원장에 반영된다.

## UI와 배포

기존 요청 상세에 공통 정책 설명, 공통 Entity 상세 링크, 사용한 근거 버전 표시를 제공한다.
작동하지 않는 구형 filler 재시도 폼은 공통 요청에 노출하지 않는다. 오류를 빈 DB로 표시하지 않는다.

게이트 `KDB_COMMON_READINESS_ENABLED=1` 및 `KDB_COMMON_ENTITY_ENABLED=1` 필요.
신규 gate가 꺼져 있으면 공통 생성/조회는 503이며, 구형 요청 생성/조회는 유지된다.
0119는 proof 컬럼을 추가하며, 구형 앱/요청에 대한 삭제나 데이터 변환은 하지 않는다.
롤백 시 신규 gate를 끄고 writer-aware 직전 이미지를 유지한다. 기존 DB나 이력을 덮어쓰지 않는다.

최신 배포 전 백업: `backups/common-readiness-20260912.UNW7xp/kdb.dump`, kdb 소유 700/600.
SHA256 `a5960b33f844e540afd2fe6d77cf71cb29c9a0f25b4c5c02d00422415df201cb`.
격리 `kdb-common-ready-verify-db`에 복원하고 0119 및 common/ownership/승인·철회/기존 준비/병합 검증 통과.
복원 당시 기존 KDB 19,491개 UUID 보존. 전체 Go/build, 관련 race, 390px/1440px UI 검사 통과.
