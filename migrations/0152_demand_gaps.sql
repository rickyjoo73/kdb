-- 0152: 결핍 원장 — **지금 못 답하는 수요**를 상시로 쌓는다 (2026-09-20).
--
-- ★왜. 성장 고리에서 [결핍 진단] 자리가 비어 있다.
--
--     요청 로그 → [결핍 진단] → 공급원 선택 → 레인 실행 → 수확 측정 → 다음 대상
--                   ↑ 여기를 사람이 매 회차 SQL 로 팠다
--
--   0151 이 「레인이 무엇을 얻었나」를 메웠다면, 이 표는 「무엇이 부족한가」를 메운다.
--   둘이 있어야 «부족한 곳에 레인을 붙였고 실제로 메워졌는가» 를 시스템이 스스로 안다.
--
-- ★두 종류의 결핍은 다른 것이다. 섞으면 처방이 갈린다.
--
--     unmet       요청이 왔는데 **그 대상을 아예 모른다**   → 발굴·공급원 문제
--     locale_gap  대상은 아는데 **그 언어 표기가 없다**     → 표기 레인 문제
--
--   지금까지 이 둘을 한 숫자로 말해서 «해소율 65%» 가 무엇을 하라는 뜻인지 불분명했다.
--
-- ★관측이지 판단이 아니다. 무엇을 먼저 할지는 사람과 다음 레인이 정한다. 그래서
--   표본(top_terms)을 함께 남긴다 — 숫자만 있으면 «왜 그 유형이 큰가» 를 다시 파야 한다.
--   이 저장소는 집계만 보고 결론 내렸다가 세 번 틀린 이력이 있다.
--
-- ★창(window_days)을 행에 적는다. 7일과 30일은 다른 이야기인데 표에 안 적으면
--   나중에 두 수를 섞어 본다.
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_demand_gaps (
  id             BIGSERIAL PRIMARY KEY,
  measured_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  window_days    INTEGER     NOT NULL,
  kind           TEXT        NOT NULL,              -- unmet | locale_gap
  entity_type    TEXT        NOT NULL DEFAULT '',   -- '' = 유형 미지정 요청
  locale         TEXT        NOT NULL DEFAULT '',   -- locale_gap 에서만 채운다
  distinct_terms INTEGER     NOT NULL DEFAULT 0,    -- 그 칸의 고유 낱말 수
  requests       INTEGER     NOT NULL DEFAULT 0,    -- 그 낱말들이 불린 횟수
  top_terms      TEXT[]      NOT NULL DEFAULT '{}', -- 수요 큰 것부터 표본(최대 10)
  CONSTRAINT kwave_kdb_demand_gaps_kind CHECK (kind IN ('unmet','locale_gap')),
  CONSTRAINT kwave_kdb_demand_gaps_window CHECK (window_days > 0),
  CONSTRAINT kwave_kdb_demand_gaps_counts CHECK (distinct_terms >= 0 AND requests >= 0),
  -- locale_gap 은 locale 이 있어야 하고, unmet 은 없어야 한다. 섞이면 표가 거짓말을 한다.
  CONSTRAINT kwave_kdb_demand_gaps_locale_shape
    CHECK ((kind = 'locale_gap' AND locale <> '') OR (kind = 'unmet' AND locale = ''))
);

CREATE INDEX IF NOT EXISTS kwave_kdb_demand_gaps_recent_idx
  ON kwave_kdb_demand_gaps (measured_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_demand_gaps_dim_idx
  ON kwave_kdb_demand_gaps (kind, entity_type, locale, measured_at DESC);

COMMENT ON TABLE kwave_kdb_demand_gaps IS
  '지금 못 답하는 수요의 주기 관측. unmet=대상을 모른다 · locale_gap=대상은 아는데 그 언어 표기가 없다. 판단이 아니라 관측이며 표본을 함께 남긴다.';

COMMIT;
