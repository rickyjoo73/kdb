# KDB 운영 인계 / 재개 / 복구 — 2026-09-12

## 현재 상태

### 2026-09-12 16:14 KST TDB 연결 비교 운영 갱신 (최신)

- 코드 커밋 `08e7fa39d91cc38ef542aeefeca32ac8c9bae4ca`, KDB UID 1014 계정 commit/push 및 원격 SHA 일치.
- 이미지 `kdb-app:tdb-shadow-20260912-1`, digest
  `sha256:bf456a416f11336c53f49e3fff8682b6ca10ad5f107033be9b4be469cd9e2301`.
- 시작 16:12:42 KST. API/admin health 200, healthy, 공유 IP 172.19.0.240 등 네트워크 유지.
- 0120+ledger 원자 적용. 기존 다섯 gate 및 `KDB_TDB_SHADOW_ENABLED=1`.
- 직전 백업 `backups/tdb-shadow-20260912.KpqXHG/kdb.dump`, SHA256
  `3c929fb55176fea5b02aff457052a42b1bff9cb065e66a23f3c8f8166199232c`.
  kdb 소유 700/600, `kdb-tdb-shadow-verify-db`에 복원한 후 0120/실제 트리거 하 검증 통과.
- 전체 Go test/build, 관련 race 및 8-worker 단일 claim, 최신 복원 스키마,
  모바일/PC 브라우저 검증 통과. 정확한 KDB release 소스 재검증 포함.
- 정상 운영 로그인으로 개요/워크플로우/준비 원장/공통 Entity/연결 목록·상세 및
  신규 `/admin/kentity/tdb` 모두 200/완전 HTML/조회 오류 없음 확인.
- KDB 운영 계정 bridge: 실제 ID 10개 dry-run(created=10, DB shadow=0) 후 제한 apply.
  16:14 기준 10개 모두 review / 총 10회 조회 / 독립 이름 기록 42개.
  페이지 cursor `Q100871017`. 전체 이관 수치가 아니며 추가 페이지는 자동 실행하지 않았다.
- 같은 10건 재적재는 created=0/changed=0/unchanged=10, 중복 생성/조사 재실행 없음 확인.
- TDB에 대한 쓰기 없음. 새 TDB origin 공통 master 0개, TDB crosswalk 0개, 표기 승인 0개.
  기존 TDB 이름/좌표/주소/source config/권한값을 가져오지 않았다.
- rollback: 신규 shadow gate를 끄거나 직전 writer-aware common-ready 이미지로 앱만 복귀.
  DB·shadow/audit는 보존한다. 단순 dump 덮어쓰기나 legacy writer로의 복귀는 금지한다.
- 계약/한계/bridge 명령은 `TDB_SHADOW_INTEGRATION_20260912.md` 참조.
  실제 통합과 추가 언어 자동 보충, 분야별 공식 원천/기사·발행 연결은 아직 미완료다.

### 2026-09-12 15:42 KST 공통 표기 준비 원장 운영 갱신 (최신)

- 커밋 `b885a03539b63c8440429e42ad702b1e0211a873`, KDB 계정 commit/push 확인.
- 이미지 `kdb-app:common-ready-20260912-1`, digest
  `sha256:81b7596ef954e010a2466871355ae1f8beec81bd4f93573f113c67d11878c09a`.
- migration 0119+ledger 원자 적용. 기존 네 flag 및 `KDB_COMMON_READINESS_ENABLED=1`.
- 직전 백업 `backups/common-readiness-20260912.UNW7xp/kdb.dump`, SHA256
  `a5960b33f844e540afd2fe6d77cf71cb29c9a0f25b4c5c02d00422415df201cb`.
- 정확한 KDB release 소스 전체 Go test/build, 관련 race, 최신 복원 스키마+트리거,
  공통/기존 원장·철회·범위 전환·병합 및 390px/1440px UI 검증 통과.
- API/admin health 및 정상 로그인 전체 화면/상세 200 확인.
- 실제 대한적십자사 UUID의 공통 원장 생성→조회→취소 확인.
  요청 `76a7ef1d-08f3-4d92-9833-7996a48a6a9c`는 운영자 진단이며 기사 발행 요청이 아니다.
  en/zh-Hans 모두 policy_blocked, ready=0: 미승인 후보를 완료로 오인하지 않음을 확인했다.
  신규 Entity·표기·기사 생성 없음. 진단 요청은 cancelled로 종료했다.
- 대한적십자사 조사 자체는 review / 1회 / 후보 1개 / 원천 이름 8개로 정상 종료했다.
- 계약/게이트/한계: `COMMON_READINESS_20260912.md`. 새로운 요청은 catalog=common을 명시한다.
  기존 고객 요청 의미를 소급 변경하지 않는다. native의 추가 언어 자동 보충·기사 linker·TDB 이관은 미완료.
- 다음 TDB 통합은 AI Hub 및 출처 불명 파생 필드를 제외한 source-ID shadow부터 진행한다.

### 2026-09-12 15:22 KST 쓰기 책임 전환 운영 갱신 (최신)

- 커밋 `c32ed4a1078841ec470879f0e192dea302a20d01`, KDB 계정 commit/push 확인.
- 이미지 `kdb-app:ownership-20260912-1`, digest
  `sha256:4a2cb7c1dce9d4f6e449c83509186187a4607356938e34e0aa8a6f064e2bab78`.
- migration 0118+ledger 원자 적용. 기존 세 flag 및 `KDB_ENTITY_OWNERSHIP_ENABLED=1`.
- 직전 백업 `backups/entity-ownership-20260912.3CKFTp/kdb.dump`, SHA256
  `36e233e9ad147b9477b9b9fb7717aff893e9b4785d4624b76e97f8e5a69efa72`.
- 최신 복원 DB에서 legacy UUID 19,488/19,488, ownership/승인·철회/readiness/병합 검증 통과.
  전체 Go test/build(정확한 KDB 소유 release 소스 포함), race, 390px/1440px 보호 폼 검사 통과.
- 정상 관리자 로그인으로 개요/워크플로우/준비 요청/공통 Entity/연결 검수 목록·상세 200 확인.
- 실제 사회 표본 대한적십자사는 기존 UUID 그대로 공통 candidate로 전환했다.
  원래 출처 kdb / 현재 writer native / 분야 society / legacy 상태 rejected 유지.
  새로운 외국어 표기·정체성 승인이나 기사 발행은 하지 않았다. 자동 조사 job만 생성했다.
  정상 로그인+CSRF와 실제 기각 사유/현재 revision·fingerprint를 사용했다. 결정 사유에 AI 보조 검토 명시.
- 정치/경제/스포츠 앞선 표본 조사: 각각 후보 5/2/1개, 수집한 이름 기록 25/14/10개.
  3건 모두 review이며 후보 정체성·언어 표기 자동 승격은 0건이다.
- 롤백은 ownership gate를 끄고 writer-aware 앱을 유지한다. 이미 전환된 UUID가 있으므로
  writer를 모르는 구버전으로 무조건 복귀하지 않는다. 상세: `ENTITY_OWNERSHIP_20260912.md`.
- 다음: 공통 승인 표기를 요청 언어별 원장에 연결, TDB source policy/shadow 이관,
  분야별 근거 확장/전체 UI/기사 연결. 전체 TODO 미완료.

### 2026-09-12 14:49 KST 자동 조사·검수 저장 운영 갱신 (최신)

- 커밋 `48827b3ac128c0af7b682b92920876bb4ac56796`, kdb UID/GID로 commit/push 확인.
- 이미지 `kdb-app:resolver-20260912-1`, digest
  `sha256:3eea67d881a21c25f1c8250f4da2dc2bdb8eca380f3b8f439939ea4037b59810`.
- migration 0117+ledger 원자 적용. common/readiness/resolver flag 모두 1.
- API/admin health, 공유 IP 유지. 정상 관리자 로그인으로 기존/공통/연결 검수 목록·상세 HTTP 200 확인.
- 직전 백업 `backups/entity-resolver-20260912.L8Qk4k/kdb.dump`, SHA256
  `f5ef60ed5791d32557fd516413df7c09f6de06b4d79830406b54c520852733a9`.
  최신 백업을 `kdb-resolver-verify-db`에 복원하고 0117 및 공통/승인·철회/기존 보충·병합 시험 통과.
- 실제 관리자 로그인+CSRF로 공개명 후보 3개만 등록(승인/병합/발행 없음):
  정치 김대중 `76696193-70e7-475d-920b-1aac32e67f41`,
  경제 정주영 `59b8709a-9fd1-4f92-b544-65f9370f9fc4`,
  스포츠 양용은 `8c2a39e2-a6b5-473b-b032-5b600b7a390e`.
- 사회 표본 대한적십자사는 기존 기각 레코드가 있어 보수적으로 건너뜀.
  기존 UUID `7478b5bf-15c7-4fb8-a6b7-6be9eb2d5b8e`, legacy agency/rejected.
  사유는 실재하지 않아서가 아니라 "K-엔터테인먼트 엔티티가 아님"이다.
  **기존 용도 제한에 따른 기각과 공통 정체성의 유효성을 분리하는 후속 전환이 필요하다.**
  새 UUID를 중복 생성하거나 기존 기각 기록을 임의 해제하지 않았다.
- 조사/승인/원천 권리/rollback/한계는 `ENTITY_RESOLVER_20260912.md` 참조.
  자동 조사와 운영자 검수 저장의 배포를 전체 플랫폼 완료로 보고하지 않는다.

### 2026-09-12 14:08 KST 공통 Entity 운영 갱신 (최신)

- 실행 커밋 `89ce7017e8efbee6d6fb167fe9522bb8331f065d`, KDB 소유 저장소에서 kdb 계정으로 commit/push.
- 이미지 `kdb-app:common-20260912-1`, digest
  `sha256:0e3237d229f4bfda55ae1a1f6efdc88ee99a845ea0d29765c21ba6e068848487`.
- migration 0116과 ledger를 같은 트랜잭션으로 적용. common/readiness flag 모두 1.
  기존 Entity 19,480/19,480 UUID 유지; legacy 인물 5,700개는 모두 review이며 자동 승인/병합 없음.
- `/v1/health`, 관리자 health 정상, 기존 공유 IP 유지. 인증된 공통 목록·상세 API HTTP 200.
- 정상 관리자 로그인으로 기존 개요/워크플로우/준비 원장과 공통 목록/상세/연결 검수 목록/상세
  HTTP 200, 전체 HTML 및 조회 오류 없음 확인. 운영 승인 POST/합성 인물 삽입은 하지 않았다.
- 최신 백업 `backups/common-entity-20260912.S3DQry/kdb.dump`, SHA256
  `3dac245636db9e78ce56cf15109726ee3385fa4325dcfcf35c863ac430d386f6`.
  이 백업을 `kdb-common-verify-db`에 복원, 0116 및 실제 트리거 하 공통/보충/병합 테스트 통과.
- 이름 기반 legacy 자동 복사/승격 경로를 차단했다. 검수 승인 후 UUID별 복사를 재개하는
  기능은 아직 없다. 기존 프로필 원본과 UUID별 Enricher는 보존한다.
- rollback: readiness-20260912-1 이미지/빌드 버전, common flag=0으로 앱만 재생성한다.
  migration은 additive이므로 유지한다. dump를 운영에 덮어쓰지 않는다.
- 전체 통합 완료가 아니다. `COMMON_ENTITY_RELEASE_20260912.md` 및 전체 TODO의 미완료 범위 참조.
  이후 개발은 이 운영 버전과 분리해서 기록한다.

### 2026-09-12 13:21 KST 운영 갱신 (아래 이전 기록보다 우선)

- 요청 원장/보충/UI와 병합 보호: `b0286e06bbd598caa12b706958192a178d49f1b6`,
  KDB 소유 개발 저장소에서 kdb UID/GID로 commit/push 확인.
- 운영 이미지 `kdb-app:readiness-20260912-1`,
  digest `sha256:c5dfb9dcc5cdf405015dfbed4cafeee56841d29fdca18c2b8e9be5c3a57c124f`.
- migration 0115 적용과 kdb_schema_migrations 기록 완료. `KDB_READINESS_ENABLED=1`.
- API/admin health 정상, 공유 IP 172.19.0.240 유지. 기존 Entity를 사용하는
  운영자 진단 요청 ready→조회→cancelled 확인(새 Entity/번역/발행 생성 없음).
- 실제 관리자 정상 로그인으로 /admin/, /admin/workflow, /admin/preparations,
  새 요청 상세 모두 HTTP 200/완전 렌더/집계 오류 없음 확인. 세션 위조 없음.
- 직전 백업: `/data/home2/kdb.aiinplanet.com/backups/kentity-20260912.ohfsZs/kdb.dump`,
  SHA256 `172602f681aa4f9f85f824f2e2dc6bc6d93d747feb1043bc0874bda77e8d2544`.
  같은 비공개 디렉터리의 `env`는 배포 전 설정 백업(둘 다 600).
- 백업을 `kdb-readiness-verify-db`의 격리 `kdb_platform_migration_test`에 복원,
  0115 적용 후 실제 enum/제약/트리거에서 보충·병합 테스트 재통과.
- rollback: 이미지/빌드 버전을 workflow-20260912-3으로 되돌리고 readiness 설정을 0으로
  한 뒤 기존 두 compose 파일로 앱만 재생성한다. 추가 테이블은 남겨도 구버전과 호환된다.
  DB dump를 운영에 덮어써 정상 사용자 변경을 잃게 하지 않는다.
- **운영 체크아웃의 모든 소스를 최신 branch로 강제 교체하지 않았다.** 실행 코드의 정본은
  위 커밋의 kdb 소유 release 작업본과 immutable 이미지다. main 자동 배포 전에 운영의
  기존 dirty 소스와 후속 branch를 대조해야 하며 git reset/무조건 pull을 사용하지 않는다.
- 이후 공통 Entity/반복 매칭 후속 수정은 별도 검증 중이며 이 배포에 포함하지 않는다.

- 후속 Git 기록: `8caade3bdf0d9847c95703a0534b7630afebfe9c`가
  `rickyjoo73/kdb`의 `feat/kentity-platform-20260912`에 push 됨. main/운영 배포와 별도.
- 이후 Git 작업은 Linux kdb UID/GID 1014로 실행한다. GitHub 인증은 기존 KDB의
  `rickyjoo73` 자격을 사용하며 비밀값은 복사·기록하지 않는다.
- kdb 소유 개발 저장소: `/data/home2/kdb.aiinplanet.com/.worktrees/kentity-platform-20260912`.
  미커밋 작업 복사본과 정확히 대조 후 승격한다. `.worktrees/`는 Git/이미지에서 제외한다.
- 요청 언어별 준비 기능 후속 구현은 `REQUESTED_LOCALE_READINESS.md` 참조.
  migration 0115는 격리 테스트에만 적용된 상태이며 아래 운영 기준은 아직 바뀌지 않았다.

- 2026-09-12 오전 추가 작업: 병합 안전성 보강은 작업 복사본에만 있다. 운영에는 미반영.
  `MERGE_SAFETY_20260912.md`와 전체 TODO 최신 기록을 먼저 읽는다.
- 운영 소스: `/data/home2/kdb.aiinplanet.com` (소유자 kdb, UID/GID 1014).
- 작업 복사본: `/home/tdb.aiinplanet.com/.worktrees/kdb-workflow-fix`.
- 현재 이미지: `kdb-app:workflow-20260912-3`.
- 이미지 digest: `sha256:8f93d91de3d6b54af006cafb92fd1c61f64495e8e80dc88ef3fae4c407f4209a`.
- 배포 전 이미지: `kdb-app:ci-20260901-3f8af1b` (보존).
- 1차 UI 이미지 `workflow-20260912-2`도 보존.
- 코드 기준 커밋: `3f8af1b91403b53cb419f06dd37f0780ae3534b7`.
  이것은 운영 기준 커밋이다. 후속 작업 브랜치 커밋/push는 위 최신 기록과 구분한다.
- 적용한 스키마 migration 없음. API/Entity ID/외부 모델/출처 정책을 변경하지 않았다.
- API 9100, 관리자 9101, 공유망 IP `172.19.0.240` 유지.

## 백업과 실제 복원 검증

- 백업 파일:
  `/home/tdb.aiinplanet.com/.kdb-backup-20260912.czldwn/kdb-pre-repair.dump`.
- 크기 89,741,469 bytes. 디렉터리 700 / 파일 600.
- SHA256: `a62db81052f2345d4d31501892c9e3f0d8c7eefe52db441213de8a3b812d979f`.
- 운영 DB 컨테이너 내 생성본 `/tmp/kdb-pre-repair-20260912.dump`도 동일 checksum으로 보존.
- 설정 백업: `/data/home2/kdb.aiinplanet.com/.env.pre-workflow-20260912` (600).
  비밀값이 들어 있으므로 내용을 로그나 답변에 출력하지 않는다.
- PostgreSQL 16의 격리 DB `kdb_workflow_restore_test`에
  `pg_restore --exit-on-error --no-owner --no-privileges` 성공.
- 복원 당시 public 39개 테이블, entities 19,452 / active 12,644 / legacy persons 5,698 /
  research requests 26,513 / external refs 13,583 확인.
- 복원 DB 대상으로 실제 dashboard/workflow 핸들러를 default_transaction_read_only=on으로 검증.
- 백업은 관리 계정 등 민감한 데이터도 포함한다. 공유하거나 웹 공개 디렉터리로 옮기지 않는다.

## 검증 결과

1. 격리 통합 DB 이름 `kdb_workflow_test`만 허용하는 테스트 helper.
2. `KDB_SKIP_LIVE=1 go test ./... -count=1`, `go build ./...` 통과.
3. disambiguator/enricher/autopilot/admin race 통과; SQL 성능 후속 후 admin race 재통과.
4. 외부 Wikidata 테스트 별도 네트워크에서 통과 (첫 수정 검증).
5. Playwright 1.48/Chromium 390px·1440px의 합성 화면/검색/메뉴/갱신 테스트 통과.
6. 복원 DB의 개요 약 556ms / workflow 약 1,997ms.
7. 운영 HTTPS 02:13: 개요 258ms / workflow 1,954·1,916·1,948ms / 동명이인 331ms.
8. API/admin health 정상; 초기 Enricher 7항목 정상 종료, 실제 변경 0.

작업 종료/선정 건수가 실제 준비 완료 또는 채움 성공이라는 뜻은 아니다.

## 재개 순서

1. `PRESSLOCALE_EXECUTION_PLAN.md`의 최신 실행 기록과 미완료 체크박스 확인.
2. 운영 이미지/health와 git diff부터 확인. 기존 사용자 수정은 보존한다.
3. 다음 정규 Disambiguator와 별칭 정리의 결과/타임스탬프를 읽기 전용으로 확인한다.
   새 근거가 없는데 같은 라벨/별칭의 변경 이벤트가 반복되는지 비교한다.
4. 24h 후 변경 이벤트 수뿐 아니라 고유 ID 수·실제 저장 값·noop/오류를 비교한다.
5. 그다음 실제 readiness 원장 설계/테스트로 진행한다.
   오래된 요청의 requested_locales/resolved_id/ready_at을 추정해서 채우지 않는다.
6. 분야 확장/대량 보충/이관은 각 단계의 근거와 백업 게이트를 다시 통과한다.

## 테스트 재실행

호스트에 Go가 없어 Docker `golang:1.23-bookworm`을 사용했다.
테스트 컨테이너는 모두 외부 네트워크/호스트 공개 포트가 없는 전용 환경이다.

- 통합 DB 컨테이너: `kdb-workflow-test-db`.
- 복원 DB 컨테이너: `kdb-workflow-restore-db`.
- 중지된 경우 위 이름만 확인하고 시작한다. 다른 사이트 컨테이너를 조작하지 않는다.
- 모듈 cache `kdb-platform_go-mod-cache`, 별도 build cache `kdb-workflow-test-build`.
- 테스트 컨테이너 네트워크를 공유할 때에만 localhost DB URL이 해당 격리 DB를 가리킨다.
- 복원 테스트 env는 `KDB_RESTORE_TEST_DATABASE_URL`,
  함수는 `TestAdminAgainstRestoredSnapshot`. DB 이름이 다르면 테스트가 거부한다.
- 합성 브라우저 산출물은 `/tmp/kdb-admin-ui.WGQOPN`; 테스트 코드로 재생성할 수 있다.

## 앱 이미지 rollback 절차

이 절차는 코드/UI 장애 복구용이다. DB dump를 운영 DB에 덮어쓰는 절차가 아니다.

1. 현재 이미지·health·진행 중 요청을 읽어서 대상이 kdb-app인지 확인한다.
2. kdb 소유자로 `apply_patch`를 사용하여 `.env`의 아래 **두 키만** 변경한다.
   - `KDB_APP_IMAGE=kdb-app:ci-20260901-3f8af1b`
   - `KDB_BUILD_VERSION=ci-20260901-3f8af1b`
3. 기존 **두 compose 파일**을 모두 사용한다. override의 고정 IP와 인증 마운트를 보존한다.

```sh
docker compose -f docker-compose.kdb.yml -f docker-compose.override.yml up -d --no-deps --no-build kdb-app
```

4. `http://127.0.0.1:9100/v1/health`와 `http://127.0.0.1:9101/healthz`,
   정상 로그인한 외부 관리자 화면을 확인한다.
5. rollback 이유/시각/이미지를 실행 계획에 기록한다. 소스는 자동 되돌아가지 않는다.
   소스 복구가 필요하면 겹치는 변경을 검토한 별도 patch를 쓴다. git reset/checkout 강제 복구 금지.

배포 때는 Docker helper를 UID/GID 1014와 docker 그룹 999로 실행했다.
sudo, 시스템 권한 변경, 비밀번호 재설정은 하지 않았다.
`scripts/promote-workflow-repair.js`는 배포 전 기준점에서 단 한 번 적용한 도구다.
현재 운영 트리에는 이미 수정이 있으므로 **그대로 재실행하면 안 된다**.

## 아직 완료되지 않은 것

실제 준비 SLA, 검증 근거별 자동 보충 확대, 동명이인 merge 트랜잭션/잠금 보호,
공통 정규화 Entity 마스터, 레거시 인물 UUID 전환, TDB 이관,
정치/경제/스포츠 source adapter, PressLocale/Gemma/CMS end-to-end,
통합 검수 UI/서버 측 세분화 권한/장기 부하 시험.

세션이 끝난 뒤에도 별도 에이전트가 자동 작업 중이라고 가정하지 않는다.
코드·테스트·체크리스트로 작업 재개 지점을 명확히 유지한다.
