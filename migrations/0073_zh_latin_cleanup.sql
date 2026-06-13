-- 0073_zh_latin_cleanup.sql
-- 레거시 정리: person 의 canonical_zh / canonical_zh_hant 에 한국어 로마자
-- (라틴 only)가 들어간 codex-fallback 값을 비운다.
--
-- 배경: locale 문자셋 가드(IsValidSpellingForLocale, observations.go)가 도입되기
-- 전 codex-fallback 이 한자 필드(zh/zh-hant)에 "Hong Sung-yoon" 류 한국어 로마자를
-- 그대로 채웠다. 중국 매체는 한국 인명을 한자로 표기하므로(예: 박보검 → 朴宝剑)
-- 이런 라틴 표기는 명백한 오류다. 그룹/곡/브랜드는 "BTS" 처럼 라틴 통용명이
-- 정당할 수 있어 제외하고, person 만 정리한다(인명은 한자 표기가 규범).
--
-- 안전장치:
--   * 원값을 kwave_kdb_dataqa_log 에 스냅샷 → 기존 Revert 경로로 되돌리기 가능.
--   * 비운 뒤 enrich_attempts 의 해당 field 행을 삭제 → Enricher 가 재시도.
--     (이제 프롬프트가 "zh/zh-hant 라틴 금지, 모르면 skip", 쓰기 가드가 라틴 거부
--      → 한자 표기를 얻거나, 못 얻으면 빈칸 유지: 빈칸 > 틀린값.)
--   * operator_locked / wikidata-label 소스는 건드리지 않는다.
--   * 멱등: 비운 값은 다음 실행에서 다시 매칭되지 않는다.
BEGIN;

-- ── canonical_zh ──────────────────────────────────────────────────────
WITH target AS (
  SELECT id, canonical_zh AS old_val, canonical_zh_source AS old_src
    FROM kwave_entities
   WHERE status = 'active' AND entity_type = 'person'
     AND COALESCE(canonical_zh, '') <> ''
     AND canonical_zh !~ '[一-鿿]'              -- 한자 미포함 = 라틴/로마자
     AND canonical_zh_source = 'codex-fallback'
     AND NOT operator_locked
),
logged AS (
  INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
  SELECT id, 'zh', old_val, old_src, 'wrong-script',
         'codex 라틴 로마자가 한자 필드(zh)에 — 비움(빈칸>틀린값)', 'rule'
    FROM target
  RETURNING entity_id
),
updated AS (
  UPDATE kwave_entities e
     SET canonical_zh = '', canonical_zh_source = '', updated_at = now()
    FROM target t WHERE e.id = t.id
  RETURNING e.id
)
DELETE FROM kwave_kdb_enrich_attempts
 WHERE field = 'canonical_zh' AND entity_id IN (SELECT id FROM target);

-- ── canonical_zh_hant ─────────────────────────────────────────────────
WITH target AS (
  SELECT id, canonical_zh_hant AS old_val, canonical_zh_hant_source AS old_src
    FROM kwave_entities
   WHERE status = 'active' AND entity_type = 'person'
     AND COALESCE(canonical_zh_hant, '') <> ''
     AND canonical_zh_hant !~ '[一-鿿]'
     AND canonical_zh_hant_source = 'codex-fallback'
     AND NOT operator_locked
),
logged AS (
  INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
  SELECT id, 'zh_hant', old_val, old_src, 'wrong-script',
         'codex 라틴 로마자가 한자 필드(zh_hant)에 — 비움(빈칸>틀린값)', 'rule'
    FROM target
  RETURNING entity_id
),
updated AS (
  UPDATE kwave_entities e
     SET canonical_zh_hant = '', canonical_zh_hant_source = '', updated_at = now()
    FROM target t WHERE e.id = t.id
  RETURNING e.id
)
DELETE FROM kwave_kdb_enrich_attempts
 WHERE field = 'canonical_zh_hant' AND entity_id IN (SELECT id FROM target);

COMMIT;
