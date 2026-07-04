-- 0085_research_queue_source_url.sql
-- 발굴 큐에 출처 기사 URL 저장(kstory 등 소비자가 /v1/prepare 로 고유명사를 던질 때 어느
-- 기사에서 나온 것인지 추적 + 향후 역추적 재분석 기반). ResearchQueueRequest.SourceURL →
-- prepare/lookup-miss 발굴 시 기록. dedup 키(entity_ko, requested_entity_type, source_id)는
-- 불변 — source_url 은 추적 메타로만 저장(첫 통보 유지).

ALTER TABLE kwave_entity_research_queue
  ADD COLUMN IF NOT EXISTS source_url text;
