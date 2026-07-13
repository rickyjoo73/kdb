# KDB 인수인계 — 2026-07-13 (인테이크 자동 검증 — "없으면 검증 후 바로 추가작업")

## 운영 상태

- 배포 이미지: `kdb-app:intake-autoverify-20260713-4`
  - `-1`: 최초 배포 / `-2`: worker 의 approved 감사표식(reason/flags) 보존 수정
  - `-3`: 형제 row type 우선 대조 + 충돌검사 자기행 제외(여회현 케이스 — 아래 참조)
  - `-4`: ★fresh 레인(오너: "제대로 된 키워드는 유입 즉시 심사") — 소비자 유입/재요청
    순간 해당 row 만 즉시 검증(주기·백로그 순서 무대기). 일일 예산에 fresh 예약분
    `KDB_INTAKE_AUTOVERIFY_FRESH_RESERVE`(기본 120콜)를 둬 backlog 드레인이 신규를
    굶기지 못함. admin 발굴큐 카드 "자동 검증 승인/운영자 승인" 분리.
    실측: 소비자 실요청 "관식" 유입 → 즉시심사 → person 승격 → 발굴 → **active 완주**.
  - `-5`: ★요청 "내용" 로그 + admin 화면(오너: 매체가 보낸 URL·키워드·type 을 그대로
    보고 빠르게 검토). mig **0092** `kwave_kdb_request_terms`(호출 1건=request_group,
    항목별 term/type/문맥유무/응답상태, 30일 보존·qualityTicker 일1회 prune).
    prepare/lookup/corrections 핸들러가 비동기 배치 기록(CopyFrom, 핫패스 비차단).
    신규 admin `/admin/ondemand/requests`("요청 내용") — 호출 1건=카드 1개:
    시각·소비자·origin·기사URL + 키워드 칩(type·ctx·상태뱃지). 네비 "온디맨드" 섹션.
- 직전 안전 이미지: `kdb-app:ai-gateway-20260711-1` (additive 0092 유지한 채 태그 교체 롤백)
- migration: **0092**(kwave_kdb_request_terms — additive, 적용 완료). 0091 스키마 그대로 사용
- 배포 전 백업: `backups/kdb-20260713-120001.sql.gz` (정기 12:00 백업, 47.8MB)

## 배경 — 07-11 게이트의 부작용

07-11 고유명사 게이트는 낭비 호출을 막았지만 **근거 공급을 소비자 payload 에 요구**했다.
소비자(kstory 등)는 키워드만 보내므로 07-11~13 신규 인테이크의 98.5%(2,780건, 요청
5,725회분)가 review 보류됐고, 운영자 승인은 0건 — 사실상 "없으면 영원히 없음"이었다.
active 일일 갱신량이 612→57건으로 붕괴했다.

오너 판정: 매체의 요청은 3가지뿐(준비해줘 / 잘못됐으니 수정해줘 / 다국어 표기 줘).
**"없으면 (KDB 가) 검증 후 바로 추가작업"** — 근거는 소비자가 아니라 KDB 가 수집한다.

## 구현 — IntakeAutoVerifier (`internal/kdb/intake_autoverify.go`)

두 레인(기본 on, `KDB_INTAKE_AUTOVERIFY=0` 으로 끔):

- **fresh 레인(-4)**: prepare/lookup-miss/correction-miss 에서 review 보류가 생기거나
  기존 review 키워드가 재요청되는 순간, Store 훅(`OnReviewParked`)이 row id 를
  `intakeReviewKick` 채널(256 buffered)로 보내고 전용 goroutine 이 그 키워드만 즉시
  검증한다. 백오프 중이어도 재요청이면 재검증(exhausted=운영자 몫만 제외).
- **backlog 레인**: 2분 ticker 가 적체를 요청빈도순으로 배치 12씩 소진. 일일 예산에서
  fresh 예약분(120콜)을 남기고 멈춘다.

공통 파이프라인:

1. review 보류 row 를 **요청빈도(request_count) 순**으로 선별 (배치 기본 12)
   - 대상 사유: missing_or_unsupported_type / missing_exact_context /
     missing_type_context_cue / missing_source_evidence / ambiguous_common_for_type
   - 제외: 정체성/타입 충돌(운영자 몫), rejected 엔티티 보유 키워드(재발굴 금지)
2. Naver **encyc → news("정확검색")** 순으로 (type, 언급 문맥, 출처 URL) 증거 수집
   - type 대조 순서: row 힌트 → **같은 키워드 형제 row 들의 구체 type** → 기본
     우선순위(person→group→drama→…). 형제 우선은 실측 보정: 여회현(unknown row)의
     뉴스 단서 "출연"이 show 로 오추론돼 형제 person row 와 충돌 파킹되던 것 → 배우
     단서를 먼저 대조해 person 으로 수렴
   - 힌트가 틀렸으면 증거가 가리키는 type 으로 재지정(flag `autoverify_type_reassigned`);
     type 충돌 검사는 자기 행을 제외(자기 힌트 교정을 자기충돌로 세지 않음)
3. **기존 `gatekeeper.DecideIntake` 규칙을 그대로 재평가** — 규칙 완화 없음.
   일반어 비작품 type·형태 충돌·category 어는 증거가 있어도 여전히 막힌다(unit test).
4. 통과 시 `precheck_status='approved'`(reason `auto_evidence_encyc|news`, flag
   `auto_evidence`) + `status='pending'` → researchKick 으로 워커 즉시 발굴.
   - 'approved' 를 쓰는 이유: 워커의 fail-closed 재평가는 출처 신뢰를 whitelist 로만
     재계산하는데 이 증거는 KDB 가 Naver API 에서 1차 수집한 것이라 whitelist 밖.
     approved = "서버 자체 검증 완료" 표식(증거 문맥·출처는 row 에 저장).
5. 증거 미발견 → `autoverify_miss` + 백오프(1d→3d→소진). 소진분만 운영자 review 잔류.
6. 정확 1건 active 기존재 → provider 없이 existing_entity 로 종결.

가드:
- Naver 일일 예산 `KDB_INTAKE_AUTOVERIFY_DAILY_CALLS`(기본 600/쿼터 1,000) — 초과 시 중단
- 검색 오류(429/5xx)면 row 를 건드리지 않고 tick 중단(다음 tick 재시도)
- 같은 (정규화키,type) live 중복은 unique index 위반 감지 → duplicate 종결
- 끄기: `KDB_INTAKE_AUTOVERIFY=0` (interval/batch env 는 `.env.example` 참조)

### 소비자 응답 변경 (오너 계약 복원)

| 경로 | 종전(없을 때) | 현재 |
|---|---|---|
| `/v1/prepare` | `review` (보류) | **`preparing`** — 자동 검증 후 발굴 진행 |
| `/v1/corrections` 미보유 | `review_required` | **`preparing`** + "자동 검증 후 발굴 진행" |
| `/v1/lookup` miss | review 적재 후 방치 | 적재분을 자동 검증이 드레인 |

context/type/source_url 을 보내면 검증을 건너뛰고 즉시 발굴(`new`) — 이제 가속 옵션이지
필수가 아니다. docs.go / KDB-API.md 갱신 완료.

### 부수 수정

- `research/worker.go`: approved row 의 precheck_reason/flags 를 워커가
  'operator_approved' 로 덮어쓰던 것을 보존으로 변경 — 자동 승격이 사람 승인으로
  위장 기록되지 않게(정직한 가시성).
- `gatekeeper.IsConcreteIntakeType` export.
- `.dockerignore`/`.gitignore`에 `.gocache/` 추가(호스트 dockerized go 빌드 캐시).

## 검증

- `go test ./cmd/... ./internal/... ./pkg/...` 전부 통과 (신규 unit test 8건 포함:
  encyc 인물 확정, 인용 작품, 일반어 차단 유지, 언급없음, type 재지정, 사설IP 링크 거부 등)
- 라이브 tick 실측(2분 간격, 배치 12): promoted 2~5건/tick, 승격분(김디지(DEEGIE)→person,
  들어봐/아베크/ENOCH 1st Album→song_album 등)이 **즉시 발굴 완주**(candidate 생성 +
  auto_evidence 사유·출처URL 감사 보존, Wikidata 미검증이라 candidate 유지 = 설계대로).
  미스는 1d 백오프 기록. -2 이전에 type-conflict 로 파킹된 46건은 -3 배포 후 플래그
  해제해 재검증 대상으로 복귀시킴
- 소비자 표본: `prepare["닐슨코리아"]` → `preparing` / `corrections(여회현)` →
  `preparing` + 자동검증 안내 확인
- 최상위 요청 키워드(Pretty Girl·넷플릭스·닐슨코리아)는 **rejected 엔티티 기존재**로
  선별 제외 — 설계대로(재발굴 금지). 재심은 rejudge 드레인 몫.

## 운영 주의 / 다음 작업

- 드레인 속도: 배치 12 × 2분, 예산 600콜/일(키워드당 최대 2콜) → review 적체 약
  2,700건은 **수일에 걸쳐 요청 많은 것부터** 해소된다. 급하면
  `KDB_INTAKE_AUTOVERIFY_DAILY_CALLS`/`_BATCH` 상향(쿼터 1,000/일 주의 — VerifyIdentity
  등 다른 Naver 사용처와 공유).
- promoted 후에도 Wikidata 미검증이면 candidate 에 머문다 — 서빙(active)까지는 기존
  승급 드레인(gatekeeper/rejudge/KOFIC/MusicBrainz)이 이어받는다. "검증된 것만 서빙"
  원칙은 그대로.
- `autoverify_miss` 소진(3회)분과 정체성/타입 충돌분만 운영자 review 로 남는다 —
  `/admin/ondemand/queue` 에서 flag 로 식별 가능.
- 롤백: `.env` 의 `KDB_APP_IMAGE=kdb-app:ai-gateway-20260711-1` 복원 후
  `docker compose -f docker-compose.kdb.yml up -d kdb-app`. 스키마 변경 없음.
  자동검증만 끄려면 `KDB_INTAKE_AUTOVERIFY=0`.
- 미커밋 workspace 정리(릴리스 커밋화)와 공식 커넥터 확충(P1-3)은 여전히 남은 과제 —
  `KDB_UNRESOLVED_BOTTLENECK_ALTERNATIVES_20260710.md` §5 Phase 2 참조.
