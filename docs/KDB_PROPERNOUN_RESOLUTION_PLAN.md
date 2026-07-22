# KDB 고유명사(비-person) 전면 해소 설계안 — 2026-07-22

오너 지시: "인물은 채워지는데 고유명사는 점점 안 채워진다. iTunes만으론 부족하고 드라마·영화·넷플릭스
등까지 조사해서, DB 내용을 파악하고 무엇을 어떻게 승격시킬지 고유명사 전부를 해결하는 개선안을 아주
철저하게." — 본 문서가 그 답이다. 모든 수치는 2026-07-22 라이브 DB·라이브 API 실측.

## §0. 진단 요약 (실측)

**정체 현황** — status=candidate 로 갇힌 비-person 고유명사 (7월 급증):

| 타입 | 정체 | hint보유 | 비고 |
|---|---|---|---|
| song_album | 499 | 94% | MB드레인이 /artist 전용 → 전원 type_mismatch 차단(30d 501건) |
| show | 258 | 78% | TMDb 시도완료 no_match 다수(한국 예능 미등재) |
| group | 218 | 71% | MB artist 시도중 no_match 124 — 오태깅+미등재 혼재 |
| event_tour | 174 | 99% | 승급 드레인 자체 없음 |
| unknown | 141 | - | 공연장·매체·시상식·행사 — [kdb:q:official] 격리 |
| character | 133 | 100% | 드레인 없음 + 백과항목 오염 다수 |
| agency | 85 | 79% | 드레인 없음 — 실존 기획사 다수(KQ·웨이크원·초록뱀…) |
| drama | 73 | 78% | TMDb 정확일치 실패(시즌 접미사·표기변형) |
| movie | 64 | 83% | 위와 동일 + 인물 오태깅 다수 |
| channel_outlet | 46 | 89% | 유튜브 채널·라디오 — 드레인 없음 |
| brand_place | 37 | 97% | 공연장·브랜드 — 드레인 없음 |
| term | 36 | 22% | 대부분 일반어/오태깅 — 정리 대상 |
| **계** | **1,764** | | 소비자 review 보류 563 키워드가 이 풀을 대기 중 |

6월+ 요청 active 전환율: movie 74%·drama 69%·person 43%(승급드레인 有) vs **song_album 20%·event_tour 7%**.

**구조 원인 3가지**
1. **승급 드레인의 타입 편중**: person(wdperson·KMDb·kenterhub)·group(MB /artist)·movie(KOFIC·KMDb·TMDb)·
   drama/show(TMDb)만 존재. song_album 은 MB 드레인에서 명시 type_mismatch, iTunes/Discogs 드레인은
   `status='active'` en채움 전용(승급 불가), iTunes 는 officialPromotionProviders 목록에도 없음.
   event_tour/character/agency/channel_outlet/brand_place 는 드레인 자체가 없음.
2. **뉴스근거 축(cand-evidence)의 사각**: 선별이 `precheck_status='review'` 큐 행 보유분만(풀의 ~25%),
   네이버뉴스 ≥2건 요구 — 백카탈로그(옛 곡·지난 투어)는 뉴스 없음 → 7일 쿨다운 무한루프
   (song_album 433/499 쿨다운 중, 매 사이클 "요청대기 candidate=0").
3. **판정 종결권 부재**: gatekeeper "common-noun review"·triage "[triage:kept]" → "운영자 검토" 데드엔드.
   type-retrace(오태깅 교정)는 auto_evidence 경로 생성분만 대상 — on-demand(lookup-miss) 발굴분(정체
   풀 전부)은 재판정 대상 밖. claude 최종판정 배치(DrainClaudeAdjudicate)는 구현돼 있으나
   `KDB_CLAUDE_ADJUDICATE_ENABLED=0` + active 전용 + **컨테이너에 claude 바이너리 없음**(no-op).

**풀 내용물 실측(전수/샘플 판독)** — 정체는 4종 혼합물이다:
- **A. 오태깅** (~30%): 라이언 고슬링·일론 머스크=movie, 주미란(배역)·크리스 마틴=group,
  리오넬 메시=character, 백지영=channel, 수라간(우물 백과항목)=character, 똥 밟았네(곡)=group …
- **B. 실존 K 작품 — 카탈로그에 있는데 매칭 실패**: 시즌 접미사("스위트홈 시즌2"·"파친코 2" — TMDb 는
  시즌을 별도작품으로 안 둠), 표기 변형("내 머릿속의 지우개" vs 카탈로그 "내 머리 속의 지우개"),
  아티스트 스코프 필요(NUMBER NINE 티아라 — hint 에 아티스트 명시됨에도 미활용).
- **C. 실존 K — 글로벌 카탈로그 자체에 없음**: 한국 예능(만원의 행복), 웹드/숏폼(루대숲·1도 없는 남자·
  집착 결혼), 콘서트/팬미팅/페스티벌(무한대집회V·사운드베리), 기획사(웨이크원), 유튜브 채널, 공연장.
  → 뉴스근거가 유일 축인데 §원인2 사각에 걸림. **네이버뉴스 문맥증강 쿼리 실측: 1도 없는 남자+티빙
  143건, 집착 결혼+숏폼 87건, 무한대집회V 57건, 아이스-깨끼+MC몽 32건 — 증거는 풍부하다.**
- **D. 비K/스코프 밖**: 귀멸의 칼날·토이스토리5·젠다야(외화·외국인), 포브스, 벤틀리 → 정책 종결 필요.

**라이브 커버리지 프로브 (2026-07-22)**
- song_album 무작위 20: iTunes(KR) 정규화일치 10/20, MB recording 6/20. 단 title-only 는 오염
  위험(NUMBER NINE→Allen Toussaint, Grenade→Bruno Mars) → **아티스트 스코프 게이트 필수**.
  MB recording `"SEXY LOVE"+artist:T-ara` → score 100 정확 매칭 확인.
- movie/drama/show 20(시즌제거+alt-title 관용 매칭): **7/20 즉시 회수** — 폭삭 속았수다(alt-title),
  킬러들의 쇼핑몰·스위트홈·무빙·SNL 코리아·피의 게임(시즌제거), 파친코(alt). 현행 정확일치 드레인은
  이 20건 전부 0/20 이었다.
- KMDb/KOFIC: "내 머릿속의 지우개" 미스(정식표기 "내 머리 속의 지우개" — 표기변형 확장 필요).
  KMDb 검색은 루즈매칭("찻잔처럼"→"마지막 찻잔" 1979) → 정확일치 게이트 유지 필수.
- Naver encyc: 예능/웹드 커버 약함(만원의 행복 미스). **뉴스축이 정답**.

## §1. 해결 아키텍처 — 4축 파이프라인

기존 원칙 불변: **승급=공식앵커 or 뉴스근거 판정, 추측=빈칸, 오거부 금칙**. 새로 만드는 것은
"에이전트에게 종결에 필요한 증거도구를 쥐여주는 것"이다.

```
[Phase 0 정화] ──► [Phase 1 카탈로그축] ──► [Phase 2 뉴스근거축] ──► [Phase 3 claude 종결자]
 오태깅 교정          MB-rec/iTunes           선별확대+문맥증강           양축 실패 잔존의
 시즌→부모 병합       TMDb 관용화             (웹드·예능·공연·기획사)      최종 판정(승급/기각)
 비K/일반어 기각      KMDb/KOFIC 변형         evidenced 승급             "운영자 검토" 데드엔드 제거
```

### Phase 0 — 전수 정화 (선행, 코드 0줄로 최대 효과)
- claude 배치 판독(34차 §5e 방식, 세션 직접)으로 정체 1,764건 전수: ①오태깅 retype(→person 등
  올바른 드레인으로 재라우팅) ②시즌 접미사 → 부모작 alias 병합(파친코 2→파친코) ③비K·일반어·백과항목
  → rejected 명시 종결(사유 기록, 소비자에 확정 응답) ④실존 K 확정 유지 + [p0:kept] 마커.
- 재발 방지: type-retrace 선별을 auto_evidence → on-demand 발굴분까지 확대(1줄 SQL).
- 예상: ~500-600건 정리(재분류·기각·병합), 잔존 ~1,100-1,200 이 Phase 1-3 대상.

### Phase 1 — 카탈로그 승급축 확장 (결정적 앵커)
1. **MB recording/release-group 드레인 신설** (`musicbrainz_drain.go` 확장):
   hint/기사에서 아티스트 추출(gemma 기존 기사맥락 판정 재사용) → `recording:"제목" AND artist:"X"`
   정확일치(score≥95+정규화 일치) → external_ref+승급. 아티스트 스코프라 흔한단어 제목도 안전.
2. **iTunes 승급 편입**: itunes_drain 을 candidate 로 확장 + officialPromotionProviders 에 'itunes'
   추가. 게이트: 트랙/앨범 정규화 일치 **AND** artistName 이 hint 아티스트와 일치(또는 active K-아티스트).
3. **TMDb SearchExactID 관용화**: ①시즌 접미사 정규화 → 부모작 매칭 시 부모 승급+시즌명 alias 편입
   ②alternative_titles 대조 ③유일성 요구 유지(동명작 보류 불변). 실측 0/20→7/20.
4. **KMDb/KOFIC 표기변형 확장**: 공백철거 비교 + gemma 정식표기 교정 1콜(예: 머릿속→머리 속) 후 재검색.
   매칭 게이트는 정규화 정확일치 유지(마지막 찻잔 오매칭 방어). KMDb 일100 쿼터 유휴 활용.
5. **Wikidata 작품 드레인**: 기존 범용 클라이언트로 song/album/TV/film 검색 + P31(유형)·P175(가수)·
   P57(감독) claims 게이트 — K-아티스트/K-제작 확인시에만 승급(wdperson allowlist 방식 재사용).
6. **Netflix ID 앵커 활성화**: ott.go 의 `/title/{ID}` 확인 시 external_ref 기록 추가 →
   승급앵커로 정상화(§2 결론 2). disney 동일. 미구현 시 promotion 목록에서 제거(신뢰토큰 정리).

### Phase 2 — 뉴스근거축 일반화 (카탈로그 없는 실존 K: 웹드·숏폼·예능·공연·기획사·채널·장소)
- cand-evidence 선별 확대: review 큐 행 요건 제거 → 전 candidate(요청대기 우선 정렬만 유지).
- **문맥증강 쿼리**: 제목 단독 ✗ → 제목+아티스트/플랫폼/작품(hint 에서 추출). 실측 근거 §0.
- event_tour 는 이 축에서 대부분 종결(이벤트=뉴스 풍부 실측 32~151건). 판정 통과 → evidenced 승급
  (기존 경로·revert 가능 dataqa_log 불변).
- 쿨다운 무한루프 제거: unclear/근거부족 2회 누적 시 Phase 3 큐로 이관(마커), 재시도 중단.

### Phase 3 — claude 종결자 상시화 ("운영자 검토" 데드엔드 제거)
- DrainClaudeAdjudicate 확장: 대상 active 플래그분 + **candidate 양축실패 잔존**. 3분기:
  K실존 확정→승급(근거 기록) / 비K·정크 확정→기각([adjudicated:claude] 감사기록) / 불확실→유지+보드 노출.
- 실행 형태(컨테이너에 claude 없음): 호스트 cron 배치(스크립트→claude CLI→SQL 적용) 또는 세션 주기
  판독. KDB_CLAUDE_ADJUDICATE_ENABLED=1 + AUTOREJECT 는 오너 결정.
- 골든셋 게이트(33차) 재사용: 판정 정확도 회귀 감지 하에서만 자동권한 유지.

### Phase 4 — 재인테이크 (엔티티 자체가 없는 보류)
- review 보류 중 no_entity 1,510 키워드를 fresh 레인(intake-autoverify)으로 재투입 — 위 개선이
  깔린 뒤라 재발 없이 흡수. candidate 563 대기분은 승급 시 closeWaitingReviewRows 로 자동 종결.

## §2. 커넥터 감사 결과 (코드 전수 + 라이브 확인, 2026-07-22)

| 커넥터 | 실체 | 키 | 타입 | 현재 배선 | 승급앵커? |
|---|---|---|---|---|---|
| TMDb | 공식 API(search/translations/alt_titles — **credits 미구현**) | v4 토큰 | movie·drama·show | CP드레인+enrich | ✅ |
| KOFIC/KMDb | 공식 API(영화 전용, 정확일치) | 有(KMDb 일100 유휴) | movie | CP드레인 | ✅ |
| MusicBrainz | 공식 API — **/artist만 구현**(recording·release-group 없음) | 무키 | group | CP드레인 | ✅ |
| iTunes | 공식 API(무키, song+album, country=KR) | 무키 | song_album | **active enrich만** | ❌ 미등재 |
| Discogs | 공식 API(release 검색) | 선택 | song_album | active enrich만 | ❌ |
| Wikidata | **범용 검색 클라이언트**(전 타입) — 드레인만 person 한정 | 무키 | 전 타입 | person드레인+enrich | ✅ |
| Naver | openapi encyc+news — **lookup 전용**(쓰기 없음) | 有 | 전 타입 | 검증축 | `naver-people` 죽은토큰 |
| **Netflix/Disney** | **API 아님** — SearXNG `site:netflix.com` 스니펫+gemma 추출. 로케일 라벨만 기록 | 무키 | drama·show·movie | 로케일 채움만 | **목록에 있으나 external_ref 쓰는 코드 전무 = 작동불능** |
| MyDramaList | HTML 스크랩(ja만, 수동 CLI 전용) | 무키 | drama·show·movie | autopilot 미편입 | ❌ |
| (등록만) | Spotify·Watchmode·Kakao·**YouTube** — apikeys 레지스트리에만 있고 클라이언트 없음 | - | - | dead | - |

**감사 결론**
1. `officialPromotionProviders` 의 `netflix`·`disney`·`naver-people` 은 external_ref 를 만드는
   코드가 없어 **영원히 발동 불가능한 신뢰토큰**이다(source_policy.go 자체 주석 "계획명은 신뢰토큰이
   되면 안 된다"를 위반). → 구현하거나 목록에서 제거해야 함.
2. 넷플릭스 앵커의 올바른 구현: ott.go 가 이미 ID 앵커드(`/title/{숫자ID}`) 스크랩을 하므로,
   **ko 제목 정확일치 확인 시 external_ref(provider='netflix', id=타이틀ID) 기록**을 추가하면
   승급 앵커로 살아난다(스위트홈3·폭삭 속았수다 등 넷플릭스 오리지널 직결). TMDb 실패분의 보완축.
3. Wikidata 클라이언트가 범용이므로 **작품 드레인**(P31 유형확인 + P175 가수/P57 감독 = K-아티스트
   게이트)은 신규 클라이언트 없이 구현 가능 — 과다허용 오염 교훈(person 20% 오링크)의 allowlist
   게이트 방식 재사용.
4. character 는 TMDb credits 미구현 + K-드라마 배역명의 카탈로그 부재로 **뉴스축+부모작 앵커**
   (기사에서 "작품명+배역명" 동시확인, 부모작이 active+ref 보유일 때 승급)가 정도(正道).
5. channel_outlet 은 YouTube Data API 키만 있으면 결정적 검증 가능(레지스트리에 자리 이미 있음) —
   오너 키 발급 결정 대기, 그 전까지 뉴스축.

## §3. 예상 효과 (보수적 추정)

| Phase | 대상 | 예상 종결(승급+정당기각) |
|---|---|---|
| 0 정화 | 1,764 전수 | ~500-600 (오태깅 재라우팅·비K 기각·시즌 병합) |
| 1 카탈로그 | song 실곡 ~350·영상물 ~150 | song ~220-250 승급, 영상물 ~100-120 승급 |
| 2 뉴스축 | event 174·웹드/예능 ~250·agency 등 ~150 | ~350-400 evidenced 승급 |
| 3 종결자 | 잔존 꼬리 ~200-300 | 전건 판정(승급 or 정직한 기각) |
| **목표** | | **정체 1,764 → 순수 확인불가 <100 (보드에 정직 노출)** |

person 대비 격차 해소: song_album 20%→70%+, event_tour 7%→80%+ (요청 전환율 기준).

## §4. 오너 결정 필요

1. **비K 작품 정책**: 귀멸의 칼날·토이스토리 등 — ①명시 기각(not_in_scope 응답) ②k_scope=false 로
   수용·서빙(번역수요는 실존). 권고: ② (소비자 기사가 실제로 언급 — 단 K 지표와 분리 집계).
2. **연극·뮤지컬 타입**: 현행 체계에 없음(event_tour/드라마로 표류). 권고: event_tour 로 통일 수용.
3. **유튜브 채널/팟캐스트 수용 범위**: 공식 아티스트 채널만 vs 팬/개인 채널 포함. 권고: 공식만.
4. **claude 종결자 자동기각 권한**(AUTOREJECT): 권고: 초기 2주 권고모드(마킹만)→검증 후 자동.
5. ~~KOPIS API 키 신청~~ → **완료(07-22)**: 키 발급·라이브 검증(정체 candidate "쇼맨쉽 시즌2" 정확
   반환·장르 대중음악/뮤지컬/연극 커버) 후 `kwave_kdb_api_settings` name='KDB_KOPIS_API_KEY' 저장.
   드레인 구현 시 `apikeys.Resolve(ctx, pool, "KDB_KOPIS_API_KEY")` + Specs 레지스트리 등록.
   → **event_tour 는 뉴스축 보조가 아니라 KOPIS 결정적 앵커가 주 경로로 승격** (Phase 1 에 편입).
6. YouTube Data API 키 발급 — channel_outlet 결정적 검증용(apikeys 레지스트리에 자리 기존재).

## §5. 구현 순서 (승인 시)

1. P0 정화 배치(claude 세션) + type-retrace 선별 확대 — 코드 1줄+배치, 당일.
2. TMDb 관용화(시즌·alt-title) — 실측 즉효 최대, tmdb.go+tmdb_drain.go, 반나절.
3. MB recording 드레인 + iTunes 승급 편입 — musicbrainz/·itunes_drain.go, 1일.
4. cand-evidence 선별확대+문맥증강(event_tour·웹드·character 부모작앵커) — candidate_evidence.go, 반나절.
5. Netflix ID 앵커 external_ref 활성화(또는 promotion 목록 정리) + KMDb/KOFIC 표기변형 — 1일.
6. Wikidata 작품 드레인 + claude 종결자 배선 — 1일.
7. 재인테이크 스위프 — 반나절. 각 단계 배포 후 §3 지표로 검증.
