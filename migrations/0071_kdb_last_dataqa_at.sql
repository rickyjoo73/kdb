-- 0071_kdb_last_dataqa_at.sql — dataqa 자가치유 루프용 검수 타임스탬프.
--
-- 워커가 주기적으로 dataqa 를 돌릴 때, 이미 검수한 entity 를 변경(updated_at) 전까지
-- 재검수하지 않도록 last_dataqa_at 을 둔다(codex 낭비 방지 + 점진 커버리지).
-- enrich 등으로 updated_at 이 갱신되면(= 새 오염 가능) 자동으로 재검수 대상이 된다.

BEGIN;

ALTER TABLE kwave_entities ADD COLUMN IF NOT EXISTS last_dataqa_at timestamptz;

-- 미검수(NULL) 또는 검수 후 변경된 entity 만 빠르게 뽑기 위한 부분 인덱스.
CREATE INDEX IF NOT EXISTS kwave_entities_dataqa_pending_idx
  ON kwave_entities(updated_at)
  WHERE status='active' AND entity_type IN ('person','group');

COMMENT ON COLUMN kwave_entities.last_dataqa_at IS
  'gpt-5.5 dataqa 가 마지막으로 이 entity 를 검수한 시각. updated_at > last_dataqa_at 이면 재검수 대상.';

COMMIT;
