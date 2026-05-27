-- 0054_kdb_codex_audit.sql — Codex 호출 audit + status 컬럼.
--
-- 운영자 directive 2026-05-25:
--   *"ai 가 제대로 파악해서 업데이트 해야 되는데 제대로 ai 가
--    작업하는지 파악해줘야돼"*
--
-- 운영자가 admin 에서 매 Codex 호출의 quality 직접 검증.

BEGIN;

CREATE TABLE kwave_kdb_codex_runs (
  id            bigserial PRIMARY KEY,
  cycle_id      bigint REFERENCES kwave_kdb_poll_cycles(id) ON DELETE SET NULL,
  source_domain text NOT NULL,
  locale        text NOT NULL,
  -- 입력
  rss_title     text NOT NULL,
  rss_desc      text,
  hint_count    int  NOT NULL DEFAULT 0,
  -- 출력
  status        text NOT NULL,                  -- 'ok' | 'http_error' | 'parse_error' | 'no_spelling'
  spelling_count int NOT NULL DEFAULT 0,
  duration_ms   int  NOT NULL DEFAULT 0,
  error_text    text,
  raw_spellings jsonb,                          -- Codex 응답 [{ko_hint, locale, spelling, confidence}]
  -- 시각
  ran_at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX kwave_kdb_codex_runs_recent_idx ON kwave_kdb_codex_runs (ran_at DESC);
CREATE INDEX kwave_kdb_codex_runs_domain_idx ON kwave_kdb_codex_runs (source_domain, ran_at DESC);

COMMENT ON TABLE kwave_kdb_codex_runs IS
  'KDB platform 의 매 Codex 호출 audit. 운영자가 /admin/kdb/codex-runs 에서 실시간 검증.';

-- observations 에 confidence + audit (Codex 가 추출한 confidence 보존)
ALTER TABLE kwave_media_observations
  ADD COLUMN confidence numeric(4,3),
  ADD COLUMN cycle_id   bigint REFERENCES kwave_kdb_poll_cycles(id) ON DELETE SET NULL;

COMMENT ON COLUMN kwave_media_observations.confidence IS
  'Codex 가 부여한 confidence (0.7+ 만 저장 — extractor.go filter).';

COMMIT;
