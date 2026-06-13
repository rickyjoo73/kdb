# KDB API — 외부 검색요청 가이드

K-content 고유명사/인물 **다국어(9개 언어) 정규화 DB**를 조회하는 HTTP API.
mediafine 등 외부 서비스는 이 API로 KDB를 사용한다(KDB 테이블 직접 접근 금지).

- **Base URL**: `https://kdb.aiinplanet.com`
- **포맷**: 요청·응답 JSON (UTF-8)
- **인증**: 모든 `/v1/*` 엔드포인트(헬스 제외)는 API 키 필요

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
| `status` | | `active`/`candidate`/`rejected` 로 제한(빈값=제한 없음) |
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
| `auto_applied` | 200 | suggested 가 ① locale 문자셋 가드 통과 ② 현재 값이 교체가능(저신뢰 source) ③ **Wikidata label/sitelink 와 일치** → 즉시 반영(source=wikidata-label, 원값은 revert 가능하게 스냅샷) |
| `queued` | 202 | 근거 미달이거나 현재 값이 보호됨(operator-locked·검증 source) → 운영자 심사 대기 |
| `rejected` | 422 | suggested 가 해당 locale 문자셋 가드 실패(예: ja 칸에 한글) |

> 운영자 심사: `kdb-app corrections list | approve <id> | reject <id> [사유]`.
> 승인 시 source=operator 로 적용된다.

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
