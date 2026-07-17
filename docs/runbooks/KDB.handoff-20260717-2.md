# KDB 핸드오프 34차 (2026-07-17 저녁) — 게이트 웨지 수리 · triage 일일 자동화 · 다국어 오염 실태 감사

직전: 33차(07-16~17). 이번 세션 = 심사게이트 가속 점검 중 **웨지 실사고 발견·수리**,
불확실 키워드 스위프 실행, 다국어 채움 오염 제거 체계 실태 감사.
배포 `kdb-app:gatefix-20260717-1`(triage-eval PASS 후). 커밋 push 완료.

## §0. 새 세션 첫 명령 (33차 §0과 동일 + 웨지 재발 감시)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s https://kdb.aiinplanet.com/v1/health   # version=gatefix-20260717-1
./scripts/kdb-dashboard.sh | head -40
# 웨지 재발 감시: 같은 ko가 연속 반복되면 안 됨(수리됐으면 miss 백오프로 넘어감)
docker logs kdb-app --since 2h 2>&1 | grep "intake-autoverify: naver search" | sed 's/.*ko=/ko=/' | sort | uniq -c | sort -rn | head -5
# triage 일일 자동화 생존(매일 04~05 KST): kdb.triage(daily) 로그
docker logs kdb-app --since 26h 2>&1 | grep -c "triage(daily)"
```

## §1. 웨지 실사고 (발견·수리·실증 — 핵심)

- **증상**: 자동검증 레인이 12h+ 동안 "몰래카메라" 1건만 2분마다 재시도(checked=1 반복),
  네이버 예산 소모(1콜/2분 ≈ 720/일), 배치 중단 반복.
- **원인 사슬**: ①07-17 오거부 사고 전량복원 때 함께 review 복귀(복원 후 재심사 경로 없음)
  ②이 키워드만 네이버 encyc가 일관되게 http 500(키워드 특이) ③`verifyItem`이
  "네이버 장애+웹 무신호"면 **row를 안 건드리고** stop → 백오프 없는 유일 eligible 행이
  영구 재선택. eligible 자동검증 사유 행이 이 1건뿐이라 레인 전체가 웨지.
- **수리**: 해당 경로에 `recordMiss`(1d→3d→exhausted 백오프) 적용 후 stop.
  실증: 배포 직후 miss+1일 백오프 확인, fresh 레인 3건 즉시 승격 재개.
- 몰래카메라는 miss 소진 → triage(일반명사, 이중판정) 경로로 자연 종결 예정.

## §2. 이번 세션 산출물

- **A. 웨지 수리**: `intake_autoverify.go` verifyItem — §1.
- **B. triage 일일 자동화**: `cmd/kdb/main.go` — 매일 04~05 KST `TriageExhaustedBacklog(400)`
  + `TriageStuckCandidates(400)` 자동 실행(`KDB_TRIAGE_DAILY=0` 끔). 종전엔 수동 원샷뿐이라
  안 돌린 날 백로그 재적체. 골든셋 `triage-eval` PASS(오거부 0, 놓침 1/10) 후 배포.
- **C. 스위프 실행**: triage-exhausted 52건 → 기각 1(스타드 드 프랑스=비K 경기장) / 유지 51.
  triage-candidates 3일+ 정체 저신뢰 후보 513건 → 결과는 §5 갱신 참조.
- **D. 다국어 오염 데이터 교정**: ja 칸 한글 혼입 1건(코미디 복면가왕) 스냅샷 후 빈칸
  (dataqa_log revert 가능). 역방향 오염(비한국어 칸 한글)은 en/zh/vi 0건 — 사실상 청정.
- **E. 로마자 음차 오염 감사**: RR(ko)==en 휴리스틱 실측 —
  `docs/runbooks/KDB_ROMANIZATION_AUDIT_20260717.csv` (**116건**, 비인물·비캐릭터).
  타입별 brand_place 28·song_album 19·show 18·movie 14·drama 14 / 소스별 gtranslate 40·
  wikidata-label 35·codex-fallback 12. ★해석 주의: 전부 오염 아님 —
  ①진짜 음차 오염(wikidata-label 류) ②**타입 오염의 증상**(가수를 song_album으로 잘못
  타입→MT가 인명 로마자화: 손민수·정수라·조명섭) ③정당한 공식 로마자 제목(추노→Chuno,
  경주→Gyeongju). → 자동 삭제 금지, gemma 재판정 라우팅이 정답(33차 §2.3과 합치).

## §3. 다국어 채움 오염 제거 체계 실태 (감사 결론)

| 계층 | 상태 | 비고 |
|---|---|---|
| type-retrace 일일 150 | **가동** | 오늘 교정 55·잔여 ~600 |
| triage 스위프 | 수동→**일일 자동화(이번 세션)** | 04~05 KST |
| dataqa 오염 탐지 루프 | **꺼짐**(`KDB_DATAQA_ENABLED=0`) | gemma 문자셋 탐지 50% 품질 이슈로 재활성 보류 |
| 로마자 음차 스위프 | **미구현** | 후보 116 CSV 확보(§2E) — 다음 작업 |
| TTL 21일 | 가동 | triage_kept 한정. 대량 도래 08-01+ |
| 역주입 방지(suppressed) | 가동 | 13차 구조 |

## §4. 다음 작업 (우선순위)

1. **로마자 음차 재판정 스위프 구현**: §2E CSV 116건을 입력으로 type-retrace 패턴
   (무편향 재검색→gemma 판정→스냅샷 교정) 재사용. character/person/group 제외 유지.
2. **동명이인 충돌 자동 라우팅**(33차 §2.1 그대로): eligible 충돌계 ~297건
   (conflicting_entity_types 165·duplicate_live_request 80·type_shape_conflict 44) —
   autoVerifyReasons 밖이라 어떤 자동 경로도 안 탐. 무인화 마지막 조각.
3. **밤사이 수렴 확인**: 백오프 만기 오늘밤 686·내일 새벽 1,419 — 웨지 수리 후 첫 대량
   드레인. §0 웨지 감시 명령으로 재발 확인.
4. **(오너 결정 대기) TTL 단축 제안**: triage_kept+exhausted(검색 3회 무근거+에이전트 유지)
   행은 21d→10d 단축 여지 — 대량 도래가 08-01이라 결정 여유 있음.
5. dataqa 재활성 여부: gemma 단독 품질 미달이면 유지(꺼짐), 로마자 스위프가 부분 대체.

## §5. 갱신 (세션 말)

- triage-candidates 결과: **614건 처리 — 기각 20 / 유지 594**(이중판정 경유, 전부
  dataqa_log 스냅샷 복원 가능). 기각 예: 강원대학교 방송연예과(교육기관 명칭).
  잔여·신규 정체분은 매일 04~05 KST 자동 스위프가 이어받음(멱등).

## §6. 운영 참고

- 배포 후 `KDB_BUILD_VERSION`도 함께 sed 해야 /v1/health version이 갱신됨(.env 6~7행).
- 네이버 일일예산 카운터는 인메모리 — 재시작 시 0부터(과소계상). 잦은 재배포 날은 유의.
- push는 gh CLI 헬퍼 경로만 통함(6차 §0 참조):
  `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`
