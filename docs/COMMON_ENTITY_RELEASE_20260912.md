# 공통 Entity 기반과 인물 연결 검수 — 2026-09-12

14:08 KST 배포 완료: 커밋 `89ce701`, 이미지 `kdb-app:common-20260912-1`,
migration 0116, `KDB_COMMON_ENTITY_ENABLED=1`. 공통/기존 API 및 정상 관리자 로그인 화면
확인 통과. 이 배포는 아래 범위의 완료이며 전체 사업 TODO 완료가 아니다.

## 범위

Go와 기존 KDB PostgreSQL을 유지하고 공통 identity/type/domain/name/evidence/relation/
external ID/crosswalk/audit 레이어를 추가한다. 기존 UUID와 고정 언어 API는 보존한다.
별도 정치 DB를 만드는 변경이 아니다. TDB 전체 이관이나 자동 분야별 수집의 완료도 아니다.

- migration 0116: 기존 Entity 19,480건을 같은 UUID로 투영, legacy 인물 5,700건을
  미확정 연결 검수 레코드로 생성(최신 백업 시점). 원본 이름·언어·프로필은 변경하지 않는다.
- `/v1/kentity/entities`, `/{id}`: 인증된 공통 카탈로그 조회. 번역 승인 표기 API가 아니다.
- `/admin/kentity`: 분야 필터, Entity 상세/출처/쓰기 책임, 다중 분야 미검증 후보 등록.
- `/admin/kentity/mappings`: 기존 인물/동명 후보 비교, 근거 있는 수동 연결 승인/보류.
  원본·프로필 복사와 재사용 권리 승격은 하지 않는다.
- common flag=1이면 기존 PersonExtractor 이름 기반 seed/reconcile/promote와
  Sweeper 양방향 name mirror를 중지한다. UUID 기반 빈 person_details 생성/Enricher는 유지한다.
  기존 Entity 상세의 이름 기반 legacy fallback도 중지한다. 연결 승인 후 자동 복사 재개는 후속 작업이다.
- 별칭 SQL의 잘못된 `%%` 연산자를 `%`로 수정하고 조회/scan/행 순회 오류를 상위로 전달한다.
  Select와 Run이 같은 별칭/약칭/유사도 규칙으로 군집을 재구성하고 연결 요소로 중복 구성원을 제거한다.

## 안전성

후보 생성은 키+요청 해시로 중복을 막고, 동명 다른 UUID를 허용한다. 새 분야 인물을
기존 연예 전용 worker 테이블에 자동 삽입하지 않는다. 기존 데이터의 출처 라벨을 공통
검증 완료로 소급 승격하지 않는다. 각 근거는 해당 Entity에만 연결된다.

인물 연결 승인에는 운영자 권한, CSRF, HTTPS 근거 URL, 이름 외 동일인 사실, 확인 체크가
필요하다. SERIALIZABLE 트랜잭션/행 잠금으로 매핑 revision, 원본/대상 fingerprint와
잠금을 다시 검사한다. 처리 중 바뀐 기록과 중복 승인 요청은 전체 취소한다.
승인 이벤트와 내부 identity evidence는 함께 저장하되 `export_allowed=false`를 유지한다.
모든 신규 운영 폼은 CSRF 파싱 전에 16 KiB로 제한한다.

## 검증

- 전체 offline `go test ./...` 및 `go build ./...` 통과.
- common/merge/aliasmatch/PersonExtractor/Sweeper/admin/API race 통과.
- PostgreSQL 통합: 동명 복수 분야, 기존 writer 차단, 증거 다른 대상 연결 차단,
  잘못된 기간, 무근거 활성화 차단, 동시 승인 한 번만 commit, 수정된 원본/프로필 및 잠금 보호.
- HTTP: API 인증/feature gate, 관리자 조회자 쓰기 차단/CSRF/계정 철회,
  후보 중복 제출/다른 입력 재사용/큰 폼 거부, 연결 목록·상세 라우팅.
- 합성 Chromium 390px/1440px 목록/상세/빈/오류/조회자/승인 폼 전송 검사 통과.
- 직전 백업 `backups/common-entity-20260912.S3DQry/kdb.dump`,
  SHA256 `3dac245636db9e78ce56cf15109726ee3385fa4325dcfcf35c863ac430d386f6`.
  private 700/600, kdb 소유. `.env` 사본은 같은 디렉터리의 비공개 `env`.
- 이 백업은 network=none `kdb-common-verify-db`에 복원하고 migration 0116을 검증한다.
  테스트 DB 이름은 `kdb_platform_migration_test`. 운영에 테스트 인물을 등록하지 않는다.

## 배포 게이트와 한계

기능 flag `KDB_COMMON_ENTITY_ENABLED=1`은 schema 적용/검증 후 켠다. 배포 직후 API와 정상
관리자 로그인으로 목록/상세를 읽기 검증한다. 승인 POST는 격리/합성 시험에서만 실행한다.
롤백은 기존 readiness-20260912-1 이미지와 common flag=0으로 앱만 재생성하며,
additive DB 테이블은 유지한다. 운영 DB에 과거 dump를 덮어쓰지 않는다.

TDB의 AI Hub 이용 조건은 미해결이다. 전체 dump/원천 API 키 또는 생성값을 공통 자산으로
옮기지 않는다. 명시적 source policy와 shadow 비교가 통과하기 전 TDB 쓰기 책임은 TDB에 남는다.
범용 신규 Entity 발견/정체성 자동 확정, 테넌트별 표기, 기간/관계 관리 화면,
공통 DB 기반 기사 glossary와 CMS 발행 연결은 별도 TODO이며 이 배포의 완료로 표시하지 않는다.
