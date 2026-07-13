-- 0092: 소비자 API 요청 "내용" 로그 (2026-07-13, 오너 지시).
-- 기존 kwave_kdb_api_requests 는 HTTP 메타(경로·상태·속도)만 남긴다. 이 테이블은
-- 요청 본문의 항목 단위 — 어떤 기사(URL)에서 어떤 키워드를 어떤 type 으로 보냈고
-- KDB 가 뭐라고 답했는지 — 를 기록해 admin 에서 기사별 요청처럼 빠르게 검토하게 한다.
-- 한 API 호출 = 한 request_group(uuid).

CREATE TABLE IF NOT EXISTS kwave_kdb_request_terms (
  id            BIGSERIAL PRIMARY KEY,
  request_group UUID        NOT NULL,
  consumer_id   TEXT        NOT NULL DEFAULT '',
  origin        TEXT        NOT NULL DEFAULT '',  -- prepare | lookup | correction
  source_url    TEXT        NOT NULL DEFAULT '',
  term_ko       TEXT        NOT NULL,
  term_type     TEXT        NOT NULL DEFAULT '',  -- 소비자가 보낸 type 힌트(빈값=미지정)
  has_context   BOOLEAN     NOT NULL DEFAULT false,
  item_status   TEXT        NOT NULL DEFAULT '',  -- ready/preparing/new/out_of_scope/found/miss/...
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS kwave_kdb_request_terms_created_idx
  ON kwave_kdb_request_terms (created_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_request_terms_group_idx
  ON kwave_kdb_request_terms (request_group);

COMMENT ON TABLE kwave_kdb_request_terms IS
  'Consumer API request payload items (one row per requested term). 30-day retention.';
