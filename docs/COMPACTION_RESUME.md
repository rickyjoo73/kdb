# 이어서 진행하기 — 2026-09-14 12:20 KST

**대화 압축 뒤에도 방향을 잃지 않기 위한 문서다.** 새 세션은 여기부터 읽는다.
단일 실행 원장은 [`docs/KDB_INTEGRATION_TODO.md`](KDB_INTEGRATION_TODO.md).

---

## 1. ★ 운영자가 준 규칙 (절대 잃으면 안 된다)

1. **한 사람/한 대상 = 하나의 ID.** 겸업으로 ID 를 쪼개지 않는다. ID 가 갈리는 유일한
   이유는 **다른 대상**이라는 것이다. → [식별 계약 §1.1](KDB_IDENTITY_CONTRACT.md)
2. **흡수할 때 ID·분류·구분값을 함께 부여한다.** 구분되지 않으면 **지어내지 않고 검수로**.
3. **wikidata QID 는 보조다.** 자체 ID 가 주 앵커다.
4. **관측이 먼저, 흡수가 나중.** 관측하지 않은 값을 지어내지 않는다(D-37).
5. **권리 판단은 운영자 몫.** 운영자가 "사용 가능"이라 한 것을 내가 게이트로 막지 않는다.
6. **AI 가 스스로 판단해 작동하는 시스템이다.** 없던 사람 승인 단계를 만들지 않는다.
7. **다른 사이트 컨테이너는 절대 건드리지 않는다.** (2026-09-14 명시 지시)

---

## 2. 지금 상태 (실측)

```
원장 진행    38/69 (55%)   P0·P1(G1)·P2(G2)·P3(G3) 완료, P4 진행 중
표기         2,685,247     (ko 561,124 · en 560,542 · ja 534,098 · zh-Hans 444,532 …)
대상         555,895
승인 정책    36 provider
마이그레이션 82 (최신 0132)
DB           6,985 MB      디스크 494GB 여유
무결성 6종   전부 0
git HEAD     93870d6
```

**직전 완료:** TDB 다국어 표기 2,148,919건 가져오기(11개 로케일). 원본과 로케일별 수량
정확히 일치, 미처리 0, `recorded 1,838,790 / generated 310,129`(rule+llm 과 정확히 일치).

---

## 3. ★ 바로 이어서 할 일 — 5개 (스크립트·ROLLBACK 시험 전부 완료)

서버: `scratchpad/r` 로 감싸 실행. 체크아웃 `/data/home2/kdb.aiinplanet.com`.

```bash
# ① 무결성 + VACUUM (쓰기 폭주가 끝났으니 이제 재는 게 의미 있다)
docker exec kdb-db psql -U kdb -d kdb -c "VACUUM ANALYZE kentity_names, kentity_evidence, kentity_entities"
#    그 뒤 이 질의를 다시 잰다. 쓰기 중엔 53ms→4.3초였다. 여전히 느리면
#    (entity_id) WHERE status='verified' 부분 인덱스(약 40MB)를 붙인다.
docker exec kdb-db psql -U kdb -d kdb -c "\timing on" -c "SELECT count(*) FROM kentity_names WHERE status='verified'"

# ② kdb-db 재생성 — compose 에 6g/4.0 이 이미 들어가 있다. 약 11초 중단.
cd /data/home2/kdb.aiinplanet.com
docker compose -f docker-compose.kdb.yml -f docker-compose.override.yml up -d --no-deps kdb-db

# ③ 브랜드 추출 1,519개 (배치 200 권장 — 집계 비용이 배치 크기와 무관하다)
docker cp docs/p4/extract_chain_brands.sql kdb-db:/tmp/
#    루프: 남은 게 0 이 될 때까지 반복 (멱등)

# ④ 지점 범위 밖 123,787건
docker cp docs/p4/scope_branch_pois.sql kdb-db:/tmp/

# ⑤ 분류 적용 97,419건 → 유형 미상 114,152 → 16,733
docker cp docs/p4/apply_record_type_classification.sql kdb-db:/tmp/
```

**③을 ④보다 먼저** 하는 이유: 지점이 원래 상태일 때 앞머리를 세야 브랜드 근거(지점 수)가
정확하다.

**되돌리는 법**
- ④ `status` 를 `candidate` 로 되돌리면 끝 (삭제하지 않았다)
- ③ `policy_version='chain-brand-v1'` 로 선별
- ⑤ 근거 행의 코드·`decided_by` 로 코드 단위 선별

---

## 4. 운영자가 내린 결정 (이미 반영됨, 되묻지 말 것)

| 결정 | 내용 |
|---|---|
| TDB 데이터 | 사용 가능. 권리 판단은 운영자 몫 |
| `편의오락` 94,024 | `location`(세부 NULL)로 올린다. `decided_by='operator'` 로 표시 |
| 지점형 | **전부 범위 밖**. 삭제가 아니라 `status='rejected'` |
| 브랜드 | 지점 **5개 이상**인 체인만 추출 (2,298개 중 새로 만들 것 1,519개) |
| 서버 이전 | **5개 작업을 끝낸 뒤** 이전한다 |

---

## 5. 서버 이전 (5개 완료 후)

**대상:** `ssh aiin@atikar.com -p 38382` (호스트명 aiin22). KDB·TDB 관련만 옮긴다.

```
새 서버   i7-6700 4C/8T · RAM 46GB(스왑 0) · NVMe 2TB(1.6TB 여유) · 컨테이너 9개
현재      Xeon E3-1230 v6 4C/8T · RAM 15.9GB(스왑 7.2GB) · SATA SSD · 컨테이너 49개
```

**CPU 는 업그레이드가 아니다**(i7-6700 이 한 세대 구형). 이득은 **RAM 3배와 NVMe**.

**옮길 것:** KDB DB 6,985MB · TDB DB 4,472MB · 체크아웃 3.6GB ·
컨테이너 7개(kdb-app/db/searxng/p1-restore-db, tdb-api/worker/db)

**★해결해야 할 것 (조사 완료)**
1. **새 서버에 nginx 가 없다**(도커에도 호스트에도). TLS·리버스 프록시를 새로 세워야 한다.
2. **내부망 호출이 끊긴다.** 지금 `kdb-app` 은 `mediafine_default`·`dockers_backend`
   (고정 IP 172.19.0.240)에 붙어 있어 다른 사이트가 내부망으로 부른다. 물리 분리되면
   인터넷 경유(`https://kdb.aiinplanet.com`)로 바뀐다 — 동작은 하나 지연이 는다.
3. **`docker-compose.override.yml` 을 새로 써야 한다.** 새 서버엔 `dockers_backend`·
   `mediafine_default` 망이 없다.
4. 새 서버에 `node` 가 없다(검사기 `docs/checks/*.cjs` 실행에 필요).
5. 포트 5432 는 이미 tts-postgres 가 쓴다. KDB 는 5436 이라 충돌 없음.

**아직 조사 안 한 것:** 소비자 사이트들이 KDB 를 내부망 이름으로 부르는지 공개 도메인으로
부르는지 — 이걸 봐야 이전 후 무엇이 끊기는지 확정된다.

---

## 6. 작업 환경

- **저장소** `github.com/rickyjoo73/kdb` · 브랜치 `main`
- **배포** main push → GitHub Actions (build → `migrations/*.sql` 적용 → `--no-deps kdb-app` 교체 → 헬스)
  - `docs/p1`~`docs/p4` 의 SQL 은 `migrations/` 밖이라 **자동 적용되지 않는다**
  - CI 는 `kdb-db`·`kdb-searxng` 를 건드리지 않는다 — compose 값 변경은 수동 재생성 필요
- **운영 서버** `ssh -p 38371 kdb@114.203.210.38` — scratchpad 의 `r` 로 감싸 실행
  - `r` 은 expect 래퍼다. **무응답이 길면 타임아웃으로 끊긴다** — 장시간 대기엔 하트비트를 찍어라
- **격리 회귀** `kdb_platform_migration_test` 를 `kdb` 템플릿으로 재생성 후
  `go test ./internal/... -run Restored` (golang:1.23-bookworm 컨테이너)
- **관리자 UI** https://kdb.aiinplanet.com/admin · **소비자 API** `/v1/kentity/entities`

### 하지 않는 것
- 승인된 게이트 밖의 운영 DB 쓰기·마이그레이션·배포
- `sync-kdb-owner.js` 전량 실행 · 시험용 승인값(`prov-x`)의 운영 유입
- 표본 0건을 전체 0건으로 보고 · 문서에 비밀키 기록
- **다른 사이트 컨테이너 조작**

---

## 7. 이 세션에서 실측이 잡아낸 내 오판 (같은 실수 반복 금지)

1. **"적재 비계 880MB 정리"** — 681MB 는 비계가 아니라 흡수분의 **자체 ID 앵커**
   (`basis_record_id`)였다. FK 세 개가 `ON DELETE RESTRICT`.
2. **`tourapi_ko 12` → 관광지** — 문서 기억으론 맞지만 이미 분류된 2,033건 중
   **heritage 1,170 · nature 739**. 세부 일치율 57.6%. 코드는 "장소다"까지만 말한다.
3. **`shared_buffers` 4GB 권고** — 가용 메모리를 재보고 1GB 로 철회했다.
4. **"3분에 3건 오류"** — `--since` 를 잘못 걸어 재시작 **전** 로그를 보고 있었다.

**전부 그럴듯했고 전부 틀렸다.** 기억·직관이 아니라 데이터에서 도출한다.
