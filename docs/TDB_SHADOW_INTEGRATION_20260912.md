# TDB → K-Entity ID 연결 비교

상태: 2026-09-12 16:14 KST 운영 검증 완료. 0120 / `08e7fa3` / `tdb-shadow-20260912-1`.
실제 ID 10건이 각각 1회 독립 조회 후 review로 종료, 이름 기록 42개. 자동 승인/병합은 0건.
목표는 KDB 기반 공통 Entity 마스터다. 세 번째 업무 DB를 새로 만드는 것이 아니다.

## 현재 구조에서 발견한 주의점

- TDB는 장소뿐 아니라 인물·기관·작품·음식 등도 같은 원장을 사용한다.
- `source_code=wikidata`라고 모든 필드가 Wikidata에서 직접 관측된 것은 아니다.
  기존 `wdInstall`은 master 좌표를 원천 레코드에 넣기도 하며, RU 연결에는 위키백과에서
  유래한 값도 들어갈 수 있다. source_code만으로 전체 payload를 CC0라고 간주하지 않는다.
- AI Hub와 파생값은 재사용 근거를 별도로 확인해야 한다. 현재 live 상태만으로
  KDB/외부 고객에 재배포할 권리가 확인됐다고 보지 않는다.
- 따라서 이번 단계는 TDB UUID/QID/연결 방식/점수/잠금만 읽는다. 원문 이름, 주소, 좌표,
  API 키, source config, payload, 운영 계정은 복사하지 않는다.

Wikidata 구조화 데이터의 CC0 범위: [Wikidata Licensing](https://www.wikidata.org/wiki/Wikidata:Licensing).
원문 사이트의 이미지·본문 및 다른 출처에서 파생한 값에 이 라이선스를 확대 적용하지 않는다.

## 실행 경로

1. KDB 운영 계정(UID 1014)이 `scripts/import-tdb-shadow.cjs`를 실행한다.
2. TDB read-only transaction에서 최대 25개(상한 100)의 활성·비병합 ID 연결을 읽는다.
   source는 live/enabled/비생성/CC0 Wikidata만 허용한다. statement timeout 5초다.
3. 기본은 dry-run이다. `--apply`일 때만 KDB의 `tdb-shadow-import --apply`에 stdin으로
   전달한다. 파일/자격 증명을 만들지 않으며 TDB에는 어떤 쓰기도 하지 않는다.
4. 두 feature gate 아래 shadow 원장에 저장한다. 동일 snapshot은 멱등이며 기존 결과를
   재실행하지 않는다. 입력이 변경되면 generation/fingerprint를 바꾸고 결과를 폐기한다.
   최신보다 오래된 snapshot은 잠금을 해제하거나 결과를 되돌릴 수 없다.
5. worker가 해당 QID의 Wikidata 구조화 이름을 **별도로** 조회한다. 타임아웃/4회 한도/
   backoff/lease/fencing을 적용하며, 24시간이 지난 ID 관측이나 잠금은 보류한다.
6. 원문 언어 그대로 미검증 proposal에 보관한다. `pt→pt-BR`, `zh→zh-Hans` 변환을 하지 않는다.
7. 기존 KDB의 동일 외부 ID 주장을 비교 자료로 표시한다. 자동 병합, 공통 master 생성,
   crosswalk 승인, 표기 ready 승격은 하지 않는다.

`--limit N --after Q...`는 외부 ID 문자열 순서의 제한된 페이지다. 출력 `next_after`가
다음 cursor이며, 전체 스냅샷/전체 이관 완료/정확한 총량을 뜻하지 않는다.
새 QID로 바뀐 연결은 이전 shadow를 지우지 않는다. 삭제/병합/잠금의 실시간 전파와
crosswalk 확정 전 최신 원본 재검사는 후속 cutover 단계의 조건이다.
현재는 운영자 실행형 bridge이며 상시 TDB 동기화 worker가 아니다.

## 데이터 / UI / 보호

- `kentity_tdb_shadows`: ID 연결 관측과 독립 출처 proposal, 상태·시도·lease.
- `kentity_tdb_shadow_events`: 최초 적재/원본 변경/조회 결과 감사 이력.
- `/admin/kentity/tdb`: 상태 필터, 잠금, TDB 원본 링크, 외부 ID 비교,
  독립 관측 시각·정확 locale·미검증 사용 상태. 빈 결과와 DB 장애를 구분한다.
- 기존 staff 인증/계정 해제 검사를 사용한다. 이 단계에는 승인/병합 POST가 없다.
- `KDB_TDB_SHADOW_ENABLED=1`와 `KDB_COMMON_ENTITY_ENABLED=1` 필요.
  앱에 Docker socket이나 TDB DB 자격 증명을 주지 않는다. bridge만 KDB 운영 계정으로 실행한다.

## 배포 / 복구 / 후속

직전 백업 `backups/tdb-shadow-20260912.KpqXHG/kdb.dump`, SHA256
`3c929fb55176fea5b02aff457052a42b1bff9cb065e66a23f3c8f8166199232c`.
격리 복원 DB에 0120을 먼저 적용하고 master 무변경/worker/기존 트리거를 검증한다.
운영에는 0120과 ledger를 하나의 transaction으로 적용한다.
이 배포에서는 위 복원 검증·원자 적용·전체 test/build/race·모바일/PC·정상 관리자 로그인을
통과했다. 실제 10건의 default dry-run은 DB에 0건을 기록했고 명시적 apply만 10건을 기록했다.
같은 페이지를 재적재하면 created=0/changed=0/unchanged=10으로 기존 결과와 시도 횟수를 보존했다.
문제 시 신규 gate를 끄거나 writer-aware 직전 common-ready 이미지로 앱만 복귀한다.
shadow/audit/기존 DB는 삭제하지 않는다.

남은 통합: 관광 전문 속성별 재사용 근거, 최신 원본 기반 crosswalk 확정/철회 UI,
타입별 확장 테이블과 공통 UUID, 소비자 dual-read 검증, 단일 writer 전환, 상시 동기화.
이 비교 단계만으로 TDB 통합이나 모든 데이터의 준비가 끝났다고 보고하지 않는다.
