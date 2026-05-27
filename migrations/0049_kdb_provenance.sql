-- 0049_kdb_provenance.sql — KDB platform 토대 (docs/ENTITY_DB_PLATFORM.md Phase A).
--
-- 운영자 directive 2026-05-25 정공법:
--   - 현지 매체 합의 = 최우선 (operator-locked 다음).
--   - API 출처 (Wikidata/Wikipedia) 는 backbone, media-consensus 가 도착하면 덮임.
--   - canonical_X 마다 source 컬럼 + priority enum 으로 자동 덮어쓰기 결정.
--
-- 추가-only 마이그레이션 (CLAUDE.md §6 정공법).

BEGIN;

-- ─── 1. kwave_entities canonical_X_source 컬럼 8 추가 ──────────────────
ALTER TABLE kwave_entities
  ADD COLUMN canonical_en_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_ja_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_vi_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_zh_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_zh_hant_source text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_es_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_id_source      text DEFAULT 'wikidata-label',
  ADD COLUMN canonical_pt_br_source   text DEFAULT 'wikidata-label';

COMMENT ON COLUMN kwave_entities.canonical_en_source IS
  'source enum: operator-locked | media-consensus | wikidata-label | wikipedia-langlinks | wikipedia-sitelink | wikipedia-zh-variant';

-- operator_locked row 는 source 도 일괄 'operator-locked' 으로 표시.
-- 향후 덮어쓰기 가드가 source 만 보고 작동.
UPDATE kwave_entities SET
  canonical_en_source      = 'operator-locked',
  canonical_ja_source      = 'operator-locked',
  canonical_vi_source      = 'operator-locked',
  canonical_zh_source      = 'operator-locked',
  canonical_zh_hant_source = 'operator-locked',
  canonical_es_source      = 'operator-locked',
  canonical_id_source      = 'operator-locked',
  canonical_pt_br_source   = 'operator-locked'
WHERE operator_locked = true;

-- ─── 2. kwave_news_whitelist 에 RSS URL + poll 메타 ────────────────────
ALTER TABLE kwave_news_whitelist
  ADD COLUMN rss_url      text,
  ADD COLUMN last_polled  timestamptz,
  ADD COLUMN poll_status  text;  -- 'ok' | '404' | '5xx' | 'parse-fail' | 'unreachable'

COMMENT ON COLUMN kwave_news_whitelist.rss_url IS
  'RSS .xml URL. NULL 이면 poller skip (운영자 검증 전).';

-- ─── 3. kwave_media_observations — RSS 추출 원자료 ─────────────────────
CREATE TABLE kwave_media_observations (
  id            bigserial PRIMARY KEY,
  entity_id     uuid REFERENCES kwave_entities(id) ON DELETE CASCADE,
  locale        text NOT NULL,
  spelling      text NOT NULL,
  source_domain text NOT NULL,
  source_url    text,
  observed_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kwave_media_observations_consensus_idx
  ON kwave_media_observations (entity_id, locale, spelling, source_domain);

CREATE INDEX kwave_media_observations_recent_idx
  ON kwave_media_observations (observed_at DESC);

COMMENT ON TABLE kwave_media_observations IS
  'RSS 에서 추출된 entity 표기 원자료. 같은 (entity, locale, spelling) 가 N 도메인 합의 시 canonical_X UPDATE (source=media-consensus).';

-- ─── 4. kwave_entity_candidates — 신규 entity 자연 등록 큐 ─────────────
CREATE TABLE kwave_entity_candidates (
  ko_hint        text PRIMARY KEY,
  first_seen_at  timestamptz NOT NULL DEFAULT now(),
  last_seen_at   timestamptz NOT NULL DEFAULT now(),
  observed_count int NOT NULL DEFAULT 1,
  source_domains text[] NOT NULL DEFAULT '{}',
  status         text NOT NULL DEFAULT 'pending'
);

CREATE INDEX kwave_entity_candidates_status_idx
  ON kwave_entity_candidates (status, last_seen_at DESC);

COMMENT ON TABLE kwave_entity_candidates IS
  '아직 kwave_entities 에 없는 entity 후보. ≥2 매체 등장 시 자동 INSERT 로 promote, 1 매체 = pending (운영자 검토).';

-- ─── 5. 자기 감독용: poll cycle audit (간단) ───────────────────────────
CREATE TABLE kwave_kdb_poll_cycles (
  id            bigserial PRIMARY KEY,
  started_at    timestamptz NOT NULL DEFAULT now(),
  ended_at      timestamptz,
  feeds_polled  int NOT NULL DEFAULT 0,
  items_total   int NOT NULL DEFAULT 0,
  cheap_pass    int NOT NULL DEFAULT 0,
  gemma_calls   int NOT NULL DEFAULT 0,
  observations  int NOT NULL DEFAULT 0,
  candidates    int NOT NULL DEFAULT 0,
  errors        int NOT NULL DEFAULT 0
);

COMMENT ON TABLE kwave_kdb_poll_cycles IS
  'KDB RSS poll cycle audit. 30분 마다 1 row. supervisor 자기 감독 데이터.';

COMMIT;
