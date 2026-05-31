-- 0065_disambig_review_cooldown.sql
-- Disambiguator 공회전 방지 쿨다운.
--
-- 증상: quarantine(uncertain) 처리가 needs_disambig=true + updated_at=now() 로
-- 갱신하는데, near-name seed Select 가 ORDER BY updated_at DESC 로 뽑아, 메타데이터
-- 부족으로 매번 uncertain 판정되는 클러스터가 큐 맨 앞으로 점프해 매 cycle
-- 재선택·재판정된다(gpt-5.5 호출만 소모, 수렴 X). needs_disambig 자체는 Phase 1
-- markHomonymsIfConflict 가 flag 한 진짜 동명이인도 포함하므로 단순 제외는 불가.
--
-- 해결: 마지막 disambiguator 판정 시각을 기록하고, Select 에서 쿨다운(기본 14일)
-- 내 재판정된 항목을 제외한다. 리졸브(merge/distinct)는 needs_disambig 를 해제하므로
-- 자연히 빠지고, 쿨다운 경과 후엔(그 사이 enrich 로 메타데이터가 채워졌을 수 있으니)
-- 1회 재시도된다.
BEGIN;

ALTER TABLE kwave_entities
    ADD COLUMN IF NOT EXISTS disambig_reviewed_at TIMESTAMPTZ;

-- Select 의 쿨다운 필터(disambig_reviewed_at IS NULL OR < now()-interval)용 인덱스.
CREATE INDEX IF NOT EXISTS kwave_entities_disambig_reviewed_idx
    ON kwave_entities (disambig_reviewed_at)
 WHERE status IN ('active', 'candidate');

COMMIT;
