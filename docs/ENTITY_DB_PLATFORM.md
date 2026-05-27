# K-Content Entity DB Platform — 정공법 명세서

운영자 directive 2026-05-25 (3차례 정리 후 확정):

> *"디비수집관련해서는 별도로 시스템을 구축 하는것을 기획을 잡자
> 미디어 파인에게는 api 로 제시하는것이다. ... 우리가 언어별로 각각
> 그 해당언어에서는 공식력있는 사이트를 수집했다. 그럼 내가 원하는것은
> rss .xml 을 확보하라. 우리는 이것만 30분 단위로 ... 여기서 이들이
> 어떻게 사용하는지 아이디어를 얻고 우리 디비에 저장하고 버리는것이다.
> ... 우리가 있는데 현지에서는 다수가 우리가 정리한것과 다르네 그럼
> 현지기준으로 다시 우리 디비를 변경해야지 다만 마크는 api 인지
> 현지매체기준인지 해서 현지기준이 우선순위가 맞도록해야지."*

본 문서는 **운영자 결정 확정본**. 본 문서에 박제된 룰은 코드 리뷰의
승인 기준이 된다.

---

## 1. 비전 + 도메인 + 분리 시점

| 항목 | 결정 |
|---|---|
| 미래 도메인 | **`kdb.aiinplanet.com`** (확정) |
| 분리 시점 | **mediafine 프로젝트 성공 후** 별도 도커 컨테이너로 분리 |
| 현재 구현 위치 | **`internal/kdb/`** (mediafine repo 내부 모듈) |
| 현재 호출 방식 | 같은 cmd/app 프로세스 내 함수 호출 |
| 미래 호출 방식 | HTTP API (`/api/v1/entity/{id}` 등) |
| 미래 외부 consumer | mediafine 외 다른 K-content 서비스 |

**왜 지금은 분리 X**: mediafine 자체가 PMF 검증 중. 인프라 분리 = 통신
복잡도 + 운영 부담 증가. 한 프로세스 내에서 모듈 경계만 명확히 잡고,
성공 검증 후 같은 코드를 docker 단 분리. 코드 수정 거의 0.

---

## 2. 시스템 데이터 흐름 (전체)

```
┌──────────────────── 백그라운드 자율 누적 (internal/kdb) ──────────────────────┐
│                                                                              │
│  30분 cron (KdbPollerTick) → 매체별 RSS .xml fetch                            │
│      ↑ kwave_news_whitelist.rss_url                                          │
│                          ↓                                                   │
│  RSS <item> 마다:                                                            │
│    title + description 결합                                                  │
│                          ↓                                                   │
│  cheap-gate (외부 호출 0):                                                   │
│    kwave_entities.canonical_ko + aliases_ko substring 매칭 시도              │
│    ┌────────────────────┬──────────────────────────────┐                     │
│    │ 1개 이상 hit       │ 0개 hit                      │                     │
│    │ → Gemma 추출 큐    │ → 신규 entity 후보 큐        │                     │
│    └────────────────────┴──────────────────────────────┘                     │
│                          ↓                                                   │
│  Gemma LLM: title+description 에서 K-content 고유명사 + 한국어 매핑 추출      │
│    응답 예: [{ko: "지민", id: "Jimin"}, {ko: "정국", id: "Jungkook"}]          │
│                          ↓                                                   │
│  kwave_media_observations INSERT (entity, locale, spelling, source_url)      │
│                          ↓                                                   │
│  같은 (entity, locale, spelling) 가 N 매체 (≥2) 합의:                         │
│    canonical_X UPDATE (source='media-consensus', priority 가드 적용)         │
│    기존 source 가 'wikidata-label' / 'wikipedia-langlinks' / 'sitelink' 이면 덮음 │
│    기존 source 가 'operator-locked' / 'operator' 이면 보존 + audit            │
│                          ↓                                                   │
│  RSS .xml 자체 = discard. DB 에 남는 건 observations + canonical 갱신.        │
│                                                                              │
└──────────────────────────────────────────────────────────────────────────────┘

┌────────── 신규 entity 자연 등록 ────────────┐
│                                            │
│  cheap-gate 0 hit + 같은 spelling 이 ≥2     │
│  K-content 카테고리 매체에서 등장            │
│            ↓                               │
│  kwave_entities INSERT                     │
│    (canonical_ko=ko_hint, confidence=0.5,  │
│     entity_type='unknown')                 │
│            ↓                               │
│  다음 cycle 에서 정상 cheap-gate hit        │
│  → 정상 cascade (Wikidata + Stage A/B/C)   │
│                                            │
└────────────────────────────────────────────┘

┌────────── On-demand fallback (mediafine + 미래 consumer 요청 시) ──────────┐
│                                                                            │
│  publish worker / translation worker / 미래 API call                       │
│      → entity 조회 with locales=[...]                                      │
│                          ↓                                                 │
│  현재 DB SELECT                                                            │
│                          ↓                                                 │
│  요청 locale 중 빈 게 있음?  ── 아니오 ──→ 즉시 응답                         │
│                          ↓ 예                                              │
│  기존 시스템 즉시 가동 (Wikipedia cascade Stage A+B+C):                     │
│    sitelink → variant=zh-tw → langlinks                                    │
│    canonical_X UPDATE (source='wikipedia-*')                               │
│                          ↓                                                 │
│  응답 (빈 게 남아도 그 시점의 최선)                                          │
│                                                                            │
│  주의: 이때 source = 'wikidata-label' / 'wikipedia-langlinks' (priority 3+)│
│        나중에 RSS 가 media-consensus (priority 2) 를 가져오면 자동 덮음.    │
│                                                                            │
└────────────────────────────────────────────────────────────────────────────┘
```

---

## 3. Provenance 모델 (핵심)

운영자 정공법:
> *"마크는 api 인지 현지매체기준인지 해서 현지기준이 우선순위가
> 맞도록해야지"*

### 3.1 source 컬럼 (각 canonical_X 마다 짝)

각 `canonical_X` 컬럼마다 `canonical_X_source` 컬럼을 짝으로 둠.
8 locale × 1 source 컬럼 = 8 새 컬럼.

### 3.2 source enum + priority (낮을수록 우선)

| Priority | source 값 | 의미 | 덮어쓰기 정책 |
|:-:|---|---|---|
| **1** | `operator-locked` | `kwave_entities.operator_locked = true` 또는 운영자 수동 입력 | **불변** (어떤 source 도 덮을 수 없음) |
| **2** | `media-consensus` | 현지 매체 ≥2 합의 (RSS 수집) | priority 3~5 모두 덮음 |
| 3 | `wikidata-label` | Wikidata wbgetentities labels 직접 제공 | priority 4~5 덮음, 2 도착 시 덮임 |
| 4 | `wikipedia-langlinks` | Wikipedia 다국어 article 제목 | priority 5 덮음, 2~3 도착 시 덮임 |
| 5 | `wikipedia-sitelink` | Wikipedia sitelink fallback (Stage A) | 5 만 덮음, 2~4 도착 시 덮임 |
| (5) | `wikipedia-zh-variant` | zh.wikipedia.org ?variant=zh-tw (Stage B) | zh_hant 만, 같은 우선순위 |

### 3.3 덮어쓰기 룰 (`shouldReplace(current, new) bool`)

```
if current == 'operator-locked':       return false
if new.priority < current.priority:    return true   (높은 우선순위 = 낮은 숫자)
if new.priority == current.priority and same spelling: return false (no-op)
if new.priority == current.priority and different spelling:
  → drift detected → audit + 운영자 review 큐
  return false (자동 덮어쓰기 X)
if new.priority > current.priority:    return false  (낮은 우선순위 무시)
```

### 3.4 구체 예시

| 시나리오 | 기존 (source, val) | 신규 (source, val) | 동작 |
|---|---|---|---|
| API 발견 → 매체 합의 도착 | `wikidata-label, "Park Shin-hye"` | `media-consensus, "Park Sin Hye"` | **덮음** + audit `replaced wikidata-label→media-consensus` |
| 운영자 잠금 → 매체 합의 도착 | `operator-locked, "Park Shin Hye"` | `media-consensus, "Park Sin Hye"` | **보존** + audit only (다음 운영자 review 시 참고) |
| Wikidata → Wikipedia langlinks 늦게 도착 | `wikidata-label, "Park Shin-hye"` | `wikipedia-langlinks, "Park Shin-hye"` | no-op (priority 3 < 4) |
| 매체 1개 only | (없음) | `media-consensus, "Park Sin Hye"` 합의 미달 | observations INSERT 만, canonical UPDATE 안 함 |
| 매체 합의 → 다른 매체 합의 (drift) | `media-consensus, "Park Sin Hye"` | `media-consensus, "Bak Sinhye"` | audit + 운영자 review 큐 (자동 덮음 X) |

---

## 4. 데이터 모델

### 4.1 기존 `kwave_entities` 확장

```sql
ALTER TABLE kwave_entities
  ADD COLUMN canonical_en_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_ja_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_vi_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_zh_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_zh_hant_source text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_es_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_id_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_pt_br_source   text DEFAULT 'wikidata-label';

-- 운영자 잠금된 row 는 source='operator-locked' 일괄 표시
UPDATE kwave_entities
   SET canonical_en_source='operator-locked',
       canonical_ja_source='operator-locked',
       ...
 WHERE operator_locked = true;
```

### 4.2 `kwave_news_whitelist` 확장

```sql
ALTER TABLE kwave_news_whitelist
  ADD COLUMN rss_url      text,
  ADD COLUMN last_polled  timestamptz,
  ADD COLUMN poll_status  text;  -- 'ok' | '404' | '5xx' | 'parse-fail'
```

### 4.3 신규 테이블 `kwave_media_observations`

```sql
CREATE TABLE kwave_media_observations (
  id            bigserial PRIMARY KEY,
  entity_id     uuid REFERENCES kwave_entities(id) ON DELETE CASCADE,
  locale        text NOT NULL,
  spelling      text NOT NULL,
  source_domain text NOT NULL,                 -- 'soompi.com/es' 등
  source_url    text,                          -- RSS item link (참고용)
  observed_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kwave_media_observations_consensus_idx
  ON kwave_media_observations (entity_id, locale, spelling, source_domain);

CREATE INDEX kwave_media_observations_recent_idx
  ON kwave_media_observations (observed_at DESC);

COMMENT ON TABLE kwave_media_observations IS
  '현지 매체 RSS 에서 추출된 entity 표기 원자료. 같은 (entity, locale, spelling) 가 N 도메인 합의 시 canonical_X UPDATE.';
```

### 4.4 신규 테이블 `kwave_entity_candidates`

```sql
CREATE TABLE kwave_entity_candidates (
  ko_hint        text NOT NULL,
  first_seen_at  timestamptz NOT NULL DEFAULT now(),
  last_seen_at   timestamptz NOT NULL DEFAULT now(),
  observed_count int NOT NULL DEFAULT 1,
  source_domains text[] NOT NULL DEFAULT '{}', -- 매체 도메인 누적
  status         text NOT NULL DEFAULT 'pending', -- pending | promoted | rejected
  PRIMARY KEY (ko_hint)
);

COMMENT ON TABLE kwave_entity_candidates IS
  '아직 kwave_entities 에 없는 entity 후보. ≥2 매체에서 등장 시 자동 promote.';
```

---

## 5. RSS poller 정책

### 5.1 폴링 주기

- **30분** (운영자 명시).
- supervisor 의 slow tick 와 별개 — 자체 ticker (`KdbPollerTick` 30분).
- 매체별 직렬 poll (HTTP 1 connection 동시 1개), 매체간 100ms 간격.

### 5.2 cheap-gate (외부 호출 0)

RSS item 의 `title + description` 텍스트를 받아:
1. `kwave_entities.canonical_ko` + `aliases_ko` 의 모든 spelling 을 in-memory map 으로 로드 (1회/poll cycle)
2. RSS text 에 substring 매칭 시도
3. **1개 이상 hit** → Gemma 추출 큐 enqueue
4. **0개 hit** → 신규 entity 후보 큐 (조건: 매체 category='K-content')

### 5.3 RSS .xml 처리

- fetch + parse → `<item>` 추출 → discard (DB 저장 X)
- `<title>`, `<description>`, `<link>` 만 메모리 처리
- 동일 `<link>` 중복 처리 차단 (in-memory LRU, TTL 1일)

---

## 6. LLM 사용 정책 (CLAUDE.md §1.3.6 정합)

### 6.1 Codex 5.5 medium 전용 (운영자 directive 2026-05-25)

운영자 directive: *"gemma 사용안하고 codex 5.5 medium 을 사용할거야"*.

CLAUDE.md §1.3.6 박제: KDB platform 의 RSS 추출 LLM 은 **Codex 5.5
medium 전용**. Gemma 도 Opus/Sonnet 도 아님. Opus bridge 와 동일 패턴
(`scripts/codex_extract_bridge.py` → host CLI subprocess).

### 6.2 Cheap-gate 가 95% 호출 줄임

매체 ~30 × 30분 cycle = 매일 1440 poll. 각 poll 평균 20 item = 매일
~30,000 RSS items. **cheap-gate 미통과 (substring match 0) item 은
LLM 호출 안 함**. 통과율 예상 5~10% = 매일 ~3,000 Codex 호출.

### 6.3 프롬프트 정공법

Codex 입력:
- 매체 locale 정보 (e.g. "id")
- RSS title + description (잘라서 ~1000 토큰 cap)
- cheap-gate 가 hit 한 K-한국어 entity hint list (e.g. ["지민", "정국"])

Codex 출력 (JSON):
```json
[
  {"ko_hint": "지민", "locale": "id", "spelling": "Jimin", "confidence": 0.95},
  {"ko_hint": "정국", "locale": "id", "spelling": "Jungkook", "confidence": 0.92}
]
```

confidence < 0.7 = observation 무시. < 0.85 = audit 만, canonical 무영향.

**모델 설정**:
- `CODEX_MODEL=gpt-5.5-codex` (env override)
- `CODEX_REASONING_EFFORT=medium` (env override)

### 6.4 본문 fetch fallback (운영자 *"내용파악이 안되면 해당기사를 읽어서 분석"*)

- Gemma 가 title+description 만으로 추출 0 건 반환 + cheap-gate 는
  hit 있는 경우 = 매체가 다른 entity 다룸 가능성 → `<link>` 본문 fetch
- 본문 첫 ~5000 char 만 + 재추출
- 본문 fetch 실패 (5xx / robots.txt) = silent skip + audit
- 본문 fetch 도 cheap-gate 통과 item 한정 (외부 호출 cost 관리)

---

## 7. 운영자 결정 사항 (확정)

| 결정 | 확정값 | 근거 |
|---|---|---|
| **매체 합의 임계** | **2 매체** 자동 promote, **1 매체** 는 observations 만 | 운영자 *"다수"* 표현 = 2+ |
| **신규 entity 자동 등록** | **2 매체 등장 시 자동 INSERT** (status=auto-promoted), **1 매체** 는 candidates 큐 (운영자 검토) | 동상 |
| **canonical_X_source 마이그레이션 default** | **`wikidata-label`** 일괄 (operator_locked = `operator-locked`) | 보수적, RSS 가 자연 덮을 시간 줌 |
| **source priority enum** | §3.2 표 그대로 | 운영자 mark 룰 정공법 |
| **자동 덮어쓰기** | priority 룰만, operator-locked 항상 보존 | 운영자 권위 보호 |
| **분리 시점** | mediafine 성공 후 도커 분리 | 운영자 명시 |
| **현재 위치** | `internal/kdb/` | 같은 cmd/app 모듈 경계 |

---

## 8. RSS 매체 후보 (초기 시드 — locale 당 3-5 매체)

운영자 결정 미정 → **default = 3 매체부터 시작, 운영 후 확장**. 첫 매체 박제는
별도 PR 에서 운영자 검토.

| locale | 초기 후보 (예시, 확정 X) | RSS URL 확인 필요 |
|---|---|---|
| en | Soompi.com, AllKpop, ScreenRant K (entertainment) | 운영자 확인 |
| ja | Soompi.jp, KSTYLE, Modelpress K-pop 섹션 | 운영자 확인 |
| vi | Kenh14 K-pop, Saostar Korea, VietJack 한류 | 운영자 확인 |
| es | Soompi en español, La Vanguardia K-pop | 운영자 확인 |
| id | Soompi.com/id, Liputan6 hiburan K, IDNTimes K | 운영자 확인 |
| pt-br | Soompi.com/pt, G1 entretenimento (한국 태그) | 운영자 확인 |
| zh-hant | KSD韓星網, 即時新聞 한국 섹션, 三立娛樂 | 운영자 확인 |

매체별 RSS URL 은 운영자가 `/admin/entities/whitelist` (확장 후) 에서 추가.

---

## 9. 자기 감독 (CLAUDE.md §8.4 정합)

신규 hermes trigger (별도 PR 단계):

| Trigger | 임계 | 의미 |
|---|---|---|
| `KDB.rss-poll-down` | 매체 ≥3 가 1h 무동 | RSS poll 시스템 정지 |
| `KDB.gemma-extract-fail` | 추출 fail 율 > 30%/1h | Gemma 응답 stuck |
| `KDB.consensus-promote-stagnant` | 3일간 promote 0 | 매체 합의가 1 매체로 fragmented |
| `KDB.drift-review-backlog` | 운영자 review 대기 > 50 | 같은 source 표기 변동 누적 |

---

## 10. 비용

| 항목 | 비용 |
|---|---|
| RSS HTTP poll | $0 (매체 공개 채널) |
| cheap-gate (in-memory + DB) | $0 |
| Gemma 호출 (~3000/일) | 토큰 비용 (예산 내) |
| Wikipedia API (on-demand fallback) | $0 |
| Opus | **금지** (CLAUDE.md §1.2) |
| 본문 fetch fallback | $0 (HTTP) |

매월 추가 운영비 ~Gemma 토큰만.

---

## 11. 마이그레이션 경로 (구현 phase)

### Phase A (이번 PR, 즉시 진행) — 토대

1. `migrations/0049_kdb_provenance.sql` — kwave_entities source 컬럼 8 + rss_url 컬럼 + media_observations 테이블 + entity_candidates 테이블
2. `internal/kdb/source_priority.go` — enum + `shouldReplace()` 함수
3. **기존 cascade.go UPDATE 6 사이트** 에 priority 가드 통합
   - Stage A (`updateEntityWideTable`)
   - Stage B (`runStageB`, `RunSingle` 의 Stage B 분기)
   - Stage C (`applyLanglinks`)
   - admin `EntityConflictPick` (source='operator')
4. `internal/kdb/rss_poller.go` — 30분 ticker 골격
5. `internal/kdb/extractor.go` — Gemma 호출
6. `internal/kdb/observations.go` — INSERT + 합의 promote
7. `internal/kdb/candidates.go` — 신규 entity 자연 등록
8. supervisor 의 slow tick 에 `kdb.PollerTick` 연결 (단 매체 0개면 no-op)

### Phase B (운영자 매체 박제 후) — 운영 시작

1. 운영자가 `/admin/entities/whitelist` 에 매체별 RSS URL 등록 (수동)
2. RSS poll 작동 시작 → observations 누적
3. 첫 7일간 자동 promote 차단 (운영자 검토 모드 only) → false positive 학습
4. 검증 후 자동 promote ON

### Phase C (확장)

1. 본문 fetch fallback 활성화 (cheap-gate 통과 시 한정)
2. 신규 entity 자동 등록 ON
3. hermes 4 trigger 추가

### Phase D (성공 후 분리)

1. `internal/kdb/` 그대로 + 신규 HTTP API layer 추가
2. mediafine 의 entity 조회 = client HTTP call
3. dual-write 1주 (mediafine 자체 DB + kdb 동시 read/write)
4. drift 0 확인 → mediafine 의 entity 코드 제거
5. 별도 docker container + 도메인 `kdb.aiinplanet.com`

---

## 12. 운영자 인터페이스 변화

### 12.1 기존 admin 페이지

| 페이지 | 변경 |
|---|---|
| `/admin/entities` | source 컬럼 표시 추가 |
| `/admin/entities/{id}` | external_refs 외에 observations 최근 50개 표시 |
| `/admin/entities/review` | 신규 후보 섹션 + drift 섹션 + 기존 conflict 섹션 (3 zones) |
| `/admin/entities/whitelist` | RSS URL + last_polled + poll_status 컬럼 |
| `/admin/entities/sources` | 매체별 합의 통계 + observations 카운트 |

### 12.2 신규 admin 페이지

| 페이지 | 용도 |
|---|---|
| `/admin/kdb/candidates` | 신규 entity 후보 1-click promote / reject |
| `/admin/kdb/observations?entity={id}` | 한 entity 의 매체별 표기 원자료 (debug) |

---

## 13. follow-up (이 문서 다음 단계)

- Phase A 코드 PR 1~2회 (마이그레이션, source priority, kdb 모듈, supervisor 연결)
- 매체 RSS URL 초기 시드 — 운영자 검토 + admin 등록
- Phase B 7일 검증 + 매체 합의 작동 보고
- Phase C 본문 fetch 정공법 PR
- API 응답 schema (OpenAPI 3.0)
- Phase D 분리 시점 결정 (mediafine PMF 검증 후)
