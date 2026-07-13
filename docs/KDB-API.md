# KDB API — 외부 검색요청 가이드

K-content 고유명사/인물 **다국어(9개 언어) 정규화 DB**를 조회하는 HTTP API.
mediafine 등 외부 서비스는 이 API로 KDB를 사용한다(KDB 테이블 직접 접근 금지).

- **Base URL**: `https://kdb.aiinplanet.com`
- **웹 문서**: `https://kdb.aiinplanet.com/docs` (이 파일의 웹 버전)
- **포맷**: 요청·응답 JSON (UTF-8)
- **인증**: 모든 `/v1/*` 엔드포인트(헬스/docs 제외)는 API 키 필요

## 우리가 제공하는 DB (범위)

KDB는 **K-엔터테인먼트 고유명사**만 다룬다. 일반 지명·비-K 인물·일반 기업 등은
범위 밖이며, 요청해도 `out_of_scope`로 응답하고 등록하지 않는다(도메인 품질 보호).

다루는 **entity_type (13종)**:

| type | 설명 | 표기 방식 |
|---|---|---|
| `person` | 인물(배우/가수/아이돌/감독/MC/스포츠…) | 현지 **음역** |
| `group` | K-pop 그룹/듀오 | 현지 음역(공식 라틴명 포함) |
| `drama` | 드라마 | 공식 현지 **제목(번역)** |
| `movie` | 영화 | 공식 현지 제목(번역) |
| `show` | 예능/버라이어티 | 공식 현지 제목(번역) |
| `song_album` | 곡/앨범 | 공식 표기 |
| `agency` | 소속사(HYBE/SM/JYP…) | 현지 통용 표기 |
| `channel_outlet` | 방송사/매체 | 현지 통용 표기 |
| `brand_place` | 브랜드/장소 | 현지 통용 표기 |
| `event_tour` | 콘서트/투어/시상식 | 공식 표기 |
| `character` | 작품 속 캐릭터 | 현지 표기 |
| `term` | K-문화 용어(한복/김치…) | 현지 통용 표기 |

> **표기 vs 번역**: 인물·그룹은 **현지 음역**(박보검 → ja `パク・ボゴム`, zh `朴宝剑`),
> 작품(드라마/영화/예능)은 **공식 현지 제목(번역)**(오징어 게임 → en `Squid Game`,
> es `El juego del calamar`, pt_br `Round 6`)을 제공한다.

## 협업 워크플로우 (단방향 제공 아님)

KDB는 클라이언트와 교류하며 품질을 올린다:

1. **받기/준비** — 기사 작성 시점에 고유명사(한글)를 `POST /v1/prepare`로 미리 던지면,
   조회 전에 다국어 번역을 백그라운드로 준비한다(사람 개입 없음).
2. **보내기** — `POST /v1/lookup` · `POST /v1/entities/match`로 완성된 표기를 조회.
3. **개선** — 오역을 발견하면 `POST /v1/corrections`로 신고 → 권위 외부소스 검증 시
   자동 반영, 미달은 운영자 심사. 신고가 쌓일수록 품질이 향상된다.

## 인증

다음 중 하나의 헤더로 API 키를 전달한다(키는 운영자 발급 — `.env KDB_API_KEYS`):

```
X-KDB-Key: <YOUR_API_KEY>
# 또는
Authorization: Bearer <YOUR_API_KEY>
```

키가 없거나 틀리면 `401`. 에러 응답 형식은 아래 **에러 계약** 참고.

## 지원 언어 (locale)

`ko`(기준) · `en` · `ja` · `vi` · `zh`(간체) · `zh_hant`(번체) · `es` · `id` · `pt_br` — 9개.

응답의 `canonical_<locale>` 필드로 제공. 빈 칸은 조회 직후 백그라운드로 자동 채워져
**다음 조회부터** 값이 나온다(lazy enrich: TMDb/KOFIC/MusicBrainz/Wikidata + 검색).

---

## 엔드포인트

### `GET /v1/health` (인증 불필요)
```bash
curl https://kdb.aiinplanet.com/v1/health
# {"ok":true,"service":"kdb-api"}
```

### `POST /v1/prepare` — 받기 + 빠른 준비 (★협업 진입점)
기사에 등장할 한글 고유명사를 미리 던지면 KDB가 그 사이 다국어 번역을 준비한다.
각 term은 **문자열** 또는 **`{ko, type, context}` 객체**. 처음 보는 키워드는
`type + source_url + 실제 언급 문맥 + 타입 단서`가 확인되면 즉시 외부 조사를 시작하고(`new`),
근거가 부족하면 KDB가 **자동 검증(Naver 근거수집 → 게이트 재평가)** 후 발굴로 이어간다(`preparing`).
키워드만 보내도 되지만, context/type을 함께 보내면 검증 단계를 건너뛰어 더 빠르다.
```json
{ "terms": [
    {"ko": "박보검", "type": "person", "context": "배우 박보검이 출연했다"},
    {"ko": "폭싹 속았수다", "type": "drama"},
    "아이유"
  ],
  "locales": ["ja", "zh", "es"],
  "source_url": "https://kstory.aiinplanet.com/articles/…" }
```
응답:
```json
{ "items": [
  { "term": "박보검", "status": "ready", "type": "person",
    "values": {"ja": "パク・ボゴム", "zh": "朴宝剑", "es": "Park Bo-gum"} },
  { "term": "폭싹 속았수다", "status": "preparing", "missing": ["es"] },
  { "term": "아이유", "status": "ready", "values": {...} }
] }
```
| status | 의미 |
|---|---|
| `ready` | 요청 locale 다 준비됨 — 즉시 사용 가능 |
| `preparing` | 빈 locale을 백그라운드로 준비 시작 — 잠시 후 조회하면 채워짐 |
| `new` | 처음 보는 고유명사 — 발굴·분류 파이프라인 진입(K-콘텐츠면 준비) |
| `preparing`(신규어) | 근거 부족 — KDB 자동 검증(Naver 근거수집) 후 발굴 진행. 검증 실패분만 운영자 검토 잔류 |
| `out_of_scope` | K-콘텐츠가 아니거나 노이즈 — 준비/등록하지 않음 |

> 권장: 기사 발행 **전에** prepare를 호출해 두면, 실제 번역 조회(`match`) 시점엔
> 이미 `ready` 상태가 된다. type 힌트를 주면 동명이인(예: 그룹 vs 용어) 구분이 정확하다.

### `POST /v1/lookup` — 단건 검색
요청:
```json
{ "query": "도깨비", "type": "drama", "status": "active", "limit": 5 }
```
| 필드 | 필수 | 설명 |
|---|---|---|
| `query` | ✅ | 검색어(한국어/별칭/외국어 표기 모두 매칭) |
| `type` | | entity_type 필터(아래 목록) |
| `status` | | `active`(기본 권장) / `candidate` / `rejected` |
| `limit` | | 최대 결과 수(기본 서버값) |

예시:
```bash
curl -X POST https://kdb.aiinplanet.com/v1/lookup \
  -H "X-KDB-Key: <YOUR_API_KEY>" \
  -H "content-type: application/json" \
  -d '{"query":"도깨비","limit":2}'
```
응답:
```json
{
  "query": "도깨비",
  "matches": [
    {
      "id": "3605f5cc-…",
      "entity_type": "drama",
      "canonical_ko": "도깨비",
      "canonical_en": "Guardian: The Lonely and Great God",
      "canonical_ja": "トッケビ",
      "canonical_vi": "Yêu Tinh",
      "canonical_zh": "孤单又灿烂的神：鬼怪",
      "canonical_zh_hant": "孤單又燦爛的神：鬼怪",
      "canonical_es": "GOBLIN: El solitario ser inmortal",
      "canonical_id": "Goblin",
      "canonical_pt_br": "Goblin",
      "aliases": { "...": ["..."] },
      "confidence": 0.95,
      "status": "active"
    }
  ]
}
```

### `POST /v1/lookup/bulk` — 다건 검색
```json
{ "queries": ["방탄소년단", "블랙핑크"], "limit": 1 }
```
응답: `{ "results": [ {LookupResponse}, … ] }`

### `POST /v1/entities/match` — 본문에서 엔티티 매칭
기사/문장 본문에서 알려진 엔티티를 찾아 해당 locale 표기로 매핑.
```json
{ "source_text": "방탄소년단의 새 앨범…", "locale": "en", "limit": 10,
  "min_confidence": 0.9, "status": "active", "verified_only": true }
```
| 요청 필드 | 필수 | 설명 |
|---|---|---|
| `source_text` | ✅ | 매칭 대상 본문 |
| `locale` | ✅ | 목표 표기 언어 |
| `limit` | | 최대 결과 수(기본 100, 최대 200) |
| `min_confidence` | | 이 값 미만 매칭 제외(기본 0.50 floor) |
| `status` | | `active`(기본) / `candidate` / `rejected`. **빈값=`active`** — rejected merge-tombstone 유출 방지를 위해 미지정 시 active 만 반환한다. 전체 tier 전수 감사는 match 가 아니라 `GET /v1/entities?status=` 를 사용한다. |
| `verified_only` | | `true` 면 **반환되는 locale 값 자체**의 출처가 검증 소스(operator-locked·wikidata-label·external-db·media-consensus)인 것만 반환. en 은 wikidata 인데 ja 는 codex 합성인 흔한 경우, `locale=ja`로는 ja 의 출처만 본다(엔티티 전역 아님). |

응답(각 매칭):
```json
{
  "entities": [
    { "id": "593e0871-…", "ko": "방탄소년단", "locale_name": "BTS",
      "entity_type": "group", "confidence": 1.0, "status": "active",
      "operator_locked": true, "provenance": "operator-locked",
      "locale_source": "operator-locked",
      "source_urls": ["https://www.wikidata.org/wiki/Q…"],
      "updated_at": "2026-06-01T01:06:24Z", "note": "…" }
  ]
}
```
| 응답 필드 | 설명 |
|---|---|
| `id` | 엔티티 UUID (lookup 재조회·dedup 용) |
| `confidence` | 0~1 신뢰도 — **번역 힌트 게이팅에 사용** |
| `status` | `active` / `candidate` / `rejected` |
| `operator_locked` | 운영자 수동 확정(가장 신뢰 높음) |
| `provenance` | **반환된 locale 값의** 출처 라벨(신뢰 내림차순): `operator-locked` · `wikidata-label` · `external-db`(tmdb/musicbrainz 등) · `media-consensus`(≥2매체 합의) · `wikipedia-langlinks` · `media-single`(단일 매체 관측) · `llm-only`(미검증 합성) |
| `locale_source` | 그 값의 raw source 컬럼(`wikidata-label`/`codex-fallback`/`rss-observation:<domain>` 등) — 소비자 자체 게이팅용 |
| `source_urls` | 출처 URL(wikidata/wikipedia 등) |
| `updated_at` | 마지막 갱신 시각(self-heal 추적용, RFC3339) |
| `disambig` | 동명이인 구분 라벨(예: `(김하늘 배우)`). 비어있으면 단독 |
| `locale_ambiguous` | `true` 면 반환된 `locale_name` 이 같은 type 의 다른 active 엔티티와 겹침 → 번역 시 어느 엔티티인지 확인 권장(예: 영문 "Sam Kim"이 셰프·가수 둘) |

> **권장 게이팅** (소비자 측): 번역 힌트로 주입할 땐 `operator_locked=true` 또는
> `confidence>=0.9` 만 신뢰하고, 그 미만은 힌트로 쓰지 말 것(빈 힌트가 틀린
> 힌트보다 안전). 요청 단계에서 `verified_only:true` + `min_confidence:0.9` 로
> 서버측 필터링도 가능하다.

### `POST /v1/entities/match/bulk` — 다건 본문 매칭
기사당 1회 호출로 여러 본문을 배치 매칭(최대 50건).
```json
{ "source_texts": ["본문1…", "본문2…"], "locale": "ja",
  "min_confidence": 0.9, "verified_only": true }
```
응답: `{ "results": [ { "source_text": "본문1…", "entities": [ {MatchedEntity}, … ] }, … ] }`
(게이팅 파라미터 `min_confidence`/`status`/`verified_only`/`limit` 는 match 와 동일하게 적용.)

### 기타 (인증 필요)
| 메서드 · 경로 | 용도 |
|---|---|
| `GET /v1/entities?q=&type=&status=&limit=&offset=&min_confidence=&updated_since=` | 엔티티 목록/검색 (offset 페이지네이션 — 전수 감사 가능) |
| `GET /v1/entities/{id}` | 단건 상세 |
| `GET /v1/entities/{id}/spellings` | 표기/별칭 |
| `GET /v1/entities/{id}/external-refs` | 외부 ref(tmdb/wikidata…) |
| `GET /v1/entities/{id}/relations` | 관계 |
| `GET /v1/persons/{id}` | 인물 상세 |

> `GET /v1/entities` 는 `limit`(기본 20, 최대 100) + `offset` 으로 페이지네이션.
> `status` 로 active/candidate/rejected tier 를 분리 열람 가능(감사용).
> `min_confidence`(0~1) · `updated_since`(RFC3339, 예 `2026-06-01T00:00:00Z`) 로
> 저신뢰 후보 모집단·최근 변경분만 조회 가능(정기 오탐 감사용).

### `POST /v1/corrections` — 표기 정정 신고 (클라이언트 피드백)
KDB 가 잘못된 현지 표기를 줬을 때, 클라이언트가 올바른 표기를 근거와 함께 신고한다.
read 키로도 호출 가능 — **자동 반영은 Wikidata 가 독립 확인한 경우에만** 이뤄지므로
단일 클라이언트가 임의로 데이터를 바꿀 수 없다(오남용 저항).
```json
{ "entity_id": "26b5083d-…", "locale": "ja",
  "returned": "リオウン", "suggested": "リョウン",
  "evidence_url": "https://ja.wikipedia.org/wiki/リョウン", "reason": "현지 위키 표기" }
```
`entity_id` 대신 `ko`(+동명이인이면 `disambig`)로도 대상 지정 가능. 응답 `result.status`:

| status | HTTP | 의미 |
|---|---|---|
| `auto_applied` | 200 | ① 문자셋 가드 통과 ② 현재 값 교체가능 ③ **Wikidata 일치 또는 codex 검증이 제안 확인** → 즉시 반영(`value` 회신, 원값 스냅샷으로 revert 가능) |
| `proposed` | 202 | KDB 검증 결과 **제3의 수정안**을 회신(`value`+`correction_id`). 클라가 확인하면 반영 |
| `queued` | 202 | 근거 미달/불확실 또는 보호된 값 → 운영자 심사 대기 |
| `rejected` | 422 | 문자셋 가드 실패, 또는 검증 결과 현재 값이 정확(이유 회신) |

**양방향 확인(KDB가 수정안을 회신 → 클라가 확인 → 반영):**
신고를 받으면 KDB가 내용을 검증(Wikidata → codex)한다. 제안이 맞으면 바로 반영하고,
KDB가 더 정확한 값을 알면 `status:"proposed"`로 수정안(`value`)을 회신한다. 동의하면
확인 요청을 보낸다:
```json
POST /v1/corrections
{ "confirm_id": 1234, "accept": true }
→ { "result": { "status": "auto_applied", "value": "..." } }
```
적용 값은 `correction-verified` source(교차검증 등급)로 기록돼, 이후 자동 파이프라인이
저신뢰 값으로 되돌리지 못한다.

> 운영자 심사(큐로 간 건): `kdb-app corrections list | approve <id> | reject <id> [사유]`.

**권장 워크플로우(지속 자가개선):** 소비자는 `verified_only:true` + `locale_source`
로 로컬 게이팅 → `llm-only` 값을 만나면 현지 표기를 `POST /v1/corrections` 로 신고 →
주기적으로 `GET /v1/entities?updated_since=<마지막동기화>` 로 개선분 델타 동기화.

---

## Entity 응답 스키마 (주요 필드)

| 필드 | 타입 | 설명 |
|---|---|---|
| `id` | uuid | 엔티티 ID |
| `entity_type` | enum | 아래 목록 |
| `canonical_ko` | string | 한국어 정규명(항상 존재) |
| `canonical_en/ja/vi/zh/zh_hant/es/id/pt_br` | string | 언어별 표기(빈 칸은 enrich 후 채워짐) |
| `aliases` | object | locale별 별칭 배열 |
| `confidence` | number | 0~1 신뢰도 |
| `status` | string | active / candidate / rejected |
| `disambig` | string | 동명이인 구분 라벨(예: `(배우)`) |
| `primary_role`,`agency`,`birth_year`,`notable_works` | | 인물 메타(person) |

### entity_type 값
`person`, `group`, `show`, `drama`, `movie`, `song_album`, `agency`,
`channel_outlet`, `brand_place`, `event_tour`, `character`, `term`, `unknown`

> 고유명사DB = 인물 제외 타입, 인물DB = `person`. 운영 구분일 뿐 API 응답은 동일 스키마.

---

## 에러 계약

성공이 아닌 모든 응답은 표준 봉투를 따른다:
```json
{ "ok": false, "error": { "code": "bad_request", "message": "source_text required" } }
```
| HTTP | `error.code` | 의미 |
|---|---|---|
| 400 | `bad_request` | 필수 필드 누락·잘못된 값(예: invalid status, locale required) |
| 401 | `unauthorized` | API 키 누락/오류 |
| 404 | `not_found` | 엔티티 없음 |
| 500 | `internal` | 서버 내부 오류 |
| 503 | `unavailable` | DB 등 의존성 일시 장애 |

- 검색/매칭 결과가 없을 때는 에러가 아니라 **빈 배열**(`"matches": []`, `"entities": []`)을 반환한다(null 아님).

## 데이터셋 버전 (`X-KDB-Version`)

모든 `/v1/*` 응답 헤더에 `X-KDB-Version: <entity수>.<max(updated_at) epoch>` 를 노출한다.
데이터가 self-heal 로 바뀌면 이 값이 변한다 — **발행 당시 어떤 버전이었는지 로깅**해 두면
재현·디버깅에 쓸 수 있다(스냅샷 `as_of` 핀 고정은 미지원, 변경 감지용 버전만 제공).

## 동작 메모

- **lazy enrich**: 조회한 엔티티에 빈 locale이 있으면 응답은 즉시 반환하고, 백그라운드에서
  권위 API(TMDb/KOFIC/MusicBrainz/Wikidata) + 검색으로 채운다. **다음 조회**부터 채워진 값 반환.
- **자동 확장**: RSS(국내 Daum + 해외) 수집 → LLM 추출 → 30분 autopilot이 분류·승격·다국어 채움·
  동명이인 정리·unknown 해소를 자동 수행. 새 엔티티는 시간이 지나며 자동으로 풍부해진다.
- **rate/동시호출**: 현재 명시적 rate limit 은 없다(soft). 핫패스에서 기사당 다건 매칭은
  `POST /v1/entities/match/bulk`(최대 50건)로 묶어 호출 수를 줄일 것. 동일 질의 반복은
  캐시된 결과. 과도한 동시 호출(수십 동시연결 초과)은 자제 — 필요 시 운영자와 협의해 한도 조정.

## 빠른 점검
```bash
# 키 없이 → 401
curl -s -o /dev/null -w '%{http_code}\n' -X POST https://kdb.aiinplanet.com/v1/lookup \
  -H 'content-type: application/json' -d '{"query":"BTS"}'

# 키로 검색 → 200 + matches
curl -X POST https://kdb.aiinplanet.com/v1/lookup \
  -H "X-KDB-Key: <YOUR_API_KEY>" -H 'content-type: application/json' \
  -d '{"query":"방탄소년단","limit":1}'
```
