-- 0141: 역·정류장 898건이 `unknown` 이라 영원히 공급될 수 없었다.
--
-- 소비자 신고(2026-09-15): "`익산역` 을 찾았는데 GS25 익산역점·정관장 익산역점만 나오고
-- 역 본체가 안 잡힌다."
--
-- 원장을 보니 역들은 **있었다.** 다만 전부 이렇게 앉아 있었다:
--
--   강남역  entity_type=unknown  status=candidate
--     en `Gangnam Station` · ja `カンナム駅` · zh `江南站`   ← 표기는 이미 다 있다
--   classification_reason:
--     "P3 확대: TDB transit 선매핑(tdb-premap-v1). **구분값 미상** — 검수 대기(지어내지 않음)"
--
-- `guardTyped` 가 `entity_type <> 'unknown'` 이므로 이 행들은 **공급 대상이 될 수 없다.**
-- 표기를 다 갖고도 소비자에게 한 번도 나갈 수 없는 상태로 898건이 있었다.
--
-- ★흡수기가 두 가지를 섞었다.
--     "어느 강남역인가"(구분값)를 모르는 것과
--     "그것이 장소인가"(유형)를 모르는 것은 **다른 문제다.**
--   구분값을 못 정했다고 유형까지 미상으로 둘 이유가 없다. 지어내지 않겠다는 원칙은
--   옳았지만, 적용된 자리가 틀렸다 — 유형은 지어낸 것이 아니라 **출처가 말한 것**이다.
--
-- ★근거는 둘이고 둘 다 출처 쪽이다.
--   ① `kentity_crosswalks.source_table = 'tdb_places'` — **장소 표**에서 왔다
--   ② `classification_reason` 에 TDB 의 **transit 선매핑** 판정이 적혀 있다
--   장소 표 안의 transit 레코드는 역·정류장이고, 역은 장소다.
--   (외국어 표기까지 보면 226건은 en `Station` · ja `駅` · zh `站` 셋이 다 일치한다.
--    나머지는 한국어 이름에 '역' 접미가 없는 것들이다 — `여의도`·`산본`·`평택`.
--    이름 모양은 근거로 쓰지 않는다. 출처가 이미 말했다.)
--
-- ★유형만 바꾼다. status 는 candidate 그대로다 — 공급 활성화는 나머지 가드
--   (자체 ID 근거 · 외국어 표기 · 동명 함정)를 각자 통과해야 한다. 이 파일은
--   **유형 게이트에 걸려 있던 것만 푼다.** 구분값은 여전히 미상이고 지어내지 않는다.

BEGIN;

CREATE TEMP TABLE kentity_m0141_target ON COMMIT DROP AS
SELECT DISTINCT e.id
  FROM kentity_entities e
  JOIN kentity_crosswalks c ON c.entity_id = e.id
 WHERE e.entity_type = 'unknown'
   AND e.write_owner <> 'kdb'
   AND e.subtype IS NULL
   AND c.source_table = 'tdb_places'
   AND e.classification_reason LIKE '%TDB transit 선매핑%';

CREATE TEMP TABLE kentity_m0141_before ON COMMIT DROP AS
SELECT (SELECT count(*) FROM kentity_entities) AS total_before,
       (SELECT count(*) FROM kentity_m0141_target) AS target,
       (SELECT count(*) FROM kentity_entities WHERE status = 'active') AS active_before;

UPDATE kentity_entities k
   SET entity_type = 'location',
       classification_reason = COALESCE(k.classification_reason,'')
         || ' / 0141: 출처가 장소 표(tdb_places)의 transit 레코드라고 말한다 — 유형만 확정. 구분값은 여전히 미상',
       revision = k.revision + 1,
       updated_at = now()
 WHERE k.id IN (SELECT id FROM kentity_m0141_target);

DO $chk$
DECLARE n bigint; b record;
BEGIN
  SELECT * INTO b FROM kentity_m0141_before;

  SELECT count(*) INTO n FROM kentity_entities;
  IF n <> b.total_before THEN
    RAISE EXCEPTION '행수가 %에서 %로 바뀌었다 — 유형 확정은 대상을 만들거나 없애지 않는다', b.total_before, n;
  END IF;

  -- ★활성 수가 늘면 안 된다. 이 파일은 유형 게이트만 푼다 — 공급 활성화가 아니다.
  SELECT count(*) INTO n FROM kentity_entities WHERE status = 'active';
  IF n <> b.active_before THEN
    RAISE EXCEPTION '활성 수가 %에서 %로 바뀌었다 — 이 파일은 status 를 건드리지 않는다', b.active_before, n;
  END IF;

  SELECT count(*) INTO n
    FROM kentity_entities e JOIN kentity_crosswalks c ON c.entity_id = e.id
   WHERE e.entity_type = 'unknown' AND e.write_owner <> 'kdb' AND e.subtype IS NULL
     AND c.source_table = 'tdb_places' AND e.classification_reason LIKE '%TDB transit 선매핑%';
  IF n <> 0 THEN
    RAISE EXCEPTION 'transit 선매핑분 %건이 아직 unknown 이다', n;
  END IF;

  RAISE NOTICE '0141: transit 선매핑 %건을 location 으로 확정(구분값은 미상 유지)', b.target;
END $chk$;

COMMIT;
