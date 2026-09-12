# 분야 공통 자동 조사와 검수 저장

2026-09-12. migration 0117. 승인 전 조사 자료와 실제 승인 표기를 분리한다.

14:49 KST 운영 반영: `48827b3`, `resolver-20260912-1`, resolver flag=1.
정상 관리자 로그인 화면 검증 후 공개명 후보 3개(정치/경제/스포츠)를 등록했다.
사회 표본은 기존 기각 레코드와 이름이 같아 중복 생성하지 않았다. 자세한 UUID/사유는 운영 인계 참조.
운영에서 임의의 동일인/표기 승인은 실행하지 않았다.

## 동작

- common/resolver flag가 모두 켜진 서버에서 신규 분야 후보를 등록하면 같은 트랜잭션으로
  조사 job을 만든다. 기존 native 후보는 상세 화면에서 한 번 요청할 수 있다.
- 30초 주기의 단일 worker가 lease로 한 작업을 가져간다. 최대 5개 Wikidata 검색 결과를
  다시 조회하며 연예 전용 필터는 사용하지 않는다. 이름 요소/동음이의 페이지, 다른 QID,
  이름·별칭 불일치, 인물/비인물 불일치는 걸러낸다. 비인물의 상세 유형·분야는 검수 사항이다.
- 개별 원천 요청 12초, 처리 주기 80초, lease 120초. 오류 재시도는 1/2/4분, 최대 4회.
  HTTP 공용 클라이언트의 내부 2회 재시도를 포함하면 한 job의 상한은 최대 48 GET이다.
- 검색 결과가 없어도 '실재하지 않는 대상' 또는 '공식 표기 없음'이라고 기록하지 않는다.
  검색 후보가 하나여도 동일인으로 자동 승인하지 않는다. 새 기사에서의 NER 추출은 별도다.
- 조사 시각, 원천 URL, 원문 라벨/원천 locale, 기존 DB의 외부 ID 주장 목록을 기록한다.
  `pt`를 `pt-BR`, `zh`를 `zh-Hans`로 바꾸어 원천에 기록됐다고 주장하지 않는다.
  기존 KDB 클라이언트의 locale 접기/표기 정리는 호환성을 위해 유지하되 공통 조사는 raw 필드를 쓴다.
- 처리 완료 시 UUID/revision/유형/이름/잠금과 lease token/generation/만료를 다시 확인한다.
  취소나 입력 변경 뒤 도착한 결과는 저장하지 않는다. 마지막 시도 중 worker가 죽어도
  lease 만료 후 terminal failed로 정리하며 무한 running/재시도를 만들지 않는다.

## 검수 승인과 안전성

운영자는 원천과 동명 후보를 비교하고, 이름 외 동일인 확인 사실과 사유를 남기며 사용할
언어만 선택한다. 승인 입력에 이름값·출처 URL을 받지 않고 서버의 조사 snapshot만 사용한다.
SERIALIZABLE 트랜잭션에서 root→job→외부 ID 예약 순서로 확인 후 아래를 함께 commit한다.

1. 정체성 evidence와 원천 관측 시각/검수 시각.
2. Wikidata QID의 verified claim.
3. 선택 locale의 recorded/verified 표기(미선택 언어는 승인하지 않음).
4. native Entity active/revision, job approved, 감사 이벤트.

이것은 검수한 원천 기록의 사용 승인이다. 정부·당사자가 지정한 **공식 명칭이라는 보증은 아니다**.
Wikidata 구조화 자료의 CC0 정책은 [공식 Licensing 문서](https://www.wikidata.org/wiki/Wikidata%3ALicensing)를 확인했다.
이 정책을 위키백과 본문/이미지나 TDB의 AI Hub 자료에 확대 적용하지 않는다.

`kentity_id_reservations`의 유일 행을 native 승인과 legacy Wikidata 쓰기 양쪽에서 잠근다.
legacy에 이미 ID 주장이 있거나 다른 native UUID가 예약한 ID면 승인을 거부한다.
기존 legacy 중복 주장을 강제로 삭제하거나 일괄 병합하지 않는다.

검증된 evidence의 출처·대상·관측 정보를 덮어쓰지 못하게 하고, 철회/재사용 허용 제거 시
관련 verified 이름을 blocked, 외부 ID를 withdrawn으로 바꾼다. 다른 유효 정체성 근거가
없으면 native는 candidate로 돌아간다. 외부 ID 예약은 남겨 임의의 다른 UUID 재배정을 막는다.
같은 URL을 재검증할 때는 새 observation UUID를 만들어 이전 증거와 철회 이력을 보존한다.

## UI와 권한

`/admin/kentity/{id}`에 조사 원장, 동명 후보, 원천별 언어 기록, 기존 UUID 링크,
조사 요청/취소/선택 승인과 실패·빈 상태 설명이 있다. admin/operator만 변경할 수 있으며
DB 계정 해제 여부와 CSRF를 매 요청 확인한다. 검수자·조회자는 읽기만 가능하다.
운영 폼은 파싱 전 16 KiB로 제한한다. 승인·취소의 중복/오래된 요청은 409로 돌려준다.

## 검증 기록

- 전체 Go test/build 및 common/Wikidata/admin/readiness/merge race 통과.
- 잠금/취소/만료·오래된 lease/최종 시도 중단/중복 claim/재시도 상한 검사.
- 잘못된 대상·이름 요소·다른 유형 배제, 실제 원문/locale 보존 검사.
- 승인: 선택 언어만 저장, 중복 승인/없는 proposal/없는 locale/변경된 잠금 거부.
- native 승인과 legacy ID 삽입의 동시 실행에서 한 경로만 commit하는 PostgreSQL 시험.
- 승인 evidence 변경 차단, 철회 시 표기 차단, 재관측/재승인 후 이력 보존.
- HTTP 역할/CSRF/취소/승인/중복 요청, 합성 Chromium 390/1440px 폼 전송 시험.
- 공개 원천+격리 DB 실험: 정치 5후보/25표기, 경제 2/14, 스포츠 1/10, 사회 1/8.
  모두 조사 자료이며 이 실험에서 운영 인물/승인 표기를 만들지 않았다. 수치는 관측 시점 값이다.
- 직전 백업 `backups/entity-resolver-20260912.L8Qk4k/kdb.dump`, SHA256
  `f5ef60ed5791d32557fd516413df7c09f6de06b4d79830406b54c520852733a9`.
  kdb 소유, 디렉터리 700/파일 600. 같은 경로 `env`는 비공개 설정 사본이다.
- 이 백업을 network=none `kdb-resolver-verify-db`에 복원, 0117 적용 후 실제 스키마에서
  공통/조사 승인·철회/기존 준비 보충/기존 병합 검증 통과. 원본 UUID 19,489개 투영 대조.

## 운영 반영 및 범위 제한

배포 시 `KDB_ENTITY_RESOLVER_ENABLED=1`을 0117 적용 후 켠다. 앱 이미지는 별도 태그로 빌드한다.
롤백은 common-20260912-1 이미지, resolver flag=0으로 앱만 재생성한다. 추가 원장·근거·
외부 ID 보호 트리거는 유지한다. 과거 dump로 운영 DB를 덮어쓰지 않는다.

`scripts/seed-common-domain-candidates.cjs`는 명시적 flag와 실제 관리자 로그인으로 정치/경제/
스포츠/사회 각 1개 공개명 후보만 등록한다. 기존 같은 이름은 건너뛰며 미검증 후보로만
등록하고 승인·병합·번역·발행은 하지 않는다. 운영 실행 여부/결과는 운영 인계 문서에 별도 기록한다.

정치·경제·스포츠의 모든 공식 원천 adapter, 기사 자동 후보 추출/문맥 연결,
native 표기에 대한 요청 언어별 readiness 연결, 전체 관리자 RBAC/관계·기간·근거 철회 UI,
TDB 권리별 이관, 기사 glossary 및 PressLocale/CMS 발행은 여전히 별도 TODO다.
자동 조사/검수 저장이 구현되었다는 것을 전체 플랫폼 완료로 표시하지 않는다.
