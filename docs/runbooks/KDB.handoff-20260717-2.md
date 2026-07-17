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

## §5b. 저녁 추가 세션 (오너 지적: "미해결 보류가 안 줄잖아") — cand-evidence 신설

- **미해결 2,122 분해 실측**: 재시도 대기 1,517 / **candidate 있는데 승급 정체 484** /
  충돌계 77 / TTL 대기 41. 두 번째 버킷 = 구조 결함: 승급 경로가 공식앵커
  (ResolveOnDemand)뿐 → 공식소스 없는 실존 롱테일이 conf 0.40 영구 정체 +
  3회 무검증 자동기각(오거부 벡터, 2스트라이크 15건 실재). `processed=5 promoted=0`
  로그 반복이 이 증상.
- **`verify.CandidateEvidencePass` 신설**(`candidate_evidence.go`): 요청(review)이
  기다리는 candidate 를 뉴스근거(naver news+SearXNG)+gemma 기사맥락 판정으로
  active(evidenced) 승급 + 대기 review 즉시 종결(closeAsExisting 미러). contaminated 는
  기각 아닌 [cand-evidence:review] 플래그만(FP ~33% 실측·오거부 금칙). unclear 는
  [cand-evidence:unclear] 태그+쿨다운 7d(last_enriched_at 공유 — 이중작업 방지).
- **오거부 가드**(sweep.go ResolveOnDemand): 요청 대기 candidate 는 뉴스근거 판정
  이력([cand-evidence*])이 있어야만 3회 무검증 기각 — 두 축 모두 실패해야 죽음.
- **첫 드레인 실측**: 114건 → **승급 52 / 오염검토 58**. 승급 품질: 파신(킬러들의
  쇼핑몰 등장인물)·샤먼: 귀신전(티빙 다큐)·임기학(셰프)·하재근(평론가) — 전부
  뉴스로 정체 특정. 오염검토: 포켓몬스터(일본 IP)·영웅시대(임영웅 팬덤) — 정확.
  **미해결 2,122→2,067**. 쿨다운 걸린 ~363건은 며칠에 걸쳐 자동 소진.
- **자동화**: 매일 05~07 KST(type-retrace 직후) 100건 — `KDB_CAND_EVIDENCE_DAILY=0` 끔.
  CLI: `kdb-app verify-entities cand-evidence [n]`.
- **admin 검토 항목 신설**: `[cand-evidence:review]` 태그 candidate 58건 — 운영자 확인 후
  기각/승급. 배포 `gatefix-20260717-3`.
- **번역 채움 생존 확인**(오너 질문): 24h 실적 gtranslate 33·wikidata 121·tmdb 30·
  캐시 +579. ja 빈칸 1,914는 §2.4(가타카나 규칙변환) 오너 결정 대기 그대로.

## §5c. 심야 추가: active 고유명사 전수 오염 감사 (오너 지시 "오염 DB=번역 못 채움, 찾아서 제거")

- **모집단 실측**: active 7,257 = 권위앵커 보유 4,929(실존 증명, 제외) + **무ref 2,385(감사 대상)**.
  우선순위: unverified 418 → en 빈칸 352(번역 못 채우는 오염 의심군) → evidenced 무ref.
- **`verify.ActiveAuditPass`**(`active_audit.go`, 배포 `audit-20260717-1`): 이중 게이트 —
  뉴스근거(judgeVerify) × 내용판정(TriageKeywordConfirmed 콜백, import 순환 회피) **모두 동의
  시만** tombstone 기각(dataqa_log `audit-contam-reject`, `revert-contam` 복원 편입).
  news real → unverified 승격(감사=검증 겸임). 커서=last_dataqa_at(30d 재감사).
  CLI `verify-entities audit [n]` + **일일 150**(05~07 KST, KDB_ACTIVE_AUDIT_DAILY=0 끔).
- **1차 배치 250 실측: 기각 0 / 검토 플래그 46 / 승격 170**. ★이중 게이트 실증: 뉴스 축
  단독이면 레이오버(뷔 앨범)·This is for(트와이스 투어)·404 (New Era)(KiiiKiii)·A.N.JELL
  (미남이시네요 가상밴드)이 오거부될 뻔 — 내용판정이 전부 보호. 진짜 오염 의심(모토·
  메트로놈·아몬드·호라이즌 등)은 [audit:review] 플래그로 운영자 몫.
- **운영자 검토 큐 조회**: `SELECT canonical_ko, entity_type, notes FROM kwave_entities
  WHERE notes LIKE '%[audit:review]%' AND status='active';` (46건 + cand-evidence 58건).
- 잔여 ~2,100은 일일 150 자동 — **전수 1회전 ~2주**, 이후 30d 주기 재감사.

## §5d. 심야 2: 요청 유입 품질 진단 + 요청 계약 v1.1 (docs-20260717-1)

- **죽은 URL 원인**(오너 질문): kstory 가 **로그인 벽 뒤(비공개/초안) 글**의 URL 로 prepare
  전송 — /articles/{id} 와 /k-movie/{id} 두 경로 변형 모두 /login 리다이렉트 실측.
  키워드 3건(로키·크리스토퍼 놀런·라이언 고슬링) 딸려 유입. KDB 는 URL 존재만 요구,
  접근성 미검증(27차 백로그 "URL 기사읽기" 미구현).
- **48h 요청 3,076건 실측**: kstory 1,464(형식 A, 단 비K·비공개URL 내용 위반) /
  issuetalk 1,060(context 38%·URL 54% — 07-16 대비 개선, 여전히 C) / 위키피디아를
  source_url 로 보내는 키 1개(333건). 문제군 4종: ①비K 실존 셀럽(놀런·고슬링·로키·
  오모이노타케·M!LK·그록 — triage 가 "실존 고유명사"로 보호해 TTL 21d 가 유일한 출구,
  미해결 최장기 점유) ②오탈자 혼종(JYP엔터테인ement) ③직함 결합(박세영 감독) ④URL 위반.
- **요청 계약 v1.1**: 공개 /docs §5(5-3 확장 + 5-4 source_url 규칙 + 5-5 체크리스트 6항,
  전부 실측 사례 포함) + docs/KDB-REQUEST-GUIDE.md(§3 확장·§3b·§3c·§7 재실측) 동기 갱신.
  "따라 하면 되는" 수준으로 기계 구현 가능하게 서술(오너 지시).
- **오너 결정 대기 추가**: 비K 명시 종결 규칙(15차 보류 사유였던 K-활동 외국적 멤버
  오거부 위험 → "비K 확정+한국활동 무" 이중조건+골든셋 케이스 추가 후 도입 제안).

## §6. 운영 참고

- 배포 후 `KDB_BUILD_VERSION`도 함께 sed 해야 /v1/health version이 갱신됨(.env 6~7행).
- 네이버 일일예산 카운터는 인메모리 — 재시작 시 0부터(과소계상). 잦은 재배포 날은 유의.
- push는 gh CLI 헬퍼 경로만 통함(6차 §0 참조):
  `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`
