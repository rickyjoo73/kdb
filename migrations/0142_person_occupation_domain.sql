-- 0142: 정치인·운동선수·기업인을 **막을 것이 아니라 제대로 분류해서 서빙**한다.
--
-- ★운영자 결정 (2026-09-15): "정치, 경제, 시사, 스포츠 등 모두 해결이 되기 때문에
--   분류를 제대로 주어야 할 것 같은데, 가이드를 제대로 주어야 정치인·경제인 등
--   모두 제대로 DB를 활용하지."
--
-- ★계기. presslocale 이 종합지 기사를 보내기 시작하면서 답변률이 이렇게 갈렸다:
--
--     www.famtimes.co.kr     (연예 전문)   52건 중 즉시 47  →  90%
--     www.specialtimes.co.kr (종합지)     338건 중 즉시 33  →  10%
--
--   그런데 비-연예 인물은 **이미 들어와 있었다.** 앵커 보유 person 3,625건의 영역 분포:
--
--     연예 2,752 · 스포츠 320 · 정치 76 · 언론 40 · 학계 15 · 경제 14 · 기타 378
--
--   4분의 1이 비-연예인데 그것을 구분할 칸이 없었다. entity_type 은 person 하나뿐이고
--   category_hint 는 12,881건 중 12,346건(96%)이 빈칸인 데다 값도 k_pop·k_drama 같은
--   콘텐츠 분류라 사람의 영역을 담지 못한다.
--
-- ★왜 entity_type 을 안 늘리나.
--   정치인도 가수도 존재론적으로 person 이다. 다른 것은 **영역**이지 종류가 아니다.
--   유형을 늘리면 동명이인 가드·병합·앵커 감사·게이트 분기가 새 유형마다 갈라지고,
--   기존 person 5,264건을 전부 재분류해야 한다. 영역을 따로 들면 그 셋이 그대로 살고
--   동명이인 가드는 오히려 세진다 — 박찬호(야구선수)와 박찬호(가수)는 이름도 유형도
--   같지만 영역이 다르다.
--
-- ★두 칸을 둔다.
--   occupation_qids   위키데이터 P106 **원자료 그대로**. 영역 표가 늘어나면 다시 판정한다.
--   occupation_domain 그 원자료를 접은 값. 소비자가 이것으로 거른다.
--                     모르는 QID 는 빈 문자열이다 — 지어내지 않는다(D-37).

BEGIN;

ALTER TABLE kwave_entities
  ADD COLUMN IF NOT EXISTS occupation_qids   text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS occupation_domain text   NOT NULL DEFAULT '';

COMMENT ON COLUMN kwave_entities.occupation_qids IS
  '위키데이터 P106(occupation) QID 원자료. 영역 판정의 근거이며 규칙이 바뀌어도 다시 판정할 수 있다.';
COMMENT ON COLUMN kwave_entities.occupation_domain IS
  '직업 영역: entertainment·sports·politics·business·academia·media·arts. 빈 문자열 = 판정하지 않음(D-37).';

-- 소비자가 영역으로 거른다. 빈 문자열(미판정)은 색인에서 뺀다 — 대다수가 그 값이라
-- 색인에 넣으면 커지기만 하고 안 쓰인다.
CREATE INDEX IF NOT EXISTS kwave_entities_occupation_domain_idx
  ON kwave_entities (occupation_domain, entity_type)
  WHERE occupation_domain <> '';

COMMIT;
