# KDB 운영 노트 — 세션을 넘어 반복해서 쓰이는 사실들

핸드오프(`KDB.handoff-*.md`)가 **한 세션에 무엇을 했는가**의 기록이라면, 이 문서는
**여러 세션에 걸쳐 반복해서 참조되는 사실과 함정**만 추린 것이다. 핸드오프는 차수가
쌓이면 아무도 42차까지 거슬러 읽지 않는다 — 그래서 "다음에도 또 필요할 것"만 여기 남긴다.

> **출처**: 이 문서는 에이전트 메모리
> (`.claude/projects/-data-home2-kdb-aiinplanet-com/memory/*.md`, gitignore 대상)의
> 내용을 팀이 읽을 수 있게 옮긴 것이다. 그쪽은 매 세션 자동 로드되는 실동 파일이라
> 지우거나 옮기면 안 된다. **둘 중 하나만 고치면 갈라진다** — 내용을 바꿀 때 양쪽을 함께
> 볼 것. 최종 갱신 2026-08-09.

---

## 1. 서버는 KDB 전용이 아니다 — 부하 보고가 오면 테넌트부터 분리하라

`/data/home2/kdb.aiinplanet.com` 호스트는 **8코어 멀티테넌트**다. kdb 외에 mediafine ·
issuetalk · trendbiz · kenterhub · nbntv · edipresso · hobbyissue · tdb 등이 같은 머신에 있다.

**KDB 평시 기준선** (2026-08-08 실측, 10초 지속 표본)

| 컨테이너 | CPU |
|---|---|
| kdb-app | 0.22% |
| kdb-db | 0.26% |
| kdb-searxng | 1.23% |
| **합계** | **1코어의 1.7% = 서버 전체의 0.2%** |

평시 load average 2.17 / 8코어 = 27%.

**왜 적어두나:** "kdb 실패로 서버 부하, CPU 높음" 보고를 받아 점검했는데 **KDB 가 이 서버에서
가장 가벼운 축**이었다. 진짜 소모원은 전부 KDB 밖이었다 — 최대는 root SSH 세션에 남겨진
무한 스핀 루프(`until [ $(wc -l < f) -gt $(wc -l < f) ]` — **자기 자신과 비교라 참이 될 수
없다**), 그다음이 mediafine · issuetalk 컨테이너의 `go run`.

**적용법**

- 부하 보고가 오면 **`docker stats` 를 컨테이너별로 먼저 찍어 테넌트를 분리**한다.
  보고에 프로젝트 이름이 들어 있다고 그 프로젝트가 원인인 게 아니다.
- **순간값이 아니라 누적 CPU 시간**(`ps -eo pid,time --sort=-time`)으로 정렬한다.
  순간값은 psql 쿼리 하나에도 흔들린다(실제로 kdb-db 가 0.06% ↔ 11% 로 요동쳤다).
- `docker stats --no-stream` 1회는 스파이크를 잡는다. **10초 이상 표본**을 봐라.
- 증상이 CPU 가 아닐 수 있다. 저 보고 때 KDB 의 진짜 실패는 **검색 엔진 7/10 사망**이었고
  CPU 로는 1.23% 였다.
- ⚠ **컨테이너 로그는 UTC, 호스트는 KST.** `docker logs` 본문 타임스탬프를 그대로 읽고
  "9시간 전에 멈췄다"고 오판하기 쉽다 — `docker logs --timestamps` 로 대조할 것.
- ⚠ **mt-fill 로그는 `docker logs kdb-app` 에 없다.** cron(매시 :20)이 `docker exec` 로
  돌려 `logs/locale-fill.log` 로 간다. 앱 로그만 보고 "미배선"이라 오판하기 쉽다.

---

## 2. CJK 채움 천장 — 어디까지가 부재이고 어디부터가 배선 구멍인가

### 소진이 실측된 경로 (다시 파지 말 것)

| 경로 | 상태 | 근거 |
|---|---|---|
| 위키데이터 레이블 | 소진 | 앵커 보유 207건 전수 → 순증 **7칸**. zh 레이블 96건 중 66건이 로마자·한글 복사본(`체리블렛→Cherry Bullet`) |
| TMDb | 소진 | 410건 처리 → 유효 앵커 22건(**수율 5%**). 무매칭 384건은 편성 시간대(`SBS 금토드라마`)·코너명이라 애초에 엔트리가 없다 |
| 구글번역 + 게이트 | 소진 | 이미 다 돌았다(gtranslate ja 1,888 · zh 2,664칸). gemma 게이트가 2,232건 기각. **오너 결정(08-07): 게이트 유지** |
| **MusicBrainz** | **소진(08-08 측정)** | zh 빈칸 표본 30건에서 **MB 매칭 93%인데 CJK 별칭 0%** |

**MusicBrainz 는 머리에만 있고 꼬리에는 없다.** `BTS→防弾少年団` `소녀시대→少女時代` 는
있지만 그 머리는 이미 채워져 있고, 방탄소년단·트와이스·블랙핑크는 **operator-locked 로
`BTS`/`TWICE`/`BLACKPINK`** 다 — 오너가 한자 번역이 아니라 라틴 브랜드명을 잠갔으므로
MB 값을 가져오면 그 결정을 덮는다. **레인을 만들 근거가 없다.**

### zh 소스 실측 분포 (2026-08-08)

gtranslate 28.5% · wikidata-label 22.3% · tmdb 14.8% · wikipedia-zh-variant 11.9% ·
codex 8% · romanization 6.4% · **searxng 경로 3.2%** · musicbrainz 0.2%

두 가지를 오해하기 쉽다.

1. **`wikipedia-zh-variant`(11.9%)는 새 출처가 아니다** — 기존 한자값의 간↔번 변환이라
   zh 가 아예 없는 건은 못 채운다. 획득이 아니라 파생이다.
2. **searxng 의 baidu/sogou 가 CAPTCHA 로 죽어도 CJK 병목이 아니다** — 3%다.

### ★그러나 "소스 없음"으로 닫기 전에 레인의 선정 조건부터 읽어라

천장은 맞았지만 그 **아래에 배선 구멍이 셋** 있었다(2026-08-08 하루에 발견·수정).
전부 "채울 수 없어서"가 아니라 **선정 쿼리가 안 집어서** 비어 있었다.

| # | 구멍 | 원인 |
|---|---|---|
| ① | 라틴 원제 18건이 8칸씩 | `canonical_ko ~ '^[ -~]+$'` (ASCII 전용). 최다 원인은 `It’s 2PM` 의 **곡선 아포스트로피 U+2019** — K-pop 표제의 표준 표기다 |
| ② | `term` 11건이 영구 미판정 | mt-fill 이 타입으로 제외만 하고 **판정을 원장에 안 남김** |
| ③ | 라틴 `zh` 의 `zh_hant` 영구 빈칸 | OpenCC 가 한자를 요구. 변환이 아니라 **항등**이 정답(`Wavve` 는 번체도 `Wavve`) |

결과 `active-cjk-actionable` **34 → 0**.

**네 번째를 찾으려면** 각 레인의 `~ '[...]'` 스크립트 조건과 `entity_type` 필터를 훑고
**"이 조건에 안 걸리는 건은 누가 판정하나"** 를 물어라.

### "레인이 판정했다"와 "레인이 판정을 기록했다"는 다르다

레인이 성공(UPDATE)만 기록하고 기각을 안 남기면, 못 채운 건이 경보에 영원히 "미판정"으로
남는다. **새 CJK 레인을 만들면** (a) `cjkFillLaneSQL` 에 field 를 추가하고
(b) **기각도 원장에 남겨야** 한다. `fill_input_hash` 가 `entity_type` 을 포함하므로(0104)
타입제외 판정은 타입이 고쳐지면 자동으로 풀린다 — 낙인 걱정 없이 남겨도 된다.

### 경보 읽는 법

`active-cjk-gap-raw`(~1,900)가 아니라 **`active-cjk-actionable`** 을 볼 것. raw 의 98%는
판정이 끝난 원본 부재분이다. `wd-locale` 이 `nothing-new` 만, `tmdb-anchor` 가 `no-match`
만, `mt-fill` 이 `bad-title` 만 쌓는 건 **정상**이다.

agency · channel_outlet · brand_place · character 는 권위 출처 자체가 없으니 기계번역을
붓지 말 것 — 오너 원칙 **"빈칸 > 틀린값"**.

**미착수 지렛대 하나**: `source_urls` 에 zh.wikipedia 문서를 이미 갖고 있는데
`canonical_zh` 가 빈 active 엔티티가 **68건**이고 문서 제목이 곧 zh 값이다
(`엠블랙 → MBLAQ`, `베이비복스 → Baby V.O.X`). 외부 호출 0. 단 `GOOD_DAY_(組合)` 처럼
동음이의 괄호가 붙은 게 있어 닫힌 목록으로 처리해야 한다.

---

## 3. 대리지표 함정 — 이 저장소가 반복해서 밟은 것

집계 숫자는 **모든 경우 그럴듯했다.** 틀렸다는 건 개별 값을 눈으로 봐야만 보였다.
`filled=144` 는 그 자체로는 성공처럼 읽힌다.

### 같은 유형의 반복 (전부 실제 사고)

1. **sitelink ≠ label** — 위키백과 *문서* 만 세고 위키데이터 *레이블* 을 안 세서 확보
   가능량을 3배 낮게 봤다(ja 40 → 실제 148).
2. **레이블의 존재 ≠ 유효성** — "zh 112건 확보 가능"이라 했는데 96건 중 66건이 로마자·
   한글 복사본. 실제 확보분 1건.
3. **별칭 일치 ≠ 동일성** — 앵커 가드가 위키데이터 별칭까지 인정해 **다른 대상의 이름**을
   4건 써넣었다(`바이브`←네이버 VIBE, `DK(SEVENTEEN)`←Dplus Kia 구단,
   `제이(ENHYPEN)`←DAY6 제이 박).
4. **alt 표기 일치 ≠ 동일성** — 3번을 고치고 **하루 만에** 다른 소스에서 재발. TMDb 가
   시즌을 항목 하나로 접고 각 시즌 제목을 alternative_titles 에 넣기 때문에, 시즌
   엔티티가 프랜차이즈 항목에 붙었다.
5. **검수 범위도 대리지표가 된다** — 레인의 *대상* 컬럼(ja/zh)만 봐서 "오염 2칸"으로
   셌는데, 그 앵커를 소비하는 레인이 **7개 로케일**을 채우고 있어 실제로는 6칸이었다.
6. **가드가 막던 것 ≠ 가드가 지키던 것** — ASCII 전용 가드를 넓혔더니 그 가드가 **우연히**
   격리하던 매큔-라이샤워 로마자(ŏ/ŭ)가 풀려 14칸이 오염됐다. 한 인물이 locale 마다 다른
   이름이 됐다(`양수경`: vi/es=`Yang Su-kyŏng` vs id/pt_br=`Yang Soo Kyung`).
7. **"개선 가능 N건" ≠ "N건을 고쳐야 한다"** — 세 항목을 처리하는데 **셋 다 드라이런이
   "하지 마라"고 답했다.** zh_hant 368건 덮어쓰기는 값이 바뀌는 6건이 전부 나빠졌고
   (`씨스타 SISTAR→Sistar` — 공식이 대문자다), 비ASCII 하이픈 32건은 30건이 정상 표기였다.
   **검증 소스가 더 높다고 값이 더 나은 게 아니다.**

### 적용법

- **큰 UPDATE 전에는 반드시 드라이런하고 표본을 읽는다** — 건수만 보지 않는다.
  **한 변경이 건드리는 가드가 둘이면 드라이런도 둘이다.** 결과 건수가 예상과 자릿수로
  다르면(14 → 144) 그건 성공이 아니라 **먼저 읽어야 할 신호**다.
- 새 레인이 처음 쓴 값은 양이 적을 때 **전건 검수**한다(11칸 중 4칸이 틀렸다).
- "X 가 있다"와 "X 를 쓸 수 있다"를 같은 쿼리에서 세지 않는다.
- **별칭·alt 표기·짧은 활동명으로 일치한 앵커는 무조건 의심한다** — 네 번 밟았다.
- 검수 쿼리의 컬럼 범위를 **하위 소비자 기준**으로 넓힌다.
- 가드를 넓히기 전에 **"이 조건이 우연히 걸러주던 게 뭔가"** 를 따로 묻는다. 통과시킬
  것 목록만 만들면 안 보인다.
- 값을 지어내 근거 있는 값을 덮지 않는다. 승격할 근거가 없으면 **남긴다.**

### 보이지 않는 문자는 쓰는 쪽에서도 보이지 않는다

폭 0 문자(U+200B/200E/202C/FEFF 등)를 조회하려고 정규식 문자클래스에 **리터럴**을 넣었다가
일반 공백이 섞여 **수천 건 오탐**을 냈고, 그래서 "코드포인트로 다뤄라"고 주석에 써놓고는
정작 Go 소스에 리터럴 BOM 을 넣어 **컴파일을 깨뜨렸다**(Go 는 소스의 리터럴 BOM 을 거부한다).

**SQL 이든 Go 든 항상 `chr()` · `\uXXXX` 로 다룰 것.**

실해는 조용하다 — 보이지 않아 눈으로는 못 찾는데 문자열 비교가 어긋나 중복 탐지 · 별칭
매칭 · 소비자 검색이 전부 빗나간다. 유입 차단은 `IsValidSpellingForLocale`
(외부 소스 쓰기 경로의 공통 관문)에 들어 있다.

---

## 4. 빌드 · 배포

**빌드** — 호스트에 `go`/`gofmt` 가 없다. 컨테이너에서 돌리고 `-buildvcs=false` 를 붙인다
(마운트 소스에서 VCS 스탬핑이 실패한다).

```bash
docker run --rm -v "$PWD":/src -w /src \
  -v kdb_go-mod-cache:/go/pkg/mod -v kdb_go-build-cache:/root/.cache/go-build \
  -e PATH=/usr/local/go/bin:/usr/bin:/bin golang:1.23-bookworm sh -c '
  go build -buildvcs=false ./... && go test -buildvcs=false ./internal/...'
```

⚠ `cmd/kdb/main.go` · `internal/kdb/backlog_watch.go` · `internal/kdb/tmdb/tmdb.go` 는
**원래부터 gofmt 미준수**다(기존 주석 블록·import 순서). 전체 포맷하지 말 것.
`internal/kdb/romanize.go` 는 반대로 **준수 파일**이라 고칠 때 포맷을 유지해야 한다.

**배포** — compose 파일을 **둘 다** 넘긴다. `-f` 하나만 쓰면 override 가 자동 로드되지
않아 `dockers_backend` 정적 IP **172.19.0.240** 을 잃고 nginx 오라우팅이 난다(23d8be8).

```bash
sed -i 's/^KDB_APP_IMAGE=.*/KDB_APP_IMAGE=kdb-app:<tag>/; s/^KDB_BUILD_VERSION=.*/KDB_BUILD_VERSION=<tag>/' .env
docker build -q -f Dockerfile.kdb-app -t kdb-app:<tag> .
docker compose -f docker-compose.kdb.yml -f docker-compose.override.yml up -d kdb-app
docker inspect kdb-app --format '{{.Config.Image}} {{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}'
```

`KDB_APP_IMAGE`(compose 가 읽음)와 `KDB_BUILD_VERSION`(health 가 읽음,
`kdbapi/api.go:748`)은 **별개 변수**다. 후자를 안 바꾸면 health 가 옛 버전을 보고한다.
