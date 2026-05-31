# KDB 핸드오프 #5 (2026-05-30)

`KDB.handoff-20260529.md`(#4) 이후 — 운영자 질의(고유명사DB/인물DB 구분, 9개 언어 채움, 분류 적체)에서 출발해 **classify schema 치명 버그**를 발견·수정하고 자율분류를 복구.

## 0. 30초 상태 확인

`.52`(`114.203.210.52`, port 38371, user `kdb`) ssh 후 작업 디렉터리 `/data/home2/kdb.aiinplanet.com`:

```bash
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep -E 'kdb|NAMES'   # kdb-app / kdb-db healthy

# 분류 현황 (candidate 줄고 active/persons 늘어야 정상)
docker exec -i kdb-db psql -U kdb -d kdb -c \
 "SELECT status,count(*) FROM kwave_entities GROUP BY 1 ORDER BY 1;"

# classify 살아있나 (group 0.99 나와야 정상 — unknown/0 이면 schema 깨진 것)
docker exec kdb-app kdb-app classify-test 방탄소년단 2>&1 | tail -1

# autopilot audit (이제 cycle 당 1 row)
docker exec -i kdb-db psql -U kdb -d kdb -c \
 "SELECT ran_at,classified,promoted,enriched,non_entity_reject FROM kwave_kdb_autopilot_log ORDER BY ran_at DESC LIMIT 5;"
```

## 1. ★ 치명 버그: classify schema strict-mode 위반 (이번 핵심)

**증상**: autopilot 이 cycle 은 돌지만 분류가 전혀 안 됨. `DrainCandidatesConcurrent` 첫 실행 751건 **전부 deferred(promoted=0/reject=0)**.

**원인**: codex 0.134.0 의 structured output(strict)은 `properties` 의 **모든 키가 `required` 에 있어야** 한다. 4개 schema 가 위반:
- `kdb_classify.schema.json` — gender/groups/secondary_roles 누락 → **classify 전건 실패** (`Invalid schema ... Missing 'gender'`, codex exit 1). `aijudge.Classify` 는 codex 에러 시 `{unknown,0}` 을 **에러 없이** 반환 → 전부 defer.
- `kdb_fill_person.schema.json` — fields 의 7개 필드 누락 (person L4 enrich 깨짐).
- `kdb_disambiguate.schema.json` — assignments[] same_as/relation/disambig 누락.
- `kdb_gatekeeper.schema.json` — canonical_suggestion 누락.

**수정**: 각 `required` 에 모든 property 추가 (nullable 은 type 에 null 허용한 채 required 유지). `kdb_extract`/`kdb_fill_locale`/`kdb_person_reconcile` 는 원래 정상(enrich L4 locale 채움은 작동했음).

**검증**: `classify-test 방탄소년단` → `group conf 0.99`. drain 재실행 시 정상 승격/reject.

> 교훈: 새 codex schema 추가 시 **required = properties 전체** + `additionalProperties:false` 필수. 감사 one-liner 는 §6.

## 2. 이번 세션 변경 (handoff #4 대비)

### 2.1 9개 언어 전체 채움 (한국어 포함)
- enrich 엔진(orchestrator)은 원래 8개 외국어 전부 커버. **트리거가 ja/zh 누락**이라 보강:
  - `autopilot.stepEnrichEmpty` SELECT 에 `canonical_ja`, `canonical_zh` 추가.
  - `kdbapi.hasEmptyPriorityLocale` 에 `zh` 추가 (lazy enrich).
- **인물DB(kwave_persons) 다국어 확장**: `migrations/0063` 으로 `name_es/id/pt_br/zh/zh_hant` 추가(기존 ko/en/ja/vi 4개 → 9개). `stepSyncPersons` 에 entities.canonical_* → persons.name_* **mirror** 추가 (빈 칸만, operator 보호).
- admin 인물DB 진도바도 9개 언어로 확장.

### 2.2 분류 적체 해소 (candidate 751 → 정리)
- `stepReviewCandidates`: 단일매체 후보도 gpt conf ≥ 임계(단일0.85/≥2 0.75/≥3 0.70) + 실제 type 이면 자동 active 승격(+persons sync). 애매건은 `updated_at` touch 로 큐 회전(ASC) → 무한 적체 방지. **inline enrich 는 제거**(후보마다 풀 cascade 돌면 cycle 폭주 → stepEnrichEmpty 가 담당).
- `DrainCandidatesConcurrent(ctx, workers)` 신규: 적체 전체를 1 pass·동시(workers개) gpt 분류로 일괄 정리. `kdb-app drain-candidates [workers]` 서브커맨드로 실행. enrich 안 함(분류만).

### 2.3 자율성 보강
- autopilot `Sweeper.running atomic.Bool` **single-flight 가드**: cycle 이 30분 넘겨도 다음 ticker 가 중복 실행 안 함.
- autopilot cycle audit: `migrations/0064` `kwave_kdb_autopilot_log` + `Run()` 끝 `persistLog` + dashboard "최근 autopilot cycle" 표.

### 2.4 고유명사DB / 인물DB 구분 (운영자 요청)
- 운영자 방침: **고유명사DB = 인물 제외**(group/drama/movie/song_album/show/agency/brand_place…), **인물DB = 순수 인물**. group 은 고유명사DB(이미 그러함). person 은 양쪽이 아니라 인물DB 전용으로 보이게.
- `handlers_entities.entitiesList`: type 미지정 시 기본 `entity_type <> 'person'` 필터(명시 type=person 선택 시만 예외). 데이터는 그대로, **뷰만 분리**.
- 데이터 정리는 drain+autopilot 이 person→persons sync 로 수행.

### 2.5 migrations 적용
`0062`(enrich_attempts) · `0063`(persons 다국어) · `0064`(autopilot_log) 모두 `.52` DB 적용 완료.

## 3. 실측 (2026-05-30 ~08:38 UTC, drain 진행 중 스냅샷)
- classify 복구 후 drain: candidate 751 → 451(진행중), active 901 → 1163, rejected → 78, persons 664 → 820.
- active entity_type: person 817 / drama 114 / group 61 / brand_place 45 / show 38 / term 25 / song_album 24 / movie 19 / channel_outlet 14 / event_tour 3 / agency 3.
- API lookup 9개 언어 정상(모모랜드 ko/en/ja/vi/zh/zh-hant/es/id/pt-br).

## 4. 실행 중 백그라운드
- **detached drain** (`docker exec -d kdb-app sh -lc 'kdb-app drain-candidates 6 > /tmp/kdb-drain.log 2>&1'`) — 컨테이너 init 소유, 세션 종료해도 생존. 남은 candidate 일괄 분류 중.
- 진행: `docker exec kdb-app tail -5 /tmp/kdb-drain.log`

## 5. 남은 작업 (다음 세션)
1. drain 완료 후 최종 분리 결과 확정 + 운영자 inbox 에 남은 deferred(gpt 확신 미달) 검토.
2. **GitHub push** — 이번 세션 변경분(schema fix 4개 + autopilot/api/admin + migrations 0063/0064 + cmd drain·classify-test) 미푸시. deploy key write 권한 필요(handoff #4 §3.4 그대로).
3. TMDb/KOFIC/KMDb key — 영상물(drama 114·movie 19) 다국어 채움률. 사용자 발급 결정.
4. 동명이인 Phase 2 / playwright UI 검증(admin cookie).
5. (선택) drain 을 admin 버튼/주기 cron 으로 승격 — 현재는 수동 서브커맨드.

## 6. 운영 도구
```bash
# codex schema strict-mode 감사 (required = properties 전체?)
for f in internal/kdb/codexcli/schemas/*.json; do python3 - "$f" <<'PY'
import json,sys; d=json.load(open(sys.argv[1])); bad=[]
def c(n,p="root"):
 if isinstance(n,dict) and n.get("type")=="object" and "properties" in n:
  m=set(n["properties"])-set(n.get("required",[]))
  if m or n.get("additionalProperties",True)!=False: bad.append((p,sorted(m)))
  for k,v in n["properties"].items(): c(v,p+"."+k)
 if isinstance(n,dict) and n.get("type")=="array" and "items" in n: c(n["items"],p+"[]")
c(d); print(sys.argv[1], "BAD",bad) if bad else print(sys.argv[1],"OK")
PY
done

# 일괄 분류 (적체 즉시 정리)
docker exec -d kdb-app sh -lc 'kdb-app drain-candidates 6 > /tmp/kdb-drain.log 2>&1'

# 단건 분류 진단
docker exec kdb-app kdb-app classify-test "<한글>"
```

## 7. 한 줄 요약
> classify schema strict-mode 위반으로 **autopilot 분류가 그동안 무력화**돼 있던 것을 발견·수정(4개 schema). 9개 언어 enrich 트리거 보강 + 인물DB 다국어 + candidate 일괄 drain + single-flight + cycle audit. 고유명사DB(인물 제외)/인물DB 뷰 분리. 변경분 **미푸시** — 다음 세션 push 권한 필요.
