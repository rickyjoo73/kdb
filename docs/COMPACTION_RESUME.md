# 이어서 진행하기 — 2026-09-13

**이 문서는 대화 압축 뒤에도 방향을 잃지 않기 위한 것이다.** 새 세션은 여기부터 읽는다.
단일 실행 원장은 여전히 [`docs/KDB_INTEGRATION_TODO.md`](KDB_INTEGRATION_TODO.md) 다.

---

## 1. 지금 무엇을 하고 있나

**P3.06 확대 — TDB 를 KDB 공통 모델로 흡수하는 중이다.** 백그라운드 루프가 유형별로 돌고 있다.

- 방금 시점: **TDB 흡수 157,633건 / 연결 157,633건** (오류 0, `kdb-app healthy restarts=0`)
- 완료된 유형: district 82,120 · accommodation 26,502 · sports_facility 20,789 ·
  heritage_site 13,877 · cultural_facility 5,909 · legal_dong 4,901 · festival_series 3,292 ·
  admin_region 242
- 남은 유형(알파벳 순으로 진행): **nature 19,664 · restaurant 145,433 · shopping 25,504 ·
  tourist_spot 36,981** → 최종 약 **385,000건** 예상

**루프가 끊겼으면 그냥 다시 돌리면 된다.** 멱등이다 — 이미 연결된 원본은 다시 고르지 않는다.

```bash
# 서버에서 (scratchpad/run 으로 감싸 실행)
cd /data/home2/kdb.aiinplanet.com && git pull --ff-only origin main
docker cp docs/p3/p3_expand.sql kdb-db:/tmp/p3_expand.sql
bash /tmp/load_type.sh kdb-db kdb <유형>     # 적재대 채우기
# 그 뒤 1,000건씩:
docker exec -i kdb-db psql -X -q -v ON_ERROR_STOP=1 -U kdb -d kdb \
  -v ptype=<유형> -v lim=1000 -v runid=$(uuidgen) -f /tmp/p3_expand.sql
```

---

## 2. ★ 방향 — 운영자가 준 규칙 (절대 잃으면 안 된다)

1. **한 사람 = 하나의 ID.** 한 사람이 배우·가수·MC 를 겸하면 직업 행이 여러 개일 뿐 ID 는
   하나다. ID 가 갈리는 유일한 이유는 **다른 사람**이라는 것이다. 겸업으로 ID 를 쪼개지 않는다.
   → [식별 계약 §1.1](KDB_IDENTITY_CONTRACT.md)
2. **흡수할 때 ID·분류·구분값을 함께 부여한다.** 나중에 채울 칸으로 비워 두면 이름이 사실상
   키가 되고 같은 이름의 다른 대상이 한 UUID 로 합쳐진다. 구분되지 않으면 **지어내지 않고
   검수로 보낸다.** → [식별 계약 §1.2](KDB_IDENTITY_CONTRACT.md)
3. **wikidata QID 는 보조다.** 자체 ID 가 주 앵커다. QID 는 있을 때 얹어 빈 곳을 채우는 역할이지,
   없는 DB 를 채우는 역할이 아니다. 나중에 다른 언어를 할 때도 여러 곳의 데이터를 쓴다.
4. **관측이 먼저, 흡수가 나중이다.** 흡수가 원본 binding 을 지어내면 안 된다(D-37 실증).
5. **지금은 소스 공백 문제까지 생각하지 말고, 있는 것을 제대로 통합하고 확장에 집중한다.**
   (운영자: "진짜 병목은 나중에 없는 것을 채워 넣을 때 소스를 못 찾을 때" — 그건 나중 일이다.)

---

## 3. 운영 상태 (2026-09-13 기준)

| 항목 | 값 |
|---|---|
| 표 | **82** (시작 60) |
| 마이그레이션 원장 | **76** (0123·0124·0125·0126 추가) |
| 승인 원천 정책 | **14 provider** (13 + `tdb`) |
| TDB 흡수 Entity / 연결 | **157,633 / 157,633** (진행 중) |
| 차단 guard | **751** (OUTOFSCOPE 583 · MISLINK 168) |
| 직업 행 | **204** (100명, 76명 겸업) |
| legacy `kwave_entities` | **19,544 — 소비자가 보는 것은 불변** |
| 무결성 | 외부키 중복예약 **0** · 근거 교차 **0** · conflict 연결 **0** |
| 앱 | `kdb-app:ci-20260913-*` **healthy restarts=0** |

### 적용한 마이그레이션

| 번호 | 내용 |
|---|---|
| 0123 | P1.02 격리 구조 승격 (표 20개·복합 FK·EXCLUDE·트리거·사전 seed·정책 13) |
| 0124 | 이관 원장 보호선 (완료 guard·단조성·원본 키 인덱스·적재대) |
| **0125** | **원본 binding 계약 보강** — `basis_record_id`(자기 ID 관측) 를 1차 binding 으로. QID 없이도 흡수 가능 |
| **0126** | 선매핑 코드표 승격 (`kentity_source_type_map`, 51 결정) |

---

## 4. 원장 현황

**66개 중 33개 완료.** P0(10)·P1(10, G1 인수)·P2(8, G2 인수)·P3(.01~.05 인수).

- **P3.06 진행 중** — 확대
- **P3.07 다음** — 조사 분모와 최종 처리/보류/제외 공개 후 G3 인수
- 이후 P4(11) · P5(6) · P6(6, 24h 관측 필요) · P7(8)

---

## 5. 다음에 할 일 (순서대로)

1. **확대 완료까지 돌린다.** nature → restaurant → shopping → tourist_spot.
   매 유형 뒤 원본 대조(원본 수 = 흡수 + 보류)와 무결성 4종 0 확인.
2. **P3.07 G3 인수 준비** — 유형별 최종 처리/보류/제외를 분모와 함께 공개한다.
   UI 의 등록 수를 검증 표기 수로 표시하지 않는다(원장 금지사항).
3. 그 뒤 P4(자동 보충·분야 공급 연결)로.

### 흡수하지 않는 것 (선매핑 `hold`/`exclude`)

`other` 106,602(추측 금지) · `person` 31,378(동일인 판정 선행) · `work` 3,822 ·
`food` 2,774(비고유명사 위험) · `organization` 2,557 · `education` 1,122 · `transit` 911.
**건수를 늘리려고 이 규칙을 풀지 않는다.**

---

## 6. 작업 환경

- **저장소**: `github.com/rickyjoo73/kdb`, 브랜치 `main` (feat 브랜치는 병합 완료)
- **배포**: `main` push → GitHub Actions (build gate → `migrations/*.sql` 적용 → 컨테이너 교체 → 헬스)
  - `docs/p1`·`docs/p2`·`docs/p3` 의 SQL 은 `migrations/` 밖이라 **자동 적용되지 않는다**
- **운영 서버**: `ssh -p 38371 kdb@114.203.210.38` — scratchpad 의 `run`/`rsh` 로 감싸 실행
  - 운영 DB 컨테이너 `kdb-db`, 앱 `kdb-app`, 원본 `tdb-db`(읽기 전용), 격리본 `kdb-p1-restore-db`
  - 운영 체크아웃 `/data/home2/kdb.aiinplanet.com` (clean, main)
- **관리자 UI**: https://kdb.aiinplanet.com/admin — 계정 `rickyjoo@aiinad.com`(admin)
  - 흡수분 보기: `/admin/kentity?origin=tdb`
- **복구점**: 백업 `backups/kdb-20260913-200626.sql.gz` + 이미지 `kdb-app:entity-center-20260912-1`
  - 모든 변경이 `run_id` 에 묶여 있어 역순 회수 가능
- **보존물**: `backups/uncommitted-main-*.patch`, `backups/opcheckout-*.tar.gz`
  (운영 체크아웃에 있던 미커밋 변경 — 전부 브랜치가 포함하고 있음을 대조로 확인했다)

### 검증 명령 (한 번에)

```sql
SELECT '표' , count(*) FROM pg_class WHERE relnamespace='public'::regnamespace AND relkind='r'
UNION ALL SELECT 'TDB 흡수', count(*) FROM kentity_entities WHERE origin_system='tdb'
UNION ALL SELECT '연결', count(*) FROM kentity_crosswalks WHERE source_system='tdb'
UNION ALL SELECT '★외부키 중복예약(0)', count(*) FROM (SELECT provider,external_id FROM kentity_id_reservations GROUP BY 1,2 HAVING count(DISTINCT entity_id)>1) x
UNION ALL SELECT '★근거 교차(0)', count(*) FROM kentity_names n JOIN kentity_evidence v ON v.id=n.evidence_id WHERE v.entity_id<>n.entity_id
UNION ALL SELECT '★conflict 연결(0)', count(*) FROM kentity_crosswalks WHERE source_system='tdb' AND status='conflict'
UNION ALL SELECT 'legacy(소비자, 불변이어야)', count(*) FROM kwave_entities;
```

---

## 7. 금지사항

- 운영 DB 에 **승인 범위 밖** 변경 금지. 확대는 선매핑 `map` 유형만.
- `sync-kdb-owner.js` 전체 실행 금지.
- TDB 원본(`tdb-db`)에 **쓰기 금지** — 읽기 전용.
- 시험 전용 승인값(`prov-x` 등)을 운영에 넣지 않는다.
- 표본 0건을 전체 0으로 표현하지 않는다.
- 비밀값·API 키를 문서에 적지 않는다.

---

## 8. 이 세션에서 배운 것 (반복하지 말 것)

- **지연 제약은 "행위"를 못 막는다.** COMMIT 시점 최종 상태만 본다 → 즉시 실행으로.
- **0행 UPDATE 는 오류가 아니다.** 시험이 아무것도 검사하지 않은 채 통과한다.
- **조건절만 쓰는 단언은 거짓이어도 PASS 다** → `p1_must`/`p2_must` 로 감싼다.
- **재실행이 승인 범위를 넓힐 수 있다.** run 이 정한 대상 목록을 그대로 써야 한다.
- **요청키가 batch 마다 같으면 두 번째가 통째로 롤백**되고 루프는 조용히 0건을 반복한다.
- **psql 변수는 `DO $$ … $$` 안에서 치환되지 않는다.** 매개변수를 임시 표로 넘긴다.
- **호스트 `/tmp` 와 컨테이너 `/tmp` 는 다른 곳이다.** `docker cp` 로 넣는다.
- **UUID·QID 리터럴에 16진수/숫자 아닌 글자**를 쓰면 파일 전체가 한 트랜잭션이라 결과 표조차
  안 만들어진다 → `docs/checks/validate-p1-sql-literals.cjs` 가 막는다.
