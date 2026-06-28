# KDB 미확보 다국어 DB 확보 로드맵 (2026-06-28, 8차원 멀티에이전트 리서치)

# KDB 미확보 다국어 DB 충전 — 실행 로드맵 (8차원 종합)

## 핵심 진단 (gap-profile 기반, 모든 우선순위의 출발점)
갭의 본질은 "빈칸"이 아니라 **"LLM이 채운 미검증 외국어 표기 ≈12,322셀 / 2,961 엔티티(54%)"**다. 진짜 NULL은 locale당 person 100~160·show 70~92로 적다. 두 음성신호가 전략을 결정한다.
- **음성신호1**: codex 셀이 같은 locale의 wiki langlink를 가진 경우 ≈0~6건. → Wikidata/Wikipedia **단순 라벨 재조회는 신규수율 거의 0**. 레버는 ① altLabel/aliases 미수집분 ② 동기화 이후 추가된 라벨 ③ **codex가 아예 못 만든 진짜 빈칸**뿐.
- **음성신호2**: 무QID codex 꼬리는 매체관측조차 없는 **source-starved** 구간(1,245엔티티, 42%) → 외부 권위소스에 존재할 확률 자체가 낮음.

따라서 전략은 **"외부에서 더 긁자"가 아니라 "이미 가진 앵커(QID·canonical_en·hanja·zh)에서 결정적 규칙으로 재속성하고, song_album·CJK person만 외부 API로 정밀타격"**이다.

---

## 즉시 착수 TOP 5  ((수율×벌크안전)/공수 최상위)

### 1. Latin-locale person 로마자/en 복사 재속성 (외부호출 0)
- **무엇을**: es·pt_br·vi·id person codex 셀(vi 1,227·es 741·id 998·pt_br 1,432 ≈ **4,400셀, 전체의 36%**)을 canonical_en/aliases_en 복사+검증 규칙으로 재속성. Latin표기 locale의 한국 인물명은 사실상 영문 로마자와 동일.
- **갭**: codex 셀의 최대 덩어리. QID보유율도 높음(pt_br 48%·vi 46%).
- **통합**: 내부 codify 규칙. **handoff 18차의 codex→tmdb 영문복사 재귀속 선례 그대로**. 공수 S.
- **커버**: high. 단번에 갭의 1/3 해소.
- **벌크안전**: 신규 외부호출 0 = 차단위험 0. vi 성조·pt 발음변형은 예외 플래그.
- **source**: `romanization`(또는 canonical_en 재귀속), verified_only=false 마킹.

### 2. OpenCC zh → zh_hant 결정적 변환 (외부호출 0)
- **무엇을**: 보유 zh(간체) 라벨을 OpenCC `s2t`로 zh_hant 파생. zh_hant 1,625셀(전 locale 중 2위)을 거의 공짜로.
- **통합**: opencc-data 사전 로드 후 순Go 최대매칭(또는 cgo). 공수 S.
- **커버**: high — zh 보유 엔티티 전부.
- **벌크안전**: 오프라인 결정적 = 무한벌크 안전.
- **주의**: 고유명사 과변환 방지 위해 **s2twp(대만관용어) 금지, s2t 보수 변환만**. 빈칸>틀린값.

### 3. Wikidata 덤프에서 hanja(P1814) → zh_hant + OpenCC → zh
- **무엇을**: 한국 인물의 한자 본명(Wikidata P1814 한자표기·이미 연동된 KMDb)을 zh_hant에 직접 채우고 OpenCC로 zh 파생. **합성 아닌 정답**.
- **갭**: CJK person(zh 568·zh_hant 851 codex) 중 한자명 보유 인물 — 가장 어려운 구간을 정밀도 최상으로.
- **통합**: latest-all.json.gz 로컬 해시조인(보유 QID셋 필터). 추가 외부요청 0. 공수 S(덤프 처리 인프라 있으면).
- **커버**: high (한자명 알려진 중장년 배우/감독·전통작품). 아이돌 한글예명은 미적용 → 빈칸 유지.
- **벌크안전**: 덤프 단일 GET = 차단 무관(safe).

### 4. iTunes Search API — song_album ja/zh 정밀타격 (무키)
- **무엇을**: `itunes.apple.com/search?country=jp|tw|hk|cn&entity=song|album` → 스토어프론트 공식 현지표기. **라이브검증됨**: jp→ja, tw/hk→zh_hant, cn→zh_hans.
- **갭**: **song_album codex 316건(앵커율 7%, 최난구간)**. Wikidata/TMDb/MB에 없는 음악 작품의 ja/zh를 유일하게 뚫음.
- **통합**: ott.go의 ID앵커+throttle 패턴 그대로. `lookup?id=<trackId>&country=jp` 재조회로 동명이인 방지. **non-ASCII 가드 재사용(영문 leak 차단)**. 공수 M.
- **커버**: ja high(일본 발매분), zh_hant medium. vi/es/id/pt_br는 라틴유지라 low.
- **벌크안전**: **약 20 req/min — 벌크 절대금지**. 기존 넷플릭스 10초 pacing 재사용. 무키·무계약.

### 5. RR 라이브러리 임베드 (#1·CJK가타카나의 엔진) + MusicBrainz alias 확장
**5a. koroman/korean-romanizer-go**: 한글 표제어 → RR 라틴. #1에서 canonical_en이 없는 person의 Latin 4 locale 생성 엔진. **koroman**(발음규칙 풀구현, Python 사이드카로 기존 gemma/codex 파이프라인 재사용, M)을 품질 1순위, **korean-romanizer-go**(순Go·cgo불요, S)를 인라인. 오프라인=벌크안전. user-dict로 검증완료 인명 우선매핑.
**5b. MusicBrainz release-group/recording alias 확장**: 현 client.go는 artist만 → `/release-group/{mbid}?inc=aliases`·`/recording/{mbid}?inc=aliases` 추가(locale 태그 alias). `mbLocaleToKDB()`·`matchesQuery()` 그대로 재사용. **이미 prod인 코드 수시간 확장**, song_album ja/zh 보조. 공수 S. 단 search는 alias 미inline → 2-step(search→lookup) 필수.

---

## 2차 웨이브 (TOP5 후 잔여 꼬리)

| 방법 | 갭 | 통합 | 커버 | 벌크안전 |
|---|---|---|---|---|
| **MediaWiki langlinks** (ko.wikipedia 문서제목) | QID없지만 ko위키 문서 있는 niche drama/film/idol의 ja/zh/vi/es/id/pt 제목 | M | medium-high (꼬리의 유일 정공법) | UA+maxlag=5+50건/batch+1req/s = safe준함. zh_hant 분리는 langlink 불가→덤프로 |
| **VIAF cluster + dump** | CJK person 한자/가나 변형명(무앵커 person 534) | M | medium (배우/감독 hit, 신인 miss) | 덤프=safe. P214로 안전연결 |
| **MyDramaList alt_titles** (공식 API) | drama/movie ja/zh/zh_hant 대체제목 | M | high (niche 드라마) | 공식키+throttle=safe. **키 발급 가부 직접 확인 필요** |
| **Wikidata altLabel 재마이닝(덤프)** | 작품 QID분(drama 80%·movie 84%) 별칭 | S | medium (라벨 아닌 altLabel만) | 덤프=safe |
| **kpopnet.json (CC0)** | 일본인 K-pop 멤버 ja 한자명 핀포인트 | S | medium(좁음) | 단일파일=safe |
| **per-locale SearXNG+gemma (LocalFill 확장)** | source-starved 무앵커(song_album 294·brand_place·character·event_tour) | M | medium | **scrape-risky → 소량 on-demand만, 우선순위 최후** |

---

## 중복 병합 결정
- **Wikidata 일가**(SPARQL·wbgetentities·REST·EntityData·RDF·WDumper·DBpedia): **전체 백필=JSON 덤프(safe)**, **델타=wbgetentities(50/batch+maxlag)**, **QID없는 꼬리=langlinks**. REST/EntityData는 on-demand 단건만. DBpedia·WDumper는 보조.
- **Apple 음악**: iTunes Search(무키) 채택. **Apple Music API($99/yr+JWT, L)는 ISRC앵커 대량재번역 필요시에만** — 현재 불요.
- **RR 라이브러리**: koroman(품질)+korean-romanizer-go(Go인라인) 2종. ICU·uroman·osori는 폴백/제외.
- **VIAF**: 3차원 중복 → 덤프 1개로 통합.
- **MDL**: 공식 API만. kuryana/kastden 스크래퍼는 risky 대안.
- **MusicBrainz**: API alias 확장 우선(S), 풀덤프(L)는 페이싱이 느릴 때만.
- **명시적 제외**: Spotify·Last.fm·Korean DSP(Melon/Genie/Bugs/VIBE)·Naver(영화API 폐지)·Daum·KMDb·KOBIS·CJ/방송사 페이지 — 타깃 7 locale 0% 또는 현지화 미반환. (Spotify는 ISRC 크로스워크 용도로만 잔존 가치.)

## 현 연동(Wikidata/TMDb/MB/OTT)으로 안 뚫리는 꼬리 → 무엇이 뚫나
- **song_album(앵커 7%)**: → **iTunes Search**(ja/zh) + **MusicBrainz alias 확장**(보조). 무앵커 294건 잔여는 SearXNG 소량.
- **CJK niche person 한자/가나**: → **hanja P1814 재속성**(정답) + **VIAF**(변형명) + ja가타카나 RR합성(근사·플래그).
- **무QID·무매체관측 1,245 엔티티**: → 외부 권위소스에 존재 확률 낮음. **LocalFill(SearXNG+gemma) on-demand**가 유일 경로, 비용↑·최후순위. **상당수는 빈칸 유지가 정답.**
- **Latin-locale person 대량**: 외부 불필요 — **내부 로마자 재속성**으로 해결(현 연동의 "꼬리"가 실은 합성 과잉이었던 것).

## 음역(algorithmic) 허용선 — 빈칸 > 틀린값
- **허용(고신뢰, 권위표기와 사실상 일치)**: es/id/pt_br/vi person 로마자(RR=현지통용), zh→zh_hant OpenCC s2t, hanja→zh_hant. source=`transliteration`/`romanization`, verified_only=false.
- **조건부(근사·게이트 필수)**: ja 가타카나 RR합성은 발음음차라 근사치 → **권위소스(TMDb ja/넷플릭스/iTunes) 부재 시에만**, gemma 사후검증 후 채택. 작품제목 NLLB MT는 고유명사 환각 위험 → round-trip+gemma 게이트 통과분만.
- **금지(빈칸 유지)**: **Hangul→zh 한자 합성**(1:다 모호, 오염 리스크 최대). zh 한자는 권위소스/hanja에만 의존. song_album 제목 음역도 무의미(공식 현지판 제목이어야 함).
- **품질감사 레버**: LaBSE 임베딩으로 기존 12,322 codex값 ↔ Wikidata alias 교차정렬해 불일치분 자동 강등 — provenance/verified_only 체계와 직결, 오너의 "정직한 가시성" 방침 부합.

## 벌크 차단 주의 종합
- **safe(무한벌크)**: Wikidata/MusicBrainz/VIAF/Discogs **덤프**, OpenCC·RR·LaBSE·NLLB **오프라인**, kpopnet.json 단일파일. → 대량 백필은 전부 이 경로로.
- **api-ratelimited(throttle 필수)**: iTunes ~20/min, MusicBrainz 1/s, Deezer 50/5s, MediaWiki(UA+maxlag=5+50/batch+gzip+1req/s+429 backoff), MDL 공식키. → **넷플릭스 10초 pacing 패턴 재사용**.
- **scrape-risky(소량 on-demand만·오너 방침)**: SearXNG/LocalFill, kuryana, kastden, Namuwiki, HYBE/SM 일본사이트.
- **금지(즉시 IP밴)**: Google/Naver SERP 스크래핑, 무UA/blank-UA Wikimedia 요청, Wikipedia HTML 대량렌더.

## 권장 실행 순서
1주차: TOP5 #1·#2·#3(전부 외부호출 0, 갭의 ~50% 해소) → 2주차: #4 iTunes + #5b MusicBrainz(song_album) → 3주차: langlinks·VIAF·MDL(꼬리) → 상시: LaBSE 감사로 잔존 codex값 강등/승격. **OTT autopilot 편입 결정 대기 항목과 동일하게, iTunes도 throttle 확정 후 autopilot 편입 검토.**