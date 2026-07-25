# KDB 핸드오프 39차 (2026-07-25) — e2e 전수감사 + 잔존결함 5건 즉일 해소 + ★속도전략 실행

**★오후 추가(같은 날): "빠르게 해결" 전략 1·2·3·5 실행 완료 — 요청→서빙 p50 15h → 초 단위.**
실측 p50 910분/p90 29h 의 주범 = 근거 부재가 아니라 20분 스위프 대기 + 실패 시 7d 쿨다운.
①프레시 패스트레인: research worker 가 소비자 origin candidate 유지 시 `CandidateEvidenceOne`
즉시 실행(스위프와 동일 게이트 코드 공유) ②waiting 건 쿨다운 7d→1h ③waiting 그룹 최신 우선
④prepare 번역예산 초과분 백그라운드 프리페치. **라이브 실증: "착한 사나이" 요청→active 3초
(wikidata 경로), "모래알갱이" 요청→active 4초(패스트레인 뉴스근거 경로 — 고의 오힌트 '아이유'를
검색증거가 '임영웅의 노래'로 교정 특정, 오염방어 동작 확인)**. 배포 `fastlane-20260725-2`,
커밋 `9039705`+`d0c9af4` push. 전략4(match 인메모리 매처)는 후순위 미착수.
★부수 실사례: '나는 반딧불'(실존 히트곡, 황가람 리메이크)이 게이트 문장형 오판으로 rejected
tombstone — ①근거 URL 동반 정정신고는 tombstone 우회 재심(탈출구 배선) ②해당 건 candidate
재개방(song_album, 뉴스근거 재심 대상 — 다음 스위프에서 판정 예정, 승급 확인 요).

직전: 38차(`KDB.handoff-20260724.md`). 이번 아크 = 오너 질의("느린 곳·부정확한 곳·답변
못 주는 곳이 남아 있는가, e2e 로 전부 찾아라") → 7일 실측 전수감사 → 오너 지시("단계별로
모두 하나씩 해결하자") → 5건 전부 수정·배포·라이브검증. 커밋 `7d0dacc` push 완료.
배포 `kdb-app:e2efix-20260725-1`. 상세 결과 = 메모리 `project-kdb-e2e-audit-20260725`.

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s -m 5 http://127.0.0.1:9100/v1/health   # version=e2efix-20260725-1

# ★trendbiz 400 루프 본문 캡처(이번 아크 신설 계측) — 원인 특정 후 소비자 통보 필요
docker logs kdb-app --since 24h 2>&1 | grep 'corrections 400 consumer=9ee49cd4' | tail -3

# 정정 무인정체 재검증 레인(신설) 정상 가동 확인
docker logs kdb-app --since 24h 2>&1 | grep 'retried .* stuck pending' | tail -3

# 38차 이월 관찰(뉴스축·P4·드레인)은 38차 §0 명령 그대로.
```

## §1. 감사 실측 (7일 창, kwave_kdb_api_requests + request_terms)

- 인프라 건강: 5xx/타임아웃 0. lookup/bulk p50 36ms · prepare p50 360ms · match p50
  1.4s(p95 3.2s, 기사 본문 × 14k 스캔 — 허용 범위, 28차 이월 "match 핫패스"는 잔존 backlog).
- 발굴 큐: 인테이크→종결 p50 2~3초, pending 백로그 0(33차 게이트지연 5.3h 구조해소 확인).
  autoverify 1,768건/24h — P4 정상 페이스. preparing 갔던 정확매치 1,726건 중 96% 해소.
- 소비자 체감 preparing→ready p50 ~16h 는 소비자 폴 주기가 지배(상한치).

## §2. 해소한 결함 5건 (전부 커밋 7d0dacc · 라이브 재현→수정→라이브 검증)

1. **정규화 교착(최중요)**: 인테이크(intakeNormalizedKey)는 "6시내고향"=활성 "6시 내고향"
   을 알며 existing_entity 로 접수를 무시하는데, 서빙(ListEntities ILIKE·exactKoMatch)만
   정규화가 없어 **영원한 new/preparing**(7일 47키워드). → gatekeeper.NormalizedKey 노출,
   ListEntities 정규화 branch+랭킹 티어1, exactKoMatch/prepareMatchSafe 정규화 폴백,
   lookup 부분일치-only 응답에 발굴 트리거(hasNormalizedHit). 검증: "6시내고향"→ready,
   "행복 (Happiness)"→found, "주이"→이주원(alias 주이) 1순위 랭킹.
2. **tombstone 무종결**: rejected/merged 판정 키워드(7일 270)에 영원 preparing →
   `Store.Tombstoned`(정규화 키, active/candidate 부재 확인) + prepare/lookup/corrections
   3경로 out_of_scope 종결. LookupResponse 에 status(found|miss|out_of_scope) 신설.
3. **corrections 400 본문 미계측**: trendbiz(9ee49cd4) 10분 주기 400 루프(7일 113건)를
   서버측에서 진단 불가 → 400 시 본문 512B 샘플 로깅(logBadCorrection). **다음 액션:
   §0 명령으로 캡처 확인 → 원인 특정 → trendbiz 통보(오너 채널)**.
4. **정정 운영자큐 3주 정체**: '검증 실패/불가'·'검증 미완료'가 ReapStale 자동기각('검증
   불확실'만) 사각 → `RetryStuckPending`(1회 재검증 후 반드시 종결, verifying 선점으로
   겹침 안전, worker 8s tick 배선). 결과: 정체 8건 즉시 종결 — **3건 반영**(더 지니어스
   ja ザ・ジーニアス, 에스파 ja エスパ, 금해나 es), 5건 근거로 기각(전건 사유 기록).
5. **소스천장 무종결**: enrich exhausted 인 locale 도 preparing 만 반복(66키워드) →
   prepare 응답에 `unavailable` 필드(missing 의 부분집합, "재폴링해도 안 채워짐" 통지).

부수: prepare_test 2건이 07-21 en-폴백 계약 이전 기대값으로 HEAD 에서도 실패하던 것 갱신
+ 정규화 테스트 신설. docs(/v1/docs) 에 신규 필드 문서화.

## §2b. ★저녁 추가 — LLM 교체(gemma→qwen3vl) 대응·최적화 (배포 qwenopt-20260725-1, 커밋 62e90be)

오너가 게이트웨이(ai.aiinplanet.com) 모델을 qwen3vl 로 교체(ai1/ai2/ai4 gemma 용도 폐기,
게이트웨이 단일 — 오너 확정). 대응: ①`KDB_GEMMA_MODEL=qwen3vl` 전환(교체 직후 구 별칭
404 로 전 LLM 콜 사망 상태였음) ②비교실측 — gemma 약점(문자셋 오염탐지 50%·JSON 재시도
필요)에서 qwen 우위, 판정 동급, 버스트 12병렬 1.4s ③최적화 — defaultGemmaModel 동기화·
**response_format json_object 상시**(vLLM guided JSON, KDB_GEMMA_JSON_MODE=0 롤백)·
KDB_GEMMA_TEMP/TOP_P 노브(기본 0/0.1 유지)·타임아웃 240s→120s. e2e 재검증: "청혼하지
않을 이유를 못 찾았어" 4초·"사건의 지평선" 5초 승급. **관찰항목: ①qwen classify 가
소비자 타입힌트를 뒤집은 오분류 1건(사건의 지평선 song_album→brand_place, 수동교정) —
일일 스팟체크에서 타입힌트 오버라이드 사례 주시 ②DATAQA 재가동 검토 근거 생김(문자셋
탐지 개선). 교체 다운 3분(14:40~43) 여파 0 확인.** '나는 반딧불' 오거부 재개방 건은
MusicBrainz 드레인이 공식근거로 승급 종결(15:48).

## §2c. ★밤 추가 — P3 배치 실행 완료 + churn 구조수정 (배포 p3churn-20260725-1)

판정대기 726건 P3 배치 실행(claude 7병렬 판독 → 구조게이트 → 트랜잭션 반영, 스냅샷
`kwave_kdb_p3_snapshot_20260725` + dataqa_log 전건 revert 가능). 결과 **promote 127·
retype 55·reject 148·keep 396**. candidate 815→498, active 9,020→9,233. 오거부 금칙:
reject 스팟체크로 K-아티스트 연관 2건(레프트 앤 라이트=정국 피처링·CRACK=원호) keep 강등,
homonym promote 가드 작동. 서빙 실증(버즈 group·씨네21 channel_outlet·최성국 person).
★반영 중 homonym 유니크 충돌 발견 → 타입변경에 NOT EXISTS 가드 추가(반영기 v2).
★churn 구조수정: P3가 keep 정리한 candidate 를 20분 cand-evidence 스위프가 매 사이클
review 재플래그하던 무한왕복(27건 실측) → 스위프 선별에서 `[adjudicated:claude]` 제외
(claude 판정=자동 스위프 상위 결정). DB 재플래그 27건 정리. 잔여 판정대기 9건은 P3 이후
신규 유입 churn(정상, 다음 스위프 소화). 참고: Fable5 한도로 판독 에이전트 1차 전멸→
Opus 4.8 전환 후 재실행 완료(모델 교체 무관, subagent 한도 이슈였음).

## §2d. ★심야 추가 — review 큐 대량 판정(3,000+ 막힘 해소) + codex/claude A/B

오너 지적 "고유명사 3,000+ 해결 안돼" 실체 = research 큐 **precheck='review' 2,005건**이
autoverify(일 600 Naver콜) 물방울 처리를 3일씩 대기(유입>처리로 오히려 증가). P3처럼
claude 14병렬 판정으로 Naver 대기 없이 즉시 처리: **approve 342·reject 384·keep 1,279**.
반영: reject 384 즉시 종결(LLM 무관), approve는 게이트웨이 모델교체 500 감지→복구 후 자동
적용(2단계 dedup: 335 pending 발굴승격 + 7 중복 done, 유니크제약 kwave_entity_research_
live_term_type_uidx 충돌 회피). review 큐 2,005→1,278. 발굴 파이프라인 실측 드레인(12분 내
69건 active — 김재경·조현영·쿨·청춘불패 시즌2 등). 스냅샷 `kwave_kdb_rq_snapshot_20260725`
전건 revert 가능. 스크립트 rq_rubric/rq_repair/rq_apply(scratchpad).
★교훈: TSV에 `"` 포함 행 → csv.reader 깨짐(QUOTE_NONE/순수 split 필수), 에이전트 id
오전사로 오인했던 62건은 실제 파서 버그였음(위치기반 rq_repair로 정답 id 복원).

**★codex vs claude A/B 실측(동일 50행, 문맥없는 이름)**: 일치율 72%, 불일치 14건 전부
claude=keep vs codex=commit. codex(gpt-5.6-sol, 로그인·결제 불필요·이미 작동)는 처리량↑
(keep 절반)이나 **문맥없는 한국 인명을 오거부(김성연·신소정·정유인)** — 최상위 금칙 위반.
claude는 오거부 0이나 keep 다수. **결론: 상시 tier-3 레인은 "2-모델 합의→적용, 불일치
→keep" 패널 권장**(codex 처리량+claude 안전). 이번 배치는 오거부 0 위해 claude 채택.
tier-3 진입조건=aged/exhausted(현재 exhausted 176·14d경과 366). 상시화=오너 결정 대기.

## §3. 다음 작업

1. **trendbiz 400 원인 통보** — §0 캡처 로그로 페이로드 확인 후 오너가 trendbiz 에 전달.
   (계측은 상시 가동 — 캡처는 자동으로 쌓인다.)
2. 38차 이월: 뉴스축 승급 품질 스팟체크 · P3 배치 재실행(플래그 713 재누적) · P4 관찰(~07-27).
3. (낮은 우선) match 핫패스 최적화 — p95 3.2s, 오류 0 이라 급하지 않음.
4. (관찰) 신규 out_of_scope/unavailable 응답의 소비자측 수용 — request_terms 에서
   out_of_scope 비율 급증 등 이상 시 tombstone 판정 정밀도 재점검.

## §4. 오너 결정 대기 (38차 이월 + 신규)

- 신규: trendbiz 통보 채널/문구. tombstone 오판(실재 K-엔티티를 결번 처리) 신고 창구는
  기존 corrections 로 수용 가능(제출 시 재발굴 경로 유지).
- 이월: P3 상시화 · 비K 작품 정책 · KOPIS 키 재발급 · C등급 3사 계약 등 38차 §3.
