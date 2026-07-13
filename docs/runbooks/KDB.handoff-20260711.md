# KDB 인수인계 — 2026-07-11 (고유명사 사전 게이트 / 병목 해소)

## 운영 상태

- 배포 이미지: `kdb-app:ai-gateway-20260711-1`
- 이미지 ID: `sha256:ff7b95e7f3540b36055077a6483713d7af2b98b98fcd532e7f04264ccd68b66f`
- health: `ok=true`, `version=ai-gateway-20260711-1`, container healthy, restart 0
- 직전 안전 이미지: `kdb-app:bottleneck-20260710-4`
- 배포 직전 백업: `/data/home2/kdb.aiinplanet.com/backups/kdb-20260711-002427.sql.gz`
  - 46,985,871 bytes, `kwave_kdb_backup_log.status=ok`
- 적용 migration: `0087`~`0091`
- Gemma 라우팅: `https://ai.aiinplanet.com`, 모델 `gemma4`
  - KDB의 개별 백엔드 직접 라운드로빈 제거; 백엔드 분산은 통합 게이트웨이가 담당
  - 기존 모델명 `gemma-4-26b-a4b`는 통합 게이트웨이에서 404이므로 함께 변경
  - 통합 주소의 root/models/chat 및 동시 12요청 모두 200 검증
- `0091_proper_noun_intake_gate.sql` 적용 결과:
  - research 기존 8,831행 normalized key backfill
  - Wikidata 존재만으로 재활성화됐던 active `term` 49행을 candidate/review로 가역 격리
  - live queue invariant: `pending/in_progress => precheck_status IN ('pass','approved')`

## 원인과 실측

기존 `PreGate`는 명백한 문법 노이즈만 reject하고 정상 형태는 모두 `PreGray`였는데, API가
`PreGray`를 사실상 pass로 취급했다. `/prepare`는 이마저 우회해 2글자 이상이면 큐에 넣었고,
research worker는 candidate를 먼저 만든 뒤 Wikidata/TMDb/KOFIC/MusicBrainz/검색/LLM을 호출했다.

운영 DB 읽기 전용 감사 결과:

- candidate 1,129건 중 1,098건(97.3%)은 URL·domain 근거가 전혀 없음
- 고립 candidate에 enrich attempt 1,326회 소비
- 최근 research 73건 중 no-match 67건(91.8%), candidate 결말 56건(76.7%)
- 최근 Hermes 3시간 단순 벽시계 합: Gatekeeper 661초, LocalFill 673초,
  Enricher 357초, ResolveUnknowns 268초
- NULL source_id/표기 변형/상충 type 때문에 같은 이름을 반복 조사
- URL이나 type이 있어도 `What is N/a?`, 장소, 행사, 역할어가 잘못 show/person으로 처리된 사례 다수

## 구현된 비용 게이트

`gatekeeper.DecideIntake`가 모든 신규 조사 요청을 3상태로 결정한다.

- `pass`: concrete type + 신뢰 출처 + 문맥 내 정확 언급 + type cue가 모두 있거나,
  정규화 기준 기존 active/alias가 정확히 하나인 경우
- `review`: 실제 이름일 수 있으나 근거 부족, type 충돌, 범위/형태 모호. 감사 row만 저장하고 API 0회
- `reject`: 빈값, control/format/PUA, 깨진 자소, 키보드 난수, 명백 category/term. API 0회

길이, 4단어 이상, 숫자 혼합, 조사/어미, 문장부호는 영구 reject가 아니라 review다.
실제 작품·인명(`성난 사람들 시즌 2`, `카이라` 등)의 과거 오거부를 반영했다.
정확 언급은 단어 경계(한국어 조사 결합 허용)를 확인하고, type cue는 해당 언급의 32 rune
근처에서만 인정한다. `건강·인기·도움` 같은 일반어는 person/group 등 비작품 type으로 통과하지 않는다.

중앙 적용 경로:

- lookup miss / bulk lookup miss
- `/v1/prepare`
- correction miss
- match-miss extractor
- 직접 `/v1/research-queue` (이 경로는 write scope로 강화)
- RSS/LLM `CandidateStore.Observe`

`/v1/prepare` 신규 term 자동 조사를 원하면 term 또는 batch에 `context`와 `source_url`,
구체 `type`을 함께 보내야 한다. 근거 부족 응답 status는 `review`다.

## 이중·후단 방어

- research claim SQL 자체가 `pass/approved`만 선점
- worker가 enrich 직전에 동일 rule을 재평가; 위조/stale pass도 fail-closed
- MusicBrainz/KOFIC/Wikidata-person/rejudge/ResolveUnknowns/Google News rescue selector는
  intake pass/operator approval 또는 CandidateGatekeeper keep 근거 없이는 provider 호출 금지
- CandidateGatekeeper의 고확신 common-noun 판정도 Wikidata veto를 호출하지 않고 operator review
- active `term/unknown`은 LocalFill/Enricher에서 제외
- zero-yield `QualityReview` Hermes step 제거(12 → 11 steps)
- PersonExtractor는 결과를 기록하고 14일 cooldown 적용

## 중복·정체성 방어

- `intake_normalized_key`: NFC/lower + 공백/구두점 제거(identity lookup/dedupe 전용)
- canonical/aliases의 normalized exact match를 provider 호출 전에 확인
- 같은 normalized key+type의 live work는 partial unique index로 single-flight
- 같은 normalized key의 상충 type 또는 복수 active identity는 review
- raw spelling은 변경하지 않음

## 운영자 화면

`/admin/ondemand/queue`에서 pass/review/reject/approved 수와 reason/origin/request_count를 표시한다.

- review 승인: `/admin/ondemand/queue/{id}/approve`
- review/pass 기각: `/admin/ondemand/queue/{id}/reject`
- CSRF 및 admin auth 적용

## 검증 결과

- `go test ./cmd/... ./internal/... ./pkg/...` 통과
- `go vet ./cmd/... ./internal/... ./pkg/...` 통과
- `go build -buildvcs=false ./cmd/kdb` 통과
- compose config 통과, immutable image context 1.75 MB
- 운영 API 표본:
  - `게이트검증일반어` → review, queued=false, attempts=0
  - `qwertyzxcv12345` → reject/fatal_mash, queued=false, attempts=0
  - `건강` + person/type/source/context 위장 → review/ambiguous_common_for_type, attempts=0
  - 두 표본 모두 entity/candidate/provider/enrich attempt=0
- 위조한 `precheck_status=pass` 난수도 worker가 다시 reject; candidate/provider attempt=0
- `오징어게임`은 `오징어 게임` normalized existing으로 단락, queued=false
- 현재 invalid live queue(`non-pass pending/in_progress`) = 0

## 운영 주의 / 다음 작업

- 기존 candidate 1,129건을 외부 API로 일괄 재검사하지 말 것. admin review 또는 로컬/DB 근거
  audit로 단계적으로 정리한다.
- `review` 적체는 실패가 아니라 비용 차단 상태다. operator 승인 전 자동 조사되지 않는 것이 정상이다.
- client가 context를 아직 보내지 않으면 신규 이름은 review로 보류된다. 먼저 kstory/소비자 payload를
  `{ko,type,context}` + `source_url`로 바꾼다.
- 앱 긴급 rollback은 additive 0091 schema를 유지한 채 `kdb-app:bottleneck-20260710-3`를 사용한다.
  DB 전체 rollback이 필요한 경우 위 백업을 복원한다.
