-- 0151: 레인 성과 원장 — **레인이 돌았는지, 무엇을 얻었는지를 남긴다** (2026-09-20).
--
-- ★왜. 지금은 레인이 돌았는지조차 알 수 없다. 하루 동안 같은 자리에서 세 번 데였다:
--
--     zhwiki    dry-run 이 「389건 채움」이라 말하는 동안 원장은 **0건** 바뀌었다
--     localfill 로그 13줄을 24시간치로 읽었다 — 앱이 48분 전 재시작한 것이었다
--     org-anchor 유형 둘을 추가했는데 실제 수확은 50건 검사에 **0건**이었다
--
--   셋 다 「레인이 무엇을 했는가」가 어디에도 안 남아서 생겼다. 로그는 앱 수명만큼만
--   살고, 0건일 때는 아예 안 찍는 레인이 많다. **안 돈 것과 돌았는데 0건인 것을
--   구별할 수 없다** — 이 저장소가 「조용한 0건」으로 반복해 데인 병의 뿌리다.
--
-- ★그래서 세 수를 나눠 적는다.
--     scanned  선정 쿼리가 뽑은 행
--     applied  **원장이 실제로 바뀐** 수
--     skipped  뽑았지만 쓰지 않은 수 (+ reasons 에 사유별 계수)
--
--   `scanned > 0 AND applied = 0` 이 반복되면 그것이 조용한 0건이다. 사람이 로그를
--   뒤져 찾아낼 일이 아니라 **질의 한 줄로 나와야 한다.**
--
-- ★값이 아니라 관측이다. 판단(어느 레인을 끌지)은 사람과 다음 레인이 한다.
--   보존은 90일이면 충분하다 — 추세를 보는 표이지 원본이 아니다.
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_lane_runs (
  id          BIGSERIAL PRIMARY KEY,
  lane        TEXT        NOT NULL,
  started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  duration_ms INTEGER     NOT NULL DEFAULT 0,
  scanned     INTEGER     NOT NULL DEFAULT 0,
  applied     INTEGER     NOT NULL DEFAULT 0,
  skipped     INTEGER     NOT NULL DEFAULT 0,
  reasons     JSONB       NOT NULL DEFAULT '{}'::jsonb,
  dry         BOOLEAN     NOT NULL DEFAULT false,
  CONSTRAINT kwave_kdb_lane_runs_lane_not_blank CHECK (lane <> ''),
  CONSTRAINT kwave_kdb_lane_runs_counts_sane
    CHECK (scanned >= 0 AND applied >= 0 AND skipped >= 0 AND applied <= scanned)
);

CREATE INDEX IF NOT EXISTS kwave_kdb_lane_runs_lane_idx
  ON kwave_kdb_lane_runs (lane, started_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_lane_runs_recent_idx
  ON kwave_kdb_lane_runs (started_at DESC);

COMMENT ON TABLE kwave_kdb_lane_runs IS
  '레인 1회 실행의 관측. scanned>0 AND applied=0 이 반복되면 조용한 0건이다. 90일 보존.';
COMMENT ON COLUMN kwave_kdb_lane_runs.applied IS
  '원장이 실제로 바뀐 수. 선정 수(scanned)와 반드시 구분한다 — 이 둘을 섞어 적은 것이 zhwiki 389건 거짓말의 원인이었다.';
COMMENT ON COLUMN kwave_kdb_lane_runs.reasons IS
  '쓰지 않은 사유별 계수(jsonb). 뭉뚱그린 0건은 「대상이 없다」와 「못 찾았다」를 구분하지 못한다.';

COMMIT;
