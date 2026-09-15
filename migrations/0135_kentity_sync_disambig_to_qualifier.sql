-- 0135: 원장의 구분값(disambig)이 서비스 표까지 안 내려왔다.
--
-- 운영 실측(2026-09-15). 소비자가 이름으로 `한혜진` 을 찾으면 세 건이 나오는데
-- 셋 다 구분값이 비어 있어 **응답만 보고는 누가 누구인지 가릴 수가 없었다.**
--
--   한혜진 | 구분= '' | 33fef12f | candidate native
--   한혜진 | 구분= '' | a94de1ec | active kdb      ← 원장엔 disambig='(Esteem)'
--   한혜진 | 구분= '' | 001b11bc | active kdb
--
-- 값이 없어서가 아니다. `kwave_entities.disambig` 에는 있었다. 문제는
-- kentity_sync_legacy_identity() 가 (entity_type, canonical_ko, status,
-- operator_locked) **넷만** 투영하고 disambig 를 안 실어 날랐다는 것이다.
-- write_owner='kdb' 행은 kentity_guard_legacy_owner 가 트리거 밖 쓰기를 막으므로
-- (I04: 낡은 정체성은 원 KDB 작성자의 것) 나중에 UPDATE 로 메울 수도 없었다.
-- 실제로 시도했다 — `legacy identity belongs to original KDB writer` 로 막혔다.
-- 그러니 메우는 자리는 투영 그 자체다.
--
-- 규모: kdb 소유 19,671행 중 구분값을 가진 것 0건. 원장에 disambig 가 있는 493건.
--       native 537,841행은 이미 496,539건이 차 있다(0134 이전 승격분). 겹침 0건.
--
-- D-04 §16: qualifier_ko 는 **표시 한정어이지 정체성 키가 아니다.** 붙는다고 ID 가
-- 갈리지 않고 같다고 합쳐지지도 않는다. 이 마이그레이션은 누가 누구인지를 바꾸지
-- 않는다 — 이미 우리가 알고 있던 것을 화면까지 내려보낼 뿐이다.

-- (1) 정규화를 한 곳에 둔다. 트리거와 소급분이 서로 다른 규칙을 쓰면 같은 행이
--     언제 만들어졌느냐에 따라 다르게 보인다.
--     · `(가수)` → `가수`   원장은 괄호를 쓰지만 기존 496,539건은 전부 맨값이다.
--     · `(가수) [merged→<uuid>]` → `가수`
--       ★이 `[merged→]` 는 표시용 문자열에 박아 넣은 병합 표식이다(31건, 전부
--        rejected). 제대로 된 자리는 kentity_redirects 인데 그쪽은 0건이다.
--        여기서는 표시값에서 걷어내기만 한다. 병합 자체는 이 파일의 일이 아니다.
CREATE OR REPLACE FUNCTION kentity_normalize_disambig(src text)
RETURNS text LANGUAGE sql IMMUTABLE AS $nd$
  SELECT NULLIF(left(btrim(regexp_replace(
           regexp_replace(COALESCE(src, ''), '\s*\[merged[^\]]*\]\s*$', ''),
           '^\((.*)\)$', '\1')), 100), '')
$nd$;

-- (2) 투영에 구분값을 더한다.
--     ★조기 반환 조건에도 disambig 를 넣어야 한다. 안 넣으면 구분값만 바뀐 UPDATE 가
--       "달라진 게 없다"로 판정돼 되돌아가고, 투영은 영원히 일어나지 않는다.
CREATE OR REPLACE FUNCTION kentity_sync_legacy_identity() RETURNS trigger LANGUAGE plpgsql AS $sync$
BEGIN
 IF TG_OP='DELETE' THEN
  UPDATE kentity_entities SET status='retired',revision=revision+1,updated_at=now() WHERE id=OLD.id AND write_owner='kdb';
  RETURN OLD;
 END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND origin_system<>'kdb') THEN
  RAISE EXCEPTION 'Entity UUID belongs to another source';
 END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND write_owner<>'kdb') THEN
  IF NEW.operator_locked THEN UPDATE kentity_entities SET operator_locked=true,revision=revision+1,updated_at=now() WHERE id=NEW.id AND NOT operator_locked; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='UPDATE' AND (OLD.entity_type,OLD.canonical_ko,OLD.status,OLD.operator_locked,kentity_normalize_disambig(OLD.disambig))
 IS NOT DISTINCT FROM (NEW.entity_type,NEW.canonical_ko,NEW.status,NEW.operator_locked,kentity_normalize_disambig(NEW.disambig)) THEN RETURN NEW; END IF;
 -- subtype 은 더 이상 투영하지 않는다(D-16). 목표 사전 코드만 들어가야 하며 선매핑은 P2.02.
 INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,write_owner,status,operator_locked,qualifier_ko,created_at,updated_at)
 VALUES(NEW.id,kentity_legacy_type(NEW.entity_type::text),NEW.canonical_ko,'kdb','kdb',
 CASE WHEN NEW.status IN ('active','candidate','rejected') THEN NEW.status ELSE 'retired' END,NEW.operator_locked,
 kentity_normalize_disambig(NEW.disambig),NEW.created_at,NEW.updated_at)
 ON CONFLICT(id) DO UPDATE SET entity_type=EXCLUDED.entity_type,canonical_ko=EXCLUDED.canonical_ko,
 status=EXCLUDED.status,operator_locked=EXCLUDED.operator_locked,qualifier_ko=EXCLUDED.qualifier_ko,
 revision=kentity_entities.revision+1,updated_at=now()
 WHERE kentity_entities.write_owner='kdb' AND (kentity_entities.entity_type,kentity_entities.canonical_ko,kentity_entities.status,kentity_entities.operator_locked,kentity_entities.qualifier_ko)
 IS DISTINCT FROM (EXCLUDED.entity_type,EXCLUDED.canonical_ko,EXCLUDED.status,EXCLUDED.operator_locked,EXCLUDED.qualifier_ko);
 RETURN NEW;
END $sync$;

-- (3) 이미 들어와 있는 행에 소급한다.
--     0123 이 같은 자리에서 쓴 방식 그대로다 — 구조 변경은 migration 의 정당한 일이므로
--     이 UPDATE 동안만 가드를 끈다. **런타임 pool 에는 이 권한이 없다**(WRITER_AUTHORITY A01).
--     조건에 write_owner='kdb' 를 명시해 흡수분(native) 의 기존 구분값은 건드리지 않는다.
ALTER TABLE kentity_entities DISABLE TRIGGER kentity_legacy_owner;
UPDATE kentity_entities k
   SET qualifier_ko = kentity_normalize_disambig(w.disambig),
       revision     = k.revision + 1,
       updated_at   = now()
  FROM kwave_entities w
 WHERE w.id = k.id
   AND k.write_owner = 'kdb'
   AND COALESCE(k.qualifier_ko,'') = ''
   AND kentity_normalize_disambig(w.disambig) IS NOT NULL;
ALTER TABLE kentity_entities ENABLE TRIGGER kentity_legacy_owner;

-- (4) 불변식. 구분값은 정체성 키가 아니므로 행수도, 누가 누구인지도 그대로여야 한다.
DO $chk$
DECLARE remaining bigint; touched_native bigint;
BEGIN
  SELECT count(*) INTO remaining
    FROM kentity_entities k JOIN kwave_entities w ON w.id = k.id
   WHERE k.write_owner = 'kdb' AND COALESCE(k.qualifier_ko,'') = ''
     AND kentity_normalize_disambig(w.disambig) IS NOT NULL;
  IF remaining <> 0 THEN
    RAISE EXCEPTION '구분값을 못 내려보낸 kdb 행이 %건 남았다', remaining;
  END IF;
  SELECT count(*) INTO touched_native
    FROM kentity_entities WHERE write_owner <> 'kdb' AND COALESCE(qualifier_ko,'') <> '';
  IF touched_native < 496539 THEN
    RAISE EXCEPTION '흡수분 구분값이 줄었다 (% < 496539)', touched_native;
  END IF;
END $chk$;
