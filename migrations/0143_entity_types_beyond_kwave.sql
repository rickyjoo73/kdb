-- 0143: 정치·경제·시사·스포츠를 받으려면 **그것들을 담을 유형**이 있어야 한다.
--
-- ★운영자 지시 (2026-09-15): "KDB가 정치·경제·스포츠까지 받게 되면 정당·정부기관·
--   기업·선수 같은 유형 목록을 모두 준비해야지. 그래야 문서도 업데이트하고."
--
-- ★지금 유형은 12종이고 전부 K-wave 를 전제한다:
--     person · group · show · drama · movie · song_album · agency
--     channel_outlet · brand_place · event_tour · character · term · unknown
--
--   그래서 종합지 기사의 고유명사가 이렇게 잘못 앉는다:
--     정당            → 담을 칸이 없다
--     정부기관·부처     → 담을 칸이 없다
--     일반 기업        → agency(연예기획사)로 간다
--     프로 구단        → group(아이돌 그룹)으로 간다
--     협회·재단        → 담을 칸이 없다
--     학교·대학        → 담을 칸이 없다
--
-- ★사람은 유형을 안 늘린다. 선수·정치인·기업인은 전부 person 이고, 다른 것은
--   **영역**이지 종류가 아니다(0142 occupation_domain). 유형으로 가르면 배우 겸
--   정치인을 어느 칸에 넣을지 정할 수 없고, 동명이인 가드·병합·앵커 감사가 새
--   유형마다 갈라진다.
--
-- ★조직은 다르다. 정당은 person 도 group 도 아니다 — 담을 칸 자체가 없다.
--   그래서 조직·기관만 늘린다.

BEGIN;

ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'political_party';  -- 정당
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'government_body';  -- 부처·청·위원회·공공기관
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'company';          -- 일반 기업(연예기획사 아님)
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'organization';     -- 협회·재단·노조·학회
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'sports_team';      -- 프로 구단·국가대표팀
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'school';           -- 학교·대학

COMMIT;

-- 공통 플랫폼 원장의 유형 접기. kentity_legacy_type 은 이미 company·organization 을
-- 알고 있었다(0116) — KDB 쪽에 그 유형이 없었을 뿐이다. 새 유형을 제자리에 접는다.
--
-- ★0136 본문 위에 얹는다. 그 판본이 character·term 을 따로 접고 brand_place 를
--   **일부러 뺐다**(상표와 장소가 섞여 행별 근거가 필요하다). 다시 쓰면서 그 결정을
--   지우면 안 된다.
--
-- ★새 enum 값은 **같은 트랜잭션에서 쓸 수 없다**(PG 규칙). 그래서 위 COMMIT 뒤에 온다.
BEGIN;

CREATE OR REPLACE FUNCTION kentity_legacy_type(t text) RETURNS text LANGUAGE sql IMMUTABLE AS $lt$
 SELECT CASE
   WHEN t = 'person' THEN 'person'
   -- ★새 조직 유형을 organization 에 얹는다. 나머지 분기는 0136 본문 그대로다 —
   --   character·term 매핑과 brand_place 를 **일부러 뺀 것**을 건드리지 않는다.
   WHEN t IN ('group','agency','organization','channel_outlet',
              'political_party','government_body','sports_team','school') THEN 'organization'
   WHEN t IN ('company','location','event','product') THEN t
   WHEN t = 'character'  THEN 'character'
   WHEN t = 'event_tour' THEN 'event'
   WHEN t = 'term'       THEN 'concept'
   WHEN t = 'unknown'    THEN 'unknown'
   -- brand_place 는 의도적으로 여기 없다(0136 주석). 상표와 장소가 섞여 있어 행별 근거가 필요하다.
   ELSE 'work' END
$lt$;

COMMIT;
