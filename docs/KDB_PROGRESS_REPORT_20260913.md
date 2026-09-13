# KDB·TDB 통합 진행 보고 및 세션 인수인계

기록일: 2026-09-13 KST. 이번 상태 확인: 08:37 KST, 로컬 문서·Git 기준.
요청: 진행상태 보고를 문서로 보존하여 다른 세션에서 이어서 작업할 수 있게 한다.
비밀번호·세션·API 키·운영 행 데이터는 기록하지 않는다.

## 후속 커밋·푸시 확인 — 2026-09-13 08:55 KST

사용자의 후속 요청 “커밋푸시해”에 따라 아래 작업을 수행했다. 본문 08:37 시점의 미커밋/미푸시는 과거 상태다.

- 설계·검증·시제품·인수인계 28파일을 `ee63d18cd8b9ebcc1b4bbad7d051a77201f6fa4b`로 커밋했다.
- 저장소 `rickyjoo73/kdb`, 브랜치 `feat/kentity-platform-20260912`에 일반 push했고, `ls-remote`로 동일 SHA를 확인했다. main 병합/강제 push는 하지 않았다.
- 실행 UID/GID는 KDB 1014:1014, Git 작성자/커미터는 `KDB <kdb@kdb.aiinplanet.com>`이다. KDB 계정에 설정된 GitHub 인증 주체 `rickyjoo73`를 확인했다. TDB 인증 정보는 사용하지 않았다.
- 아래 정적 검사 4종을 이번 커밋 요청에서 다시 실행하여 모두 PASS했다. `git diff --cached --check`도 통과했다. UI 브라우저/실제 DB/Go/API 재현 시험은 이번에 실행하지 않았다.
- 미검증 0123 SQL/importer, 운영 코드/설정/DB 데이터는 커밋 대상에 포함하지 않았다. 배포·서비스 변경은 없다.
- 이 확인 기록과 재개 링크는 후속 문서 커밋으로 보존한다. 후속 문서 커밋 자체의 최종 SHA·원격 일치는 Git에서 확인한다. 위 SHA는 설계 본체의 원격 확인 완료 기록이다.

이번 푸시는 작업 보존이지 P0 인수나 DB 통합 완료가 아니다. TODO는 여전히 설계 4개 완료/62개 미완료다.
다음 세션은 원격 브랜치와 작업 트리 상태를 확인하고, 아래 미완료 작업부터 재개한다.

## 1. 결론과 보고 규칙

**P0 상세 설계·사전 검증 단계다. 통합 DB 구현·TDB 본 이관·새 통합 UI 배포는 미완료다.**
[실행 TODO](KDB_INTEGRATION_TODO.md) 66개 중 완료 체크는 4개, 미완료는 62개다. (후속 §7~§11: 같은 날 P0.06·P0.02·P0.05·P0.08·P0.09 인수로 9개/57개.)
완료 항목은 P0.01/P0.03/P0.04/P0.07의 설계 산출물이며, 전체 구현 완료율로 환산하지 않는다. G0는 미인수다.

앞으로 사용자에게 진행 보고할 때마다 해당 시점의 보고를 문서로 먼저 남기고 재개 문서에서 연결한다.
보고에는 확인 시각·근거, 완료/미완료, 실제 실행한 검사, 미실행 검사, 운영 변경 여부,
커밋·푸시·배포 여부, 다음 작업과 금지 사항을 구분한다. 상태의 단일 정본은 실행 TODO다.
이 보고는 시점별 스냅샷이며, 다른 문서에 별도의 완료 체크 목록을 만들지 않는다.
후속 변경은 새 날짜 또는 순번 보고로 보존하고 최신 링크를 갱신한다.

이번 세션은 보고 문서와 재개 링크만 갱신한다. 직전 설계 작업 이후 추가 구현·배포가 있었다고 해석하지 않는다.
현재 운영 건강 상태·데이터 건수·Gemma 부하는 이번 보고에서 재조회하지 않았다.

## 2. 유지할 방향

- KDB의 Go/PostgreSQL 운영을 유지하고, 목표 구조를 먼저 확정한 뒤 필요한 TDB 자산만 흡수한다.
- 정치·경제·사회·연예·스포츠·관광을 별도 마스터 DB로 나누거나 세 번째 DB를 추가하지 않는다.
- 인물 직업은 복수 가능하다. 동일인 가수+배우는 같은 UUID, 동명 가수와 배우가 다른 사람이면 별도 UUID다.
- 이름은 식별 키가 아니다. 근거·문맥·소속·외부 식별자로 판정하며 표기/프로필/보충/캐시가 다른 ID로 섞이면 안 된다.
- 장소·기업·상품 등도 유형/세부유형/분야를 구분한다. 분류표 작성이 실데이터 분류·등록 완료를 뜻하지 않는다.
- TDB의 모든 메뉴·에이전트·작업 레인·운영 이력을 복제하지 않는다. 선별 제외가 원본 삭제를 뜻하지 않는다.
- 요청 언어의 검증된 표기와 생성값·대체 언어·근거 없음·보류를 구분한다. 빈칸을 임의 추론으로 승인하지 않는다.

## 3. 현재 산출물과 범위

| 영역 | 작성·확인한 내용 | 아직 완료되지 않은 부분 |
| --- | --- | --- |
| 분류·정체성 | 13유형/61세부유형/26직업·직군/8분야, 동명·복수 직업·ID별 값 소유 규칙 | 운영 코드 반영, 실제 분야별 데이터 정답 검수 |
| 목표 구조 | [통합 스키마 draft-v4](KDB_TARGET_SCHEMA.md), 이름·언어 준비·출처·동시 수정 계약 | 최종 물리 계약 인수, 실제 DB 제약/경쟁 테스트 |
| 필드 이관 | KDB_TDB_FIELD_MAPPING.json v2: 선정한 14개 원천 표, 185필드의 원본 추적/채택/수정 책임 | 원천 전체 조사 및 실제 데이터 이관 아님 |
| 전체 표 목록 | KDB_TABLE_SCOPE.json: 87표의 이름 단위 범위 대조 | 전체 컬럼/전체 원본 PK/전체 writer 전수 검증 아님 |
| 이관 제어 | [runs/records/source_guards 3표 설계](KDB_MIGRATION_CONTROL_SCHEMA.md), 기존 감사 기록 연결 | 테이블 생성·이관 실행·복구 시험 없음 |
| 권한·API | KDB_WRITER_AUTHORITY.md, KDB_WRITER_DELTA_CONTRACT.md, KDB_API_COMPATIBILITY_CASES.md | 실제 역할 분리와 고객 API 재현 시험 없음 |
| UI | KDB_ENTITY_ADMIN_UI_DESIGN.md, docs/ui/kdb-entity-console.html 시제품 | 합성 데이터/저장 없음. 새 통합 UI 운영 배포 없음 |

87표 범위: 선정 원천 27 / 기존 공통 21 / 기존 운영 유지 25 / 복구용 유지 7 / 추가 판단 7.
추가 판단 7표는 아래와 같다. 판단 전 복사·폐기·원본 삭제하지 않는다.

- 현재 수요: `tdb_name_misses`, `kwave_entity_research_queue`, `kwave_kdb_request_terms`.
- 기존 의존성 확인: `kwave_person_research_queue`, `kwave_entity_candidates`.
- 추가 조사: `kdb_audit_suspects`, `tmp_ld`.

## 4. 검증 결과와 발견한 위험

직전 작업에서 아래 네 가지 정적 검사가 통과했다. 이번 보고 작성에서는 재실행하지 않았으며 과거 검사 결과를 구분해서 기록한다.

```sh
node docs/checks/validate-kdb-classification.cjs
node docs/checks/validate-kdb-absorption-design.cjs
node docs/checks/validate-kdb-writer-design.cjs /data/home2/kdb.aiinplanet.com/.worktrees/kentity-platform-20260912 /home/tdb.aiinplanet.com
node docs/checks/validate-kdb-control-design.cjs
```

실행 위치는 아래 KDB 정본 저장소다. 검사 범위는 분류/문서 계약/필드 및 PK 누락/원천 코드 해시/합성 부정 사례다.
직전 확인의 writer 참조 62개(고유 파일 48개) SHA256 불일치는 0이었다.
이 결과를 실제 PostgreSQL FK·GRANT·트랜잭션·병합 경쟁·Go·고객 API replay 통과로 바꾸어 보고하지 않는다.
해당 새 설계의 실제 DB/Go/API 검사는 아직 미실행이다.
시제품의 이전 390px/1440px 브라우저 검사는 운영 UI 인수가 아니다.

소스/메타데이터 조사에서 발견하여 설계에 대응책을 반영했지만 운영 해결·검증은 남은 위험:

1. 잠금 확인과 저장 사이의 경쟁 및 일부 이름 승격 경로의 차단 원천 검사 범위.
2. 하위 이름 데이터만 바뀔 때 부모 시각/리비전에 반영되지 않아 증분 동기화가 누락될 가능성.
3. 9월 12일 관측 DB 실행 계정의 superuser/owner 권한과 API/worker 실행 계정 분리 미확인.
4. 기존 API/클라이언트의 UUID·fallback·중첩 오류·경로·페이지 커서 계약 차이.

## 5. 다음 세션 작업 순서

1. 이 보고 → 실행 TODO → 최신 스키마/이관 제어/권한/필드 및 범위 원장을 읽고 Git 변경 상태를 확인한다.
2. P0.02/.05: source policy의 namespace·우선순위·정확한 locale 물리 계약을 확정하고 위 7표의 현재 효력/수요 필드를 대조한다.
3. runtime writer/pool 귀속, 원천 수정 경로, migration 실제 내용 대조와 동기화/권한 설계를 보강한다. 표 개수 일치를 SQL 바이트 일치로 간주하지 않는다.
4. P0.06/.08/.09: 실제 소비자 계약, 독립 실데이터 정답/판정자, 원천 이용권과 제한된 최초 이관 범위를 확인한다. 담당자 응답만이 유일한 미완료 사유는 아니다.
5. P0.10 G0 인수 후 P1 격리 환경에서 스키마·FK·잠금/동명 경쟁·권한·Go/API/UI를 검증한다. 검증 로그를 TODO에 연결한다.
6. P2 시험 이관·대조·복구 검증 → P3 승인된 제한 범위 흡수 → P4 자동 보충 → P5 writer/API 전환 → P6 운영 UI/관측 인수 → P7 기사 번역 플랫폼 연동 순서로 진행한다.

앞서 사용자에게 실제 KDB/TDB 사용 서비스와 전환 확인 담당자를 질문했으나 답변은 기록되지 않았다. 답변이 도착했는지 확인한다.
이름/직업 사전이나 메뉴만 추가하고 정치·경제·사회·스포츠 데이터 등록·자동 갱신이 완료됐다고 하지 않는다.

## 6. 저장소·재개 위치 및 변경 보호

- 정본: `/data/home2/kdb.aiinplanet.com/.worktrees/kentity-platform-20260912` (KDB uid/gid 1014).
- 브랜치: `feat/kentity-platform-20260912`, origin: `https://github.com/rickyjoo73/kdb.git`.
- 보고 시 확인한 HEAD: `872d237e95828b889436dcda89803c49ac2c74d7`.
- 최근 설계 문서/검사와 이번 보고는 작업 트리 파일이며 아직 커밋·푸시하지 않았다. 소유권 동기화는 Git 커밋·푸시가 아니다.
- 편집 미러: `/home/tdb.aiinplanet.com/.worktrees/kdb-workflow-fix` (TDB uid/gid 1015). 오래된 코드가 있어 정본에 통째로 덮어쓰지 않는다.
- 재개 진입점: `/home/tdb.aiinplanet.com/PRESSLOCALE_HANDOFF.md`, `/home/tdb.aiinplanet.com/COMPACTION_RESUME.md`, 정본 `docs/COMPACTION_RESUME.md`.
- 체크포인트의 이전 이력은 보존한다. 최신 보고와 단일 실행 TODO를 우선 확인하고 실제 Git 상태가 달라졌으면 대조한다.
- 기존 `PRESSLOCALE_EXECUTION_PLAN.md`, `KDB_ADMIN_UI_PLAN.md`와 운영 체크아웃의 사용자 변경을 보존한다.
- 로컬 수정은 apply_patch로 한다. KDB 정본 반영은 대상 파일·기존 해시를 확인한 선택 동기화만 하고 소유권을 유지한다. `sync-kdb-owner.js` 전체 실행 금지.
- 미러 `migrations/0123_kentity_tdb_inventory.sql`, `scripts/import-tdb-inventory.cjs`, `docs/TDB_FULL_INTEGRATION.md`는 미검증 초안이다. 이번 보고 근거로 실행·배포하지 않는다.
- 과거 운영 기준: migration 0122까지, `kdb-app:entity-center-20260912-1` 및 a707978. 이는 9월 12일 이력이며 현재 건강 상태 보장이 아니다.
- 이번 요청으로 운영 DB 쓰기·마이그레이션·대량 병합·UI 배포·Gemma 일정 변경·서비스 재시작을 하지 않는다.
- 다른 세션에 자동 전달되거나 백그라운드 에이전트가 계속 실행된다고 가정하지 않는다. 다음 세션은 위 문서를 직접 읽고 재개한다.

## 7. 후속 — 2026-09-13 P0.06 실사용 계약 인수

- 확인 시각·근거: 운영 KDB READ ONLY 집계(api_requests 89,014행 등). replay·서버 호출·payload 복사 없음.
- 완료: P0.06. 유지 소비자 4곳(issuetalk·mediafine·trendbiz·kstory), route 27개 분류(제외 0), 고객의 KDB UUID 저장 확정, delta cursor 소비 0, fixture 12건 실사용 상태 기록.
- 미완료: P0.02/.05/.08/.09/.10, G0 미인수. TDB 측 R03/R09/R10 은 TDB 로그로 P1.01 에서 확인.
- 운영 변경·배포: 없음. 커밋·푸시: 이 후속 커밋. 백업 수리 스크립트는 같은 날 d24c418 로 브랜치에 반영.
- 다음: P0.02(ERD·DB 역할 분리) → P0.05 → P0.08/.09 → P0.10 G0 → P1.01.

## 8. 후속 — 2026-09-13 P0.02 물리 구조 대조 인수

- 확인 시각·근거: 운영 KDB 카탈로그 READ ONLY(표 22·뷰 1·트리거 16·함수 20·역할 1·확장). DDL·GRANT·데이터 변경 없음.
- 완료: P0.02. 기존 표 차이표, 신규 20표 위치, 불일치 D-01~D-15(P1 시험 항목으로 지정), 원천→최소속성 대조, 사전 seed 8종, 권한 매트릭스. 설계 결정 변경 0, 추가 결정 1(`kentity_entities.qualifier_ko`).
- 미완료: P0.05/.08/.09/.10, G0 미인수. 제약 실동작·btree_gist·기존 행 계수는 P1.
- 운영 변경·배포: 없음. 커밋·푸시: 이 후속 커밋.
- 다음: P0.05 pending 7표 판정 → P0.08 → P0.09 → P0.10 G0 → P1.01 격리 복원.

## 9. 후속 — 2026-09-13 P0.05 원본 범위·정책 namespace 인수

- 확인 시각·근거: KDB·TDB 카탈로그·행수·최종 쓰기·코드 경로 READ ONLY. 행 이전·삭제·DDL 없음.
- 완료: P0.05. pending 7표 → 운영 유지 3(research_queue·request_terms·tdb_name_misses) / 복구 보관 4(person_rq·entity_candidates·kdb_audit_suspects·tmp_ld). selected 27·필드 185 불변. source_code→정책 namespace 8단계·form·locale alias 계약.
- 미완료: P0.08/.09/.10, G0 미인수.
- 인계: kdb_audit_suspects MISLINK 168건(현재 QID 연결 유지) → P2.05 guard 제안. tier 7·8(생성·기계번역) 약 63,500칸은 strict-ready 제외.
- 운영 변경·배포: 없음. 커밋·푸시: 이 후속 커밋.
- 다음: P0.08 → P0.09 → P0.10 G0 → P1.01.

## 10. 후속 — 2026-09-13 P0.08 인수 시험 fixture 인수

- 확인 시각·근거: KDB/TDB 건수 READ ONLY. fixture 실행·LLM 호출·DB 쓰기 없음.
- 완료: P0.08. M01~M14 합성 fixture(정답은 구성으로 확정, 자리표시자 이름), T01~T19 층화 표본 명세(TDB 536,329행 전수 분모), 판정 경로 7(자율 판정기 5·구조 규칙·운영자 escalation). 검사 5종으로 확장(`validate-kdb-acceptance-fixtures.cjs`).
- 미완료: P0.09/.10, G0 미인수.
- 운영 변경·배포: 없음. 커밋·푸시: 이 후속 커밋.
- 다음: P0.09 → P0.10 G0 → P1.01.

## 11. 후속 — 2026-09-13 P0.09 분모·파일럿·중지 기준·권리 기본값 인수

- 확인 시각·근거: 요청 로그 30일 percentile·카탈로그·행수 READ ONLY. DDL·데이터 변경 없음.
- 완료: P0.09. 기준선 10종(API p95: lookup/bulk 472 · match 3,346 · prepare 2,500 · corrections 1,002 · lookup 930 ms; 5xx 0~0.119 %; 일 873건), 파일럿 A(KDB person_roles ≤100)·B(TDB admin_region 249 전수), 표본(M 14 · T ≤1,000 · linking 200), 중지 수치, 권리 기본 차단·운영자 단일 escalation.
- 미완료: P0.10(모순 검토·승인 범위 기록·G0 인수) 하나.
- 운영 변경·배포: 없음. 커밋·푸시: 이 후속 커밋.
- 다음: P0.10 → G0 → P1.01 격리 복원.
