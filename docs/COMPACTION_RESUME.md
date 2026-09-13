# 이관 제어·필드 책임·권한 보강 — draft-v4 / 전체 TODO 미완료

## 최신 보고·다른 세션 재개 (2026-09-13 KST)

사용자 추가 지시: **보고할 때 문서로 남겨 다른 세션에서 이어서 작업할 수 있도록 한다.**
진행 보고마다 확인 시점, 완료/미완료, 실행/미실행 검증, Git·배포 상태, 다음 순서와 금지 사항을 문서로 보존하고 이 진입점을 갱신한다.
최신 보고: [KDB_PROGRESS_REPORT_20260913.md](/data/home2/kdb.aiinplanet.com/.worktrees/kentity-platform-20260912/docs/KDB_PROGRESS_REPORT_20260913.md).
실행 상태 정본: [KDB_INTEGRATION_TODO.md](/data/home2/kdb.aiinplanet.com/.worktrees/kentity-platform-20260912/docs/KDB_INTEGRATION_TODO.md).
08:37 KST 당시 66개 중 설계 4개 완료/62개 미완료, G0 미인수였다. 같은 날 후속으로 P0.06(운영 요청 로그 실측)·P0.02(운영 카탈로그 대조)·P0.05(87표 범위·정책 namespace)·P0.08(fixture M14+T19)·P0.09(분모·파일럿·중지 수치·권리 기본 차단)·P0.10(모순 검토 6축)·P1.01~P1.03(격리 복원·forward migration·제약 검증)을 인수해 **13개 완료/53개 미완료**다. 격리 제약 시험 69/69 PASS, go race 29 ok. D-16~D-28 을 새로 찾아 전부 고쳤다. D-26 은 원천 정책 13 provider 승인(운영자 결정)과 공통·legacy 양쪽 정책 게이트로 해소했다. P1.07 도 인수 — kentity_names 보호선(잠금·출처 우선순위·현재 효력 guard). 시험 83/83, 격리 회귀 4/4, race 29 ok. **G0 는 2026-09-13 인수**됐고 승인 범위는 파일럿 A(KDB person_roles ≤100)·B(TDB admin_region 249)다. 다음은 P1.08 API·최소 UI 연결(M06/M07)·P1.06 동시성·P1.09·P1.10 G1.
후속 “커밋푸시해” 처리: 08:55 KST 설계·검사·시제품·인계 28파일을 KDB UID1014/작성자 KDB로 커밋·푸시했다.
설계 본체 `ee63d18cd8b9ebcc1b4bbad7d051a77201f6fa4b`, 원격 rickyjoo73/kdb의 feat/kentity-platform-20260912 SHA 일치 확인.
이번 정적 검사 4종과 staged diff 검사는 PASS. 이 확인 기록은 후속 문서 커밋으로 남긴다. 최종 HEAD/원격은 Git으로 확인한다.
아래 “872d237/미커밋/미푸시” 기록은 이전 시점이다. 추가 구현·운영 DB 쓰기·배포·Gemma 일정 변경은 없다.
다음은 source policy 물리 계약/미확정 7표/writer 권한 및 migration 대조/실고객·정답·권리 인수다.
아래 내용은 이전 설계 및 배포 이력이다. 시점별 상태를 섞거나 이전 검사 결과를 이번 실행으로 보고하지 않는다.

2026-09-12 KST. 비밀번호·세션·API 키 없음.

## 최신 지시 (최우선)

최신 요청: 병렬 에이전트 진행 제안 → “솔 high로 하면 되겠니” → “진행해”. 임시 API/설계 검토를 Sol high로 실제 분담했다.
후속 “진행해”에서 source 권한/필드 책임을 같은 Sol high 2개에 분담하고 root가 이관 제어 3표를 설계했다.
최신 산출물: docs/KDB_MIGRATION_CONTROL_SCHEMA.md(runs/records/source_guards+기존audit FK), KDB_WRITER_AUTHORITY.md,
KDB_FIELD_WRITER_MAP.json(14표,62 source path refs/48 distinctfiles SHA256), KDB_TABLE_SCOPE.json(23:53KST public일반표87개),
docs/checks/validate-kdb-control-design.cjs(27표typedPK예제/9negative), 기존 KDB_TDB_FIELD_MAPPING.json v2(185필드trace/target_adoption/writer/test분리).
매핑v2수량: include28/conditional122/archive34/exclude1, target_adoption conditional150/archive34/exclude1; 실제 PKcomponent18전부trace include.
정의되지않은 source_projection컬럼/operator_lock guard값은 검사에서거부해수정완료. tdb_placeslock은정확operator_locked대상,
false≠unlock/status≠common승인/modified_at≠사실날짜/deleted≠Entity삭제/집계form≠Entityno_form유지.
target-schema-draft-v4 §15는제어원장문서연결. planned/newUUID와실재EntityFK분리,target_mode별expectedrevision(신규계획만0),
specific rejected_entity_id,pausedapply금지,승인자/실행capability분리,실제write+audit+record원자성,복구afterhash검사설계.
23:36및23:45KST READONLY: KDB kdb/TDBtdb각DB유일한non-systemlogin,모두superuser/owner. 관측세션11/4도같은role.
API/workerbinary귀속은application_name없어미확인. migrationKDB72(last0122)/TDB87(last0087),trigger16/13.
KDBmigrationDBchecksum없음/TDB87checksum소스전수대조미실행. 운영role/GRANT/REVOKE변경0;격리negativeA01~06명세추가.
PostgreSQL공식함수보안기준으로SECURITYDEFINERsearch_path에pg_temp명시적마지막/PUBLIC회수+grant동일transaction설계보강.
87표table-name scope: selected_source27/existing_common21/operational_retained25/recovery_only7/pending_review7.
pending7: 현재수요(tdb_name_misses/kwave_entity_research_queue/kwave_kdb_request_terms),legacy2(kwave_person_research_queue/kwave_entity_candidates),
미대조2(kdb_audit_suspects/tmp_ld). 처음pending10중8표코드대조후3표(source_candidates/invariants/enrich_attempts)는기존운영유지.
테이블이름계상이지전체컬럼/실데이터PK/worker전수조사나흡수완료아님. sourcepolicy namespace/우선순위locale·현재guard/수요필드및권한실행/실고객정답인수남음.
검사: 분류/흡수/writer/control 정적검사,27PK예제9negative,62pathrefs(48files)실제hash mismatch0.
실제PostgreSQLFK/GRANT/race/Go/APIreplay시험0,UI배포/DB쓰기/Gemma일정변경/commitpush0. 새문서와정본만UID1014선택동기화.
root/선행 source_writer_audit의 모델을 바꿨다고 말하지 않는다. 서버 daemon/모델 설정 파일/서비스 설정은 변경하지 않았다.
api_contract_sol은 전용 새 docs 2파일만 작성, schema_review_sol은 읽기검토만 수행. root가 코드 대조·target/name/identity 보정·TODO/검사를 통합했다.
새 산출물: docs/KDB_WRITER_DELTA_CONTRACT.md(W01~W10, X01~X12), KDB_SOURCE_SUPPLEMENT.json(추가13표161컬럼 metadata/제한projection),
KDB_API_COMPATIBILITY_CASES.md + docs/checks/kdb-api-compatibility-cases.json(R01~R12, 실제replay 미실행), docs/checks/validate-kdb-writer-design.cjs.
추가metadata는 22:33 KST 별도 READ ONLY 관측. 키/행값/config/rawpayload는 읽지않음. 기존32표387컬럼 snapshot과 다른시각이므로 단일snapshot이라 부르지않는다.
24개 source 파일SHA256 및 API10파일 digest 고정. 원천키/잠금/차단·현재정정/철회 보호가 없는 단순값복사 금지.
앞선 target-schema-draft-v3 §14 S01~S08: bound item/readiness/job UUID·revision·scope복합FK, typed evidence/no_form,
정책revision/만료/재개cursoroutbox, reserve/verify교차유일성, appliedmerge/redirect/reversal, 부분날짜, 반증상태.
2차검토보정: 최초정책승인은 새approvedversion INSERT, policyfanout cursor+epoch원자commit, confirmedbinding identityclaim검사.
독립 검토에서 actualCode상 확인: TDB installName판정→tx 사이lock경쟁가능, kind-onlycanonical에blockedsource gate범위누락,
KDB0104 child업데이트는parentupdated_at불변,0118 foreign-name-only commonrevision불변; TDBchanges도name-only delta못포착가능.
API고객 영향미확인: client의nestederror파서/UUID·fallback누락,/api/v1 alias중복,locale/prepare/correction/cursor계약차이.
검사: 분류/흡수 정적검사+writerdesign검사PASS. 추가161컬럼/완전PK/24sourcehash/6개negativefixture검사.
실제 SQL/FK/병합경쟁/Go/APIreplay 실행0. UI파일/운영UI 이번변경0. migration/DB쓰기도0. Gitcommit/push없음.
다음: source policy namespace/우선순위·locale 최종컬럼, pending7표의현재효력/수요·필드, runtime writer/pool권한및migrationbyte대조, 독립정답P0.08/범위P0.09/실고객계약확인 후G0.
기존 master TODO 및 과거 KDB_ADMIN_UI_PLAN.md는 원본byte보존. 새문서만 UID1014 clone에 선택반영한다.
전체66항목 중 완료 체크14개(P0 전체 + P1.01~P1.03 + P1.07), 나머지52개 미완료다. G0 인수, G1 미인수. 완료는 설계 산출물이며 구현/운영 완료 아님.
실행 상태 정본은 `docs/KDB_INTEGRATION_TODO.md`. P0.02/.05/.06/.08/.09/.10 및 G0 미인수.
앞선 턴에는 읽기 조사/설계/합성 HTML 프로토타입/정적·브라우저 검사를 했으며 실제 구조 구현/흡수에는 착수하지 않았다.
새 계약: docs/KDB_IDENTITY_CONTRACT.md, KDB_NAME_READINESS_CONTRACT.md, KDB_ENTITY_ADMIN_UI_DESIGN.md,
KDB_ABSORPTION_CONTRACT_REVIEW.md, KDB_ACCEPTANCE_LIMITS.md. 물리 구조는 최신 draft-v4 §15 및 이관제어별도데이터사전까지 확장.
새 inventory: docs/KDB_TDB_SCHEMA_INVENTORY.json(32표/387컬럼), KDB_TDB_FIELD_MAPPING.json(선택14표/185컬럼).
선택 표의 미매핑0. 전체 source/writer 조사를 완료했다는 뜻이 아님. 실제 row payload/키/비밀값은 수집하지 않음.
UI 시안 docs/ui/kdb-entity-console.html은 합성 데이터만, HTTP 요청0/DB 저장0. 실제 /admin UI는 바뀌지 않음.
Playwright 1.48.0 network=none, 390/1440px 브라우저 검사 PASS; 캡처 /tmp/kdb-design-ui.FDNMCf.
검사 scripts: docs/checks/validate-kdb-classification.cjs, validate-kdb-absorption-design.cjs, test-kdb-admin-prototype.cjs.
사용자에게 현재 사용하는 KDB/TDB API 서비스 목록과 전환 확인 담당자를 async로 질문했음. 답변 여부 확인 후 이어갈 것.
20:56 KST KDB 최근5000 API 요청에서 match2036/lookup-bulk1415/prepare1180/corrections280/lookup55 확인.
TDB15소비자/14사용 이력, 최신last_used 8/20 23:09 KST. 로그 시각만으로 미사용 판정/폐기하지 말 것.
실제 고객 계약·독립 실데이터 정답 검수/판정자·원천 이용권(AI Hub 등 보류)·writer/manifest 전체 대조가 남음.
G0~G2 인수 전 운영 적재/기존 API 전환/미검증0123 실행 금지. 24시간 관측을 테스트 로그로 대체하지 말 것.
새 산출물: `docs/KDB_CLASSIFICATION_RULES.md`, `docs/KDB_TARGET_SCHEMA.md`,
`docs/checks/validate-kdb-classification.cjs`. KDB 소유 clone에 선택 동기화한다.
검사 실행: `node docs/checks/validate-kdb-classification.cjs`.
13유형(현행 11+brand/character 목표 추가), 61세부유형, 26직군·직업, 8분야.
KDB 원천 13유형/16역할, TDB 22지원 유형 매핑·참조·계층·현행 Go 호환 기준 PASS.
매핑은 후보 경로다. 실데이터 정답 검증이나 FK/잠금 동시성 시험을 한 것은 아니다.
P0.02는 속성/원본 binding/관계/표기·근거/준비·작업 컬럼까지 확장했지만 전체 원본/고객/manifest 대조 전이라 완료 표시하지 않았다.
다음은 남은 인수 자료와 P0.02/.05/.06/.08/.09/.10 대조다. 승인 판단 없이 스스로 G0를 닫지 않는다.
운영 DB 쓰기/적재/병합/배포/Gemma 일정 변경/이번 단계 커밋·푸시 모두 없음.

사용자가 “기존 TODO를 잊지 않도록 갱신 → compact 먼저 → KDB/TDB 및 추가 영역까지
통합할 설계도·기획안을 먼저 만들라”고 지시했다. **구현·운영 이관은 중단했다.**
기존 KDB도 미분류·잘못된 값이 해결돼야 하며, TDB 고유명사 자산과 누락 보충이 대상이다.
숫자만 숨기거나 모두 기타/연예로 분류하지 않는다. 설계안을 먼저 제시한다.

후속 턴에서 시스템의 압축된 요약을 수신하고 이 체크포인트를 읽어 재개했다.
선행 사용자 요청은 “불필요한 기능을 가져오면 통합 운영이 힘들다. 합친 다음 분리하지 말고,
용도에 맞는 DB 구조를 먼저 제대로 설계한 뒤 흡수하라”다.
이후 추가 지시: “기업인/연예인/가수/배우/정치인 등 인물 구분, 장소/상품 등 유형을 반드시 두고,
이름이 같은 다른 인물/대상을 별도 ID로 관리하도록 철저히 설계하라.” 두 요청 모두 유효하다.
최신 강조: 같은 이름의 가수/배우가 다른 사람이면 내용/문맥을 보고 해당 사람 ID에 맞는 값을 주어야 한다.
별도 compact 명령을 실행했다고 주장하지 않는다. 구현 대신 설계 문서를 작성했다.

## 현재 산출물과 다음 작업

KDB 소유 clone의 `docs/KDB_TDB_INTEGRATION_BLUEPRINT.md`를 v1.3 선택 흡수+필수 분류/ID별 값 소유 보강안으로 갱신했다.
편집 미러에도 동일 파일이 있다. v1의 TDB 운영 기능·테이블·메뉴 전량 복제는 철회했다.
공통 이름/정체성/근거/품질은 필수, 주소·좌표·직업·소속은 필요한 구분 정보,
에이전트/레인/정찰 포털/과거 로그 전체는 업무 DB와 메뉴 흡수 제외다. 원본 삭제는 하지 않는다.
필요한 보호 규칙은 공통 writer/보충에 재사용한다. 모든 분야를 별도 관리 모듈로 만들지 않는다.
단, 통제된 유형/세부유형/인물 직업 코드 및 person_roles는 필수다. 운영 단순화를 이유로 제거하지 않는다.
4.4~4.7에 직군-세부 직업, singer+actor 한 사람, 동명 인물/회사/장소/상품 별도 UUID,
FK·인덱스 후보·동명 판정·ID 유지·UI 등록/필터·표본 시험 계약을 추가했다. 물리 SQL 적용/인수는 아직 없다.
4.8에는 동명 가수/배우를 문맥/근거로 해소하고 각 ID의 이름/프로필/보충/캐시를 분리하는 계약을 추가했다.
하나의 이름 키로 표기·프로필을 복사하거나 다른 사람의 직업을 합쳐주는 것을 금지한다.
master TODO의 D1~D6은 v1 문서 작성 이력으로 남기고 새 R1~R5를 추가했다. 기존 0~11 본문 보존.
R1(범위 수정), R1a(분류/ID), R1b(동명별 문맥/값 소유 계약) 문서 보강 완료.
실행 상태는 새 `docs/KDB_INTEGRATION_TODO.md` 한 곳만 갱신한다. master의 D/R/0~11은 이력이고 기획안 단계표는 요약이다.
**P0.01/.03/.04/.07 설계 산출물 완료. P0.02 확장 및 .05/.06/.09 부분 조사·초안 작성, 실제 인수 미완료**다.
P0 설계 → P1 격리 검증 → P2 dry-run → P3 승인 범위 흡수 → P4 보충 → P5 writer/고객 전환 → P6 인수 → P7 기사 플랫폼.
실행 TODO의 완료 체크는 P0.01/.03/.04/.07 네 개다. M01~M14 동명/값 소유 시험과 G0~G7 게이트를 건너뛰지 않는다.
이번에는 문서만 변경했다. 운영 DB 쓰기·migration·이전·UI 배포·Gemma 일정 변경 없음.
전체 수량 기준은 19:28 KST: KDB 19,511 / TDB 536,329 / 이름 2,685,251 /
원천 레코드 1,782,902 / 링크 1,745,118. 자동 worker가 진행 중이므로 이전 snapshot 수량이 아니다.
새 문서는 별도 후속 commit 여부를 git status/log로 확인한다. 기존 마지막 문서 커밋은 872d237이다.
20:21 KST 추가 READ ONLY 조사: 양쪽 PostgreSQL 16.14, KDB enum/공통 컬럼·FK·트리거·인덱스,
TDB 22유형/12locale 사전·이름/장소/원천 링크 구조를 확인했다. TDB th는 disabled,
zh-Hant→zh-Hans 등 legacy fallback은 strict-ready 승인이 아니다.
현재 evidence.claim_type은 identity/name/relation뿐이다. 분류/직업 주장 근거와 복수 근거 연결을 P0.04에서 보강한다.
Go supportedTypes는 아직 11코드다. 새 brand/character를 이미 API에서 사용할 수 있다고 말하지 않는다.

1. 실제 KDB/TDB 테이블·제약·조회/쓰기·출처·보충 worker 구조 대조.
2. 공통 정체성/유형/복수 분야/직군·직업 분리. 인물 복수 직업과 분야별 검색은 필수, 별도 분야 시스템은 제외.
3. 직업/시간별 소속·직책·이적, 학교 조직 vs 캠퍼스, 점포 vs 역 등 구분.
4. 원본 UUID→공통 UUID crosswalk, 기존 KDB UUID 보존, 동명 자동 병합 금지.
5. 이름·별칭·원문 locale·형식·근거·검증·권리·기간·철회·잠금·감사 계약.
6. 전체 원본은 조사하되 목표 필드별 포함/조건부/보관만/제외를 먼저 판정. 승인 범위만 변환·대조·흡수.
7. KDB 기존 직업/유형 근거에 따른 분류, 기각 비고유명사와 실제 검수 대상 구분.
8. requested locale별 실제 준비, 신규 발견/원천 조사/제한 재시도/검수/정정/갱신 정책.
9. snapshot+증분/삭제/병합/잠금 추적, 단일 writer 전환·소비자 호환·복구.
10. 일상 UI는 개요/고유명사/검수/보충/설정 중심. 이전 도구는 운영자용, 옛 TDB 메뉴 1:1 복제 금지.

## 운영 기준점

- 코드 `a70797848f8ad23b505c31b07cf1c1592d9ecb8e`, 문서 HEAD `1109df27ad724e878b9e0f5ef57ee1b506cb298c`.
  체크포인트 문서만 후속 커밋될 수 있다. 새 실행 코드 배포 금지.
- 이미지 `kdb-app:entity-center-20260912-1`, digest
  `sha256:a1915f17d274fdb0f58633bf3f9ebc7d3debe24812be28fcfdcdb5abec3a65b6`.
- 18:46:14 KST 시작, healthy, API9100/admin9101 200. 기존 4개 네트워크/IP 유지.
- migrations0115~0122 적용. **0123은 미적용.**
- UI 홈 실제 집계/8분야/최근 등록, 공통 복합 필터·50개 페이지·등록 폼 진입 배포됨.
- 전체 Go test/build/race, 복원본 읽기, 390/1440px, 실제 정상 로그인 검증 통과.
- 마지막 이관 구현 턴에는 운영 DB 쓰기/분류/새 이관/재배포를 하지 않았다.

## 실제 수량 (9/12 19시대 조회; 필요하면 bounded READ ONLY 재확인)

- TDB 전체536,329, active+unmerged536,322. 이름2,685,251행/이름 보유 UUID536,327.
- 실제 데이터 유형19종(코드 지원22유형과 구분).
- restaurant145433 / other106602 / district83897 / tourist_spot36981 / person31378 /
  accommodation26569 / shopping25509 / leisure_sports20796 / nature19664 / heritage13888 /
  cultural_facility5943 / legal_dong4912 / work3822 / festival_event3322 / food2774 /
  organization2557 / education1122 / transit911 / admin_region249.
- Wikidata 연결 UUID17,915 / 기존 선택 조건 링크 주장17,999. QID-only로는 나머지 대부분을 못 다룸.
- KDB kwave_entities19,501. 공통 origin KDB19,501/native3/TDB1.
- 공통 분야 정치1/경제1/사회1/스포츠2, 전부 미검증 후보. 나머지 분야0.
  기존 KDB는 UUID 투영만 되고 공통 분야 연결은 아직 없음.
- KDB primary_role 전체 상태 혼합 집계: actor1335/idol767/singer753/athlete62/
  businessperson21/politician14/other2013 등. 이미 있는 근거를 분류 설계에 활용한다.

## TDB 연결 상태 — 전체 이관 아님

- kentity_tdb_shadows10개, review8/blocked2; person8/shopping1/restaurant1.
- TDB crosswalk review1/confirmed0.
- 정유인 공통 UUID `2b923a2d-42d0-4005-8e07-194fc0c150ee`,
  TDB UUID `4d0619e2-ee7b-480d-a910-837b5b5617f2`, Q100623651.
  origin=tdb/writer=native/candidate. ko/en/zh3개 unverified, verified0.
- 지호한방삼계탕 압구정역(Q100834), 에뛰드하우스 양재역(Q100852)이 역에 잘못 연결됨.
  공통 등록은 차단했으나 **원래 TDB 링크/파생 표기는 교정 안 됨**.
- `/etc/cron.d/kdb-tdb-id-observer`는 등록된10개만 매분 재관측, 전체 이관 작업 아님/Gemma 안 씀.
- 앱에 Docker socket/TDB 비밀값 추가 없음. KDB UID1014 운영자 helper가 원본 ID만 확인.

## 미배포 초안 — 먼저 설계 검토

미러 `/home/tdb.aiinplanet.com/.worktrees/kdb-workflow-fix`에만 새 파일3개:

- migrations/0123_kentity_tdb_inventory.sql
- scripts/import-tdb-inventory.cjs
- docs/TDB_FULL_INTEGRATION.md

앞의 두 파일은 **미검증·미실행·미커밋·미배포**. KDB 소유 clone에 복사하지 않았다.
전량 UUID·유형·상태·병합 대상·잠금·갱신시각·집계만 수집하는 초안이고 이름/속성 이전은 아니다.
원자성·중단/중복·회차 불변성·수량·삭제·메모리/시간 제한 등의 검토·테스트가 필요하다.
지금 실행하지 않는다. 설계 후 채택/수정/폐기를 판단한다.
sync-kdb-owner.js를 무조건 실행하면 초안까지 복사되므로 **현재는 문서만 선택 반영**한다.

## 기존 TODO를 잊지 말 것

정본은 KDB clone `docs/PRESSLOCALE_EXECUTION_PLAN.md`.
0~11의 상세 체크는 유지, 새 설계 D1~D6이 선행 게이트.

- 1/3/4: 24h 반복 수렴·실제 요청 준비·보충 효과·근거/재시도 비용 관찰.
- 5/6: 공통 스키마/시간 관계/tenant override/legacy persons UUID 연결·writer 전환.
- 7: TDB 전체 이관/관광 속성/원본 오류 교정/권리/소비자 전환.
- 8: 정치·경제·스포츠·사회 공식 출처와 정답 세트 기반 자동 발견·검증.
- 9: 기사 span별 연결/동명 문맥/버전 고정 Glossary.
- 10: CMS 인증 이벤트/큐·outbox/Gemma pool·공정성/검증·반환/구버전 덮어쓰기 차단/부하검증.
- 11: 상세/검수함/권한/감사/실제 지표/운영 훈련/UAT. 홈·목록 개편만 완료.

## 환경/안전

- cwd /home/tdb.aiinplanet.com, uid1015(tdb), Docker group999, 호스트 Go 없음.
- KDB 소유 clone(uid1014): /data/home2/kdb.aiinplanet.com/.worktrees/kentity-platform-20260912.
  feature branch feat/kentity-platform-20260912, origin https://github.com/rickyjoo73/kdb.git.
- 미러(uid1015): /home/tdb.aiinplanet.com/.worktrees/kdb-workflow-fix.
- 운영 checkout /data/home2/kdb.aiinplanet.com의 dirty main을 reset/pull/전체 덮어쓰기하지 않는다.
- TDB root 사용자 수정(.dockerignore/TDB_STATE/internal/match/*/quality/watch/cmd/tdb-recordfix/
  migrations0086/0087/.codex) 보존. root 인계 문서만 이번 작업 소유.
- 파일 변경 apply_patch. UID1014 파일은 Docker UID1014 apply_patch로 수정. KDB 계정 feature branch만 push.
- 테스트는 kdb-workflow-test-db와 kdb-tdb-observer-verify-db 등 격리 DB. 지금 설계는 쓰기 불필요.
- 최신 합성 UI /tmp/kdb-entity-center-ui.I7y4Nv, 개인 cache /tmp/kdb-admin-ui.WGQOPN/go-cache.
  과거 root 소유 파일이 섞였으므로 공유 캐시/HTML 권한을 광역 변경하지 않는다.
- TDB CLAUDE.md 완독: installName, source policy migration-only, holdout/생성값 구분/표기 위생/
  정정 시 파생값과 재유입 방지/동일인·동일소 보호를 설계에서 보존해야 함.
- AI Hub 이용권 미확인. live/enabled라고 재사용 조건이 확인된 것은 아님.
- 사용자가 임시 병렬 위임과 Sol high를 명시 요청했다. 새 구체 하위 작업은 model=gpt-5.6-sol, reasoning_effort=high, fork_turns=none을 사용했다.
  같은파일/운영DB 동시수정금지, root최종검토·정본통합 유지. 상주 서버에이전트 설치승인이 아니다.
- Gemma 기존 일정 00~05 KST 야간,04~05 정체 후보 점검, 모델gemma4:26b/동시12.
  9/12 야간 유형교정62/승급1/기각1은 변경 기록이지 전체 호출/GPU 사용량이 아니다.
  작업별 비용·수율 계측은 미완료이며 이 설계 작업 중 스케줄을 변경하지 않는다.
