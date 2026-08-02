# KDB 핸드오프 42차 (2026-08-02) — 배포 파이프라인 복구 + 41차 이후 문서공백 메움

41차(07-31) 이후 **배포 6회·커밋 5건이 어느 핸드오프에도 없었다.** 그 사이 CI 는 08-01
부터 죽어 있었고 아무도 몰랐다. 이번 세션은 새 기능이 아니라 **가동 상태와 문서를
실제에 맞추는 작업**이다. §2 가 문서공백을 메운 부분이고, §3 이 이번에 새로 고친 것이다.

그리고 41차 §6-1 이 1순위로 지목한 `active-no-anchor` 4,415건을 전수 계측했더니
**유입 문제가 아니라 티어 기계의 결함 2건**이었다 — §8 이 이번 세션의 본체다.

현재 `tier-20260802-2`. 커밋 4건 전부 푸시, CI 그린.

---

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /home/kdb.aiinplanet.com          # ★경로 이전됨 (구 /data/home2, 41차 문서 기준)
curl -s -m 5 http://127.0.0.1:9100/v1/health   # version=tier-20260802-2

# ★최우선 — 커버리지 계측. 기동 20초 후 1회 + 이후 6h 주기.
docker logs kdb-app 2>&1 | grep backlog-watch | tail -8
#  ⚠ 가 붙은 줄 = 그 조건을 담당하는 레인이 자기 백로그를 못 덮고 있다는 뜻.
#  oldest_days 가 세션마다 커지면 그 레인은 사실상 죽어 있다.

# ★앱 로그는 이제 KST 다(42차). 41차까지의 로그는 UTC 라 9시간 어긋나 있으니
#  과거 로그와 대조할 때 주의. 호스트·cron·kdb-db 는 원래부터 KST.

# 로그가 비었으면 DB 직접 조회 폴백
docker exec kdb-db psql -U kdb -d kdb -c "
SELECT 'candidate-stuck' c, count(*) n, COALESCE(EXTRACT(day FROM now()-min(COALESCE(last_enriched_at,created_at)))::int,0) oldest_d
  FROM kwave_entities e WHERE status='candidate' AND operator_locked=false
UNION ALL SELECT 'active-cjk-gap', count(*), COALESCE(EXTRACT(day FROM now()-min(updated_at))::int,0)
  FROM kwave_entities e WHERE status='active' AND (COALESCE(canonical_ja,'')='' OR COALESCE(canonical_zh,'')='' OR COALESCE(canonical_zh_hant,'')='')
UNION ALL SELECT 'active-no-anchor', count(*), COALESCE(EXTRACT(day FROM now()-min(updated_at))::int,0)
  FROM kwave_entities e WHERE status='active' AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id) AND COALESCE(array_length(source_urls,1),0)=0"

# 상시 레인 4종
tail -2 logs/locale-fill.log; tail -2 logs/adjudicate.log
tail -2 logs/recheck-active.log; tail -2 logs/retype-stuck.log

# ★CI 배포 상태 — 08-01 에 조용히 죽어 있었다. 매 세션 확인할 것.
gh run list --limit 3
```

**08-02 12:21 기준값(비교용)** — 경보 0건, 전부 임계 내:
```
candidate-stuck   342 / 9d      active-cjk-gap      2,050 / 42d
active-latin-gap  166 / 37d     active-no-anchor    4,421 / 63d
active-no-evidence 4,361 / 63d  type-mismatch           2 / 1d
never-examined-gap  2 / 7d      never-examined-raw    343 / 70d
entities 16,057 (active ~10,506 · rejected 5,216 · candidate 342)

verify-sweep  authoritative 5,904 / evidenced 4,236 / unverified 366   ← §8 적용 후
  (적용 전: 5,891 / 3,764 / 834. unverified 순감 468 = 복원 638 − 강등 170)
```

---

## §1. 이번 세션 커밋 (전부 푸시, CI 그린)

| 커밋 | 내용 |
|---|---|
| `4b5ac1e` | fix(deploy) 경로 이전을 코드에 반영 — CI 배포 08-01부터 중단 상태 |
| `18024ae` | fix(ops) kdb-app 을 KST 로 |
| `27c95df` | fix(ops) log.LUTC 제거 — TZ env 만으론 로그 시각이 안 바뀐다 |
| `acccac8` | **fix(verify) evidenced 티어가 컬럼 기본값에서 나오고 있었다** (§8) |

배포 2회: `flowttl-20260731-14` → `tzlog-20260802-1` → **`tier-20260802-2`**

---

## §2. ★41차 이후 미문서화분 (07-31 저녁, 배포 5회)

핸드오프 없이 넘어간 커밋들이다. **지표 정의 변경 2건과 주 유입경로 판정 변경 1건이
섞여 있어**, 41차 문서만 읽으면 기준값과 지표 의미를 잘못 잡는다.

| 커밋 | 내용 | 주의 |
|---|---|---|
| `b21f439` | 백로그 계측을 기동 시 1회 실행 | §0 폴백 추가 |
| `5ed2af4` | 뉴스근거 기사 URL 을 근거 대장에 보존 | **migrations/0098 적용됨** |
| `669874b` | never-examined 오탐 94% 정정 | **지표 정의 변경** |
| `b55d02a` | cand-evidence 적합성 게이트 | **주 유입경로 변경** |
| `ddc77d5` | 무조건 종결 기한(candidate TTL 21일) | **신규 종결 규칙** |

**꼭 알아야 할 세 가지:**

1. **`never-examined` 는 이제 두 지표다**(`669874b`). 종전 정의는 370건 중 346(94%)이
   로케일 전부 채워진 완성 엔티티였다 — 드레인이 손댈 게 없어 안 건드린 것이지
   커버리지 구멍이 아니다. `never-examined-gap`(시도 이력 없음 ∧ 로케일 빈칸)이
   배선 구멍의 실제 정의고, `never-examined-raw` 는 종전 정의를 경보만 해제해 남긴 것.
   **41차의 "372건"은 raw 쪽이며 §6-2 의 "1순위" 판단은 무효다** — gap 은 현재 2건.

2. **candidate 에 21일 TTL 이 걸렸다**(`ddc77d5`). candidate 604 → 398, 21일+ 체류 208 → 0.
   현재 누적 종결 **213건**(`kwave_kdb_recheck_log` verdict='ttl-expire', 전건 revert 가능).
   ★기각이되 **tombstone 아니다** — Tombstoned() 는 이름 기준이라 기각하면 그 이름의
   재조회가 막힌다(41차 김은정 사고). 명제가 "이 레코드가 기한 내 실증되지 않았다"이지
   "이 이름의 K-엔티티가 없다"가 아니다. **종결 + 재요청 시 재진입**이 설계의 핵심.

3. **cand-evidence 에 적합성 게이트가 붙었다**(`b55d02a`). 이 레인은 엔티티와 **무관한
   검색결과로 승급**시키고 있었다(표본 10건 중 근거가 엔티티를 실제로 언급한 건 1건).
   근거 URL 보존(`5ed2af4`)을 깔자마자 드러난 결함이다. 되돌린 7건 재실행 → promoted 7 → 1.
   `KDB_CAND_EVIDENCE_RELEVANCE=0` 으로 재배포 없이 종전 복귀 가능.
   ★한계 — **동명이인은 못 잡는다**(임미라: 대전시 공무원 기사 → "예능 출연자"). 판정기 몫.

---

## §3. 이번에 고친 것 2건

**(a) ★CI 배포가 08-01부터 죽어 있었다.**
08-01 에 프로젝트를 회전 HDD(`/data`)에서 NVMe(`/home`)로 옮기면서 **서버만 이동시키고
코드는 안 고쳤다.** `deploy.yml` 의 `DEPLOY_PATH` 가 아직 `/data/home2/...` 인데 그 경로는
이미 없어서 `cd "$DEPLOY_PATH"` 에서 즉시 실패한다.

가려진 이유가 중요하다 — **실패한 커밋이 하필 docs 전용**이라 가동 바이너리에 공백이
없었다. 다음 코드 커밋에서야 드러났을 것이다. 수정본은 08-01 이후 워킹트리에
**미커밋으로 떠 있었고**, 이 3파일이 인프라 이전의 코드측 반영분 전부라 커밋 전까지는
재해복구 시 복원되지 않는 상태였다. (deploy.yml / docker-compose.kdb.yml — CODEX_HOME
경로 + pgdata named volume 선언 / kdb-db-backup.sh)

**(b) 앱만 UTC 라 같은 로그 파일에 9시간 어긋난 두 시각이 섞였다.**
`TZ: Asia/Seoul` 이 kdb-db 에만 걸려 있고 kdb-app 은 미설정이었다. cron 레인은 셸에서
KST 로 찍고 같은 파일에 앱이 UTC 로 이어 찍는다:
```
[2026-08-02 10:20:04] locale-fill: en 구글번역 폴백        ← 스크립트 KST
2026/08/02 01:20:05 gtranslate_fill.go:102: 완료 ...       ← 앱 UTC (같은 순간)
```

---

## §4. ★진단이 뒤집힌 것 두 건 (같은 세션 내 자기정정)

**(a) "앱 로그가 9시간째 멈췄다" → 방금 찍힌 줄이었다.**
점검 중 앱 로그 마지막 줄이 `02:10:10` 인데 시각이 `11:10` 이라 "로그 정지"로 판단하고
컨테이너 이상을 의심했다. 멈춘 게 아니라 UTC 였다. **계측을 깔아놔도 시각이 어긋나면
같은 오독이 반복된다** — 41차 §0 은 backlog-watch 의 oldest_days 와 레인 최종 실행시각을
대조해 정체를 판정하라고 하는데, 그 대조가 9시간 틀어져 있었다. 이게 (b)를 고친 이유다.

**(b) "TZ env 만 넣으면 끝" → 절반만 맞았다.**
compose 에 `TZ: Asia/Seoul` 을 넣고 커밋 메시지에 "재빌드 없이 적용된다"고 적었다.
컨테이너 시각은 KST 가 됐는데(`docker exec kdb-app date`) **앱 로그는 그대로 UTC**였다.
원인은 `log.SetFlags(... | log.LUTC | ...)` — LUTC 는 TZ·time.Local 과 **무관하게** 표준
로거를 UTC 로 강제한다. 4개 main 전부에 박혀 있었다.
**환경만 고치고 끝냈으면 "TZ 를 맞췄다"고 문서에 남긴 채 증상이 그대로 남았을 것이다.**
→ LUTC 제거 + 재빌드. TZ env 는 유지한다(LUTC 를 빼면 로거가 time.Local 을 따르므로
TZ 가 있어야 KST 가 된다). **두 변경은 대체재가 아니라 짝이다.**

---

## §5. 실측으로 확인한 것 (41차 §5 에 추가)

7. **TZ 의존 로직은 전수 확인했고 회귀 없음.** `intake_autoverify.go:132` 일 예산 경계는
   09:00 KST 리셋 → 00:00 KST 로 정렬(인메모리 카운터 1회 리셋뿐), `apikeys/probe.go:40`
   KOFIC '어제' 는 UTC 라 00~09시 KST 구간에서 이틀 전을 조회하고 있었다(헬스 프로브 전용),
   `kdbadmin/router.go:191` 관리화면 표시 UTC → KST. gtranslate 월 상한은 Postgres
   `date_trunc('month', now())` 라 앱 TZ 와 무관(kdb-db 는 원래 KST).
   `sourcehealth.go:41` 은 명시적 `.UTC()` 라 불변.
8. **`.env` 를 건드리면 kdb-db 도 재시작된다.** kdb-db 가 `env_file: [.env]` 를 공유해
   compose 가 환경 변경으로 감지한다. 버전 올릴 때마다(KDB_APP_IMAGE) DB 가 잠깐 끊긴다.
   볼륨·데이터는 무영향(kdb-platform_pgdata 유지, 건수 일치 확인).
9. **서버에서 git push 는 SSH 로 안 된다.** origin 이 배포키(읽기 전용)라 거부된다.
   `.git/config` 에 빈 credential.helper 항목이 있어 전역 gh 헬퍼까지 리셋된다.
   푸시하려면 명시적 override: `git -c credential.helper='!/usr/bin/gh auth git-credential'
   push https://github.com/rickyjoo73/kdb.git main`.
   **이 빈 설정이 의도된 안전장치인지 확인 전까지 지우지 않았다**(§7).

---

## §6. 다음 작업

1. **★사전-게이트 뉴스근거 승급 2,733건 재심** — §8 에서 규모만 계측했다(오너 결정:
   "이번엔 규모만, 재심은 별도"). 07-17~07-31 에 `cand-evidence` 로 승급돼 아직 active 인
   엔티티다. `b55d02a` 실측에서 게이트 재적용 시 **7건 중 6건이 차단**됐으므로 이 모집단의
   상당수가 무관 근거 승급일 수 있다. 게이트 후 승급분은 60건뿐이라 재심 대상은 사실상
   전부 게이트 이전분이다. 재심에는 엔티티당 재검색(naver+SearXNG)이 필요해 쿼터·시간
   비용 판단이 선행돼야 한다. **먼저 표본 50건으로 오염률을 실측하는 편이 싸다.**
2. **`api-source-no-ref` 573건 — 버려진 식별자 회수**(§9). `runMusicBrainz` 가 MBID 를
   `external_refs` 에 안 남긴다(musicbrainz 481·kofic 85·netflix 6·disney 1).
   `musicbrainz.Client.FindAliases` 가 `AliasByLocale` 만 반환하므로 시그니처를 바꿔
   MBID 를 함께 돌려주고 ref 를 적재하면 된다. **강등이 아니라 기록이 처방이다.**
   같은 점검을 kofic/netflix/disney 경로에도 해야 한다.
3. **`active-cjk-gap` 2,050건** — 07-31 2,027 대비 소폭 증가. 단 41차 §5 의 반증 3건
   (ja 는 TMDb·ko위키 langlinks 로 못 채움, MT 확대 금지)이 그대로 유효하니
   **드레인 신설 전에 반드시 41차 §5 를 읽을 것.** 빈칸 유지가 정답인 구간이 있다.
4. **`retype-stuck` 잔여 정체 71건** — 08-02 10:04 실행에서 pool 75 중 hold 75, retype 0.
   전건 보류만 나오면 판정기가 결론을 못 내는 것이라 프롬프트·근거 쪽을 봐야 한다.
5. **하츄핑류 active 오분류** — 41차 §6-3 그대로 미해결(wikidata 무등재라 결정적 신호 없음).
   회전 스윕 비용 판단이 오너 결정 대기(§7).
6. **gtranslate 290셀 감사** — 41차 §6-5 그대로. `DrainTMDbLocaleFill` 이 상위소스 우선
   자동 교체하나 zh 가 있는 건만 된다. 잔여 재측정 필요.

---

## §7. 오너 결정 대기

- **하츄핑류 전수 스윕 비용** — active 10,491 을 회전 패널로 돌릴지(§6-4). 안 하면 조용한
  오분류는 계속 남는다. 41차에서 이월.
- **`.git/config` 의 빈 credential.helper** — 배포키가 읽기 전용인데 로컬 config 가
  전역 gh 헬퍼까지 리셋한다. **프로덕션 서버에서 실수로 푸시하는 걸 막는 의도된
  안전장치라면 그대로 두는 게 맞다.** 의도인지 사고인지 확인 필요(§5-9).
- **`docker-compose.kdb.yml.bak-namedvol-260801-0349`** — 08-01 볼륨 이전 백업. 이전이
  커밋·검증 완료라 지워도 되지만 백업이라 임의 삭제하지 않았다.
- **`pgdata` 볼륨을 `external: true` 로 바꿀지** — 현재 compose 가
  "volume kdb-platform_pgdata already exists but was not created by Docker Compose" 경고를
  낸다. `external: true` 면 `docker compose down -v` 가 **DB 볼륨을 지우지 않는다**.
  프로덕션 DB 라 안전 쪽이 맞아 보이나 선언 변경이라 확인 후 적용.
- **트래블 프로젝트 분리** — 07-31 오너 결정으로 KDB 와 분리(완료). 조사 결과는
  `project-traveli-multilingual` 메모리.

---

## §8. ★active-no-anchor 4,415건 전수 진단 — 유입이 아니라 티어 기계의 결함이었다

41차 §6-1 이 1순위로 지목하며 "오염 재심 대상인지 근거 수집 대상인지부터 나눠야
한다"고 한 건이다. 나눠보니 **셋 다 아니었다.**

**먼저 게이트는 실제로 먹었다.** `b55d02a` 배포(07-31 20:04) 전후 cand-evidence 승급:
```
07-30  90건/일   07-31  49   08-01  17   08-02  ~15
```
약 80% 감소. 단 `ddc77d5`(candidate TTL)가 1시간 차이로 같이 배포돼 단독 귀인은 불가.

**그런데 4,415 의 구성이 예상과 달랐다.** 최대 블록은 뉴스근거가 아니라 `strong-source`
2,331건(53%)이었고, 그 대부분이 **근거 없이 라벨뿐**이었다.

### 결함① `strong_src` 가 "라벨"만 보고 "값"을 안 봤다

`verify.go` 의 `strong_src` 는 `canonical_*_source` 8개 컬럼과 강한소스 목록의 **배열
겹침**이다. 그런데 그 8개 컬럼의 **DB 기본값이 `'wikidata-label'`** 이고 그게 강한
목록에 들어 있다. 값이 빈 슬롯에 남은 기본값만으로 `evidenced` 가 나온다.

```
어머니 전 (show, conf 0.700)   en=Mother's Story [gtranslate]  ja=(빈값) [wikidata-label]
                               외부 ref 0 · source_urls 0 → tier='evidenced'
```
실측: 앵커 없는 strong-source 2,331 중 **1,443(62%)이 값 없이 라벨뿐**.
`wikidata-label` 라벨을 단 슬롯 1,591건이 빈 값이었다.

### 결함② 스윕이 자기가 지켜야 할 신호를 자기가 덮어썼다

종전 주석은 "evidence 패스가 올린 값은 강등하지 않고 보존한다"고 선언하고 그 판정을
`verification_evidence LIKE 'search+gemma%'` 로 했다. **그런데 같은 UPDATE 의
evidenceCASE 가 바로 그 컬럼을 덮어쓴다.** `strong_src` 가 한 번이라도 이기면 gemma
근거 문자열이 영구 소실되고 보존조항은 그때부터 무력이다.

**★①만 고쳤다면 974건을 조용히 강등시켰을 것이다.** 강등 후보 1,143 중 974(85%)가
뉴스근거 승급 이력 보유 — 근거가 없어서가 아니라 증거를 스윕이 지워서 강등되는 것이다.
→ append-only 대장(`kwave_kdb_dataqa_log`, verdict=`candidate-evidence-promote`)에서
읽는 `sig.gemma_reason` 신설. 컬럼이 살아 있으면 원문, 지워졌으면 대장에서 복원.

**두 결함은 짝으로만 고칠 수 있다.**

### 결과 (dry-run 예측과 정확히 일치)

| 변화 | 건수 | 의미 |
|---|---:|---|
| evidenced → unverified | **170** | 진짜로 근거가 기본값뿐이던 건 |
| unverified → evidenced | **638** | 결함②로 이미 잘못 강등돼 있던 건의 복원 |
| 변화없음 | 9,689 | |

no-anchor 집합 구성이 정직해졌다 — search+gemma 2,541 / strong-source(값 동반) 888 /
confidence≥0.75 626 / unverified 366. `search+gemma` 근거 문자열 보유 2,540건으로,
종전엔 `'strong-source'` 가 실제 근거를 가리고 있었다.

### ★자기정정 — 강등 대상으로 지목했던 표본이 전부 정상이었다

근거 대장의 **제목만** 보고 `어머니 전`·`풍향중`·`인터넷 걸`·`SNL 코리아 시즌8` 을
무관근거 승급으로 의심했다. 복원된 근거는 각각 "EBS 방영, 장윤주 출연 show",
"유튜브 '뜬뜬' 웹예능, 유재석·이성민 출연", "캣츠아이(KATSEYE)의 노래"로 **전부 타당**했다.
41차 §5-4(어휘 부분매칭 오탐 27건)와 같은 실수를 반복할 뻔했다.

**원인: 대장은 `title` 만 저장하는데 게이트·gemma 판정은 `title + 스니펫` 으로 한다.**
→ **대장 제목만으로 적합성을 판단하지 말 것.** 대장으로 게이트 결정을 재현할 수 없다는
뜻이기도 하다(5ed2af4 의 목적이 사후검증 복구인데 스니펫이 없어 절반만 달성).

### 되돌리는 법

`verify.go` 를 revert 하고 재배포하면 다음 스윕이 전건 원복한다. set-based UPDATE 라
수천 행도 1초 미만이고 판정 입력이 전부 DB 안에 있어 외부 호출 0.

---

## §9. ★기제1 — 불변식 경보 (오너 지시: 오염·병목에 철저한 대안)

### 진단: 41차 배선은 틀리지 않았다. 경보가 발화 불가능했을 뿐이다

§8 에서 찾은 결함(evidenced 인데 되짚을 근거 없음 3,995건)은 `active-no-evidence` 로
**정확히 같은 크기로 이미 계측되고 있었다.** 그런데 6시간마다 이렇게 찍혔다:
```
active-no-evidence total=4361 oldest=63d (임계 90d 내)     ← 건강해 보인다
```

원인 두 겹, 둘 다 실측:

| | 문제 | 실측 |
|---|---|---|
| ① | 나이 기준이 `updated_at` | `ddc77d5` 가 **이틀 전** "updated_at 은 정체 지표로 못 쓴다"고 결론냈는데 8개 조건 중 **6개**가 그 컬럼. 해당 집합의 **83%가 7일 내 갱신** → 임계 도달 불가 |
| ② | 임계 90일 > **DB 수명 70일** | 어떤 행도 DB 보다 오래될 수 없다. 세 조건은 **이 DB 역사상 한 번도 울릴 수 없었다** — 우연이 아니라 구조적 불가능 |

**계측의 실패가 아니라 경보 설계의 실패다.** 크기는 정확히 알고 있었고 6시간마다
찍고 있었는데, 판정 기준이 그 집합에 대해 영원히 거짓이었다.

### 대안 3기제 중 이번에 구축한 것

**기제1 · 경보를 "나이"가 아니라 "명제 위반"으로** (`acf7cd8`)

`WatchInvariants` — 나이를 보지 않고 "참이면 안 되는 상태"를 세어 **count>0 이면 경보**.
도입시 값과 Δ 를 같이 찍어 큰 잔여 위반도 노이즈가 아니라 진행 지표가 되게 했다.

```
⚠ evidenced-unretrievable 위반=3995 (도입시 3995, Δ+0) — 확증 주장에 지시대상이 없다
  authoritative-no-ref  위반=0 ✓
  strongsource-empty    위반=0 ✓   ← acccac8 회귀 가드
  tier-missing          위반=0 ✓
```

★`tier-missing` 의 30분 유예는 **"얼마나 오래 깨졌나" 임계가 아니라 정상 과도기 제외**다
(승급 직후~verify 스윕 10분). 유예 없이 세니 방금 승급된 2건이 잡혔고, 그런 상시 거짓
지표는 `669874b`(94% 오탐이던 never-examined)처럼 곧 무시된다. 명제 자체는 여전히
이분법이다 — "스윕 주기를 한참 넘겼는데도 티어가 없다".

**임계·기준컬럼 교정** — StaleCol 을 `updated_at` → `created_at`, 임계는 실측 p90 기준.
**경보량을 미리 계산해 노이즈가 아님을 확인한 뒤** 넣었다:

| 조건 | 변경 | 실제 경보 |
|---|---|---:|
| candidate-stuck | 30d → **21d**(=TTL 기한, 초과=TTL 고장) | 0 |
| active-cjk-gap | StaleCol → created_at | 5 |
| active-latin-gap | StaleCol → created_at | 4 |
| active-no-anchor | 90d → **60d**, created_at | 145 |
| active-no-evidence | 90d → **60d**, created_at | 145 |
| never-examined-gap | 90d → **60d** | 0 |

**★`warnDecorativeThresholds` — 워치가 자기 임계를 점검한다.** 임계가 DB 수명 이상이면
"이 조건은 울릴 수 없다(장식 임계)"고 스스로 경보한다. ②번 계열이 다시 생겨도 사람이
발견할 때까지 기다리지 않게 하려는 것이다. **우연히 안 울리는 것과 울릴 수 없는 것은
다른데, 로그만 보면 둘이 똑같이 "임계 내"로 보인다.**

### 기제2 — 근거 원장을 승급의 전제조건으로 (`b378540`, 완료)

**① 계약 반전: "적재 실패해도 승급 유지" → "적재 못 하면 evidenced 를 주지 않는다".**
종전엔 승급 UPDATE 뒤에 best-effort 로 적재해서 **적재 0건이어도 evidenced 가 나갔다** —
그 순간 위반이 조용히 하나 는다. 대장은 계측이 아니라 `tier='evidenced'` 라는 주장의
근거 자체다. 순서를 뒤집어 대장을 먼저 쓰고 0건이면 **기각이 아니라 보류**한다.
★`dataqa_log` 보다도 먼저다 — 스윕(§8)이 dataqa_log 만 보고 evidenced 를 복원하므로
그게 먼저 들어가면 대장 없이도 티어가 살아난다. 파이프라인을 멈추는 게 아니라 등급을
안 주는 것이고, 승급은 다음 회차에 재시도된다.

**② 스니펫 저장(`migrations/0099`)** — 게이트·gemma 의 입력은 제목이 아니라
`evHit.Line`("제목 — 설명")인데 대장엔 제목만 있어 **판정을 재현할 수 없었다.**
제목 기준 적합률은 11.6%로, 이름이 설명에만 나오는 건이 흔하다 — §8 자기정정의 원인이
바로 이것이다. 기존 행은 빈 값으로 남고 재적재 시 채워진다.

**③ `evidenced-unretrievable-new` 불변식(7일 창)** — 3,995 더미에서 Δ+1 은 안 보인다.
배포시 7일 414 / 1일 0. 414 는 대장(`5ed2af4`) 이전 유입이라 창이 지나면 빠진다.
**이후 0 을 유지 못 하면 ①계약이 어딘가에서 새고 있다는 뜻이다.**

라이브 실증 — cand-evidence 1회 실행에서 게이트(킬릿 6/6 제외)·오염판정(월량대표아적심
= 영화 '첨밀밀' OST)이 정상 동작, 신버전 적재 5건 전부 스니펫 보유(구버전분 0건).

### 기제2 후속 — 티어 정책 정리 (`79f4d9a`, 완료)

**`confidence` 를 tier 입력에서 제거.** evidenced 의 정의는 "독립 확증"인데 confidence 는
우리가 우리에게 매긴 숫자라 독립이 아니다. 게다가 **대부분 상수다** — `autopilot/sweep.go`
는 조건만 맞으면 0.750 을, 각 드레인은 0.75~0.8 을 그냥 쓴다. 즉 "conf>=0.75"는 **어떤
외부 사실도 주장하지 않으면서** evidenced 를 내주고 있었다(627건이 오직 이 분기로만).
오늘 고친 `strong_src`(라벨만으로 evidenced)와 같은 계열의 마지막 하나다.
숫자 자체는 랭킹·정렬 입력으로 남는다 — 없앤 건 tier 판정에서의 자격뿐.

```
evidenced 4,236 → 3,626      unverified 366 → 992
evidenced-unretrievable 3,995 → 3,368 (-627)
남은 evidenced: search+gemma 2,556 / strong-source 1,067 / wikipedia-langlink 3
→ 이제 evidenced 는 전부 "외부의 무언가가 그렇게 말했다"에 근거한다.
```

**★`api-source-no-ref` 불변식 신설 (573건) — "포인터를 버리고 값만 쓴다" 계열의 세 번째.**

| | 사례 | 상태 |
|---|---|---|
| ① | cand-evidence 가 기사 URL 을 버림 | `5ed2af4` 수정 |
| ② | verify 스윕이 gemma 근거를 덮어씀 | `acccac8` 수정 |
| ③ | **enrich orchestrator 가 MBID 를 버림** | **미수정** (이 지표) |

`runMusicBrainz` 는 `FindAliases` 로 아티스트를 찾아 표기를 채우면서 그 MBID 를
`external_refs` 에 남기지 않는다. 값은 `musicbrainz` 라벨을 달았는데 **어느 아티스트였는지
되짚을 수 없다**(musicbrainz 481 · kofic 85 · netflix 6 · disney 1).

★**처방은 강등이 아니라 ref 기록이다** — 값의 출처는 실재하고 기록만 안 한 것이라,
강등하면 사실을 지우는 쪽으로 틀린다. 다만 MBID 는 `musicbrainz.Client.FindAliases` 가
`AliasByLocale` 만 반환하며 내부에서 버리므로 **클라이언트 시그니처부터 바꿔야 해**
별건으로 뒀다.

### 아직 안 한 기제3 (오너 결정 대기)

- **기제3 · 승급 단일 관문** — `status='active'` 를 쓰는 코드 지점이 **18곳**이고 각자
  계약을 갖는다. 앵커 기반 8곳은 되짚기 100%인데 비앵커 5곳은 **5.3%** 다. 하나의
  `Promote(ctx, id, claim)` 로 모아 되짚을 근거를 필수 인자로 요구하면 오염 유입구가
  18개 → 1개가 된다. 비용 중간(호출부 18곳), 리스크 있으나 기계적.

**하지 말 것: 새 드레인 레인 추가.** 41차의 동일 결함 4회가 전부 "레인을 늘려서" 생긴
사각지대였다.
