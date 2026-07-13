-- 0087_kdb_whitelist_channels.sql
--
-- kwave_news_whitelist.enabled 가 RSS polling 과 온디맨드 site discovery 를
-- 동시에 제어하던 결합을 해소한다. 기존 행은 RSS 상태만 legacy enabled 값을
-- 승계하고, discovery 는 복구한다. 이후 새 행의 안전 기본값은 discovery ON,
-- RSS OFF 이다.

BEGIN;

ALTER TABLE kwave_news_whitelist
  ADD COLUMN IF NOT EXISTS rss_poll_enabled boolean;

ALTER TABLE kwave_news_whitelist
  ADD COLUMN IF NOT EXISTS discovery_enabled boolean;

-- 새 컬럼이 NULL 인 행만 backfill 하므로 파일을 재실행해도 운영자 토글을
-- 덮어쓰지 않는다. 현재 99개 legacy 행은 enabled 상태를 RSS에만 승계한다.
UPDATE kwave_news_whitelist
   SET rss_poll_enabled = enabled
 WHERE rss_poll_enabled IS NULL;

-- RSS 중단과 함께 잘못 꺼졌던 온디맨드 discovery 를 모든 기존 행에서 복구한다.
UPDATE kwave_news_whitelist
   SET discovery_enabled = true
 WHERE discovery_enabled IS NULL;

ALTER TABLE kwave_news_whitelist
  ALTER COLUMN rss_poll_enabled SET DEFAULT false,
  ALTER COLUMN rss_poll_enabled SET NOT NULL,
  ALTER COLUMN discovery_enabled SET DEFAULT true,
  ALTER COLUMN discovery_enabled SET NOT NULL,
  ALTER COLUMN enabled SET DEFAULT false;

COMMENT ON COLUMN kwave_news_whitelist.rss_poll_enabled IS
  'RSS poller 전용 상태. 7회 연속 fetch 실패 시 자동 OFF. discovery 상태와 독립.';

COMMENT ON COLUMN kwave_news_whitelist.discovery_enabled IS
  '온디맨드 site search 전용 상태. RSS polling 상태와 독립.';

COMMENT ON COLUMN kwave_news_whitelist.enabled IS
  'Legacy compatibility only. 신규 RSS/discovery 경로는 사용하지 않으며 새 행 기본값은 안전하게 false.';

COMMENT ON COLUMN kwave_news_whitelist.consecutive_failures IS
  '연속 RSS fetch 실패 카운트. 7회 도달 시 rss_poll_enabled=false 자동.';

COMMIT;
