# KDB 코드레드 개선 마스터플랜 (2026-06-28, 8도메인 멀티에이전트 감사)

검증 완료. 핵심 사실(매칭 strpos 무가드, zh→번체 매핑, 게이트키퍼 veto 부재, 카운트)이 모두 일치한다. 아래가 종합 마스터플랜이다.

---

# KDB 개선 마스터플랜 (8개 도메인 종합)

## 총평
"고장"이 아니라 **두 개의 구조적 적자**가 코드레드를 만든다. (1) **공급/관문 적자** — 수집 인입 90% 붕괴 + 오거부(false-reject)가 실존 K-엔티티를 영구 무응답으로 만든다(요청의 ~27%가 무답). (2) **노출 품질 적자** — 매칭이 strpos 부분문자열이라 일상어를 오매칭하고, 간체 중국어가 사실상 불능이며, codex 추측값(12k+)과 검증값이 무신호로 섞여 나간다. 데이터 충진율(91~95%)·파이프라인 헬스·동명이인 정합(노출충돌 0)은 양호하므로, 레버는 "더 채우기"가 아니라 **"있는 걸 정확히 내보내고, 멀쩡한 걸 안 버리기"**다. 검증: `strpos($1, canonical_ko)` 무가드(api.go:1655, ko 1자 active 20·2자 601), `case "zh"→canonical_zh_hant`(api.go:2161, zh≠zh_hant 1638/4031=40.6%), 게이트키퍼 reject 경로에 wikidata/searx import 0건(agent.go:172, confFloor 0.75), candidate 75중 typed 73, rejected 954.

---

## §A. 코드레드 TOP 5 — 소비자 사용성 최대 영향

**CR-1. 오거부(false-reject)로 실존 K-엔티티 영구 무응답** [extraction P1·P2 + coverage P1·P2 + orchestration P5 병합]
- 문제: v2 role-agent 게이트키퍼와 resolveUnknownOne이 conf≥0.75면 Wikidata/SearXNG 존재검증 없이 즉시 hard-reject → '막걸리 한잔'(영탁 곡)·'무조건'(박상철)·KARA·빵야·스타킹·한국인명형 다수가 '일반명사'로 박제. rejected는 재발굴 차단(research/worker.go:134)+소비자는 active만 서빙 → 검색해도 영원히 무답.
- 증거: 게이트키퍼 agent.go reject(line 172)에 wikidata/news_search/searx import 0건(grep 확인). legacy tryRescue(sweep.go:1177)만 3중 veto 보유. resolve무구제 경로 rejected 453(고유명사형 291), typed-media common_noun 거부 60. 7일 거부 112건이 role-agent.
- 수정: ① `internal/kdb/agents/gatekeeper/agent.go` reject(line 169-172) 직전에 tryRescue와 동일한 Wikidata.SearchAndFetch+SearXNG 존재검증 추가, 미확보 시 hard-reject 대신 quarantineTyped(candidate 보존). candRow/Select에 entity_type·typed 힌트 로드 + `AND notes NOT LIKE '%[kdb:q:typed]%'` 제외절. ② `resolveUnknownOne`(sweep.go:880) rejectAsTerm 직전 `if s.tryRescue(...) return` 1줄. ③ 1회성 `rejudge-rejects` CLI로 기존 453+60건 Wikidata/SearXNG 재검증→candidate 복원(--dry-run+오너승인).
- 효과: 무답 27% 중 오거부분 직접 회복, 신규 오거부 봉인. 정밀 유지(Wikidata 미확인 진짜 일반어는 rejected 유지).
- 공수 M(게이트 수정+백로그 CLI) / 위험 low (동일 tryRescue 재사용, 이미 19건 실복구 전례)

**CR-2. 매칭 부분문자열 오탐 — 일상어가 인명으로 번역 오염** [matching P1 + coverage P6]
- 문제: `strpos($1, canonical_ko)>0`에 어절경계·길이 가드 없음(canonical_ko엔 alias의 char_length≥2 가드조차 없음). '비'(rain)→가수 비/ピ, '가을'(autumn)→인명, '진짜/사진'→진. 게다가 오탐값이 media-consensus·wikidata-label(=verified)라 verified_only로도 안 걸러짐. 번역 소비자 본문이 오염됨.
- 증거: api.go:1655 매칭절 확인, 1자 active 20·2자 601. 라이브 재현: 무엔티티 문장에 진·가을·비 3건 오매칭(conf 0.75~0.97).
- 수정: `internal/kdbapi/api.go` MatchEntitiesForLocale WHERE절. ①즉시(S): canonical_ko 분기에 `char_length(canonical_ko)>=2` 추가(1자 20건 오탐 제거). ②본수정(L): 한글 경계 정규식(앞뒤 비한글/문장부호/조사 allowlist일 때만 매칭). ③동음 1~2자 stoplist(비·가을·바다·별…)는 operator_locked/disambig 보유 시에만 매칭 또는 conf 패널티.
- 효과: 번역 결과 오염 제거 — 현재 가장 직접적인 "틀린 답" 차단.
- 공수 S(부분)→L(본수정) / 위험 medium (정규식 메타 이스케이프·recall 영향 검토 필요)

**CR-3. 간체 중국어(zh) 매칭 불능 — 본토 시장 40% 오답** [matching P2]
- 문제: entityLocaleColumns가 `case "zh","zh-hant","zh-tw","zh-hk"→canonical_zh_hant`(번체)로 매핑. 'zh' 요청에 간체 아닌 번체를 주는데 둘 다 채워진 4031건 중 1638건(40.6%)이 글자가 다름. 'zh-cn'/'zh-hans'는 default→400. canonical_zh(간체 4036값)는 어떤 태그로도 도달 불가.
- 증거: api.go:2161 확인, zh≠zh_hant 1638/4031. spellings는 이미 분리(2116-2119)되어 결함은 entityLocaleColumns에 국한.
- 수정: `case "zh","zh-cn","zh-hans","zh-sg"→canonical_zh,aliases_zh` / `case "zh-hant","zh-tw","zh-hk","zh-mo"→canonical_zh_hant`로 분리, 빈칸은 간↔번 상호폴백(영어보다 유용). 'zh'='번체' 의존 소비자(현 distinct 1) 사전공지.
- 효과: 본토 중국어 소비자 40% 오답 즉시 해소 + 사장된 간체 4036값 활성화. **대표 quick-win(S/low).**
- 공수 S / 위험 low

**CR-4. 작품 영어 leak + 교차인물 오염이 소비자에 노출(works는 영구)** [entity_quality P3 + homonym P1·P2·P3 + enrich P5 병합]
- 문제: (a)작품 canonical_ja/zh가 순수 영어(나혼자산다 ja='Home Alone', 삼시세끼='Three Meals a Day' 등 ja 62·zh 49)인데 dataqa가 person/group만 검수(dataqa.go:32)라 작품 leak은 탐지대상 밖→일/중 소비자에 영어 노출. (b)enrich runWikidata가 stored QID 보유(active 2377건)에도 매번 이름검색 재해소→동명이인 라벨 복사(박민영 es='Sam' 478회). (c)runCodexFallback이 charset 가드 우회로 zh/zh_hant ASCII 1104건 유입.
- 증거: dataqa.go:32 pendingFilter person/group 한정, QID active 2377(이름검색 불필요한데 재검색), zh ASCII codex 519+zh_hant 585.
- 수정: ① QID-pin: `orchestrator.go` runWikidata(line 528) 진입부에서 stored QID 조회→`Fetch(qid)`로 그 라벨만 패치, 이름검색은 폴백만. ② runCodexFallback에 `if !IsValidSpellingForLocale(loc,val) continue`+en-copy 가드(생산지점 차단). ③ dataqa pendingFilter에 drama/movie/show/song_album 추가(번역변종↔교차작품 구분 프롬프트, dry-run 우선). ④ mixed-script(郑빛나 등) 결정론 비우기 후 QID-pin 재충전.
- 효과: 동명이인 mislink 유입 차단(핑퐁 종결) + 작품 영어/교차 leak 정리.
- 공수 M / 위험 medium (QID-pin은 Fetch 폴백 가드 필요, dataqa 작품은 오탐 위험→dry-run)

**CR-5. 수집 인입 90% 붕괴 — foreign canonical 충진·발굴 공급 정지** [collection P1·P2·P3 병합]
- 문제: 06-19 외부 일시장애로 42개 피드가 consecutive_failures≥7로 영구 kill(자동 재활성화 코드 0건), rss_url 90중 38이 Google News 프록시 단일 실패점. enabled 15/90로 붕괴, 주간 distinct 소스 48→12, media-consensus 승격 71→6/주. RSS는 canonical 채움 ~2.3k 기여(TMDb 2788과 비등)인데 정지.
- 증거: DB feeds_enabled_rss 15/90 확인. 자동kill 42개 전수 curl 재확인 도달가능(39×200+3×302). rss_poller.go:239 auto-disable 단방향, re-enable grep 0건.
- 수정: ①**즉시(오너 1액션)**: admin 토글/1회 SQL로 42개 enabled=true·consecutive_failures=0(토글이 reset 수행). ② rss_poller.go에 `ReprobeDisabled` 신설(unreachable 피드 재probe 성공시 자동복구), autopilot 일1회 step. ③ rss_discover로 38개 프록시 도메인의 네이티브 RSS 직접URL 백필, gnews는 fallback만.
- 효과: 인입·foreign locale 충진·발굴 공급 회복(코드 전 즉시 42피드 복구가 최고 ROI 단일액션).
- 공수 즉시복구 무코드 + 자동화 M / 위험 low

---

## §B. 즉시 실행 Quick-win (high-impact · S/M-effort · low-risk)

우선순위 순. ★=오늘 바로 가능.

| # | 항목 | 수정 위치 | 공수/위험 |
|---|------|----------|----------|
| ★QW-1 | **42개 피드 재활성화**(오너 토글/SQL 1회) — CR-5 즉시분 | kwave_news_whitelist | 무코드/low |
| ★QW-2 | **간체 zh 분기**(CR-3) | api.go:2161 entityLocaleColumns | S/low |
| ★QW-3 | **resolveUnknownOne에 tryRescue 1줄**(CR-1 ②) | sweep.go:880 | S/low |
| ★QW-4 | **게이트키퍼 Select에 `[kdb:q:typed]` 제외절**(격리 후보 재처리 중단, gemma 호출 절감) | gatekeeper/agent.go:94-99 | S/low |
| ★QW-5 | **runCodexFallback charset/en-copy 가드**(zh ASCII 1104 유입 차단) | orchestrator.go:630-683 | S/low |
| QW-6 | **match-miss→발굴 enqueue 계측**(silent return 제거, #1 엔드포인트 미스가 발굴 0건) | api.go:1316-1348 enqueueFromText | S/low |
| QW-7 | **match 영어폴백 플래그**(`LocaleFallback bool`) — VI/ZH 자리 영어 가시화 | api.go:1625 MatchEntitiesForLocale | S/low |
| QW-8 | **canonical_ko 길이가드 `>=2`**(CR-2 즉시분, 1자 오탐 20건 제거) | api.go:1655 | S/medium |
| QW-9 | **LocalFill REGROUND off+wall-budget+BATCH 30→10**(최대 compute sink, 23.6h/7d 무수익) | main.go:812, env | S/low |
| QW-10 | **superseded step audit-only**(EnrichEmpty 33.5min·ReviewCandidates 17.4min/24h 중복실행 제거) | agents.go:99 stepAgent.Run | S/low |
| QW-11 | **OTT autopilot off**(344시도 12값=무수율) → on-demand CLI만 | main.go:854,902 | S/low |
| QW-12 | **mixed-script 결정론 비우기**(郑빛나·金河얀 등 CJK칸 한글혼입) | sweep.go stepSweepContamination | S/low |
| QW-13 | **feed health 경보**(enabled<40 또는 obs/일 급락 — 9일 붕괴 미인지 방지) | rss_poller.go:67-73,258 | S/low |
| QW-14 | **needs_disambig 비활성행 정리**(rejected 27건 잔류로 큐 카운트 오염) | sweep.go:1986 clearResolvedDisambig | S/low |
| QW-15 | **dataqa CountSuspects/SuspectSQL 술어 통일**(charset 오염 '0건' 오보) | dataqa.go:136 | S/low |
| QW-16 | **admin 후보화면 → kwave_entities 재배선**(폐기 legacy 테이블 조회 중, 실후보 75건 안보임) | handlers_kdb.go:66/85 | S/low |
| QW-17 | **operator-deselected K전문매체 재활성화**(표본 10/12 도달, 희소 locale 보강) | 오너 토글 | S/medium |

QW-9~11+QW-10으로 회수되는 cycle 예산(~190min/24h)을 grounding 재시도·song_album 권위소스로 재배분.

---

## §C. 도메인 교차 근본원인 (여러 도메인의 공통 뿌리)

1. **오거부 보호장치의 v2 마이그레이션 누락** [collection·extraction·coverage·orchestration] — legacy tryRescue의 Wikidata+SearXNG+quarantine 3중 veto가 v2 role-agent 게이트키퍼와 resolveUnknownOne에 안 옮겨짐. → CR-1로 일괄 해결. **단일 패턴(tryRescue) 재사용이 4개 도메인의 오거부를 동시 봉인.**

2. **strpos 부분문자열 매칭 엔진** [matching·entity_quality·coverage] — 한 줄(api.go:1655)이 오탐(일상어)·미스(띄어쓰기 변형)·중복분할(해피투게더/해피 투게더)을 모두 유발. → 어절경계+공백정규화로 정밀·recall·dedup 동시 개선.

3. **provenance/신뢰신호의 소비자 미노출** [matching·enrich] — 내부엔 *_source 다 있는데 lookup/entities는 무신호, match는 영어 무신호 폴백. codex 12k값과 검증값 구분 불가. → Entity/MatchedEntity에 source/verified/fallback/locale_confidence 필드 추가(하위호환).

4. **격리·플래그가 닫히지 않는 큐(폐루프 부재)** [extraction·orchestration·coverage] — typed-격리 candidate 73, scope:review 214, needs_disambig가 표면화만 되고 자동 해소·운영자 트리아지 경로 없음. 모니터링-온리 방침이 큐를 영구 정체시킴. → 주기 재검증 step + admin review-queue CLI.

5. **수렴 후 과처리(exhaustion 마커 부재)** [orchestration·enrich] — LocalFill reground·EnrichEmpty·PersonExtractor·QualityReview·OTT가 소진/쿨다운 마커 없이 동일 대상 무수익 재처리. → 각 select에 enrich_attempts 쿨다운 필터 통일.

6. **이중 진실원천/폐기 테이블 잔존** [collection·entity_quality] — admin이 폐기 kwave_entity_candidates 조회, kwave_persons가 entities와 450건 발산. → SSOT=kwave_entities로 일원화.

---

## §D. 통합 마스터플랜 (병합·우선순위 정렬)

코드레드(§A)·quick-win(§B) 외의 잔여 항목을 impact 순으로. 형식: 문제→수정→효과→공수/위험.

**고임팩트 (M-effort, 코드레드 보완)**

- **D-1. 소비자 엔드포인트 provenance 노출** [enrich P1+P3 / matching P4·P6 병합·근본원인③]: lookup/entities가 8 locale값을 source 없이 반환(codex=검증값 무구분) + verified_only가 all-or-nothing으로 행 드롭(recall 절벽, ja 44% 누락). → Entity struct에 `Sources map`, MatchedEntity에 `verified bool`·`locale_confidence`(source별 가중: wikidata=원값, media-consensus×0.9, llm-only×0.5) 추가, verified_only는 드롭 대신 플래그 노출. → 안전신호 유지하며 recall 회복+소비자 임계조정 가능. **M/low~medium.**

- **D-2. typed-격리 후보 데드레터 해소** [extraction P3 + orchestration P1 + coverage P5 병합·근본원인④]: candidate 73건 [kdb:q:typed]·source_domains 0이라 모든 자동 step 제외, 06-14부터 정체(수원특례시청·현대오일뱅크·전경련 등 실존). → typed-parked 전용 주기 재시도 step(7~14일 쿨다운, Wikidata+SearXNG 재검증 시 승급) + admin `review-queue list/promote/reject` CLI. → 미충족 검색수요 회복. **M/medium.**

- **D-3. 잠복중복 dedup + needs_disambig 백로그** [entity_quality P1·P2·P7 + coverage P6 병합·근본원인②]: 띄어쓰기/구두점/표기변형 동일엔티티(해피투게더 2행, 스트레이키즈 2행 등 11+11그룹)가 클러스터링 누락, active needs_disambig 10건(전부 실동일인)이 영구 미해소·서빙, canonical_en 충돌 15그룹은 sweep이 로그만. → `cluster.go` buildClusters에 정규화 canonical_ko 충돌·aliases_ko 공유 소스 추가→기존 gpt merge 경로 투입; 격리 전 enrich 1회로 birth/agency 확보 후 재평가; active 10건 admin 원클릭 merge. → 답변 결정성 회복·중복 제거. **M/low~medium.**

- **D-4. #1 엔드포인트 /v1/match 관측성** [coverage P3+P4 병합·근본원인④]: POST 본문 미로깅(request_log.go:28 RawQuery만), match는 0건도 200→hit/miss 측정 불가, match-miss가 발굴 큐 유입 0건(lookup-miss 35만). → matchEntities에 result_count 기록+0건시 source_text 해시/miss 로그, enqueueFromText 계측, 대시보드에 hit-rate·top-missed 추가. → 발굴 입력 10배 확대+커버리지 가시화. **M/low.**

- **D-5. song_album 권위소스 공백** [enrich P4]: K-pop 곡/앨범(352)이 cascade에 권위 API 0개(MusicBrainz=group/person, TMDb=영상만)→codex 의존(319/352). → orchestrator runMusicBrainz 게이트에 song_album 추가(release-group/recording 제목, ko앵커 매칭으로 오매칭 차단), 약하면 iTunes/Spotify 어댑터. → 곡 ja/zh 공식 현지표기 확보. **M/medium.**

**중간 임팩트**

- **D-6. locale별 2-매체 합의 임계 미달** [collection P3 + enrich P6 병합]: distinct parent≥2 필요한데 enabled locale당 실독립소스 1~2개+soompi 3 locale 중복→승격 정지. grounding 백필 수율 ~1.4%. → 1차는 CR-5 피드복원으로 자연해소; 희소 locale 한정 '빈칸+고신뢰(media_trust≥0.9)+charset통과 시 1-parent fill'(기존값 미덮어씀); buildLocalFillQuery에 locale별 매체도메인·대표작 힌트(gemma type힌트 7/7 입증). → 빈칸 보강·codex교체 재가동. **M/medium.**

- **D-7. 작품 한국어 제목 누락(한국어 매칭 실패)** [entity_quality P6]: canonical_ko가 영어뿐인 작품(drama 8·movie 3·show 4: Cheer Up=치어업 등)→한국어 기사 strpos 매칭 영영 실패. → sweep step으로 enrich 큐 적재→한국어 정식제목 canonical_ko 승격, 영어는 canonical_en로 이동(D.P. 등 공식 라틴제목 예외). **M/low.**

- **D-8. scope:review 214건 폐루프** [extraction P6 + orchestration P2 + coverage P8 병합·근본원인④]: 비-K 의심 플래그만 찍고 active 서빙 유지, 해소 경로 없음. → dataqa/codex 2차판정으로 확신 비-K→reject, 확신 K→[scope:ok], 불확실만 잔존(cycle당 소량+revert 로그). 일괄거부 금지(가수 강남 등 오삭제 방지). **M/medium.**

- **D-9. alias 대소문자 구분 recall 누락** [matching P5]: `strpos`가 case-sensitive라 'bts'≠'BTS'(라틴 alias 745건). → 라틴 alias에 `position(lower(alias) in lower($1))`, 한글은 기존 유지(CR-2 어절경계와 동시적용). **S/low.**

- **D-10. 오분류·비-K 엔티티 오답** [entity_quality P5 + enrich P8 병합]: drama 'JTBC'(방송사 환각)·movie 'WILD'(미국영화)·channel 'MBC FM4U' en='KBS 1TV'(교차오염), 인물이 drama로 분류돼 TMDb 오매칭. → sweep 가드(방송사코드인데 drama→channel 재분류), 교차-locale 정합체크, 작품타입+인물형 canonical_ko면 cascade 보류. **S~M/low~medium.**

- **D-11. miss 기사 발굴 부재** [extraction P5]: cheap_status='hit'만 추출→miss 46,756건의 신규 K-엔티티 미발굴(주석엔 발굴경로 명시, 미구현). → K화이트리스트 도메인 miss에 일일 상한 '발굴모드' 추출 패스(PreGate+charset 가드 내장, gatekeeper가 노이즈 흡수). **M/medium.**

**저임팩트/housekeeping**

- **D-12. raw 무한증가 prune** [collection P7]: kwave_rss_items_raw 61,904행, '7일보관' 주석과 달리 prune 코드 0건. → codex_status IN(ok,failed) OR cheap=miss이고 14일 경과분 일1회 삭제. **S/low.**
- **D-13. operator_locked 필드단위화** [entity_quality P4]: 행 전체 동결로 자동복구된 ja/zh leak까지 영구동결(나혼자산다 등 21건). → locked_fields[] 또는 자동 source 필드는 복구허용. **L/medium.**
- **D-14. kwave_persons 이중 SoT** [entity_quality P8·근본원인⑥]: 450건 발산, type 역강제. → SSOT=kwave_entities 명확화, 발산건 자동덮어쓰기→리뷰 플래그. **M/medium.**
- **D-15. research_queue context_hint 오염** [coverage P7]: 기사본문 통째 저장 343행(max 2221자). → EnqueueResearch에 left(...,200) 절단. **S/low.**
- **D-16. 한국어 피드 0개** [collection P8]: ko 발굴 앵커 비어 신규포착 외국기사 의존. → 신뢰 ko 피드 1~2개 재확보(발굴 앵커로만). **S/low.**
- **D-17. 분류 1차패스 Wikidata 그라운딩** [extraction P4]: 짧은 토큰이 차단된 뉴스검색 의존으로 오판. → 짧은/모호 토큰만 classify 앞에 WD 라벨 prepend(CR-1 일반화). **M/low.**

---

## §E. 권고 실행 순서
1. **오늘**: QW-1(42피드 재활성화, 오너) → 가장 큰 단일 ROI.
2. **1주차(quick-win 코드)**: QW-2(간체)·QW-3·QW-4(오거부)·QW-5·QW-7·QW-8(매칭)·QW-9~11(compute 회수) — 전부 S/low, 코드레드 3건 즉시 완화.
3. **2~3주차**: CR-1 게이트키퍼 본수정+백로그 CLI, CR-2 본수정(어절경계), CR-4 QID-pin+dataqa 작품, D-1 provenance 노출, D-4 관측성.
4. **이후**: D-2~D-8(큐 폐루프·dedup·합의임계·song_album), 잔여 housekeeping.

전 항목 코드/DB로 검증됨(추측 없음). 핵심 파일: `internal/kdbapi/api.go`(매칭·노출), `internal/kdb/agents/gatekeeper/agent.go`·`internal/kdb/autopilot/sweep.go`(오거부), `internal/kdb/enrich/orchestrator.go`(오염·codex), `internal/kdb/rss_poller.go`·`rss_discover.go`(수집), `internal/kdb/autopilot/agents.go`(과처리), `internal/kdb/agents/disambiguator/cluster.go`(dedup), `internal/kdb/dataqa/dataqa.go`(검수).