# KDB 이름·근거·요청 언어 준비·자동 보충 계약 — P0.04

버전 `name-readiness-design-v1` · 2026-09-12 KST · 설계 계약, 운영 적용 아님.
기준: [정체성 계약](KDB_IDENTITY_CONTRACT.md), [물리 구조](KDB_TARGET_SCHEMA.md), [실행 원장](KDB_INTEGRATION_TODO.md).

## 1. 현재 코드와 남은 간극

0115~0122의 preparations/items/readiness/events/fill_jobs, 같은 UUID의 evidence FK,
검수한 정체성에 의존하는 이름 보충, lease/generation·취소/철회 보호는 재사용한다.
현재 이름은 evidence_id 1개, evidence_dependencies는 자식 evidence_id PK라 부모 1개,
fill job은 qid NOT NULL이다. 복수 독립 근거와 QID 없는 대상을 지원하려면 이를 보강해야 한다.
TDB `verified_at`은 설치/관측 과정에도 쓰이므로 공통 검수 시각으로 복사하지 않는다.

## 2. 언어·이름·형식

목표 locale 사전은 현행 합집합 `ko,en,ja,zh-Hans,zh-Hant,vi,id,es,pt-BR,de,fr,ru,th`를 보존한다.
TDB의 th는 현재 disabled다. 사전에 존재하는 것과 새 요청/공급이 활성화된 것은 구분한다.
locale 키는 정규화된 정확 태그다. 새 공통 API에서 모호한 zh를 무조건 Hans로 바꾸지 않는다.
기존 API의 명시적인 zh→Hans 계약은 호환 adapter 내부에서만 보존하고 응답에 실제 locale을 표시한다.
zh-Hant→zh-Hans, ja→en, pt-BR→en 등의 대체는 fallback이지 해당 요청 언어 준비가 아니다.

이름 행은 UUID를 갖고 entity_id/locale/value/kind/form/기간에 귀속한다.
kind는 canonical/alias/transliteration, form은 recorded/generated/translated/unknown이다.
recorded는 원천에서 관측했다는 뜻이며 공식·검증·재사용 가능과 동의어가 아니다.
별칭·옛 이름·예명도 실제 같은 대상임이 확인돼야 한다. 검색용 NFC/공백 정규화 키는 정체성 유일 키가 아니다.
표시값은 원문을 보존하며, 정규화 버전 변경은 후보 검색 색인만 재생성한다.

생성한 한자/로마자/번역명은 출처가 있는 기록 표기로 승격하지 않는다.
공식 외국어 기관명이 의미 번역인 경우도 form을 속이지 않고 translated로 보존한다.
고객이 그런 공식 번역명을 허용하는 정책은 별도의 usable 판정으로 제공하며 strict-recorded-ready와 합산하지 않는다.
현재 TDB 문자 위생·괄호/인증 표식·원천 우선순위·인물 음차 예외 보호를 재사용한다.
검사에서 예외가 필요한 공식 상표/인용은 검수된 정책으로만 허용하며 ‘공식 사이트’라는 이유로 전체 게이트를 해제하지 않는다.

## 3. 주장별 복수 근거와 권리

근거는 주장 대상 UUID + claim_type + claim fingerprint + 원천 관측 버전에 속한다.
claim_type은 identity/name/occupation/classification/relation/profile/no_form으로 제한한다.
이름 근거에는 정확 locale/표기/기간 범위가 포함된다. 인물 정체성 근거를 모든 언어 표기의 근거로 재사용하지 않는다.
같은 기사나 같은 upstream에서 복제된 두 페이지는 두 개의 독립 근거로 세지 않는다.

`kentity_name_evidence`로 이름별 근거를 N:M 연결한다. 기본 키/stance 외 resolution·revision·판정 주체/시각/이유를 [물리 구조 §14.8](KDB_TARGET_SCHEMA.md#148-반증의-해결-상태--s08)에 추가했다.
양쪽 복합 FK로 name과 evidence가 같은 entity_id인지 확인한다. stance는 supports/contradicts다.
supports가 여럿 있어도 다른 대상의 근거를 섞을 수 없고, 해결되지 않은 contradicts는 검수 사유다.
supports는 upheld일 때만 유효하다. contradicts unresolved/upheld는 ready를 막고, upheld이면 이름 verified 유지도 거부한다. 기각/철회한 반증도 이력으로 보존한다.
동일 Entity FK만으로 충분하지 않다. 참조 위치별 claim_type 및 locale/value/직업/기간 등 주장 일치와 역방향 변경을 구조 §14.2의 제약으로 검증한다.
name.evidence_id는 전환 중 주 근거 호환 포인터로 유지하되 연결된 유효 supports만 가리켜야 한다.
독립 근거가 추가됐다고 같은 표기의 이름 행을 출처별로 무한 복제하지 않는다.

권리는 다음을 분리한다: 내부 보관 허용, 내부 검증 사용 허용, 외부 이름 공급 허용, 원문/요약 재배포 허용.
provider의 live/enabled와 export_allowed를 혼동하지 않는다. 기본값은 미확인/외부 공급 차단이다.
비밀값·연락처·불필요한 개인정보·전체 raw payload를 증거 summary에 복사하지 않는다.
source_url은 인증정보/토큰이 없는 정규 공개 URL이며 허용된 호스트만 서버에서 가져온다.
네트워크 재조회는 사설/메타데이터 IP·redirect 우회·크기 초과·시간 초과를 차단한다.

AI Hub 등 이용권이 미확인인 출처는 운영 흡수/외부 공급 대상에서 보류한다.
권리 담당자의 승인/적용 범위/유효일/조건이 기록되기 전에는 자동으로 unreviewed를 허용으로 바꾸지 않는다.
정책 허가 내용은 불변이다. approved→blocked/expired 차단만 동일 행의 revision/사유를 증가시킨다. 재허가·허가 확대는 새 버전 검토다.
최초 승인도 검수 완료한 새 version/id를 approved로 생성한다. 기존 unreviewed 근거를 그 정책에 자동 연결하지 않는다.
readiness는 선택 근거·필수 의존들의 policy ID/revision을 고정하며 읽기에서도 현재 status/revision/valid_until을 확인한다.
정책 철회와 정책 outbox는 원자적이다. Entity별 대량 무효화는 나누어 실행하되 그 지연 동안 옛 proof를 공급하지 않는다. 구조 §14.3을 따른다.
공개 웹페이지의 데이터 안에 있는 지시문은 권한·프롬프트·도구 정책을 바꾸지 못한다.

## 4. 요청별 실제 준비 판정

단위는 `(owner_key, preparation_id, ordinal, requested_locale)`다.
ordinal은 기사 span/입력 항목을 구분한다. 같은 문자열이 두 번 등장해도 서로 다른 사람을 바인딩할 수 있다.
identity가 ambiguous이면 이름이 발견돼도 ready가 아니다.
items의 bound UUID/identity·entity revision과 readiness를 복합 FK로 묶는다. A 입력에 B의 이름/근거를 넣는 것과 NULL proof로 ready/no_form을 만드는 것을 거부한다.
no_form은 typed no_form_evidence_id와 정확 locale/시점/형식 근거가 필수다. 물리 구조 §14.1~§14.2를 따른다.

strict-ready의 모든 조건:

1. 입력 항목이 검증된 현재 Entity UUID/identity_revision에 연결됨.
2. 요청 locale과 같은 locale의 선택된 이름이 있고 status=verified, form=recorded임.
3. 같은 UUID·해당 표기 주장에 유효한 supports 근거가 있고 미해결 반증이 없음.
4. 정체성 의존 근거와 외부 사용권이 현재 유효함.
5. 기사 기준 시점/이름 기간과 선택 정책이 일치함. 기간 불명으로 과거 사실을 단정하지 않음.
6. 이름/근거/권리/정책/잠금·입력 버전이 결과 snapshot과 같음.

| state | 의미 | 자동 후속 |
|---|---|---|
| pending | 진행 가능한 조사/보충 대기 | 예산 내 job |
| ready | 위 strict-ready 조건 충족 | proof와 준비 시각 제공 |
| ambiguous | 대상 UUID 불명/충돌 | 동명 검수 |
| unverified | 값은 있지만 승인 조건 부족 | 근거/표기 검수 |
| no_evidence | 정해진 검색에서 유효 근거를 못 찾음 | 변경/TTL 후 제한 재조회 |
| no_form | 그 locale의 해당 형식이 없다는 명시 근거로 판정 | 근거 만료/변경 시 재검수 |
| policy_blocked | 권리/금지/잠금 등 정책 차단 | 사유별 담당자 |
| stale | 입력/정체성/정책/근거 버전 변경 | 재평가, 옛 값 공급 차단 |
| failed | 일시/영구 처리 오류 | 원인별 제한 재시도 |
| cancelled | 요청 취소 | 해당 요청 추적 종료 |

fallback_value/fallback_locale와 usable_value/usable_form은 별도 필드다. fallback이나 generated 때문에 state=ready로 바꾸지 않는다.
전체 preparation=ready는 요청한 모든 유효 항목×locale이 ready일 때만 가능하다. 일부 실패·보류는 review로 표시한다.
first_ready_at은 원장상 최초 준비, ready_at은 현재 준비 시작이다. 철회하면 ready_at=NULL, first_ready_at은 이력으로 유지한다.
완료 job 수와 준비 이름/요청 수는 다르다. no_form은 DB에 빈칸이 많다는 통계로 생성할 수 없다.

## 5. 보충·캐시·늦은 응답

후보 발견/정체성 조사와 알려진 UUID의 locale 결손 보충은 다른 job이다.
입력이 이름만인 미지 후보는 context fingerprint+요청 ordinal로 추적하며 기존 동명 UUID를 임의 대입하지 않는다.
알려진 이름이 기사에 있다는 이유로 다른 span의 미지 동명을 스캔에서 제거하지 않는다.

공통 보충 입력 fingerprint는 다음 정규 JSON의 SHA-256이다:

```text
entity_id + identity_revision + entity_revision
+ exact_locale + provider_namespace + source_identity/reference_generation
+ selected_name_revision + evidence_set_revision + rights_policy_revision
+ spelling_policy_version + request_form_policy + lock_revision
```

DB UNIQUE는 `(entity_id,locale,input_fingerprint,policy_version,scope_key)`이며 scope_key는 공통이면 global,
고객 전용이면 서버 인증으로 정한 tenant namespace다. 클라이언트가 임의 owner_key를 지정해 타 고객 값을 조회할 수 없다.
같은 UUID/입력의 공통 조사 결과는 공유할 수 있지만 고객 전용 표기/기사 본문/설정은 공통 근거로 승격하지 않는다.
여러 요청이 같은 공통 job을 기다릴 수 있다. 하나의 요청 취소가 다른 요청의 job까지 취소하지 않도록 waiter를 분리한다.

worker는 짧은 트랜잭션에서 claim/lease_token/generation을 부여받고 DB 잠금 밖에서 원천·Gemma를 호출한다.
commit 때 entity/identity/source/policy/lock revision, lease/generation, 취소, 권리, 타입 충돌을 다시 검사한다.
오래된 결과는 stale로 기록하고 새 UUID로 재목적화하지 않는다. 다른 동명의 빈칸을 채우는 재사용은 금지다.
새 provider는 공개된 고유 식별자/검수된 binding으로 조회한다. QID가 없는 것이 곧 조사 불가능의 뜻은 아니다.

캐시 키는 `(scope_key,entity_id,identity_revision,locale,as_of,selection_policy_version,dependency_epoch)`다.
정규화된 이름 문자열만을 키로 한 최종 표기 캐시는 금지한다. 후보 검색 캐시는 후보 목록만 저장한다.
권리 철회/병합/분류 변경은 DB에서 epoch를 올리고 outbox로 캐시를 무효화한다.
epoch/권리 검증을 할 수 없을 때 strict 공급은 fail-closed다. TTL만으로 권리 철회 지연을 정당화하지 않는다.

## 6. 재시도·자동 등록 경계

| 원인 | 동작 |
|---|---|
| 네트워크 timeout/429/5xx | Retry-After/지수 backoff+jitter, 원천별 예산 내 최대 3회, 고갈 시 failed |
| 근거 없음 | 동일 입력은 기본 24시간 내 반복하지 않음. 새 출처/입력·정책 변경 또는 제한 수동 재조회로 해제 |
| 권리/유형/동명 충돌 | 자동 재시도 금지, 검수로 전환 |
| 오래된 입력/취소/lease 만료 | 결과 저장 금지. 현재 수요가 있으면 새 generation만 생성 |
| Gemma 응답 오류/검증 실패 | 제한 재시도 후 검수. 같은 무변경 결과를 야간마다 전량 재생성 금지 |

정해진 출처/유형/locale의 독립 정답 검증을 통과한 정책만 자동 등록·승격할 수 있다.
HIGH/MEDIUM/LOW 모델 점수만으로 권리·동일인·공식 표기를 승인하지 않는다.
새 정치·경제·스포츠 원천은 source policy/용도/권리/holdout/예산을 인수한 뒤 연결한다.
미검증 provider는 제안만 생성한다. 불필요한 TDB lane/agent 체계를 통째로 복사하지 않는다.

job마다 목적, provider, 모델/prompt 버전, 시작/종료, 호출/입출력 토큰, 변경 수, 새 준비 수, 거절 이유를 남긴다.
Gemma 야간 사용 이유는 이 지표로 확인한다. 본 설계 작업은 현재 스케줄이나 동시 실행 수를 바꾸지 않는다.

## 7. 철회와 잠금

의존성은 같은 Entity 내 DAG이며 parent evidence 여러 개를 허용한다. 복사 원천·정체성·표기·권리의 필수 의존을 구분한다.
필수 부모 하나가 무효면 파생 근거도 사용할 수 없다. 독립 supports 근거의 대안과 필수 dependencies를 혼동하지 않는다.
부모 철회 → 파생 근거 → 영향을 받은 이름/분류/직업/관계 → 요청 준비 → 캐시/Glossary 순서로 재평가한다.
독립적이고 적법한 다른 supports가 남아 있으면 그 근거로 재승인할 수 있지만 단순히 이전 verified를 유지하지 않는다.
오래된 준비 응답은 읽기에서도 proof revision을 재검사하므로 대량 전파 job이 끝나기 전 공급되지 않는다.
순환/교차 Entity 의존은 DB 검증 및 보호된 writer에서 거부한다.
waiter도 readiness와 job 양쪽의 UUID/locale/identity·entity revision/scope에 복합 FK로 연결한다. 인증된 owner의 scope 또는 global만 사용하고 재해소 시 옛 waiter를 명시 종료한다. 구조 §14.6을 따른다.

Entity 전체 잠금과 이름/직업/속성 행 잠금을 분리한다. 자동 writer는 둘 중 하나라도 잠기면 변경하지 않는다.
수동 정정은 기대 revision·사유·권한·전후값을 요구한다. 잠금은 권리 철회로 잘못된 값을 계속 외부 공급하게 하는 권한이 아니다.
철회는 물리 삭제가 아니라 사용 차단과 이력 보존을 기본으로 한다. 원천 재수집 시 차단 이유/정정 버전도 검사한다.
사용자가 확정 이름을 비워 둔 경우 별칭을 자동 승격해 그 판단을 되돌리지 않는다.
저장 직전 부모 잠금 후 현재 잠금·source 우선순위·정정 guard를 다시 읽고, 실제 저장 결과·감사·멱등 결과를 원자적으로 대조한다.
현행 TDB installName 규칙 재사용은 경쟁 보호까지 이미 통과했다는 뜻이 아니다. [공통 저장·증분 계약](KDB_WRITER_DELTA_CONTRACT.md)의 X01~X12를 P1/P2에서 시험한다.

## 8. 검증 경계

설계 검토: M06/M07/M12/M13/M14, fallback≠ready, no_evidence≠no_form,
근거 다중 부모/독립 근거/권리 철회, A/B 동명 job·캐시·scope 교차 오염을 명세했다.
현재 설계의 실제 DB/API/worker 구현 및 회귀 시험은 P1/P4에서 수행한다.
P0.04 설계 산출물은 물리 데이터 사전의 새 컬럼/키와 연결해 검사한다.
설계 산출물 완료와 실제 소비자 요청·권리 인수는 다르다. 후자가 없으면 G0/운영 준비 완료로 처리하지 않는다.
