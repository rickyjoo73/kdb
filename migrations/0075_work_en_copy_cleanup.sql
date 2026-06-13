-- 0075_work_en_copy_cleanup.sql
-- 작품(drama/movie/show) 번역 실패 정리: 비영어 locale 값이 영어 제목을 그대로
-- 복사한 codex-fallback 건을 비운다.
--
-- 배경: 현재 주력 채움 엔진(Hermes Enricher)에 작품 공식 현지 제목의 정답 소스인
-- TMDb 가 빠져 있어, codex 가 모르는 작품의 비영어 제목을 영어로 복사해 왔다
-- (예: 드라마 '신사와 아가씨' es='Young Lady and Gentleman' = en 복사). 작품 제목은
-- 음역이 아니라 "번역/현지화"여야 한다(오징어게임 es='El juego del calamar').
--
-- 수정: ① Enricher cascade 에 TMDb L1 배선(공식 현지 제목, codex 보다 먼저)
--       ② FillLocale 프롬프트에 "비영어 locale 영어복사 금지, 모르면 skip" 규칙.
-- 이 migration 은 ③ 기존 영어복사 오염을 비워(스냅샷) 재채움 backlog 로 돌린다.
--   재채움 시: TMDb 공식 제목 → 없으면 codex(개선 프롬프트, 영어복사 대신 skip)
--   → 그래도 모르면 빈칸(빈칸 > 영어복사).
--
-- 안전장치: 원값을 kwave_kdb_dataqa_log(verdict='en-copy')에 스냅샷 → Revert 가능.
--   operator-locked / 비-codex source 는 건드리지 않는다. song_album 은 제외(곡명은
--   영어 원제 유지가 정당한 경우가 많음). 영문 원제 작품(Dear.M 등)은 비워져도
--   재채움이 TMDb/codex 로 같은 값을 복원(자가치유) + revert 가능.
BEGIN;

DO $$
DECLARE
  loc text;
  cols text[] := ARRAY['canonical_es','canonical_vi','canonical_pt_br','canonical_id',
                       'canonical_ja','canonical_zh','canonical_zh_hant'];
  col text;
  code text;
BEGIN
  FOREACH col IN ARRAY cols LOOP
    code := replace(col, 'canonical_', '');
    -- 스냅샷(revert 용)
    EXECUTE format($f$
      INSERT INTO kwave_kdb_dataqa_log (entity_id, locale, old_value, old_source, verdict, reason, model)
      SELECT id, %L, %I, %I, 'en-copy',
             '작품 비영어 locale 이 영어 제목 복사(번역실패) — 비움 후 재채움', 'rule'
        FROM kwave_entities
       WHERE status='active' AND entity_type IN ('drama','movie','show')
         AND COALESCE(%I,'')<>'' AND %I = canonical_en
         AND %I = 'codex-fallback' AND NOT operator_locked
    $f$, code, col, col||'_source', col, col, col||'_source');
    -- 비우기
    EXECUTE format($f$
      UPDATE kwave_entities
         SET %I='', %I='', updated_at=now()
       WHERE status='active' AND entity_type IN ('drama','movie','show')
         AND COALESCE(%I,'')<>'' AND %I = canonical_en
         AND %I = 'codex-fallback' AND NOT operator_locked
    $f$, col, col||'_source', col, col, col||'_source');
    -- 재채움 backlog 진입(exhausted 행 제거)
    EXECUTE format($f$
      DELETE FROM kwave_kdb_enrich_attempts a
       USING kwave_entities e
       WHERE a.entity_id=e.id AND a.field=%L
         AND e.entity_type IN ('drama','movie','show') AND COALESCE(e.%I,'')=''
    $f$, col, col);
  END LOOP;
END $$;

COMMIT;
