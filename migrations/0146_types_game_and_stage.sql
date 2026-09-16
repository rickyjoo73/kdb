-- 0146: 범위밖 더미에서 **실제로 들어오는 것**을 보고 유형을 연다.
--
-- ★운영자 지시 (2026-09-15): "out_of_scope 라고 된 내용을 모두 찾아서 분류체계를
--   만들어야 되지 않을까?" / "게임쪽은 playstore 또는 게임사이트에서 찾아야 될 듯한데."
--
-- ★추측하지 않고 기각 더미를 읽었다. `term` 으로 들어와 "일반어"로 죽은 것들이다:
--
--   게임      리니지 · 리니지W · 아이온2 · 마비노기 모바일 · 블루 아카이브 · 붉은사막
--             승리의 여신: 니케 · 브라운더스트2 · 씰M · 서든 어택 제로 포인트 · P의 거짓
--             나 혼자만 레벨업: 카르마 · 그랑사가 · 데스티니 차일드 · 미르의 전설2
--   뮤지컬     레 미제라블 · 노트르담 드 파리 · 몬테크리스토 · 스위니토드 · 아이다
--             안나 카레니나 · 베니스의 상인 · 브로드웨이 42번가
--   웹툰      복학왕 · 설마! 하숙생이 미녀라고요?
--   잡지      쎄씨 · 뷰티쁠 · 디 에디션
--
--   90개 표본에서 게임이 가장 많았다. 담을 칸이 없어 전부 `term`(일반어)으로 갔다.
--
-- ★게임에는 권위 출처가 있다. 위키데이터에는 거의 없지만 **앱스토어에는 퍼블리셔가
--   직접 등록한 나라별 공식 제목**이 있다 — 우리가 찾는 바로 그 값이다:
--     블루 아카이브 → jp ブルーアーカイブ · us Blue Archive · tw 蔚藍檔案
--   internal/kdb/appstore 가 그 출처다.
--
-- ★뮤지컬·연극을 event_tour 로 두지 않는 이유. `event_tour` 는 **그 공연 회차**이고
--   `musical_play` 는 **작품 자체**다. 레 미제라블은 매년 다시 올라가는 작품이지
--   한 번의 행사가 아니다. 드라마(작품)와 방영(회차)을 안 섞는 것과 같다.

BEGIN;

ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'game';          -- 게임(모바일·PC·콘솔)
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'musical_play';  -- 뮤지컬·연극 (작품)
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'webtoon';       -- 웹툰·웹소설·만화
ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'publication';   -- 잡지·도서

COMMIT;

-- 새 enum 값은 같은 트랜잭션에서 쓸 수 없다(PG 규칙). 그래서 아래가 따로 온다.
BEGIN;

-- 공통 플랫폼 원장의 유형 접기. 넷 다 **작품**이다 — 0136 본문 위에 얹는다.
CREATE OR REPLACE FUNCTION kentity_legacy_type(t text) RETURNS text LANGUAGE sql IMMUTABLE AS $lt$
 SELECT CASE
   WHEN t = 'person' THEN 'person'
   WHEN t IN ('group','agency','organization','channel_outlet',
              'political_party','government_body','sports_team','school') THEN 'organization'
   WHEN t IN ('company','location','event','product') THEN t
   WHEN t = 'character'  THEN 'character'
   WHEN t = 'event_tour' THEN 'event'
   WHEN t = 'term'       THEN 'concept'
   WHEN t = 'unknown'    THEN 'unknown'
   -- game·musical_play·webtoon·publication 은 전부 작품이라 아래 ELSE 로 간다.
   -- brand_place 는 의도적으로 여기 없다(0136 주석). 상표와 장소가 섞여 있어 행별 근거가 필요하다.
   ELSE 'work' END
$lt$;

COMMIT;
