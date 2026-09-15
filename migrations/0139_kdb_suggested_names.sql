-- 0139: 소비자가 만든 제안 표기를 **값이 아닌 재료로** 받는다.
--
-- presslocale 이 기사를 번역하면서 GPT 로 음역·직역한 고유명사 표기를 갖고 있다.
-- 지금은 그것을 KDB 에 줄 자리가 없어서, 사이트마다 따로 만들고 매번 달라진다
-- (실측: 『기쁜 우리 좋은 날』 일본어 직역이 8회 호출에 2~4가지).
--
-- ★그런데 제안은 **값이 아니다.** 오늘 실측 셋이 그 이유다.
--     gtranslate-raw   40% 틀림 → 레인 자체를 없앴다(수제천→手工制作的)
--     앵커 오염 110건   틀린 항목에서 베낀 값이 authoritative 로 나갔다
--     제목 6,640칸      번역할 자리를 음역으로 채웠다
--   셋 다 "일단 채워 넣은 값"이 원인이다. 그래서 제안은 표기 칸에 안 들어간다.
--
-- ★승격 경로를 **만들지 않는다.** 근거가 생기면 그 근거로 표기 칸에 새로 쓴다.
--   "제안이 먼저 있었다"가 승격 이유가 되면, 우리가 보낸 값이 검증값으로 되돌아온다
--   (순환 오염). 그래서 이 표에서 kwave_entities 로 가는 코드 경로는 없다.
--
-- ★그러나 **입증되면 교체된다.** 승격이 아니라 교체다 — 둘은 다르다.
--     승격: 제안이 자라서 값이 된다              (금지. 순환)
--     교체: 근거 있는 값이 들어오면 제안은 물러난다 (당연. 여기서 강제한다)
--   superseded_at/superseded_by 가 그 순간을 적는다. 물러난 제안은 **서빙에서 빠진다** —
--   안 빼면 검증값과 제안이 동시에 나가서 받는 쪽이 어느 것을 쓸지 다시 헷갈린다.
--   기록은 지우지 않는다. 우리가 채운 값과 소비자 제안이 얼마나 맞았는지가
--   그 자체로 지표다(제안 품질을 재는 유일한 방법이다).
--
-- ★한 라벨로 전량 회수된다. gtranslate-raw 222칸을 되돌려 봐서 아는데,
--   잘못된 유입을 한 번에 걷어낼 수 있어야 한다. producer 하나로 지우면 된다.
--
-- ★이름이 아니라 **대상 ID** 에 붙인다. 이름에 붙이면 동명 함정에 그대로 들어간다 —
--   오늘 채영(TWICE)/채영(CLC), 한혜진 셋을 겪었다. 아직 대상이 안 정해진 요청은
--   entity_id 가 NULL 로 들어오고, 나중에 정해지면 그때 채운다.

BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_suggested_names (
  id             bigserial PRIMARY KEY,
  entity_id      uuid,                        -- 정해졌으면 대상. 아직이면 NULL.
  term_ko        text        NOT NULL,        -- 소비자가 물은 이름 (대상 미정일 때의 유일한 단서)
  locale         text        NOT NULL,
  value          text        NOT NULL,
  basis          text        NOT NULL DEFAULT '',   -- literal · transliteration · official …
  producer       text        NOT NULL,              -- presslocale · …
  model          text        NOT NULL DEFAULT '',   -- gpt-5.6-sol …
  reasoning      text        NOT NULL DEFAULT '',   -- low · medium …
  preparation_id uuid,
  source_url     text        NOT NULL DEFAULT '',
  seen_count     integer     NOT NULL DEFAULT 1,    -- 같은 제안이 몇 번 왔나
  first_seen_at  timestamptz NOT NULL DEFAULT now(),
  last_seen_at   timestamptz NOT NULL DEFAULT now(),
  -- 입증된 값이 들어와 이 제안이 물러난 시각과, 그때 우리가 채운 값·출처.
  -- NULL 이면 아직 우리 값이 없다는 뜻이고, 그때만 서빙 대상이다.
  superseded_at    timestamptz,
  superseded_by    text        NOT NULL DEFAULT '',   -- 교체한 우리 값
  superseded_src   text        NOT NULL DEFAULT '',   -- 그 값의 출처 (tmdb · operator …)
  matched_ours     boolean,                           -- 교체 시점에 제안과 우리 값이 같았나
  CONSTRAINT kwave_kdb_suggested_names_value_len CHECK (char_length(value) BETWEEN 1 AND 400),
  CONSTRAINT kwave_kdb_suggested_names_term_len  CHECK (char_length(term_ko) BETWEEN 1 AND 200)
);

-- ★이름당·로케일당·제작처당 **하나만** 유지한다. 호출마다 달라지는 직역을 매번 쌓으면
--   후보가 여러 개가 되고, 그러면 "먼저 정한 것을 모두가 다시 쓴다"는 목적 자체가 깨진다.
--   먼저 온 것이 남고, 같은 값이 다시 오면 seen_count 만 오른다.
CREATE UNIQUE INDEX IF NOT EXISTS kwave_kdb_suggested_names_one
  ON kwave_kdb_suggested_names (term_ko, locale, producer);

CREATE INDEX IF NOT EXISTS kwave_kdb_suggested_names_entity
  ON kwave_kdb_suggested_names (entity_id, locale) WHERE entity_id IS NOT NULL;

-- 아직 안 물러난 것만 서빙·집계 대상이다.
CREATE INDEX IF NOT EXISTS kwave_kdb_suggested_names_live
  ON kwave_kdb_suggested_names (locale, term_ko) WHERE superseded_at IS NULL;

COMMIT;
