-- 0058_pg_trgm.sql — alias matcher 의 typo 매칭에 사용 (similarity 0.0~1.0).
-- 운영자 정공법: 신규 candidate 가 기존 active entity 의 ko 와 가까우면 (similarity ≥ 0.6)
-- 오타로 추정 → merge 후보 (운영자 확인).
CREATE EXTENSION IF NOT EXISTS pg_trgm;

-- GIN index — `canonical_ko` 에 trigram 으로 유사도 검색 가속.
CREATE INDEX IF NOT EXISTS kwave_entities_canonical_ko_trgm
  ON kwave_entities USING gin (canonical_ko gin_trgm_ops);
