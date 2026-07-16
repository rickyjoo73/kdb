-- 0095: 번역 캐시를 엔티티 단위로 확장 — 맥락 문장(gemma) 채움 경로는 같은 이름이라도
-- 엔티티별 맥락이 달라 결과가 다를 수 있다(동명이인). 읽기(재매칭) 경로는 entity_id=''
-- 로 기존 키 그대로. 기존 실패 캐시(''키)가 엔티티-키 재시도를 막지 않게 되는 효과도 있음.

ALTER TABLE kwave_kdb_translate_cache
  ADD COLUMN IF NOT EXISTS entity_id text NOT NULL DEFAULT '';

ALTER TABLE kwave_kdb_translate_cache
  DROP CONSTRAINT IF EXISTS kwave_kdb_translate_cache_term_ko_term_type_key;

CREATE UNIQUE INDEX IF NOT EXISTS kwave_kdb_translate_cache_term_type_entity_key
  ON kwave_kdb_translate_cache (term_ko, term_type, entity_id);
