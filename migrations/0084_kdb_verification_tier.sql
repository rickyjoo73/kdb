-- 0084_kdb_verification_tier.sql
-- 엔티티-레벨 "정체성 검증 tier"(증분2). 기존 verified_only 게이트는 per-locale VALUE
-- 신뢰(이 일본어 표기가 믿을 만한가)를 다뤘고, 이 tier 는 엔티티 IDENTITY(이 항목이
-- 실재하는 올바른 K-엔티티인가 — 오염/동명이인 mislink 아닌가)를 다룬다. 상호보완.
--
-- 설계(handoff 27차 §6 증분2): "1회 검증 → 캐시 → 빠른 서빙" 게이트.
--   - 서빙(핫패스)은 이 캐시 컬럼만 읽어 즉답(요청마다 재검사 안 함).
--   - authoritative : Wikidata QID/TMDb/KOFIC/KMDb/MusicBrainz 등 권위앵커 보유(결정론·무료).
--   - evidenced     : 권위앵커 없지만 wikipedia langlink·강한 source·conf≥0.75 로 독립 확증,
--                     또는 검색근거+gemma 판정으로 확인(evidence 'search+gemma%').
--   - unverified    : 독립 확증 없음 = 리스크 표면(네이버news+gemma 업그레이드 or 운영자 검토 대상).
-- verification_evidence = 사람이 읽는 근거 한 줄. verified_tier_at = 마지막 검증 시각(재검증 staleness).

BEGIN;

ALTER TABLE kwave_entities
  ADD COLUMN IF NOT EXISTS verification_tier     text,
  ADD COLUMN IF NOT EXISTS verification_evidence text,
  ADD COLUMN IF NOT EXISTS verified_tier_at      timestamptz;

-- 서빙 게이트/admin 필터가 tier 로 자주 스캔 → 부분 인덱스(active 만).
CREATE INDEX IF NOT EXISTS kwave_entities_verification_tier_idx
  ON kwave_entities (verification_tier)
  WHERE status = 'active';

COMMIT;
