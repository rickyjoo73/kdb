-- 0150: 분류군 앵커 — 학명을 원장에 적는다 (2026-09-20).
--
-- ★왜 (실측). 번역 쪽에서 다섯 건이 한꺼번에 틀렸다:
--
--     전어     zh 鲭鱼(고등어)       실제 窩斑鰶
--     광어     zh 平鱼(병어)         실제 扁口鱼   (표준 한국명 넙치)
--     우럭     zh 石首鱼(조기)       실제 許氏平鮋 (표준 한국명 조피볼락)
--     도라지   es bellota(도토리)    실제 Platycodon grandiflorus
--     거위벌레 ja ゴキブリ(바퀴벌레) 실제 オトシブミ科
--
--   전부 **뜻을 옮겨서** 생긴 오류다. 그런데 우리가 소비자에게 주는 지시는 둘뿐이고
--   (`transliterate` 소리 · `translate_title` 뜻), 종명이 들어오는 유형 `term` 은
--   TitleTypes 에 있어 **`translate_title`(뜻을 옮겨라)이 나간다.** 우리가 틀린 지시를
--   보내고 있었다. 소리로 옮겨도 틀리므로 유형을 옮겨서는 풀리지 않는다.
--
-- ★그래서 유형이 아니라 **대상 자체의 사실**을 적는다. 이 대상이 분류군이면 학명이
--   있다(위키데이터 P225). 학명이 적혀 있으면 지시는 `use_standard_name` 이 된다 —
--   "소리도 뜻도 아니다. 그 언어의 표준 통용명을 써라. 없으면 학명을 그대로 둬라."
--
--   유형 표를 건드리지 않는 이유: `term` 에는 분류군이 아닌 것도 들어 있다. 유형 전체를
--   옮기면 그것들의 지시가 같이 틀린다. 학명은 **그 행에 대한 관측**이라 틀릴 여지가 없다.
--
-- ★값은 채우지 않는다. 이 마이그레이션은 칸만 연다. 채우는 것은 taxon 레인이
--   위키데이터 P225 를 근거로 한 건씩 적는다(internal/kdb/taxon_drain.go).
BEGIN;

ALTER TABLE kwave_entities
  ADD COLUMN IF NOT EXISTS taxon_name text;

COMMENT ON COLUMN kwave_entities.taxon_name IS
  '학명(위키데이터 P225). 값이 있으면 이 대상은 분류군이고, 빈 locale 의 fill_hint 는 use_standard_name 이 된다. 관측값이므로 추정해서 적지 않는다.';

-- 레인이 "이미 앵커가 붙은 것"을 고를 때 쓰는 부분 인덱스. 전체의 극히 일부라 작다.
CREATE INDEX IF NOT EXISTS kwave_entities_taxon_name_idx
  ON kwave_entities (taxon_name)
  WHERE taxon_name IS NOT NULL AND taxon_name <> '';

COMMIT;
