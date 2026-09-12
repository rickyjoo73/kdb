# KDB/TDB API 호환 replay 계약 — P0.06 증분 조사

작성: 2026-09-12 KST. 상태: **코드 근거 조사 완료 / 합성 replay 미실행 / 실제 고객 계약 미확인**.

기준 문서는 [선택 흡수 계약 검토](KDB_ABSORPTION_CONTRACT_REVIEW.md)와
[실행 TODO](KDB_INTEGRATION_TODO.md)다. 기계 판독 fixture와 assertion은
[`checks/kdb-api-compatibility-cases.json`](checks/kdb-api-compatibility-cases.json)에 있다.
이 문서는 API·DB·운영 서버를 호출하거나 변경한 결과가 아니다. 비밀키와 원천 payload를 읽거나 기록하지 않았고,
모든 인증값은 합성 owner placeholder다.

## 판정 범위

- `source finding`: 지정한 KDB/TDB Go 코드와 migration에서 직접 확인한 현행 동작이다.
- `expected legacy`: 격리 fixture에서 현행 route에 기대할 결과다. 아직 실행 결과가 아니다.
- `proposed target`: 선택 흡수 adapter가 보장해야 할 최소 불변조건이지 구현 완료 표시가 아니다.
- `actual consumer`: 두 지정 tree에서 outbound 소비 call site를 찾지 못했다. 최근 요청 집계는 경로 사용만 보여 주며,
  UUID 저장·fallback·cursor·에러 문자열 의존 여부를 증명하지 않는다.

## 추가로 확인한 계약 차이 10개

1. **KDB 에러 봉투와 공식 client가 어긋난다.** 서버는
   `{ok:false,error:{code,message}}`를 보내지만 client는 `error` 문자열을 기대하고 decode 실패를 버린다.
   따라서 실제 client에는 세부 원인 대신 HTTP status text만 남을 수 있다.
2. **공식 KDB match client가 서버 UUID와 안전 신호를 버린다.** 서버에는 `id`, status, provenance,
   locale source/fallback/ambiguity가 있지만 client 결과형에는 없다. 이 client를 실제 고객이 쓰는지는 미확인이다.
3. **`/api` alias와 base path 조합은 별도 계약이다.** `/api/health`는 `/v1/health`가 되지만,
   base URL `/api`에 client path `/v1/health`를 붙인 `/api/v1/health`는 단순 rewrite상 `/v1/v1/health`가 된다.
4. **lookup/match는 시스템 사이에서 동형이 아니다.** TDB lookup은 기사 text·locale·region hint와
   known/unknown/form을 다루고, KDB lookup은 query 기반 Entity 부분검색이다. TDB match는 이름 배열별
   found/ambiguous/miss map이고 KDB match는 본문에서 찾은 flat Entity 배열이다.
5. **locale 규칙이 route마다 다르다.** KDB match는 alias를 정규화하고, KDB legacy prepare는 미지원값을
   request 단계에서 거부하지 않으며, durable preparations는 엄격히 거부한다. TDB lookup/match는 enabled locale의
   exact tag를 요구하지만 TDB prepare에는 같은 검사가 없다.
6. **fallback과 ready는 같은 축이 아니다.** TDB는 요청 locale의 `form`과 fallback 여부를 분리하지만 실제
   fallback locale은 전용 필드가 아니다. KDB match는 영어 fallback 가능성이 있고 durable readiness만
   exact-locale 근거와 fallback value를 분리한다.
7. **prepare의 완료 의미와 한도가 다르다.** TDB는 text에서 최대 40개 unknown을 뽑아 요청 안에서 최대 5분
   채움을 시도한다. KDB legacy는 terms를 200개로 조용히 자르고 비동기 상태를 답한다. durable readiness만
   owner·멱등성·revision·cancel을 갖는다.
8. **correction의 대상·권한·원자성이 다르다.** 양쪽 모두 명시 ID가 있으면 함께 온 한국어 이름과 동일 대상인지
   비교하지 않는다. TDB는 모든 consumer가 즉시 설치할 수 있고 설치 뒤 감사 row를 별도 쓰므로 후자 실패 시
   값만 바뀔 가능성이 있다. KDB는 queued/proposed/confirm과 복수 HTTP 상태를 쓴다.
9. **TDB changes는 timestamp tie 외에 이름-only 변경도 놓친다.** endpoint는 `tdb_places.updated_at`만 읽고,
   검토한 이름 writer는 `tdb_place_names.updated_at`만 바꾼다. 병합 item에는 `merged_into`가 없고 loser 상세는
   active 필터 때문에 404다.
10. **KDB 목록도 안전한 delta cursor가 아니다.** 잘못된 `updated_since`를 조용히 무시하고, `>=`와 offset,
    relevance/confidence/updated_at 정렬을 쓰며 ID tie-break·next cursor가 없다.

## Replay matrix

| ID | 합성 요청 | 현행 응답 의미(source finding 기반) | 목표 불변조건 | 실제 고객 | 상태 |
|---|---|---|---|---|---|
| R01 | KDB match에 `source_text` 없이 POST | 400 nested error; 공식 client는 message 유실 가능 | status/code/message/retryable 보존 | 미확인 | 미실행 |
| R02 | `/api/health`와 `/api` base + `/v1/health` | 단순 rewrite 결과가 각각 `/v1/health`, `/v1/v1/health` | 한 번만 정규화, 두 legacy 조합의 승인 결과 고정 | `/api/v1` 사용 미확인 | 미실행 |
| R03 | 같은 이름·다른 UUID TDB match, region hint 유/무 | ambiguous 후보 또는 hint로 found | 후보 UUID 유지, 유명도/첫 행 자동 선택 금지 | hint 의존 미확인 | 미실행 |
| R04 | known+unknown 장소와 fallback이 섞인 기사 lookup | places·unknown·fallback_used·form을 별도 반환 | requested/resolved locale, form, provisional 분리 | prompt 처리 미확인 | 미실행 |
| R05 | `zh-Hans/ZH_hant/pt-PT/de/th/xx` route matrix | route별 normalization·enabled·400 규칙이 다름 | legacy 규칙 보존, 신규 exact locale 명시 | 사용 locale 미확인 | 미실행 |
| R06 | TDB unknown 41개, KDB terms 201개 prepare | 40/200 cap; 동기 fill과 비동기 상태가 다름 | accepted/truncated 수와 execution model 노출 | batch 크기 미확인 | 미실행 |
| R07 | 같은 멱등키 재사용·owner 교차·stale cancel | 같은 owner/body 재사용, 다른 body·revision은 409 | owner 격리, revision CAS, workspace로 owner 대체 금지 | idempotency 사용 미확인 | 미실행 |
| R08 | ID A와 이름 B를 함께 보낸 correction 및 fault injection | ID 우선; TDB 값 설치와 감사 기록 비원자 가능 | target mismatch 차단, write+audit 원자, 재시도 중복 0 | 교정 retry 미확인 | 미실행 |
| R09 | 같은 timestamp의 TDB 변경 1001개 | 1페이지 1000개 뒤 `>` cursor가 나머지를 누락 | frozen snapshot/cohort·late commit 경계에서 중복 허용+dedup 후 누락 0 | cursor 저장 방식 미확인 | 미실행 |
| R10 | parent touch 없는 name upsert/withdraw | 현 `/changes` item에 나타나지 않음 | name revision의 upsert/withdraw event 제공 | name delta 소비 미확인 | 미실행 |
| R11 | loser L→survivor S 병합 뒤 changes/detail/snapshot | changes는 merged지만 target 없음; loser 404; snapshot 제외 | requested/resolved ID와 tombstone/redirect 제공 | loser ID 저장 여부 미확인 | 미실행 |
| R12 | invalid KDB time과 offset 중간 변경 | invalid time은 전체검색처럼 진행 가능; page 이동 가능 | invalid cursor 400, epoch/cohort·tombstone·dedup 후 누락 0 | KDB delta 사용 미확인 | 미실행 |

JSON fixture의 request에는 실제 HTTP method·route·body field·header 이름이 들어 있다. 토큰은
`${KDB_READ_OWNER_TOKEN}` 같은 owner 기호이며 실제 키 literal이 아니다. 대량 입력 R06의
`TEXT_WITH_41_UNKNOWN_PLACE_NAMES`와 `KDB_TERMS_201`는 **아직 materialize하지 않은 생성기 요구사항**이다.
결정적 생성 코드나 고정 fixture가 이미 존재한다는 뜻이 아니며 P1 실행 전에 동결해야 한다.

## 최소 compatibility adapter 계약

1. 기존 KDB/TDB route, HTTP status, content type, response field와 ordering 의미는 고객별 replay 승인 전 제거하지 않는다.
2. `/lookup`과 `/match`, sync prepare와 durable preparation을 이름이 비슷하다는 이유로 서로 redirect하지 않는다.
3. 모든 결과에 내부적으로 `requested_id`, `resolved_id`, identity state, revision을 보존한다. legacy shape에 필드 추가가
   위험하면 versioned 응답이나 sidecar metadata를 쓴다.
4. exact locale 이름, fallback, generated, translated, missing, no-form을 분리한다. fallback으로 strict-ready를 만들지 않는다.
5. correction은 대상 ID·이름/disambiguator 일치를 확인하고 값 변경·근거·감사 기록을 한 transaction/idempotency 경계에 둔다.
6. 변경 feed는 name·alias·권리 철회·삭제·병합을 action으로 전달하고 snapshot/cohort epoch와 tombstone floor를
   제공한다. `(updated_at,id)` tuple은 동결된 snapshot 안의 정렬에는 쓸 수 있지만 mutable row·late commit까지
   해결하지 않는다. 재전달 중복은 허용할 수 있으며 immutable event/revision으로 소비자가 dedup한 뒤 누락이 0이어야 한다.
   sequence를 commit 전에 발급한다면 숫자 순서를 commit 가시성 순서로 간주하지 않고 closed high-water mark를 둔다.
7. KDB와 TDB 키 저장소를 복제하지 않는다. 인증된 consumer/owner, operation scope, 401/403/429와 `Retry-After`를 보존한다.
8. 공식 client가 신규·기존 error 봉투와 서버가 주는 UUID/fallback/provenance를 손실 없이 표현하게 한다.

## 근거 위치

| 주제 | 코드 근거 |
|---|---|
| KDB route/auth/shape/error/list | `KDB internal/kdbapi/api.go:429-488, 530-590, 644-653, 2028-2100, 2192-2324, 3376-3385` |
| KDB durable preparation | `KDB internal/kdbapi/preparations.go:18-43, 64-138`; `internal/kdb/readiness/types.go:90-143, 170-207` |
| KDB correction | `KDB internal/kdb/corrections/corrections.go:37-61, 214-255` |
| KDB client | `KDB pkg/kdbclient/client.go:83-126, 301-321, 492-545`; `client_test.go:14-21, 465-475` |
| TDB route/auth/lookup/match/detail/changes/snapshot | `TDB internal/api/server.go:77-160, 179-241, 318-477` |
| TDB prepare/correction | `TDB internal/api/pipeline.go:23-78, 98-154` |
| TDB ID/fallback/form | `TDB internal/lookup/resolve.go:14-59, 149-290` |
| TDB parent/name timestamps | `TDB migrations/0003_places.sql:9-59` |

JSON에는 위 핵심 원천 10개의 SHA-256을 기록했다. 해시는 조사 시점 파일 식별용이며 빌드 provenance나 운영 배포
버전을 대신하지 않는다.

## P0.06 계약 인수 게이트

P0.06은 **고객 계약과 실행할 fixture 기준을 인수하는 설계 단계**다. 격리 replay 실행을 완료 조건으로 두면
G0 뒤의 P1을 P0.06이 다시 요구하는 순환이 생기므로, 이 단계에서는 다음만 요구한다.

1. 유지 고객 서비스와 전환 책임자를 실명 인수하고, 고객별 필수 route·저장 ID 종류·locale/fallback·cursor·error/retry 계약을 적는다.
2. 12개 fixture의 입력·데이터 전제·legacy 기대·목표 assertion·실행 책임자를 검토하고 승인한다. 실행했다고 표시하지 않는다.
3. 각 route를 유지/버전 전환/제외로 분류하고 실제 고객 확인 없는 종료를 금지한다.
4. snapshot/cohort, late commit, 중복 dedup, merge/delete/withdraw tombstone과 전체 재동기화의 목표 계약을 고정한다.

## P1/P5 실행 및 기존 API 종료 게이트

1. P1 격리 DB에서 승인된 fixture를 실제 생성·동결하고 legacy/proposed adapter replay를 수행한다.
2. P5에서 고객별 확정 계약을 dual-read/replay하고 해당 고객의 전환 승인을 받는다.
3. 설명 없는 ID 치환, tenant 교차 노출, fallback의 ready 오인과 **소비자 dedup 후** change event 누락을 각각 0으로 확인한다.
4. tuple cursor만 확인하지 않고 frozen snapshot/closed cohort, late commit 포착, epoch 경계, tombstone 보존을 시험한다.
5. event 재전달 중복은 허용할 수 있으나 immutable event/revision key로 제거 가능해야 한다. sequence 발급 순서와
   commit 가시성 순서가 다를 때도 closed high-water mark 밖으로 이벤트가 사라지지 않아야 한다.
6. 보안·권리 차단 때문에 일부러 달라지는 응답은 versioned incompatibility로 승인하고 rollback/고객 안내를 붙인다.
7. 요청 원장 수나 `last_used_at`만으로 미사용을 판정하지 않는다.

R02의 rewrite 기대는 Go server를 띄운 결과가 아니라 source의 문자열 치환을 옮긴 것이다. standalone Node로
fixture/schema나 순수 rewrite 모델을 검사하더라도 그것은 JSON 정적 검사이지 Go router·middleware 통합 replay가 아니다.
