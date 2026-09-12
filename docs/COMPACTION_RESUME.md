# 압축 후 재개 — 통합 설계부터

2026-09-12 KST. 비밀번호·세션·API 키 없음.

## 최신 지시 (최우선)

사용자가 “기존 TODO를 잊지 않도록 갱신 → compact 먼저 → KDB/TDB 및 추가 영역까지
통합할 설계도·기획안을 먼저 만들라”고 지시했다. **구현·운영 이관은 중단했다.**
기존 KDB도 미분류·잘못된 값이 해결돼야 하며, TDB 전체와 누락 보충이 대상이다.
숫자만 숨기거나 모두 기타/연예로 분류하지 않는다. 설계안을 먼저 제시한다.

현재 도구에 compact 실행 기능은 없다. 실제 압축 완료라고 주장하지 않는다.
공식 문서상 사용자 입력 `/compact`가 현재 대화를 압축한다:
https://learn.chatgpt.com/docs/developer-commands?surface=cli
별도 Codex 프로세스나 API 호출로 현재 세션을 압축했다고 주장하지 않는다.

## 압축 후 첫 작업

`KDB_TDB_INTEGRATION_BLUEPRINT.md`라는 설계 문서를 작성할 예정(아직 없음).
현행/목표 구조도·ERD·필드 대응표·이관/갱신 흐름·검증 게이트를 포함한다.
master TODO에 D1~D6을 추가했고 기존 0~11은 그대로 유지한다.

1. 실제 KDB/TDB 테이블·제약·조회/쓰기·출처·보충 worker 구조 대조.
2. 공통 정체성, 유형/복수 분야 분리, 인물·기업·기관·팀·리그·대회·장소·작품·상품·개념 확장.
3. 직업/시간별 소속·직책·이적, 학교 조직 vs 캠퍼스, 점포 vs 역 등 구분.
4. 원본 UUID→공통 UUID crosswalk, 기존 KDB UUID 보존, 동명 자동 병합 금지.
5. 이름·별칭·원문 locale·형식·근거·검증·권리·기간·철회·잠금·감사 계약.
6. TDB 전체 이름·주소·좌표 등 필드별 보존/변환/권리 보류 명세와 수량 대조.
7. KDB 기존 직업/유형 근거에 따른 분류, 기각 비고유명사와 실제 검수 대상 구분.
8. requested locale별 실제 준비, 신규 발견/원천 조사/제한 재시도/검수/정정/갱신 정책.
9. snapshot+증분/삭제/병합/잠금 추적, 단일 writer 전환·소비자 호환·복구.
10. 통합 UI의 전체 이관·후보·연결 검수·언어 준비를 분리. 원장 100%를 이전 100%로 표시하지 않음.

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
- 임의 subagent 금지. 현재 요청은 병렬 위임 지시가 아니다.
- Gemma 기존 일정 00~05 KST 야간,04~05 정체 후보 점검, 모델gemma4:26b/동시12.
  9/12 야간 유형교정62/승급1/기각1은 변경 기록이지 전체 호출/GPU 사용량이 아니다.
  작업별 비용·수율 계측은 미완료이며 이 설계 작업 중 스케줄을 변경하지 않는다.
