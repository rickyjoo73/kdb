-- 0138: 앵커 판정을 **저장한다.** 지금은 물어볼 때마다 위키데이터를 3,762번 다시 부른다.
--
-- 2026-09-15 실측으로 활성 원장의 wikidata 앵커 590건이 유형과 어긋나는 것을 찾았다.
-- 그런데 그 판정이 어디에도 안 남는다. 그래서 셋 다 못 한다:
--   ① 화면에 검수 줄을 띄울 수 없다 (매번 30분짜리 조회를 돌 수는 없다)
--   ② 불변식이 읽을 수 없다 — `authoritative-no-ref` 는 **ref 가 있는지만** 본다.
--      110건은 ref 가 있었으므로 통과했다. 그 ref 가 **무엇을 가리키는지**는 아무도 안 봤다.
--      이 계열이 오래 산 직접적인 이유다.
--   ③ 다시 돌릴 때마다 전량을 다시 조회한다.
--
-- ★저장하는 것은 **관측**이지 결론이 아니다. `instance_of` 와 `description` 을 그대로
--   적어 두어, 나중에 판정 규칙이 바뀌어도 원자료로 다시 판정할 수 있게 한다.
--   `checked_at` 이 있으므로 늙은 판정은 다시 조회하면 된다 — 저장된 의견은 늙는다.
--
-- ★일치한 것도 적는다. "언제 봤는데 문제없었다"를 알아야 다시 안 본다.

BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_anchor_audit (
  entity_id   uuid        NOT NULL,
  provider    text        NOT NULL DEFAULT 'wikidata',
  external_id text        NOT NULL,
  entity_type text        NOT NULL,                 -- 판정 당시 우리 유형
  verdict     text        NOT NULL DEFAULT '',      -- '' = 어긋남 없음
  class       text        NOT NULL DEFAULT '',      -- 판정 근거가 된 P31 값
  instance_of text[]      NOT NULL DEFAULT '{}',    -- 원자료. 규칙이 바뀌어도 다시 판정 가능
  description text        NOT NULL DEFAULT '',
  checked_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_id, provider, external_id)
);

-- 검수 화면: 어긋난 것만, 최근 본 것부터.
CREATE INDEX IF NOT EXISTS kwave_kdb_anchor_audit_verdict
  ON kwave_kdb_anchor_audit (verdict, checked_at DESC) WHERE verdict <> '';

-- 재조회 대상 고르기: 오래된 것부터.
CREATE INDEX IF NOT EXISTS kwave_kdb_anchor_audit_stale
  ON kwave_kdb_anchor_audit (checked_at);

COMMIT;
