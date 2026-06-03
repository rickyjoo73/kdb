-- 0069_kdb_api_consumer_key_hash.sql — 소비자 API 키 평문 저장 폐기.
--
-- 리뷰 H4: kwave_kdb_api_consumers.key 가 평문으로 저장·조회·렌더되어 DB 덤프/
-- 백업/리플리카 노출 시 모든 활성 소비자 자격증명이 그대로 새어나갔다. 키를
-- SHA-256 해시로만 저장하고, 표시는 마스킹된 prefix 만 보관한다. 발급 시 평문은
-- 1회만 화면에 보여주고 절대 영속화하지 않는다 (handlers_consumers.go).
--
-- 인증은 제시된 키의 SHA-256 을 해시 맵에서 조회하므로(api.go), 원문 평문 비교가
-- 사라져 map-lookup 타이밍 오라클(H4 MED) 우려도 함께 해소된다.

BEGIN;

ALTER TABLE kwave_kdb_api_consumers ADD COLUMN IF NOT EXISTS key_hash   text;
ALTER TABLE kwave_kdb_api_consumers ADD COLUMN IF NOT EXISTS key_prefix text;

-- 기존 평문 키 → 해시 + 표시용 마스킹 prefix 로 backfill (sha256 는 PG11+ core 함수).
UPDATE kwave_kdb_api_consumers
   SET key_hash   = encode(sha256(key::bytea), 'hex'),
       key_prefix = left(key, 4) || '••••' || right(key, 4)
 WHERE key_hash IS NULL;

ALTER TABLE kwave_kdb_api_consumers ALTER COLUMN key_hash SET NOT NULL;

-- 평문 컬럼 제거 → 딸린 UNIQUE(key) / 부분 인덱스(btree(key) WHERE active)도 함께 삭제.
ALTER TABLE kwave_kdb_api_consumers DROP COLUMN key;

CREATE UNIQUE INDEX IF NOT EXISTS kwave_kdb_api_consumers_key_hash_uq
  ON kwave_kdb_api_consumers(key_hash);
-- 활성 키 조회용 부분 인덱스 (인증 캐시 refresh 가 WHERE active 로 전체 로드).
CREATE INDEX IF NOT EXISTS kwave_kdb_api_consumers_active_idx
  ON kwave_kdb_api_consumers(key_hash) WHERE active;

COMMIT;
