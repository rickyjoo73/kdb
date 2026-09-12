# TDB 공통 UUID 연결·후보 등록 계약

0122 개발/검증 기록. 운영 반영 여부·커밋·이미지는 운영 인계 문서의 최신 항목을 따른다.
KDB 기반 공통 마스터로 점진 통합하며 TDB를 먼저 삭제하거나 세 번째 분야 DB를 만들지 않는다.

## 타입과 보존 범위

실제 TDB `place_type` 22개를 조사했다. 원본 유형은 `source_type`과
새 후보 `subtype=tdb:<원본 유형>`으로 보존한다. 단순 인물 DB로 바꾸지 않는다.

| TDB 유형 | 공통 대응 | 보존·판단 |
|---|---|---|
| person | person | 연예 전용이 아닌 공통 인물. 분야는 복수 지정 |
| organization | organization | 기존 company/team/league 대상과의 검수 연결 가능 |
| work | work | 작품·유물 원본 구분 보존 |
| food | concept | 음식·메뉴를 인물·장소로 만들지 않음 |
| festival_event | event | TDB의 지리적 속성은 별도 보존 |
| education | 추가 선택 필요 | 학교 조직과 캠퍼스 장소를 자동 동일시하지 않음 |
| other | 추가 선택 필요 | 유형 미확인을 완료로 처리하지 않음 |
| 관광지·법정동·문화시설·여행코스·레포츠·숙박·쇼핑·음식점·교통·문화유산·자연·행정구역·도로·지구·교통시설 | location | 세부 유형·좌표·주소의 원본은 TDB가 보유 |

관광 전문 속성의 복사/재사용 권리가 확인됐다는 표가 아니다. ID/연결 메타데이터와
별도로 독립 조회한 Wikidata 구조화 이름만 취급한다. AI Hub·원본 이름·좌표·주소·payload·
source config·API 키는 가져오지 않는다. 기존 TDB 조회·쓰기 경로는 바꾸지 않는다.

## 세 가지 별도 작업

1. **미검증 후보 등록**: 최신 TDB ID 관측과 독립 QID 조회로 새 공통 UUID를 만든다.
   기존 TDB UUID를 재사용하지 않고 crosswalk에 남긴다. 출생 원천은 TDB, writer는 common native다.
   원문 locale/출처 이름을 미검증으로 기록하며 동일 QID가 기존 원장에 있으면 생성하지 않는다.
   공통·기존 writer가 같은 예약 행을 사용한다. 후보 ID 예약은 정체성 승인과 다르다.
   actor/입력 hash/원천 fingerprint에 대해 멱등이고 변경된 재요청은 거절한다.
2. **동일 대상 연결 검수**: 운영자가 원본과 공통 대상을 대조하고 이름 이외의 식별 사실,
   HTTPS 근거, 사유를 기록한다. 내부 연결 증거는 `export_allowed=false`이며 표기를 승인하지 않는다.
3. **정체성·언어 검수**: 별도의 기존 Entity Resolver 화면에서 조사 후보와 선택한 실제 언어를
   검수한다. 연결 승인, 후보 등록, worker 종료를 언어 준비 완료로 표시하지 않는다.

## 동시성·철회·신선도

- SERIALIZABLE, 2초 잠금/10초 statement 제한. shadow/매핑/기존 root/공통 root/ID를 재검사한다.
- source fingerprint·generation, 매핑 revision, 대상 Entity revision을 요청에서 확인한다.
- 최신 관측 15분, 독립 조회 24시간, 동일 QID/원천 URL/원문 유형, 잠금·원장 상태를 확인한다.
- 원본 fingerprint/잠금/generation이 바뀌면 DB trigger가 이전 연결 증거를 철회하고 conflict와
  감사 이력을 남긴다. 공통 Entity나 TDB 원본은 삭제하지 않는다.
- 상세 조회에서도 대상 revision/잠금/원장 상태/ID 주장/연결 근거/신선도를 재검사한다.
  `confirmed` 저장 기록만으로 현재 유효 연결이라고 보고하지 않는다.
- 독립 원천 재확인은 5분 간격, 24시간 창 최대 3회, 회당 최대 4번 시도.
  원본 메타데이터가 만료됐으면 운영자 bridge에서 새 관측을 받아야 한다.

## 관리자 UI

- `/admin/kentity/tdb`: 원출처 비교 목록에서 연결 관리로 진입.
- `/admin/kentity/tdb/{shadowID}`: 원본/공통 UUID, 유형, 지리 속성 보존, 관측 시각,
  현재 유효성, 미검증 후보 등록, 동일 대상 연결 승인·철회·보류, 제한 재확인, 최근 30개 처리 이력.
- 기존 staff 권한/계정 해제/CSRF 검사 유지. viewer에는 쓰기 폼을 표시하지 않는다.
- 공통 언어 표의 모바일 가독성을 위해 표 내부 가로 스크롤 폭을 보장한다.
- `KDB_COMMON_ENTITY_ENABLED`, `KDB_TDB_SHADOW_ENABLED`, `KDB_TDB_MAPPING_ENABLED`가 모두 1이어야 조작 가능.

## 검증과 배포 게이트

- 격리 DB: 후보 재전송/변경 재요청, 동일 QID 경쟁 등록, 기존 writer 예약 보호,
  동시 연결 승인, 이름 승인/재사용권 비승격, 원본/대상/증거/기간 변경, 재확인 예산.
- HTTP: viewer/CSRF/해제 계정/비활성 gate, 정상 등록·멱등·승인·오래된 결정 거절·재확인.
- 최신 운영 백업을 복원해 실제 제약/trigger 아래 후보→연결→원본 잠금→철회 경로 검증.
- 모바일 390px/PC 1440px의 폼, 탐색, 권한 표시, 미검증/유효 연결 구분과 접근성 검증.
- pre-0122 백업: `backups/tdb-mapping-20260912.K2upBp/kdb.dump`, SHA256
  `c3c7838045577d3e7a5c276d88dfde32153909a33cbf17467dcf9f512f92b83b`.
- rollback은 새 gate를 끄고 writer-aware `common-fill-20260912-1` 앱으로 복귀한다.
  데이터·감사 기록·migration을 삭제하지 않는다. 원본 TDB는 변경하지 않았다.

## 다음 통합 조건

현재 원본 메타데이터 갱신은 운영자 실행형 bridge다. 상시 ID-only 재관측과
원본 삭제/합병/출처 정책 차단의 부정 관측 전파, 관광 속성별 권리 검증,
소비자 dual-read/호환 계약 및 단일 writer 전환이 남아 있다.
이 연결 관리 배포를 TDB 전체 이관 완료나 모든 표기의 검증 완료로 보고하지 않는다.
