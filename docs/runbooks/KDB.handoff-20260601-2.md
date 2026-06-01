# KDB 핸드오프 #8 (2026-06-01, 2차)

#7(`KDB.handoff-20260601.md`) 이후 세션. **외부 매체(미디어파인) API 키 발급/관리 +
박보검류 데이터 오염 근본수정 + match 게이팅 메타데이터**. 전부 push 완료(HEAD=`ca91638`).

## 0. 30초 상태 확인
`.52`(114.203.210.52:38371, user kdb) → `/data/home2/kdb.aiinplanet.com`:
```bash
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep kdb   # kdb-app/kdb-db healthy
# 외부 매체 검색 키 (미디어파인) 동작 확인
curl -s -X POST http://127.0.0.1:9100/v1/lookup \
  -H "X-KDB-Key: <미디어파인키 = admin /admin/settings 에서 복사>" \
  -H content-type:application/json -d '{"query":"도깨비","limit":1}'
# match 게이팅 메타 (conf/status/locked 나오는지)
curl -s -X POST http://127.0.0.1:9100/v1/entities/match \
  -H "X-KDB-Key: <미디어파인키 = admin /admin/settings 에서 복사>" \
  -H content-type:application/json -d '{"source_text":"방탄소년단과 박보검","locale":"ja","limit":5}'
```

## 1. 이번 세션 변경 (전부 배포·푸시됨)

### 1.1 외부 매체 소비자 API 키 발급/관리 (56b2b43, 4eba3ec)
- **요구**: 미디어파인이 `/v1/lookup` 로 인물·고유명사 DB 조회하는 인바운드 키를 admin 에서 발급.
  (기존엔 `.env KDB_API_KEYS` 에만 있어 운영자가 추가/회수 불가.)
- **migration 0067** `kwave_kdb_api_consumers`(label/key/active/last_used_at). 0066(아웃바운드
  키)과 역할 다름 — 0067=외부가 KDB 로 들어오는 인바운드 키.
- **런타임 인증**(`internal/kdbapi/api.go` `apiKeyAuthenticator`): `.env KDB_API_KEYS`(정적)
  ∪ DB active 소비자 키 **합집합** 허용. 30s TTL 캐시, last_used_at 비동기 갱신(1h 스로틀),
  pool=nil/DB실패 시 env 키로 안전 degrade. (기존 `apiKeyMiddleware` 대체.)
- **admin** `/admin/settings` 하단 "외부 매체 검색 API 키" 섹션: 라벨로 발급(평문 1회 노출),
  마스킹 목록, 회수(active=false). `internal/kdbadmin/handlers_consumers.go`.
  **복사 버튼**(클립보드 + 비HTTPS execCommand 폴백) — 발급 직후 배너 + 목록행 각각.
  목록행은 active 키 전체를 readonly input 으로 노출(admin 세션 보호 + DB 평문이라 OK).
- **발급된 키**: 미디어파인 키는 DB `kwave_kdb_api_consumers` 에 저장. 평문은 admin
  `/admin/settings` 복사버튼으로만 노출 — **문서/git 에 키 박지 말 것(보안차단됨)**.

### 1.2 박보검류 데이터 오염 근본수정 (1b5eb3e) ★중요
- **증상**: 박보검 `canonical_ja=ホ・ソンジン`(허성진, 다른 인물). en=Park Bo Gum 인데 ja만 오염.
- **근본원인**: `internal/kdb/wikidata/client.go SearchAndFetch` 가 검색 첫 결과(`cands[0]`)를
  **무검증 채택**. "박보검" 검색의 첫 hit 가 엉뚱한 인물이어도 그 ja 라벨을 써버림.
  (KOFIC/TMDb list[0] 폴백 오매칭과 동형 버그.)
- **수정**: `entityMatchesQuery` — fetch 한 entity 의 label/alias(전 locale)가 query 와
  정규화(`normalizeName`: 소문자+공백/중점/하이픈 제거) 일치하는 첫 후보만 채택. 일치 없으면
  채택 거부(**오염 대신 빈칸 유지** = "빈 힌트가 틀린 힌트보다 안전"을 코드화). 4개 enrich
  경로(orchestrator/person/layers/admin)가 모두 이 함수 경유 → 단일 수정으로 전 경로 보호.
  단위 테스트 포함(박보검 vs 허성진 거부, 진짜/로마자/별칭 채택).

### 1.3 오염 데이터 정리 (DB 직접 수정, 가드 조건부 + Wikidata 권위 검증)
- 박보검 ja: `ホ・ソンジン`→`パク・ボゴム` (Wikidata Q15977222).
- 김영옥 ref: 잘못된 김수미 id(Q5242028)→올바른 Q12588052(김영옥 1937 배우).
- **ja-in-ko 분류오류 정리**(canonical_ko 에 일본어 가타카나가 들어간 person):
  - reject 6건(정상본 존재 중복): 고두심·남궁민·남지현·박은빈·도경수·박진영(パク・ジニ).
  - canonical_ko 교정 3건(Wikidata 검증): 김주혁·정건주·조성하.
  - `김ジウ` 1건: 동명이인(김지운/김지우/Jiwoo) 권위 불확정 → 추측 대신 `needs_disambig=true`
    플래그만(서빙 주의 신호). **여전히 active 라 남아있음 — 권위 확정되면 처리.**

### 1.4 match 게이팅 메타 + entities offset (ca91638) ★미디어 피드백 대응
- **미디어 지적(둘 다 사실 확인)**: ① match 응답에 confidence/status/id 없어 박보검(0.75)을
  BTS(1.0)와 구분 못 해 게이팅 불가. ② `/v1/entities` offset 무시(필드 자체 없었음)로
  candidate 모집단 감사 불가.
- **수정**: `MatchedEntity` 에 id/confidence/status/operator_locked 추가. `EntityFilter.Offset`
  + SQL `OFFSET` 구현. 빈 결과 `[]` 반환(null 제거). `KDB-API.md` 게이팅 권장안 문서화.
- **권장 게이팅**(소비자측): `operator_locked=true` 또는 `confidence>=0.9` 만 힌트로 신뢰.

## 2. 적용된 migration (.52 DB)
- `0067_api_consumers.sql` — 적용 완료. (0065/0066 은 #7.)

## 3. ★다음 세션: 미디어파인 의존도 — 사장님 핵심 지적
> "제대로 만들었다면 의존도가 높겠지."

KDB 게이팅 메타까지 갖췄으니 미디어파인이 KDB 에 의존할 기반은 됐다. **미해결 = 미디어 워커
쪽 통합**(KDB 레포 아님):
- **하이브리드(미디어측 권장 종착)**: KDB 를 "번역힌트 직공급"이 아니라 **후보 발굴원**으로,
  힌트는 로컬DB+Wikidata 검증 통과분만. KDB self-healing·발굴력은 살리되 미검증 데이터 핫패스
  배제. → 미디어 워커 코드에 ① 섀도 모드(플래그 off, match 병행호출 audit 로깅 N일) ②
  conf≥0.9/operator_locked 게이팅 ③ Wikidata 교차검증(미디어측 코드 이미 있음:
  `internal/worker/resolver/{wikidata,wikipedia_langlinks}.go`) 을 넣어야 함.
- **컷오버는 보류**(미디어측 판단 정정 수용). 섀도 모드 실측 후 점증 신뢰.
- **미디어 워커 레포 위치 미확인** — 사장님께 접근 경로 확인 후 그쪽에 섀도/게이팅 코드 작성 가능.

## 4. 남은 작업 (우선순위)
1. **미디어 워커 하이브리드 통합** (§3) — 의존도 실현의 핵심. 레포 접근 확인 필요.
2. `김ジウ` 등 권위 불확정 ja-in-ko 잔여 정리(needs_disambig 검토).
3. KMDb 연동(#7 §6-1, 키 승인 대기).
4. TMDb credits 인물/필모 확장(#7 §6-2).
5. (운영) 채팅 노출 미디어파인 키 로테이션 — admin 발급/회수로 가능.

## 5. ★운영/배포 주의 (불변)
- **빌드**(호스트 go 없음): `docker run --rm -v "$PWD":/app -w /app -v kdb-gomod:/go/pkg/mod
  golang:1.23-bookworm bash -c 'export PATH=$PATH:/usr/local/go/bin GOFLAGS=-buildvcs=false; go build ./...'`
- **배포**: `docker compose -f docker-compose.kdb.yml build kdb-app && docker compose ... up -d kdb-app`.
- **push**(deploy key read-only): `git -c credential.helper='!gh auth git-credential' push
  https://github.com/rickyjoo73/kdb.git HEAD:main`.
- ★**운영 DB 직접 write / 운영 컨테이너 docker exec 조회는 자동승인 차단됨** — 사장님 승인 후 진행.
  자동화 스크립트로 운영 DB 일괄수정도 차단. 가드 조건부 단건 UPDATE + 권위검증이 정공법.
- ★**Wikidata id 는 반드시 직접 라벨 확인 후 사용** — 이번에 Q12583297(석탑)을 김영옥으로
  잘못 짚었다가 차단으로 잡힘. 검색결과 id 추측 금지, getentities 로 ko/ja 라벨 확인할 것.

## 6. 한 줄 요약
> 외부매체(미디어파인) 인바운드 API 키 발급/관리(admin+복사버튼, 0067) + 박보검류 ja 오염
> 근본수정(SearchAndFetch 이름검증 가드, 빈칸>틀린값) + 오염 데이터 정리 + match 게이팅
> 메타(conf/status/locked)·entities offset 완료·푸시. 다음: 미디어 워커 하이브리드 통합(의존도 실현).
