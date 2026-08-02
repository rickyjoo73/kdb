# KDB 핸드오프 42차 (2026-08-02) — 배포 파이프라인 복구 + 41차 이후 문서공백 메움

41차(07-31) 이후 **배포 6회·커밋 5건이 어느 핸드오프에도 없었다.** 그 사이 CI 는 08-01
부터 죽어 있었고 아무도 몰랐다. 이번 세션은 새 기능이 아니라 **가동 상태와 문서를
실제에 맞추는 작업**이다. §2 가 문서공백을 메운 부분이고, §3 이 이번에 새로 고친 것이다.

현재 `tzlog-20260802-1`. 커밋 3건 전부 푸시, CI 그린.

---

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /home/kdb.aiinplanet.com          # ★경로 이전됨 (구 /data/home2, 41차 문서 기준)
curl -s -m 5 http://127.0.0.1:9100/v1/health   # version=tzlog-20260802-1

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

**08-02 11:33 기준값(비교용)** — 경보 0건, 전부 임계 내:
```
candidate-stuck   342 / 9d      active-cjk-gap      2,050 / 42d
active-latin-gap  166 / 37d     active-no-anchor    4,415 / 63d
active-no-evidence 4,361 / 63d  type-mismatch           2 / 1d
never-examined-gap  2 / 7d      never-examined-raw    343 / 70d
entities 16,049 (active 10,491 · rejected 5,216 · candidate 342)
```

---

## §1. 이번 세션 커밋 (전부 푸시, CI 그린)

| 커밋 | 내용 |
|---|---|
| `4b5ac1e` | fix(deploy) 경로 이전을 코드에 반영 — CI 배포 08-01부터 중단 상태 |
| `18024ae` | fix(ops) kdb-app 을 KST 로 |
| `27c95df` | fix(ops) log.LUTC 제거 — TZ env 만으론 로그 시각이 안 바뀐다 |

배포 1회: `flowttl-20260731-14` → **`tzlog-20260802-1`**

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

1. **`active-no-anchor` 4,415건 / `active-no-evidence` 4,361건** — 외부 ref 도 근거 URL 도
   없이 **서빙 중**. 임계(90d) 안이라 경보는 안 뜨지만 규모가 압도적이고 **계속 증가 중**
   (07-31 4,344 → 08-02 4,415, 순증 약 36건/일). 쌓인 백로그가 아니라 **유입**이다 —
   `5ed2af4` 실측으로 최근 7일 886건 중 845(95%)가 이 경로. 41차 §6-1 이 1순위로 지목한
   그대로 미해결. **오염 재심 대상인지 근거 수집 대상인지부터 나눠야 한다.**
   `b55d02a` 적합성 게이트가 신규 유입분에 얼마나 먹었는지 먼저 재볼 것.
2. **`active-cjk-gap` 2,050건** — 07-31 2,027 대비 소폭 증가. 단 41차 §5 의 반증 3건
   (ja 는 TMDb·ko위키 langlinks 로 못 채움, MT 확대 금지)이 그대로 유효하니
   **드레인 신설 전에 반드시 41차 §5 를 읽을 것.** 빈칸 유지가 정답인 구간이 있다.
3. **`retype-stuck` 잔여 정체 71건** — 08-02 10:04 실행에서 pool 75 중 hold 75, retype 0.
   전건 보류만 나오면 판정기가 결론을 못 내는 것이라 프롬프트·근거 쪽을 봐야 한다.
4. **하츄핑류 active 오분류** — 41차 §6-3 그대로 미해결(wikidata 무등재라 결정적 신호 없음).
   회전 스윕 비용 판단이 오너 결정 대기(§7).
5. **gtranslate 290셀 감사** — 41차 §6-5 그대로. `DrainTMDbLocaleFill` 이 상위소스 우선
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
