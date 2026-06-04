-- 0072_enrich_attempts_last_source.sql
-- 0062 가 kwave_kdb_enrich_attempts 를 만들 때 last_source 컬럼을 빠뜨렸는데,
-- Enricher 에이전트의 recordAttempt(internal/kdb/agents/enricher/cascade.go)는
-- INSERT/UPSERT 에 last_source 를 쓴다 → 모든 원장 쓰기가 "column last_source
-- does not exist" 로 실패해 왔고, 에러는 `_, _ =` 로 삼켜져 보이지 않았다.
--
-- 결과: exhausted 가 한 번도 set 되지 않아 gap-fill 루프가 수렴하지 못함.
-- 8개 locale 이 전부 찼지만 정당하게 빈 person 필드(예: 솔로 배우의 groups)만
-- 남은 ~1600 건의 "가짜 backlog" 가 매 cycle 무한 재선택되어 Enricher 예산을
-- 독식 → 진짜 빈 entity 가 굶고 채움이 멈춘 것처럼 보였다.
--
-- 누락 컬럼을 추가해 원장 쓰기를 복구한다(코드 변경 없이 스키마만 정합).
BEGIN;

ALTER TABLE kwave_kdb_enrich_attempts
    ADD COLUMN IF NOT EXISTS last_source TEXT NOT NULL DEFAULT '';

COMMIT;
