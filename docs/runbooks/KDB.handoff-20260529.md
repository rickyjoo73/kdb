# KDB 자율운영 핸드오프 #4 (2026-05-29)

`KDB.handoff-20260528.md` 이후 — 운영자 콘솔 보강을 멈추고 **gpt-5.5 기반 자율운영 layer** 전환. 운영자는 결정 못 한 것만 검토, 나머지는 autopilot 가 30 분 cycle 로 처리.

## 0. 30 초 상태 확인

`.52`(`114.203.210.52`, port 38371, user `kdb`)에 ssh:

```bash
exec bash -l
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep -E 'kdb|codex-bridge|NAMES'
# 기대: kdb-db / kdb-api / kdb-worker / codex-bridge / kdb-admin 모두 healthy

# 자율운영 cycle 로그
docker logs --since 1h kdb-worker 2>&1 | grep 'kdb.autopilot' | tail -3
# 기대: jamo=N/N persons=+N type→person=N term-reject=N classified=N/N
#       promoted=N enriched=N quality=N alias=N 형식

# bridge endpoint 3개
for ep in /kdb_extract /kdb_classify /kdb_fill_locale; do
  curl -sS -o /dev/null -w "%{http_code} $ep\n" -X POST http://127.0.0.1:9002$ep -d '{}'
done
# 기대: 400 (bad payload, endpoint 존재) — /kdb_extract 만 200 spellings:[]
```

전부 통과면 §3 (남은 작업) 으로.

## 1. 신규 아키텍처 (handoff #3 대비 변경)

### 1.1 codex-bridge — 새 endpoint 2 개

- `POST /kdb_classify` — entity_type 분류 + primary_role 추정. structured output schema `kdb_classify.schema.json`.
- `POST /kdb_fill_locale` — 빈 locale 합성 (LLM fallback, source=`codex-fallback`).
- 기존 `/kdb_extract` 그대로.
- 모두 `gpt-5.5` (force_model=false 면 client 요청값 우선).
- prompt 안에 `INPUT INTEGRITY CHECK` (자소 분리 거부) + `NON-ENTITY FILTER` (일반어 거부) 규칙.

### 1.2 kdb-worker — autopilot 8 step

`internal/kdb/autopilot/sweep.go` — 기본 30 분 cycle (env `KDB_AUTOPILOT_INTERVAL_SECONDS`).

```
Run():
 0.  stepRepairBrokenJamo      자소 깨진 ko → typo merge / reject
 0.5 stepSyncPersons           entities↔persons 정합성
 0.7 stepReviewCandidates      gpt 일반어 판정 → reject
 1.  stepClassifyUnknown       active+type=unknown gpt 분류
 2.  stepPromoteConsensus      ≥2 매체 candidate 자동 promote + enrich
 3.  stepEnrichEmpty           빈 locale → L2→L3→L4 cascade
 4.  stepQualityReview         conf<0.7 → Wikidata 채택 + conf 0.75
 5.  stepResolveAliasConflicts alias 다중 매핑 자동 재할당
```

각 step batch 환경변수 (`KDB_AUTOPILOT_BATCH_*`) + conf 임계 차등 (단일 0.85 / ≥2 0.75 / ≥3 0.70).

### 1.3 enrich cascade (L1→L4)

`internal/kdb/enrich/orchestrator.go`:
- **L1 로컬** — RSS observations 합의 (기존 SweepEvaluation).
- **L2 권위 API** — MusicBrainz (구현 ✓, 무인증). TMDb/KOFIC/KMDb 는 key 발급 대기.
- **L3 Wikidata** — `wbsearchentities` + `wbgetentities` (K-Wave description filter).
- **L4 codex-fallback** — bridge `/kdb_fill_locale`.

ShouldReplace 룰: L > l > O > W > w > codex.

### 1.4 kdb-api — lazy enrich

`internal/kdbapi/api.go` 의 `/v1/lookup` 핸들러가 응답 직전 `enrich.BackgroundTrigger.Trigger(id)` 호출. last_enriched_at < now() - 1h 인 row 만 background goroutine 트리거. 응답 시간 영향 0 (실측 33 ms).

### 1.5 DB schema 변경

- `migrations/0058_pg_trgm.sql` — pg_trgm + canonical_ko trigram GIN index (typo 매칭).
- `migrations/0059_last_enriched_at.sql` — `kwave_entities.last_enriched_at` (lazy enrich 중복 방지).

### 1.6 Source mark 시스템

`internal/kdb/source_priority.go` 확장:
- 신규 Source 값: `rss-observation` / `tmdb` / `kofic` / `kmdb` / `musicbrainz` / `naver-people` / `codex-fallback`
- `Mark(s)` → 🔒 / L / l / O / W / w / ? 한 글자
- `MarkClass(s)` → tailwind 색 class
- 모든 admin 페이지에서 각 locale 값 옆 뱃지 표시.

### 1.7 admin UI 재편 (handoff #3 대비)

사이드바 3 섹션:
- **INBOX** — 신규 후보 (WF-1) / 미분류 (WF-1b) / 충돌 (WF-2) / 누락 locale (WF-3) / 품질 검토 (WF-4)
- **DATA** — 고유명사 DB / 인물 DB / 매체 관리 (WF-5)
- **SYSTEM** — 매체 표기 관측 / Codex Audit

각 Inbox 메뉴에 미처리 카운트 뱃지 (≥10 빨강, 그 외 주황). dashboard 상단에도 동일 카운트 카드 5개.

신규 페이지:
- `/admin/kdb/inbox` — 신규 후보 1-click promote (entity_type select 필수, person 시 persons 자동 sync) / reject / defer
- `/admin/entities/unclassified` — 미분류 1-click classify (person 시 persons sync)
- `/admin/entities/locale-gaps` — 빈 locale 진도바 + 행별 enrich 액션
- `/admin/entities/review` — confidence < threshold + Wikidata 채택 / lock / reject 액션
- `/admin/entities/whitelist` — RSS 인라인 편집 + 추가/toggle/del

신규 패키지: `internal/kdb/wikidata`, `aijudge`, `musicbrainz`, `enrich`, `aliasmatch`, `hangul`, `autopilot`.

## 2. 자율운영 실증 데이터 (2026-05-28 20:34 ~ 05-29 02:00)

11 cycle 정상 (1 cycle ~4-8 분). bridge `/kdb_classify` 정확도 sample:
- 박하선 person 0.96 / 유키스 group 0.97 / 맵 오브 더 소울:7 song_album 0.97 / 박훈정 person 0.95
- 벤 person 0.74 / 우가팸 group 0.72 / 마이 유니버스 song_album 0.72 (단일 매체 → 임계 미달 → 운영자 큐)

임계 차등 적용 후 2 cycle 누적 효과:
- lowconf 193 → 185 (-8, quality step 누적 11건)
- vi_empty 164 → 160 (-4, enrich step)
- alias_conflicts 29 → 22 (-7, alias step)
- unclassified 4 → 2 (-2, classify 자동 적용)
- term-reject 누적 4건 (마녀/신사/우가팸 등 일반어/모호 후보)

**불량 자동 정리 실증**:
- `임ㅇ원희` (자소 깨짐, active conf 0.86) → autopilot stepRepairBrokenJamo 가 `임원희` (정상) 의 alias 로 흡수 + rejected. `/v1/lookup` "임원희" 응답에 `aliases.ko=["임ㅇ원희",...]` 포함됨.

**정합성 sync 실증**:
- entities↔persons mismatch 0 (이전 7+7건).

## 3. 남은 작업 (다음 세션 순서)

### 3.1 TMDb / KOFIC / KMDb client (영상물 다국어)
- 현재 L2 = MusicBrainz 만. drama/movie/show 다국어 채움률 가장 낮음 (id 63% / es 64% / pt-br 59% / zh-hant 57%).
- TMDb (18+ locale, vi/es/id/pt-br 모두 됨) 가 가장 임팩트 큼. 무료 발급. 5분 작업.
- 발급 후 `internal/kdb/tmdb/client.go` 신규 + orchestrator L2 에 추가. env `TMDB_API_KEY` 없으면 silent skip 패턴.
- mediafine `.59` 에 이미 발급된 key 가져오기 vs KDB 별도 발급 — 사용자 결정.

### 3.2 동명이인 분리 (윤성호 등)
- 같은 ko 가 두 인물 가리킴. 현재 1 entity / 1 persons row 로 통합됨.
- canonical_ko UNIQUE 제약 — suffix `(가수)` `(배우)` 패턴 or disambiguator 컬럼 신설.
- Phase 1: admin/conflicts 페이지에 detection 표시만 (운영자 1-click 분리).
- Phase 2: gpt 가 매체 RSS 제목/맥락 보고 자동 분리 제안.

### 3.3 운영자 UI 검증 (playwright)
- 인증 후 페이지 (특히 inbox/locale-gaps/review) 실제 렌더링 자동 검증 필요.
- 사용자 한 번 admin 로그인 후 cookie 발급 도와줘야 playwright 가 통과 가능.
- 또는 `KDB_ADMIN_SESSION_SECRET` 영구 세팅 (`.env`) 후 cookie 직접 생성.

### 3.4 GitHub push 권한
- handoff #3 §3.2 그대로. 이번 세션 변경분(autopilot / enrich / wikidata / musicbrainz / hangul / aliasmatch + 마이그레이션 2개 + admin 페이지 신규) 미푸시.
- deploy key write enable 필요.

### 3.5 자율운영 모니터링 강화 (Phase 다음)
- autopilot cycle 결과를 `kwave_autopilot_log` 같은 audit 테이블에 기록 → admin dashboard 에 추세 그래프.
- gpt 호출 비용 일일 합계 (Codex usage) 표시.

## 4. 환경변수 (신규)

```env
# kdb-worker autopilot
KDB_AUTOPILOT_INTERVAL_SECONDS=1800       # 30분
KDB_AUTOPILOT_BATCH_CLASSIFY=20
KDB_AUTOPILOT_BATCH_ENRICH=10
KDB_AUTOPILOT_BATCH_PROMOTE=20
KDB_AUTOPILOT_MIN_CONF_SINGLE=0.85        # 단일매체
KDB_AUTOPILOT_MIN_CONF_TWO=0.75           # ≥2 매체
KDB_AUTOPILOT_MIN_CONF_THREE=0.70         # ≥3 매체

# codex-bridge 추가 schema (default OK, override 시만)
# CODEX_BRIDGE_CLASSIFY_SCHEMA=/app/schemas/kdb_classify.schema.json
# CODEX_BRIDGE_FILL_LOCALE_SCHEMA=/app/schemas/kdb_fill_locale.schema.json

# kdb-admin (handoff #3 그대로)
# KDB_ADMIN_SESSION_SECRET=...
```

## 5. 보안 미해결 (회전 필수)

handoff #3 §4 그대로. 추가로 이번 세션에서 secret 노출 없음 (코드 작성만).

## 6. 외부 참조

- handoff #3: `docs/runbooks/KDB.handoff-20260528.md`
- handoff #2: `docs/runbooks/KDB.handoff-20260527-2.md`
- 마스터 플랜: `docs/KDB_PLATFORM_SEPARATION_PLAN.md`
- entity 모델: `docs/ENTITY_DB_PLATFORM.md`
- 운영 콘솔 설계 v2: `.claude/plans/sleepy-prancing-swing.md`

## 7. 한 줄 요약

> `.52` KDB 가 **autopilot 8 step × 30 분 cycle** 로 자율 운영 중. gpt-5.5 가 분류 / 일반어 거부 / 다국어 합성 / 정합성 sync 모두 자동. 운영자 결정은 confidence 임계 미달건만. 다음 — TMDb key (영상물 다국어), 동명이인 분리, playwright UI 검증, push 권한.
