-- 0157: 앵커 어긋남을 판정 모델이 **봤다**는 기록 (2026-09-30).
--
-- ★왜. 앵커 감사 → 판정 → 집행이 전부 손으로 돌리는 명령(anchor-audit · anchor-judge ·
--   anchor-apply)뿐이었다. 자동 레인이 붙이는 새 앵커는 아무도 감사하지 않은 채 «검증»
--   등급으로 나갈 수 있었고, 09-24 에 «감사 받은 적 없던 앵커 892개»가 그렇게 드러났다.
--   오너 방침(«AI 가 스스로 판단해 작동», «review = 무시가 되지 않게»)대로 anchor-review
--   레인이 감사·판정·집행을 30분마다 한다.
--
-- ★판정 기록이 필요한 이유. unclear 판정은 앵커도 유형도 그대로 두므로 다음 회차에 같은
--   건이 맨 앞에 다시 나온다 — 기록이 없으면 레인은 같은 10건에 GPT 를 부르며 맴돈다.
--   judged_at 으로 30일간 다시 묻지 않는다(감사 신선도와 같은 주기).
BEGIN;
SET LOCAL lock_timeout = '3s';

ALTER TABLE kwave_kdb_anchor_audit
  ADD COLUMN IF NOT EXISTS judged_at      timestamptz,
  ADD COLUMN IF NOT EXISTS judged_verdict text NOT NULL DEFAULT '',   -- anchor_wrong · type_wrong · unclear
  ADD COLUMN IF NOT EXISTS judged_by      text NOT NULL DEFAULT '';   -- codex(gpt-6-luna) …

COMMIT;
