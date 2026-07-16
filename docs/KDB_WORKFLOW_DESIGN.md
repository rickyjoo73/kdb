# KDB 워크플로우 설계 (SSOT)

작성 2026-07-16 (오너 지시: "워크플로우를 명확하게 설계해야 문제없이 빠르게 처리된다").
admin `/admin/workflow` 보드는 이 문서의 단계 정의를 그대로 시각화한다.

## 0. 원칙

- **두 레인**: 신규 유입 = 실시간(카드·SLA), 백로그 = autopilot 유휴 소진(배지·감소 추이).
- **한 키워드는 동시에 한 단계에만 존재한다.** 이미 서빙 중(active)인 키워드의 재요청은
  큐를 거치지 않고 `existing_entity`로 즉시 종결한다(발굴·심사 자원 낭비 금지).
- **추측은 빈칸**: 어떤 단계도 확신 없는 값을 서빙 필드에 쓰지 않는다.
- **소비자 입력은 힌트일 뿐**: 요청의 type/context 는 검증 대상이지 진실이 아니다(§6-B).

## 1. 단계 정의 (열 = 보드 컬럼)

| # | 단계 | 진입 조건 | 나가는 조건 | SLA |
|---|------|-----------|-------------|-----|
| ① | 유입 대기 | 소비자 요청 miss (prepare / lookup-miss / correction-miss) | 게이트 판정 즉시 | 초 단위 |
| ② | 심사 게이트 | 게이트 판정 = 보류(review) | 자동검증(근거수집→재평가)이 승격/기각 | 목표 ≤1h (실측 p50 5.3h — 미달, §6-A) |
| ③ | 발굴·검증 | 게이트 pass/approved → pending | 엔티티 생성(active/candidate) 또는 no_match/실패 | 평균 15s |
| ④ | 다국어 채움 | 엔티티 생성 후 locale 빈칸 존재 | 우선 locale(en) 채움 — 공식소스→검색→MT 폴백 | en은 생성 직후 |
| ⑤ | 서빙 도달 | active + 소비자 API 응답에 사용 | (종착) 상위 소스 도착 시 값 자동 업그레이드 | — |

## 2. 유입 출처 (①의 입구 — 반드시 구분 기록: `intake_origin`)

| origin | 의미 | 7일 실측 |
|--------|------|---------|
| `prepare` | 소비자가 "곧 쓸 키워드" 사전 준비 요청 (/v1/prepare) | 3,954 |
| `lookup-miss` | 번역 조회(/v1/lookup)가 miss → 자동 발굴 등록 | 1,370 |
| `correction-miss` | 교정 요청에서 파생 | 83 |
| `unknown` | 구버전/내부 경로 (origin 미표기 — 소비자 가이드로 축소 대상) | 1,066 |

"번역 요청"과 "추후 사용 키워드 요청"은 **테이블은 하나로 병합**하되 origin 으로 구분해
보드·통계에서 따로 읽는다. 처리 우선순위: lookup-miss(지금 당장 빈칸) > prepare(사전).

## 3. 중복 처리 규칙 (K-POP 사건, 07-16 확정)

1. 요청 정규화키(`intake_normalized_key`)가 **active 엔티티와 일치** → 큐 신설 없이
   `existing_entity` 종결. (요청 type 이 unknown/term 이면 무조건 호환으로 간주)
2. 같은 (정규화키, type) 의 **live 큐 행이 이미 발굴 중** → 새 행은 `duplicate_live_request`
   로 종결. 이는 "심사할 일"이 아니므로 보드 게이트 컬럼에서 제외하고 제외 건수만 표기.
3. 보드의 모든 컬럼은 키워드 단위 dedup (`DISTINCT ON entity_ko`).

## 4. 백로그 드레인 (레인 2)

| 백로그 | 정의 | 소진 주체 |
|--------|------|----------|
| 미해결 보류 | precheck review + active 없음 | intake-autoverify (Naver 근거→재평가) |
| en 빈칸 | active + canonical_en='' (가드기각 188 포함) | Enrich L5 + translate-fill / 가드기각분은 수동·가드튜닝 |
| ja·vi 등 빈칸 | 우선 locale 미채움 | 공식소스 drain 4종 + LocalFill |
| candidate 적체 | 승급 대기 | rejudge·drain (autopilot 30분 주기) |

## 5. 서빙 값의 소스 우선순위

`kdb_source_priority()`: operator(1) > 권위API(TMDb·KOFIC·MusicBrainz…) > wikidata(5) >
검색그라운드 > codex/gtranslate(7·8). 하위 소스 값은 상위 소스 도착 시 자동 교체.
MT(gtranslate) 값은 `provenance=machine-translation` 표기로 서빙, verified_only 제외.

## 6. 알려진 설계 결함과 수정 계획 (2026-07-16 실측)

- **A. 게이트 해소 지연**: 보류→해소 p50 5.3h, 24h+ 방치 3,519건(7일). 자동검증 처리량이
  유입을 못 따라감. → autoverify 주기·동시성 상향 + 보류 우선순위(lookup-miss 먼저).
- **B. ★타입 힌트 오염**: 소비자 requested_entity_type 이 검증 없이 엔티티 타입으로 굳음.
  실측(7일, 자동승인 경로 active): 가수 박학기·이승재→song_album, 유리상자→person,
  문천식·박정훈→character, "K-POP"(장르 일반어)→event_tour active. character 39건 다수 의심.
  → 발굴 시 타입은 근거에서 재판정(기사맥락 판별 엔진)하고 힌트는 tie-breaker 로만.
    기존 오염분은 역추적 배치로 재판정.
- **C. 일반어 게이트 누수**: 백과사전 근거(auto_evidence_encyc)가 "실존"만 증명하고
  "K-엔터 고유명사"는 증명 못 함 → 장르·업계 일반어(K-POP, 아이돌, 한류…) 차단 목록.
- **D. 요청 품질**: 소비자 가이드 부재 → `docs/KDB-REQUEST-GUIDE.md` (07-16 신설) 배포.
