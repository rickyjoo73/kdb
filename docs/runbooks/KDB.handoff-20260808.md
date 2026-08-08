# KDB 핸드오프 45차 (2026-08-08) — 라틴 원제 사각지대 개방 → 우연한 격리 해제 → 자기정정

44차 §3 이 "다음 세션이 여기서 시작하면 된다"고 넘긴 **CJK actionable 30건**을 실측했다.

**결론부터: 원본 부재가 아니었다. 가드 하나였다.**

한글 없는 원제 20건 중 **18건(90%)이 `canonical_ko ~ '^[ -~]+$'`(ASCII 전용) 조건
하나로만** 제외되고 있었다. 44차 §5 가 "세 경로를 전수 실측해 천장을 확정했다"고 쓴 것은
맞지만, 그 천장 **아래에 배선 구멍이 하나 남아 있었다.**

★이 세션을 관통하는 것: **가드가 막고 있던 것과 가드가 지키고 있던 것은 다르다.**
ASCII 전용 가드는 곡선따옴표를 잘못 막고 있었고(§1), 동시에 매큔-라이샤워 로마자를
우연히 잘 막고 있었다(§2). 하나만 보고 넓혔다가 다른 하나를 깨뜨렸다.

현재 `kdb-app:latinorigin-20260808-3`. 마이그레이션 0100~0109 적용 완료.
**커밋 2건 모두 미푸시다(§5).**

### 다음 세션이 가장 먼저 볼 것 (4줄)
1. **§2 를 먼저 읽어라.** 이 세션의 자기정정이고, 44차·43차와 같은 계열의 다음 판이다.
   메모리에 적어둔 규칙("큰 UPDATE 전 드라이런")을 한쪽에만 지켜서 생긴 일이다.
2. **CJK actionable 은 이제 22건이고 전부 한글 제목**이다 — 라틴 사각지대는 닫혔다. §1·§4.
3. **§3 의 미해결 3건**(44차에서 넘어온 2건 + MR en 5건)은 손 안 댔다.
4. 44차 §4.5 의 `correction-verified` 신뢰도 점검은 **세 세션 연속 미해결**이다.

---

## §0. 새 세션 첫 명령

```bash
cd /data/home2/kdb.aiinplanet.com
docker inspect kdb-app --format 'image={{.Config.Image}} ip={{range .NetworkSettings.Networks}}{{if .IPAddress}}{{.IPAddress}} {{end}}{{end}}'
# IP 는 반드시 172.19.0.240

# 경보 — actionable 만 보면 된다(44차 §3)
docker logs kdb-app --since 6h 2>&1 | grep "backlog-watch"

# ★mt-fill 로그는 앱 컨테이너에 없다. cron(매시 :20) 이 docker exec 로 돌린다.
tail -40 logs/locale-fill.log
```

빌드·배포는 44차 §0 그대로. ⚠ **`internal/kdb/romanize.go` 는 gofmt 준수 파일이다**
(`cmd/kdb/main.go`·`backlog_watch.go`·`tmdb/tmdb.go` 와 다르다) — 고칠 때 포맷 유지할 것.

---

## §1. ★가드 하나가 18건을 8칸씩 막고 있었다 (bb7d472)

44차가 넘긴 30건을 눈으로 읽고, 막힌 문자의 **코드포인트를 전부 뽑았다.** 이게 진단의
전부였다 — 집계만 봤으면 "원본이 없다"로 끝났을 것이다.

```
’ U+2019 곡선 아포스트로피  10건  It’s 2PM · Don’t Save Me · Time’s Tickin’ · ’Till I Die
á Ã æ  라틴확장             3건  DáFF · … IN SÃO PAULO · SYNK : COMPLæXITY
ẹ ề Ơ ư 베트남어            2건  Mẹ Ơi, Về Nhà · Truy Tìm Long Diên Hương
～ ［］ ♥︎ 표기기호          4건  One Last Day ～Japan Special Edition～
ل U+0644 아랍문자           1건  BE (Delلuxe Edition)   ← 이것만 진짜 오염
```

**곡선 아포스트로피는 K-pop 표제의 표준 표기다.** 그 한 글자 때문에 1건당 8칸
(ja/zh/zh_hant + en → 라틴 4종)이 영구히 막혀 있었다.

### 왜 블록리스트가 아니라 닫힌 허용목록인가

`BE (Delلuxe Edition)` 때문이다 — Deluxe 의 `l` 자리에 아랍문자 ل 이 박혀 있다.
허용목록이면 **목록에 없어서 자동으로** 기각된다. 0106 이 여는 목록으로 144칸을 망칠
뻔한 것과 같은 이유다(44차 §1).

보이지 않는 문자도 일부러 뺐다: NBSP(U+00A0)·narrow NBSP(U+202F)·줄바꿈(U+2028/9)·
**bidi 제어(U+202A–202E)**. 마지막 것은 표시 순서를 뒤집어 사람 눈과 저장값을 어긋나게
하는데, ل 혼입과 같은 계열이고 8개 locale 로 증폭되면 되짚기가 불가능해진다.

### 같은 가드가 세 곳에 있었다

`DrainLatinKoToCJK` 만 넓히면 en 만 차고 vi/es/id/pt_br 4칸은 그대로 빈다 —
`DrainRomanizeLatin`(romanize.go:62)과 `DrainReattributeRomanization`(:521)이 **en 쪽에
같은 ASCII 조건**을 들고 있었다. 셋을 한 상수(`latinOriginClass`)로 묶었다. Go 문자열
리터럴이 컴파일 시점에 실제 문자로 풀리므로 Go regexp 와 SQL(`~ $1`)이 같은 것을 쓴다.

### ★레인이 기각을 원장에 안 남기고 있었다

`DrainLatinKoToCJK` 는 성공(UPDATE)만 기록했다. 그래서 이 레인이 "못 채운다"고 판단한
건이 backlog-watch 에는 **영원히 "아직 어느 레인도 판정 안 함"** 으로 남는다.
**44차 §3 이 지표 쪽에서 잡아낸 병이 레인 안쪽에 그대로 있었다.**

`MarkLatinOriginRejects` 를 붙이고 `cjkFillLaneSQL` 에 `'latin-origin'` 을 추가했다.
사유에 **문제 문자를 그대로 적는다**:

```
latin-origin | foreign-script | 허용목록 밖 문자: ل=U+0644
latin-origin | type-excluded  | entity_type=term 는 승계 대상 아님
```

### 수확

| 지표 | 전 | 후 |
|---|---|---|
| CJK 셀 | — | **+51** (ja/zh/zh_hant 각 17) |
| en 셀 | — | **+14** |
| 라틴 4종 셀 | — | **+144 → 130**(§2 에서 14 되돌림) |
| `active-cjk-actionable` | 34 | **22** (전부 한글 제목) |
| `active-latin-gap` | 140 | **104** |

---

## §2. ★자기정정 — 넓히자 우연한 격리가 풀렸다 (40f32a7, 0109)

**예상 14칸이 144칸으로 나왔다.** 10배다. 그런데 로그의 `filled=144` 만 보면 "잘 됐다"로
읽힌다. 눈으로 읽고서야 알았다.

종전 ASCII 가드가 **의도치 않게** 하던 일이 하나 더 있었다: 매큔-라이샤워(MR) 로마자의
`ŏ/ŭ` 를 걸러 `canonical_en → vi/es/id/pt_br` 전파에서 빼고 있었던 것이다. 넓히면서
그 우연한 보호가 사라졌고, 한 번의 전파로 **5건이 4종 locale 에 퍼졌다.**

### 왜 되돌렸나 — 한 인물이 locale 마다 다른 이름이 됐다

더 신뢰되는 소스가 든 칸은 불가침이라 안 덮이고, **빈칸만 MR 로 찼다:**

```
양수경  en=Yang Su-kyŏng · vi/es=Yang Su-kyŏng · id/pt_br=Yang Soo Kyung(local-usage)
이수형  en=Yi Suhyŏng    · id=Lee Su-hyung(local-usage) · vi=Lý Tú Huỳnh(wikidata-label)
```

어느 로마자가 옳으냐 이전에 **이 불일치 자체가 결함**이다. 게다가 이 DB 의 표준은 개정
로마자이고(romanize.go 머리말: 송강호=Song Kang-ho), `안영민 → Yŏng-min An` 은 성·이름
순서까지 뒤집혀 있다.

0109 로 **14칸을 되돌리고**, 우연한 보호를 의도적 가드(`latinPropagateSQLGuard`)로 바꿨다.
`canonical_en` 자체는 안 건드렸다 — 다른 레인의 판단이다(44차 §4 와 같은 선). 빈칸으로
두면 en 이 고쳐지는 즉시 전파가 자동으로 다시 채운다.

### 교훈 — 메모리에 적어둔 규칙을 한쪽에만 지켰다

`kdb-proxy-metric-traps` 에 "큰 UPDATE 전 드라이런 표본을 눈으로 읽을 것"이라고 적어뒀고,
**ko 쪽은 지켰다**(18건 전수 확인 → `BE (Delلuxe Edition)` 이 걸러지는 것까지 봤다).
**en 쪽은 안 지켰다.** 같은 커밋 안에서 고친 두 번째 가드라 "같은 변경"으로 묶어 본 것이다.

★**한 커밋이 건드리는 가드가 둘이면 드라이런도 둘이다.** 44차 §1(검수 범위를 레인의 대상
컬럼으로 좁게 잡아 오염 규모를 1/3로 봤다)과 정확히 같은 형태 — 모수를 좁게 잡으면 좁게
잡은 만큼만 보인다.

---

## §3. 미해결 — 판정 대기

### 이 세션이 만든 것

1. **MR 로마자 en 5건**(안영민 `Yŏng-min An` · 양수경 `Yang Su-kyŏng` · 양현아
   `Yang Hyŏn-a` · 유성원 `Yu Sŏngwŏn` · 이수형 `Yi Suhyŏng`). 파생값은 되돌렸지만
   **en 자체는 그대로**다. 개정 로마자로 고치면 4종이 자동으로 찬다.
   ```bash
   docker exec kdb-db psql -U kdb -d kdb -c "
   SELECT canonical_ko, canonical_en, canonical_en_source FROM kwave_entities
    WHERE status='active' AND canonical_en ~ '[ŏŭŎŬ]';"
   ```
2. **비ASCII 하이픈 en 33건.** `시영준 → Si Young‑jun` 의 `‑` 는 U+2011(비분리 하이픈)이라
   `Si Yeong-joon`(local-usage)과 다른 문자열이다. 대부분은 정상 대시(`EXO PLANET #6 – …`)라
   전수 분류가 먼저다.
3. **ko 쪽 표기 오염 3건** — `2026 LE SSERAFIM TOUR ‘PUREFLOW’’`(따옴표 불균형) ·
   `2024 Hongseok Fanmeeting［Photo by Hongseok］`(전각 대괄호) · `Time’s Tickin'` /
   `Time’s Tickin’`(**아포스트로피만 다른 active 중복 2건**). 승계는 원문 그대로라
   값을 더 나쁘게 만들진 않지만, ko 를 고치면 8칸이 함께 고쳐진다.
4. **베트남어 제목 2건 승계.** `Truy Tìm Long Diên Hương` 은 zh 가 이미 `寻找龙涎香`
   (권위값)인데 ja 에는 베트남어 원제를 넣었다. 레인의 전제("라틴 원제는 CJK권도 그대로
   쓴다")는 `KBS Joy` 류로 실증한 것이지 **다른 자연어 제목으로 실증한 게 아니다.**
   prio7 이라 권위 현지제목이 들어오면 자동 승급하지만, 전제가 다른 집단인 건 사실이다.

### 44차에서 넘어온 것 (그대로)

- **§4 의 선행 레인 오염 2건** — `모범택시3` → 프랜차이즈 zh · `논스톱` 중복 2건.
- **43차 §4.5 `correction-verified` 신뢰도 점검** — **세 세션 연속 미해결.**
- **43차 §7-2 한국 인명 로마자화 40건** — 미착수. §3-1·§3-2 와 같은 계열이라 묶어서
  하는 게 낫다.

---

## §4. 남은 22건의 정체 — 전부 한글 제목이고 mt-fill 이 돌고 있다

라틴 사각지대가 닫히면서 남은 22건은 **전부 한글 제목**이다. mt-fill 영역이고,
**cron 이 매시 :20 에 정상 실행 중**이다(`scripts/kdb-locale-fill.sh`).

⚠ **mt-fill 로그는 `docker logs kdb-app` 에 없다.** cron 이 `docker exec` 로 돌려
`logs/locale-fill.log` 로 간다. 나는 앱 로그만 보고 "미배선"이라고 잘못 판단했다가
crontab 을 확인하고 정정했다. 다음 세션도 같은 함정에 빠지기 쉽다.

게이트는 정상이다(드라이런 실측 — 기각 사유가 전부 타당):
```
버림(bad-title) "2026 이승기 콘서트 – 기승전: 樂(락)" → "…騎乗前：樂（ロック）"
                (제목의 핵심인 '기승전: 樂'을 한자 뜻풀이로 오역)
버림(bad-title) "2026 변우석 아시아 팬미팅 …" → "2026便宇石アジア…であるソウル"
                (인물명 한자 오역 + 조사(である) 사용으로 문장형 표기)
```

**즉 22건은 시간이 지나면 판정된다 — 새 작업이 아니다.** 단 두 가지가 남는다:

1. **`term` 3건**(영웅시대·미소·스테이)은 mt-fill 이 타입으로 제외하는데 **기각을 원장에
   안 남긴다** — §1 에서 고친 것과 **같은 구조의 병**이다. 영영 actionable 로 남는다.
2. **원장 field 이름 불일치.** `cjkFillLaneSQL` 은 `'wd-locale'` 을 보는데, 원장에는
   `canonical_ja`(3,219) `canonical_zh`(4,078) 로 쓴 판정이 따로 있다. 이걸 레인으로
   인정하면 actionable 이 22 → 18 이 된다. **왜곡은 4건이라 급하진 않지만**, 누가 어느
   field 로 쓰는지 전수 확인이 안 된 상태다.

---

## §5. 이번 세션 커밋 — ★둘 다 미푸시

| 커밋 | 내용 | 푸시 |
|---|---|---|
| `bb7d472` | 라틴 스크립트 허용목록 확장 + 기각 원장(latin-origin) | ❌ |
| `40f32a7` | MR 전파 14칸 되돌림(0109) + 의도적 가드 | ❌ |

```bash
git push origin main
```

### 이 세션의 자기정정 1건

**가드 두 개를 한 커밋에서 고치면서 드라이런은 한쪽만 했다**(§2). 44차 §1·43차의
자기정정 3건과 같은 유형 — **보는 범위를 좁게 잡으면 좁게 잡은 만큼만 보인다.**

---

## §6. 오너 결정 대기

1. **§3-1 MR 로마자 en 5건**을 개정 로마자로 고칠지. 고치면 4종 20칸이 자동으로 찬다.
2. **§3-3 ko 표기 오염 3건** — 특히 `Time’s Tickin'` / `Time’s Tickin’` **중복 active
   2건**. 아포스트로피만 다른 중복은 DB 전체에 **14쌍**이 더 있다(공백 변형 포함,
   `스트레이 키즈`/`스트레이키즈` 등). 정규화 레인을 만들지 건별로 볼지.
3. **§3-4 베트남어 제목 승계**를 유지할지 — 레인 전제가 실증된 집단과 다르다.
4. **44차 §7 의 3건은 그대로 유효하다**(선행 오염 2건 · 인명 로마자화 40건 · 새 출처군).
   특히 44차 §5 의 **MusicBrainz 표본 100건 수율 측정**은 여전히 CJK 쪽에 남은 가장 큰
   미측정 지렛대다(song_album 458건).
