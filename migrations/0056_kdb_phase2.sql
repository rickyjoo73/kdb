-- 0056_kdb_phase2.sql — Phase 2 raw buffer + 합의 강화 (Agent SRE + NLP).
--
-- 핵심 변경:
--   1. kwave_rss_items_raw — RSS items buffer (bridge down 시 1265 silent loss → 0)
--   2. kwave_news_whitelist.parent_org — wire-service cascade 차단
--   3. kwave_media_observations.spelling_normalized — 합의 정확도 (NFC + hyphenless)
--   4. kwave_media_observations 48h cooldown index

BEGIN;

-- ─── 1. RSS raw buffer (Agent SRE #1, 1265 loss 0 화) ────────────────
CREATE TABLE kwave_rss_items_raw (
  id            bigserial PRIMARY KEY,
  source_domain text NOT NULL,
  locale        text NOT NULL,
  link          text NOT NULL,
  title         text,
  description   text,
  pub_date      timestamptz,
  fetched_at    timestamptz NOT NULL DEFAULT now(),
  cheap_status  text,                    -- NULL=pending, 'hit', 'miss'
  cheap_hints   jsonb,                   -- entity_id list (hit 시)
  codex_status  text,                    -- NULL=skip(miss) or pending, 'ok', 'failed', 'retrying'
  retry_count   int  NOT NULL DEFAULT 0,
  last_attempt_at timestamptz,
  UNIQUE(source_domain, link)
);

CREATE INDEX kwave_rss_items_raw_pending_idx
  ON kwave_rss_items_raw (fetched_at)
  WHERE cheap_status='hit' AND codex_status IS DISTINCT FROM 'ok';

CREATE INDEX kwave_rss_items_raw_recent_idx
  ON kwave_rss_items_raw (fetched_at DESC);

COMMENT ON TABLE kwave_rss_items_raw IS
  'RSS items buffer — PollOnce 가 INSERT 만, ExtractSweeper 가 codex 호출. bridge down 시 raw 유지, 복구 후 sweep. retention 7d.';

-- ─── 2. parent_org + media_trust (Agent NLP #2, wire cascade 차단) ──
ALTER TABLE kwave_news_whitelist
  ADD COLUMN parent_org    text,                                    -- e.g. 'soompi' (es/id/vi/pt-br/ja 다 같은 family)
  ADD COLUMN media_trust   numeric(4,3) NOT NULL DEFAULT 1.000;

COMMENT ON COLUMN kwave_news_whitelist.parent_org IS
  'wire-service grouping — 합의 카운트는 COUNT(DISTINCT parent_org) 기준. NULL = 독립 매체.';
COMMENT ON COLUMN kwave_news_whitelist.media_trust IS
  '매체 신뢰도 0~1. 가중 합의 = SUM(trust × confidence) ≥ 1.5.';

-- 알려진 wire-family backfill
UPDATE kwave_news_whitelist SET parent_org='soompi'      WHERE domain='soompi.com';
UPDATE kwave_news_whitelist SET parent_org='google_news' WHERE domain='news.google.com';
UPDATE kwave_news_whitelist SET parent_org='kbs_world'   WHERE domain='world.kbs.co.kr';
UPDATE kwave_news_whitelist SET parent_org='yahoo'       WHERE domain IN ('hk.tv.yahoo.com','news.yahoo.com.tw','hk.news.yahoo.com','tw.news.yahoo.com');
UPDATE kwave_news_whitelist SET parent_org='detik'       WHERE domain IN ('detik.com','wolipop.detik.com','hot.detik.com');

-- ─── 3. spelling_normalized + sanity (Agent NLP #1+#3) ──────────────
ALTER TABLE kwave_media_observations
  ADD COLUMN spelling_normalized text;

COMMENT ON COLUMN kwave_media_observations.spelling_normalized IS
  'NFC + lower + hyphen→space + 중점(・)→공백 + trim. 합의는 normalized 기준 grouping, 표시는 spelling (raw) 다수결.';

-- 기존 row 정규화 (서버 측 simple normalize, application 측 정공법은 코드 정합)
UPDATE kwave_media_observations
   SET spelling_normalized = lower(trim(regexp_replace(replace(spelling, '・', ' '), '-+', ' ', 'g')))
 WHERE spelling_normalized IS NULL;

CREATE INDEX kwave_media_observations_consensus_v2_idx
  ON kwave_media_observations (entity_id, locale, spelling_normalized, source_domain);

-- ─── 4. consecutive_failures (Agent SRE auto-disable) ──────────────
ALTER TABLE kwave_news_whitelist
  ADD COLUMN consecutive_failures int NOT NULL DEFAULT 0;

COMMENT ON COLUMN kwave_news_whitelist.consecutive_failures IS
  '연속 fetch 실패 카운트. 7회 도달 시 enabled=false 자동 (auto-disable).';

COMMIT;
