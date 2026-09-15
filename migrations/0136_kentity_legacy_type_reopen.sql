-- 0136: 서비스 표에 **배역이라는 유형 자체가 없었다.**
--
-- 운영 실측(2026-09-15). `kentity_legacy_type()` 는 명시 목록 밖을 전부 `work` 로 접는다.
--   CASE WHEN t='person' … WHEN t IN ('company','location','event','product') THEN t
--        WHEN t='unknown' THEN 'unknown' ELSE 'work' END
-- 이 함수는 0116 에서 만들어졌고, 그 뒤 0127·0129 가 kentity 사전에 `character`·`brand`
-- 를 열었는데 **함수가 안 따라왔다.** 사전 13코드는 전부 enabled 인데 투영은 7개만 쓴다.
--
-- 활성 기준:
--   character       462 → work    ← 배역을 작품이라고 내보내고 있었다
--   event_tour      581 → work
--   channel_outlet  282 → work
--   term             11 → work
--   brand_place     267 → work    ← 이 파일은 **안 건드린다**. 아래 이유.
--
-- ★실제로 바뀌는 행은 1,336 이 아니라 **3,946** 이다. 투영은 상태를 가리지 않으므로
--   candidate·rejected 도 같이 맞춘다(원장과 서비스 표가 상태별로 갈리면 안 된다).
--   가장 큰 덩어리는 rejected 인 term 1,970건이다. 활성 1,336 · 기각 2,504 · 후보 106.
--
-- ★brand_place 를 왜 그대로 두는가.
--   표본이 섞여 있다: 현대자동차·페리페라·애니플러스(상표)와 북촌한옥마을·호포항·
--   롯데콘서트홀(장소)이 한 유형에 있다. brand 로 옮기면 한옥마을이 상표가 되고,
--   location 으로 옮기면 페리페라가 장소가 된다. **틀린 답을 다른 틀린 답으로 바꾸는 것은
--   진전이 아니다.** unknown 으로 내리는 것도 안 된다 — guardTyped 가 unknown 을 공급에서
--   빼므로 멀쩡한 것까지 서비스에서 사라진다. 행별 근거가 필요하다(별도 항목).
--
-- ★안전 확인(실측). 바뀔 **3,946건 전부** — 활성분만 재고 일반화하지 않았다.
--   · subtype 이 NULL 이다 → kentity_entities_subtype_fk 걸리지 않는다
--     (UPDATE 조건에도 subtype IS NULL 을 넣어, 세부유형이 찬 행은 아예 건드리지 않는다)
--   · classification_status 가 전부 'pending' → verified_classification CHECK 걸리지 않는다
--   · 유형고정 FK(kentity_relations · person_profiles · location_profiles) 참조 0건
--   · 목표 유형 character·event·organization·concept 은 사전에 enabled=true

BEGIN;

CREATE OR REPLACE FUNCTION kentity_legacy_type(t text) RETURNS text LANGUAGE sql IMMUTABLE AS $lt$
 SELECT CASE
   WHEN t = 'person' THEN 'person'
   WHEN t IN ('group','agency','organization','channel_outlet') THEN 'organization'
   WHEN t IN ('company','location','event','product') THEN t
   WHEN t = 'character'  THEN 'character'
   WHEN t = 'event_tour' THEN 'event'
   WHEN t = 'term'       THEN 'concept'
   WHEN t = 'unknown'    THEN 'unknown'
   -- brand_place 는 의도적으로 여기 없다(위 주석). 상표와 장소가 섞여 있어 행별 근거가 필요하다.
   ELSE 'work' END
$lt$;

-- 소급 전 상태. 불변식은 이 값과만 견준다(0135 에서 고정 숫자를 박았다가 걸렸다).
CREATE TEMP TABLE kentity_m0136_before ON COMMIT DROP AS
SELECT (SELECT count(*) FROM kentity_entities) AS total_before,
       (SELECT count(*) FROM kentity_entities WHERE write_owner <> 'kdb') AS native_before,
       (SELECT count(*) FROM kentity_entities WHERE write_owner = 'kdb' AND entity_type = 'person') AS kdb_person_before;

-- 이미 들어와 있는 행에 소급한다. 0123·0135 와 같은 방식 — 구조 변경은 migration 의
-- 정당한 일이므로 이 UPDATE 동안만 가드를 끈다. 런타임 pool 에는 이 권한이 없다(A01).
ALTER TABLE kentity_entities DISABLE TRIGGER kentity_legacy_owner;
UPDATE kentity_entities k
   SET entity_type = kentity_legacy_type(w.entity_type::text),
       revision    = k.revision + 1,
       updated_at  = now()
  FROM kwave_entities w
 WHERE w.id = k.id
   AND k.write_owner = 'kdb'
   AND k.subtype IS NULL
   AND k.entity_type <> kentity_legacy_type(w.entity_type::text);
ALTER TABLE kentity_entities ENABLE TRIGGER kentity_legacy_owner;

DO $chk$
DECLARE n bigint; b record;
BEGIN
  SELECT * INTO b FROM kentity_m0136_before;

  SELECT count(*) INTO n FROM kentity_entities;
  IF n <> b.total_before THEN
    RAISE EXCEPTION '행수가 %에서 %로 바뀌었다 — 유형 투영은 대상을 만들거나 없애지 않는다', b.total_before, n;
  END IF;

  SELECT count(*) INTO n FROM kentity_entities WHERE write_owner <> 'kdb';
  IF n <> b.native_before THEN
    RAISE EXCEPTION '흡수분 행수가 바뀌었다 (% → %) — 이 파일은 kdb 소유만 건드려야 한다', b.native_before, n;
  END IF;

  SELECT count(*) INTO n FROM kentity_entities WHERE write_owner = 'kdb' AND entity_type = 'person';
  IF n <> b.kdb_person_before THEN
    RAISE EXCEPTION 'kdb person 이 %에서 %로 바뀌었다 — 사람은 이 매핑에서 움직이지 않는다', b.kdb_person_before, n;
  END IF;

  -- 투영이 끝났으면 어긋난 행이 없어야 한다(subtype 이 찬 행은 대상이 아니므로 제외).
  SELECT count(*) INTO n
    FROM kentity_entities k JOIN kwave_entities w ON w.id = k.id
   WHERE k.write_owner = 'kdb' AND k.subtype IS NULL
     AND k.entity_type <> kentity_legacy_type(w.entity_type::text);
  IF n <> 0 THEN
    RAISE EXCEPTION '유형이 원장과 어긋난 kdb 행이 %건 남았다', n;
  END IF;
END $chk$;

COMMIT;   -- kentity_m0136_before 는 ON COMMIT DROP
