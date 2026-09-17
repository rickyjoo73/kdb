-- 0149: 정정 검증이 «언제 시작됐는지》를 기록한다
--
-- ★왜 (2026-09-17 실측). ReapStale 은 크래시·재배포로 verifying 에 갇힌 행을 복구한다:
--
--     UPDATE kwave_kdb_corrections
--        SET status='pending', resolution='검증 미완료(프로세스 재시작) — 운영자 심사'
--      WHERE status='verifying' AND created_at < now() - interval '10 minutes'
--
--   그런데 `created_at` 은 **정정이 접수된 시각**이지 검증을 시작한 시각이 아니다.
--   그래서 접수된 지 10분이 넘은 정정은 검증에 들어가는 **즉시** 회수 대상이 된다.
--   codex 판정은 수십 초가 걸리므로, 판정이 진행 중인데 pending 으로 되돌려 놓고
--   재검증 레인이 같은 건을 다시 집는다 — **같은 정정을 두 번 판정한다.**
--
--   실측: 대기 25건을 재검증 큐에 넣었더니 codex 일일 상한 60회가 24분에 소진됐다.
--   25건에 60회면 건당 2.4회다. 한 번이면 끝날 일이었다.
--
--   이 표에는 "verifying 에 들어간 시각" 컬럼이 없었다(id · entity_id · locale ·
--   returned_value · suggested_value · evidence_url · reporter · reason · status ·
--   resolution · created_at · resolved_at · proposed_value). 그래서 created_at 을
--   쓴 것으로 보인다. 컬럼을 만든다.
--
-- ★NULL 을 허용한다. 지금 verifying 인 행은 시작 시각을 모른다 — 모르는 것을
--   지어내지 않는다. 회수 조건은 `COALESCE(verifying_since, created_at)` 로 쓰므로
--   기존 행은 종전과 똑같이(= 안전하게) 동작하고, 새로 들어가는 행부터 정확해진다.
BEGIN;

ALTER TABLE kwave_kdb_corrections
    ADD COLUMN IF NOT EXISTS verifying_since timestamptz;

-- 회수 스캔이 보는 조합. verifying 은 항상 소수라 부분 인덱스로 충분하다.
CREATE INDEX IF NOT EXISTS kwave_kdb_corrections_verifying_idx
    ON kwave_kdb_corrections (verifying_since)
 WHERE status = 'verifying';

COMMIT;
