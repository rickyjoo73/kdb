# 요청 언어별 준비 상태 — native-evidence-v1

2026-09-12. 이 문서의 기능은 신규 migration 0115와 opt-in 설정이 필요하다.
기존 `/v1/prepare`의 응답 의미는 바꾸지 않는다. 작업 종료나 영어 fallback을 요청한
다른 언어의 준비 완료로 세지 않는 별도 원장이다. 범용 신규 Entity 발견 기능은 별도 단계다.

## 계약

- `POST /v1/preparations`: terms(ko/type/entity_id/context), locales, source_url,
  article_id/article_version. terms 1–200, 기본 요청 언어 en. Idempotency-Key 사용 권장.
- `GET /v1/preparations/{id}`: 인증한 요청 소유자만 조회. 타 고객 요청은 404.
- `POST /v1/preparations/{id}/cancel`: revision, reason 필수. 오래된 revision은 409.
- 기존 prepare는 설정 활성화 시 preparation_id/tracking_status만 추가한다.
  기존 legacy ready와 원장 ready는 다를 수 있다. 저장 실패는 unavailable로 표시한다.
- `KDB_READINESS_ENABLED=1`에서 수신/조회 및 통합 cmd/kdb worker를 활성화한다.
  기능 비활성·스키마 장애는 새 API에서 503. 별도 kdb-api 프로세스에는 worker가 없다.
- ko, en, ja, zh/zh_hant 등에서 ko는 입력 이름이며, 추적 대상 외국어는
  en/ja/zh/zh_hant/vi/id/es/pt_br이다. zh-Hans, zh-Hant, pt-BR 별칭을 정규화한다.

## 판정과 보호

- 요청한 실제 locale 값 + 인정된 해당 필드 출처 + 문자권 검사에 통과해야 ready.
  기존 출처 라벨의 정확성을 소급 재검증한 것은 아니며 공식 인증으로 과장하지 않는다.
- 첫 ready 시각은 원장에서 관측한 최초 시각이며 과거 DB 저장 시각을 추정하지 않는다.
- 같은 이름의 복수 후보는 ambiguous. 처음 연결한 ID가 다른 동명이인으로 자동 바뀌지 않는다.
- 기존 비어 있지 않은 표기, 운영자 잠금, 미확정 정체성은 자동 덮어쓰지 않는다.
- 이미 연결된 Wikidata QID만 조회한다. 이름 검색으로 QID를 임의 채택하지 않는다.
  반환 QID·한국어 이름/별칭·이름 요소 제외·문자권을 검사하고 출처와 함께 저장한다.
- 조회 중 잠금/식별자/표기가 바뀌면 오래된 결과 폐기. request → job → entity 순서 잠금,
  lease generation/token, SERIALIZABLE 트랜잭션, 실제 child identity row 잠금 적용.
- 다른 유효 요청이 공유하는 보충 job은 요청 하나 취소만으로 제거하지 않는다.
- 입력/정책 fingerprint별 최대 4회 자동 호출. 오류는 1/2/4분, 근거 없음은 7일 간격.
  취소 후 재요청은 기존 예산을 유지한다. 한 번 저장 후 철회된 값은 근거 검수로 보낸다.
- 수동 재시도는 운영자 사유·revision·잠금/정체성 재검사·감사 이벤트 필요.
  최소 5분 간격, 최근 수동 시도 후 24시간이 지나야 한도가 초기화되는 보수적 3회 제한.
  이는 사용량 결제 기능이 아니라 외부 조회 비용 폭증 방지 한도다.

## 관리자

`/admin/preparations`: 최근 100개 요청, 상태 필터, 요청 항목×언어 준비 수.
상세는 native 표기와 영어 대체값을 구분하고 근거/보류 사유/관측 시각/최근 이력을 표시한다.
새 원장 경로는 매 요청 DB에서 계정 활성/역할을 확인한다. viewer/reviewer 조회,
operator/admin 제한 재시도. CSRF와 서버 권한을 함께 검사한다.
기존 전체 관리자 경로의 세분화 권한 전환까지 끝났다는 뜻은 아니다.

## 검증과 배포 게이트

- 격리 PostgreSQL: 중복 요청·고객 격리·fallback·동명이인·ID 고정·언어 독립 보충,
  오류/근거 부재 구분·잠금/식별자 변경·취소·lease fencing·중간 실패 rollback·재시도 한도.
- 전체 offline Go 테스트/build, 관련 readiness/disambiguator/admin/API race 검사 통과.
- 390px/1440px 합성 브라우저: 목록/상세/빈/오류/조회자 화면, 탐색 및 재시도 form 검사 통과.
- 운영 스냅샷 복사 DB `kdb_platform_migration_test`에서 0115 적용 성공.
  실제 enum/유일성 제약/입력 해시 트리거를 유지한 준비·보충·병합 테스트 통과.
- 운영 반영 전 최신 백업/복원, migration ledger 대조, 이미지 rollback 확보가 별도 필요.
  동시성 검증과 복원 테스트 통과를 실제 운영 장기 관찰이나 부하 SLO 통과로 간주하지 않는다.

## 남은 범위

미등록 Entity의 정체성 발굴·공통 Entity 마스터·분야별 출처·TDB 매핑·기사 glossary,
전체 관리자 RBAC·대규모 부하/공정성·PressLocale 연결은 실행 계획의 후속 단계다.
