# KDB handoff — 2026-06-15 (14차) — 데이터 dedup 전수 + 운영 백로그 5종 가시화·처리

이 세션은 두 덩어리: **(A) 데이터 중복/오염 전수 정리(운영작업, 코드 아님)** + **(B) 운영
대시보드 백로그 5종 가시화·처리 파이프라인 수정(코드, 커밋 `26aace2` push 완료)**.

핵심 교훈(사장님 피드백): **숨기지 말 것.** 지표를 낮추려 쿼리를 바꾸지 말고, 백로그를
정직하게 드러내고 각각 처리 솔루션을 만든다. (이전에 충돌 카드 집계를 임의로 바꿔 0으로
보이게 한 것을 되돌리고, 정직한 분리 라벨로 다시 만들었다.)

## 0. 30초 점검
```sh
cd /data/home2/kdb.aiinplanet.com
docker ps --filter name=kdb --format '{{.Names}}\t{{.Status}}'
# 백로그 6종 (대시보드 카드와 동일 의미)
docker exec kdb-db psql -U kdb -d kdb -t -A -F' | ' -c "select
 (select count(*) from kwave_entities where status='candidate') 신규,
 (select count(*) from kwave_kdb_api_requests where created_at>now()-interval '7 days') 클라요청7d,
 (select count(*) from kwave_kdb_corrections where status='pending') 교정pending,
 (select count(*) from (select canonical_ko from kwave_entities where status='active' group by canonical_ko having count(*)>1) a)
 +(select count(*) from (select lower(canonical_en) from kwave_entities where status='active' and coalesce(canonical_en,'')<>'' group by lower(canonical_en),entity_type having count(*)>1) b) 충돌실제,
 (select count(*) from kwave_entity_resolution_attempts where status in ('disambiguation-fail','conflict','error') and attempted_at>now()-interval '30 days') 해소실패로그,
 (select count(*) from kwave_entities where confidence<0.70 and status='active' and operator_locked=false and entity_type<>'unknown' and (canonical_en_source ilike '%wikidata%' or canonical_ja_source ilike '%wikidata%' or canonical_vi_source ilike '%wikidata%' or canonical_es_source ilike '%wikidata%' or canonical_id_source ilike '%wikidata%' or canonical_pt_br_source ilike '%wikidata%' or canonical_zh_source ilike '%wikidata%' or canonical_zh_hant_source ilike '%wikidata%')) 품질bumpable;"
```
세션 종료 시점값: 신규 6 · 클라요청 3 · 교정 17 · 충돌실제 5 · 해소실패로그 226 · 품질 2.

## 1. ★운영 핵심 (11~13차 계승)
- **빌드 없는 배포**: 코드수정 → `docker restart kdb-app`(`.:/app` bind-mount, compose가 go build→exec). `docker compose build` 금지. env 변경만 `up -d kdb-app`.
- **마이그레이션 수동 적용**(러너 없음): `docker exec -i kdb-db psql -U kdb -d kdb < migrations/0077_*.sql`.
- **push**: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`
- **admin 렌더 검증법(세션 민팅)**: ADMIN_SESSION_SECRET 으로 쿠키 만들어 curl. 컨테이너 내부 python3로:
  payload=`email:exp` → `b64url(payload)+'.'+b64url(HMAC_SHA256(secret,payload))`, 쿠키 `kdb_admin_session=`. (이 세션 내내 이걸로 모든 admin 페이지 e2e 렌더 검증함.)
- **secret 주의**: API 키 등은 컨테이너 env에서 직접 사용(`$(echo "$KDB_API_KEYS"|cut -d, -f1)`)해 화면 노출 금지.

## 2. (A) 데이터 중복/오염 전수 정리 (운영작업 — 로컬 감사, 미커밋)
감사 파일: `scripts/cleanup-20260614/` (SQL + 변경전 CSV 스냅샷 + README). **`.gitignore`의 `*.sql`
규칙으로 SQL/CSV는 미추적**(migrations만 추적). 모든 DB 변경은 `notes ILIKE '%operator(claude)%'`로 추적.
- **needs_disambig(충돌 큐) 26 → 0**: 중복쌍 병합(서인영·유재석·키·손태영·장윤주·보이넥스트도어 등), 쓰레기 reject, term 오탐 플래그 해제.
- **별칭 오염 ~280개 제거**: 직업/방송맥락/관계 수식어(`강현주 작가`·`나는 솔로 31기 영자`·`박수홍 아내`), 동명이인 잘못된 이름(`김수미←김영옥`). LLM 분류+웹검증으로 정당 별칭(본명·예명·로마자·그룹멤버) 오제거 0.
- **잠복 중복 전수 dedup(~250 entity)**: 4중 탐지(교차이름·공유 Wikidata QID·공유 영문명·정규화표기) → `kdb_merge_by_name`(media/refs/relations/person_details 이동 + 패자 rejected, 함수는 작업후 DROP). 동명이인(박진영 JYP/진영 GOT7, 신지민 AOA/지민 BTS 등)은 유지, QID 오염(나비↔'Ella Gross', 엔시티↔엔시티드림)은 잘못된 ref 삭제.
- ★**공식명칭 명명 규칙(운영자 lock 근거로 확정)**: **그룹=한국어명**(방탄소년단·블랙핑크 — 운영자가 11개 주요그룹 한국어 entity를 operator_locked 해둠), **인물=활동명/예명**(RM·아이유·슈가 — 정국·지민·로제·제니가 lock됨), **작품=한국어 정식제목**, 영문공식 곡(Dynamite 등)은 영문. → 이후 dedup도 이 방향.
- ★**자기수정 사례**: ENHYPEN 희승(이희승)을 동명 레이블 가수 EVAN(에반)으로 오병합 → QID/영문명 교차검증으로 발견·분리. **무검증 자동병합은 위험**(반드시 검증)을 입증.

## 3. (B) 운영 백로그 5종 — 코드 (커밋 26aace2)
모두 대시보드 카드 + 상세/처리. e2e 렌더 검증 완료.
1. **클라이언트 요청 로그**(신규): `migrations/0077` `kwave_kdb_api_requests` + `internal/kdbapi/request_log.go`(인증 뒤 미들웨어, async insert) + api.go(classify가 consumer id 반환→ctxKeyConsumer) + 대시보드 카드 + `/admin/kdb/requests`(handlers_requests.go). 이전엔 lookup 로깅 자체가 없었음.
2. **교정요청**(corrections): 대시보드 카드 + `/admin/corrections`(handlers_corrections.go + corrections.html, 승인=operator권한 보호값 덮어적용/거부). 기존 CLI(`kdb-app corrections`)만 있었음. pending 17은 "Wikidata 검증됐으나 보호값"이라 운영자 판단 필요분.
3. **on-demand candidate 적체**: `autopilot.ResolveOnDemand`(sweep.go) — lookup-miss candidate(매체0이라 ≥2합의 게이트 영구정체)를 검색증강 enrich로 검증 → 외부참조/외국명 확보분 즉시 active 승급, 무검증은 last_enriched_at 마킹(루프방지)+노트+카드노출. 워커 research틱(1분)마다 호출 + `kdb-app resolve-ondemand [N]` CLI. 대시보드 candidate 최고령(h) 표시.
4. **충돌 지표 정직화**: `fetchInboxCounts`(handlers.go)의 "충돌"이 rejected중복+wikidata no-match(가드성공)+검색HTTP오류까지 합산하던 **집계 버그**. → 카드 메인=실제 활성중복(active canonical-dup + 같은영문명 active-dup), 별도줄=해소실패 관측로그(30일 자동소멸). 충돌 페이지(handlers_entities.go/entity_conflicts.html)에 dupKo를 active-only로 + "같은 영문명 미병합 의심(KO/EN·예명)" 섹션 추가. **무검증 자동병합 미도입(운영자 게이트 유지)**.
5. **품질검토 적체 버그 수정**: 품질카드가 안 빠지던 진짜이유 = bump 조건(`wikidata external_ref`)이 bumpable 정의(`wikidata 소스`)와 불일치 → 132중 0건만 승급(drain 첫실행 bumped=0으로 실증). → `stepQualityReview` + `DrainQuality`(sweep.go)의 bump 조건을 정의와 일치(wikidata 소스 OR ref)+dataqa 오염분 제외로 수정. `kdb-app drain-quality [N]`(bulk-bump). **132→2 해소**.

신규 CLI: `resolve-ondemand`, `drain-quality`. (기존: drain-candidates/persons/bucket/enrich, dataqa, corrections, enrich-test, …)

## 4. ★다음 세션 작업 후보 / 관망
- **충돌실제 5**: on-demand 승급으로 생긴 KBS류 KO/EN·동명 의심. `/admin/entities/conflicts`에 노출됨 — 운영자 검토(병합 or 유지). 자동병합은 의도적으로 안 함(에반 오류 교훈).
- **교정 pending 17**: 전부 "Wikidata 검증됐으나 operator-locked/검증소스라 보호됨" → `/admin/corrections`에서 승인 시 operator 권한으로 덮어적용. 자동적용 안 함(외부 정정 무검증 적용 = 데이터 인젝션 위험).
- **on-demand 무검증 candidate 6**(그리·박효정·슛포러브·엄흥도·이아미·장연주): enrich로도 외부근거 못 찾음 → 운영자 검토 or 추가매체 대기.
- **해소실패 로그 226**: 5/24~25 단일 인시던트(codex 브레이커). 30일 후 자동 소멸. 충돌 아님.
- (옵션) on-demand/quality 처리를 autopilot Hermes registry 스텝으로 정식 편입(현재는 워커 틱/직접호출). Role enum 추가 필요.

## 5. 검증/운영 명령
- 단건 enrich: `docker exec -w /app kdb-app /tmp/kdb-app enrich-test <ko>` (또는 `go build -o /tmp/kdb-app-cli ./cmd/kdb` 후 사용).
- on-demand 해소: `... resolve-ondemand 50` / 품질: `... drain-quality 500` / dataqa: `... dataqa --apply`.
- 빌드/테스트: `docker exec -w /app kdb-app sh -c 'go build ./internal/... ./cmd/kdb/ && go test ./internal/kdbapi/ ./internal/kdb/corrections/'` (★`./...`는 /app/data 권한오류 — 패키지 지정).

연관: [[project-kdb-cutover]], 이전 핸드오프 `KDB.handoff-20260614-2.md`(13차, dataqa↔enrich 핑퐁).
