-- 0133: kentity_names.evidence_id 인덱스. 근거 철회가 268만 행을 훑고 있었다.
--
-- kentity_invalidate_withdrawn_evidence 는 근거가 철회될 때마다
--   UPDATE kentity_names SET status='blocked' WHERE evidence_id=NEW.id AND status='verified'
-- 를 실행한다. 그런데 kentity_names 에 evidence_id 인덱스가 없어 **행마다 Seq Scan** 이었다.
--
-- 실측(운영, 2026-09-14): 한 번에 **356ms**. 근거 2,000건을 철회하는 배치가
-- 712초 걸렸다(관측 726초). 인덱스 추가 뒤 **0.129ms — 2,750배**.
--
-- kentity_external_ids 에는 같은 목적의 인덱스가 이미 있었다(kentity_external_ids_evidence).
-- 두 표가 같은 트리거에서 같은 방식으로 조회되는데 한쪽만 인덱스가 있었다.
--
-- 운영에는 CREATE INDEX CONCURRENTLY 로 이미 만들어 두었다(잠금 없이). 여기서는
-- IF NOT EXISTS 라 운영에선 no-op 이고, 새로 만드는 DB·회귀 환경에 같은 인덱스를 준다.
CREATE INDEX IF NOT EXISTS kentity_names_evidence
  ON kentity_names (evidence_id) WHERE evidence_id IS NOT NULL;
