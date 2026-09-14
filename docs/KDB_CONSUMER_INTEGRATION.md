# KDB 연동 안내 — 번역 소비자용

2026-09-14 기준. presslocale 실측 신고에서 나온 것들을 반영했다.
공개 문서(`/v1/docs`)는 아직 갱신 전이다(P6.07) — **그때까지 이 문서가 최신이다.**

---

## 0. 기준 주소

```
https://kdb.aiinplanet.com
헤더:  X-KDB-Key: <키>      또는   Authorization: Bearer <키>
```

`http://kdb-app:9100` 은 **도커 내부 이름**이다. 그 망 안에서만 풀린다.
2026-09-14 에 어떤 소비자가 이 주소로 설정돼 있어 망 밖에서 1,200건 넘게 실패했다.
**밖에서 부르려면 위 도메인을 쓴다.**

**키 등급이 둘이다.**
- env 키 = write. 전부 가능
- DB 소비자 키 = read. `site-search`·`lock`·`PATCH /v1/entities/{id}`·`research-queue`·
  `qa/*` 를 부르면 **401 이 아니라 403** 이다. 403 은 "키가 틀렸다"가 아니라
  "이 키로는 그 일을 못 한다" 는 뜻이다.

---

## 1. 본문에서 고유명사 찾기 — `POST /v1/entities/match`

```json
{ "source_text": "드라마 사랑이 온다 가 방영된다",
  "locale": "ja",
  "include_absent": true,
  "disambiguate": false }
```

`locale` 은 **하나**다(복수 아님). `locales` 가 아니라 `locale`.

**정렬은 긴 매칭이 이긴다**(2026-09-14 수정). 그전엔 `confidence` 가 1순위였는데
그 값은 매칭 점수가 아니라 대상 자체의 품질 점수였다. 그래서 제목의 조각이
제목을 이겼고(`온다` 가 `사랑이 온다` 를 이겼다), 일본어판에 『オンダ』가 발행됐다.
**지금은 1등이 정답이다.**

### 응답에서 반드시 봐야 하는 것

| 필드 | 뜻 |
|---|---|
| `locale_name` | 그 언어 표기. **빈 문자열일 수 있다** |
| `provenance` | 값의 등급 — 아래 표 |
| `locale_source` | 원본 출처 컬럼값(tmdb·wikidata-label·kana-rule…) |
| `locale_absent` | 비었거나 영어로 대체된 **이유** |
| `fill_hint` | 그럼 **무엇을 해야 하는가** |
| `locale_fallback` | `locale_name` 이 요청 언어가 아니라 영어다 |
| `note` | 우리가 아는 위험. **읽어라** |

### `provenance` 등급 (높은 것이 이긴다)

```
operator-locked · local-usage        운영자 확정 / 현지 통용 확정
media-consensus                      현지 매체 2곳 이상 합의
external-db                          TMDb·KMDb·KOFIC·MusicBrainz·iTunes…
wikidata-label                       위키데이터 라벨
romanization · opencc · community-db 결정적 파생 / 커뮤니티
rule-transliteration                 규칙 음역(kana-rule) — 기계번역 아님
machine-translation                  구글 기계번역
llm-only                             LLM 추측
machine-translation-ungated          기계번역인데 품질 게이트가 흠을 잡음 ← 가장 약함
```

**빈칸을 주지 않는 것이 방침이다**(2026-09-14). 값이 있으면 등급과 함께 준다.
등급을 보고 **쓸지 말지는 받는 쪽이 정한다.** 더 좋은 출처가 나중에 들어오면
우리가 자동으로 올려 덮는다.

### `locale_absent` · `fill_hint`

| `locale_absent` | 뜻 | `fill_hint` |
|---|---|---|
| `no_value` | 그 언어 표기가 우리에게 없다 | 유형에 따라 |
| `fallback_en` | 요청 언어가 없어 `locale_name` 이 **영어**다 | 유형에 따라 |
| `llm_only` | LLM 추측이라 서빙에서 뺐다 | — |
| `unverified_source` | `verified_only` 인데 출처가 검증 등급이 아니다 | — |

| `fill_hint` | 해야 할 일 |
|---|---|
| `transliterate` | **소리를 옮겨라.** 인물·그룹·캐릭터. 뜻을 옮기면 안 된다 |
| `translate_title` | 공식 현지 제목이 있으면 그것을, 없으면 뜻을 옮겨라 |

**`transliterate` 를 어기면 이렇게 된다** — 우리 원장에서 실제로 나온 값들이다:

```
이후    → "After"          (사람 이름을 뜻으로 옮겼다)
좋은 날 → "One Sunny Day"
김도하  → "Gimdoha"        (성씨 김은 Kim 이다)
미주    → "美洲"           (아메리카 대륙)
```

active 인물 **1,101명**의 영문 이름이 이렇게 만들어졌다. 그래서 우리가 유형을 말해 준다.

### `include_absent`

기본 `false` 다. `false` 면 **그 언어 표기가 없는 대상은 응답에서 빠진다** —
그 고유명사가 KDB 에 있는지조차 알 수 없고, 그러니 제보도 못 한다.
`true` 로 주면 `locale_name=""` 과 `locale_absent`·`fill_hint` 를 함께 받는다.

### `disambiguate`

`true` 면 본문 맥락으로 gemma 가 후보를 검증해 실제로 그 K-엔티티로 언급된 것만 남긴다.
일반어 오매칭(`온다`·`좋은 날`·`이후`)이 여기서 걸러진다. **느려진다**(핫패스는 0.6ms).
기사 단위로 한 번 부르는 흐름이면 켜는 게 낫다.

---

## 2. 기사 전체에 같은 표기를 쓰기 — `POST /v1/preparations`

**"같은 작품이 기사마다 다르게 번역된다" 는 이걸로 푼다.**

```json
{ "article_id": "...", "article_version": 3,
  "source_url": "...",
  "locales": ["ja","zh-Hans"],
  "items": [ {"term":"사랑이 온다","entity_type":"drama","context_hint":"..."} ],
  "idempotency_key": "..." }
```

- `article_id` + `article_version` 에 **고정**된다. 같은 기사·같은 판이면 같은 답이다
- `idempotency_key` — 같은 요청은 같은 준비를 돌려준다
- 항목마다 대상이 묶이고(`bound_entity_id`), 언어별 준비 상태가 따로 기록된다
- **빠진 언어는 비동기로 채운다.** 조회 경로를 느리게 하지 않는다

`GET /v1/preparations/{id}` 로 결과를 받는다. 이것이 Glossary Snapshot 이다 —
따로 만들 필요가 없다.

**즉석 생성은 하지 않는다.** 요청 때마다 번역하면 같은 이름이 기사마다 달라지고,
0.6ms 핫패스가 수백 ms 가 되고, 같은 번역에 계속 과금된다.
채우는 것은 준비 경로에서 비동기로 한다.

---

## 3. 표기를 정했으면 돌려 달라 — `POST /v1/corrections`

빈칸이라 그쪽에서 만든 표기, 또는 우리 값이 틀렸을 때.

```json
{ "entity_id": "...", "locale": "ja", "spelling": "...",
  "source_url": "...", "reason": "..." }
```

검증을 통과하면 우리 원장에 올라가고, **다음 요청부터 우리가 답한다.**
`GET /v1/corrections/{id}` 로 처리 결과를 본다.

---

## 4. 우리가 아직 못 주는 것 (솔직히)

- **한국 인물의 중국어 표기** — ja 는 `kana-rule` 규칙 음역이 있는데 zh 는 없다.
  한자 이름은 음역이 아니라 본인의 한자라 규칙으로 만들 수 없다. 빈칸이면 `transliterate`
  지시가 나간다
- **영문 서비스가 안 덮는 관광지** — 한국관광공사 영문 서비스는 25,409곳,
  국문은 68,993곳이다. 나머지는 공공데이터에도 영어 이름이 없다
- **번체 칸에 간체가 든 행이 일부 있다.** 자동 교정은 위험해서 안 돌린다
  (`朴`→`樸` 처럼 한국 인명을 망가뜨린다). 손으로 골라 고치는 중이다
