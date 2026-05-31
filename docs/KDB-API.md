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

키가 없거나 틀리면 `401 {"error":"unauthorized"}`.

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
{ "source_text": "방탄소년단의 새 앨범…", "locale": "en", "limit": 10 }
```

### 기타 (인증 필요)
| 메서드 · 경로 | 용도 |
|---|---|
| `GET /v1/entities?q=&type=&locale=` | 엔티티 목록/검색 |
| `GET /v1/entities/{id}` | 단건 상세 |
| `GET /v1/entities/{id}/spellings` | 표기/별칭 |
| `GET /v1/entities/{id}/external-refs` | 외부 ref(tmdb/wikidata…) |
| `GET /v1/entities/{id}/relations` | 관계 |
| `GET /v1/persons/{id}` | 인물 상세 |

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

## 동작 메모

- **lazy enrich**: 조회한 엔티티에 빈 locale이 있으면 응답은 즉시 반환하고, 백그라운드에서
  권위 API(TMDb/KOFIC/MusicBrainz/Wikidata) + 검색으로 채운다. **다음 조회**부터 채워진 값 반환.
- **자동 확장**: RSS(국내 Daum + 해외) 수집 → LLM 추출 → 30분 autopilot이 분류·승격·다국어 채움·
  동명이인 정리·unknown 해소를 자동 수행. 새 엔티티는 시간이 지나며 자동으로 풍부해진다.
- **rate/캐시**: 동일 질의 반복은 캐시된 결과. 과도한 동시 호출은 자제.

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
