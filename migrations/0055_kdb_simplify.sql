-- 0055_kdb_simplify.sql — Phase 1 단순화 (외부 Agent 권고 반영).
--
-- 운영자 비판: *"너가 만든것 은 정말 정신없어 그리고 지저분해"*
-- Agent (Architect/SRE/NLP) 공통 권고:
--   1. kwave_entity_candidates 별 테이블 폐기 → kwave_entities.status 흡수
--   2. kwave_kdb_codex_runs 의 ok 호출 미저장 (에러만)
--
-- forward-only (CLAUDE.md §6). 기존 테이블 drop 대신 deprecated 표시.

BEGIN;

-- ─── 1. kwave_entities 에 status + source_domains 컬럼 ──────────────
-- candidates 테이블 흡수 정공법 (§5 single source of truth)
ALTER TABLE kwave_entities
  ADD COLUMN status         text NOT NULL DEFAULT 'active',
  ADD COLUMN source_domains text[]       DEFAULT '{}';

COMMENT ON COLUMN kwave_entities.status IS
  'active | candidate (RSS 발견 ≥2 매체 합의 전) | rejected (운영자 제외). 합의 도달 시 candidate→active 자동.';
COMMENT ON COLUMN kwave_entities.source_domains IS
  'RSS 매체 출처 누적 (candidates 흡수). active 후에도 audit 용.';

-- 기존 candidates promoted 마이그레이션:
--   kwave_entity_candidates.status='promoted' 인 row 가 이미 kwave_entities 에
--   INSERT 됐으므로 source_domains 만 backfill.
UPDATE kwave_entities e SET
  source_domains = c.source_domains
FROM kwave_entity_candidates c
WHERE e.canonical_ko = c.ko_hint AND c.status='promoted';

-- candidates 테이블은 drop 대신 1주 보존 (rollback 안전망). 0056 또는 30일 후 drop.
COMMENT ON TABLE kwave_entity_candidates IS
  '[DEPRECATED 2026-05-25] kwave_entities.status=candidate 로 흡수. 1주 후 drop.';

CREATE INDEX kwave_entities_status_idx ON kwave_entities (status)
  WHERE status != 'active';

-- ─── 2. codex_runs 에 인덱스 보강 (ok 미저장 정공법) ─────────────────
-- AuditCall 의 ok 분기는 코드에서 INSERT 안 함 (extractor.go 변경).
-- 기존 ok row 는 7일 retention 으로 자연 삭제 (기존 retention.go 가 처리).

COMMIT;
