-- 0144: 사람의 성별을 든다 — **추정하지 않고 위키데이터 P21 을 그대로** 적는다.
--
-- ★운영자 지시 (2026-09-15): "사람들 직업 성별도 분류할거니?"
--
-- ★두 군데에 쓴다.
--   ① 동명이인 가름. 이름도 유형도 같은 두 사람을 가르는 신호가 하나 더 생긴다.
--      직업 영역(0142)과 같이 놓으면 소비자가 후보를 보고 고를 수 있다:
--        박찬호 [person · sports · male]   vs   박찬호 [person · entertainment · male]
--   ② 현지 표기. 경칭·호칭이 성별로 갈리는 언어가 있다(es Sr./Sra.).
--
-- ★이름에서 추정하지 않는다. 지민·현우·서연은 다 양성이고, 틀리면 **사람에 대한
--   사실을 잘못 적는 것**이라 표기 오류보다 무겁다. 모르면 빈 문자열이다(D-37).
--
-- ★값이 여럿일 수 있어 원자료를 배열로 남긴다(전환 이력 등). 접은 값은 gender 가 든다.

BEGIN;

ALTER TABLE kwave_entities
  ADD COLUMN IF NOT EXISTS gender_qids text[] NOT NULL DEFAULT '{}',
  ADD COLUMN IF NOT EXISTS gender      text   NOT NULL DEFAULT '';

COMMENT ON COLUMN kwave_entities.gender_qids IS
  '위키데이터 P21(sex or gender) QID 원자료. 여럿일 수 있다(전환 이력). 순서를 재배열하지 않는다.';
COMMENT ON COLUMN kwave_entities.gender IS
  'male · female · other. 빈 문자열 = 판정하지 않음(D-37). 이름에서 추정하지 않는다.';

COMMIT;
