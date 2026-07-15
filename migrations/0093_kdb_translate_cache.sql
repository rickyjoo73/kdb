-- 0093: 번역 재매칭 캐시 (2026-07-15, 오너 승인 — docs/runbooks/KDB_TRANSLATION_API_REVIEW_20260715.md).
-- 읽기 경로 전용: miss 한글 키워드(음차 제목류)를 Google Cloud Translation v2 로
-- 영문 원형 복원해 재매칭할 때, 단어당 1회만 과금되도록 결과를 캐시한다.
-- chars 는 실제 전송 문자수(템플릿 문장 포함) — 월 무료한도(50만자) 가드 집계용.
-- translated_en='' 도 기록한다(복원 실패 재호출 방지).

CREATE TABLE IF NOT EXISTS kwave_kdb_translate_cache (
  id            BIGSERIAL PRIMARY KEY,
  term_ko       TEXT        NOT NULL,
  term_type     TEXT        NOT NULL DEFAULT '',
  translated_en TEXT        NOT NULL DEFAULT '',
  chars         INT         NOT NULL DEFAULT 0,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (term_ko, term_type)
);

CREATE INDEX IF NOT EXISTS kwave_kdb_translate_cache_created_idx
  ON kwave_kdb_translate_cache (created_at DESC);
