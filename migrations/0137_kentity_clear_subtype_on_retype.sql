-- 0137: 유형이 바뀌었는데 세부유형이 낡은 채 남았다.
--
-- 운영 실측(2026-09-15). 사람인데 drama 로 분류된 행 15건의 유형을 person 으로 되돌리려
-- 했더니 전부 이렇게 막혔다.
--
--   ERROR: insert or update on table "kentity_entities" violates foreign key
--          constraint "kentity_entities_subtype_fk" (SQLSTATE 23503)
--
-- 원인: kentity 쪽 행이 `entity_type='work', subtype='drama'` 였다. 유형만 person 으로
-- 바뀌면 `(person, drama)` 가 되는데 그런 세부유형은 사전에 없다.
--
-- ★FK 가 제 일을 했다. 잘못된 짝을 만들지 않고 거절했고 트랜잭션은 통째로 되돌아갔다.
--   104건은 깨끗이 들어갔고 15건만 거부됐다 — 반쯤 적용된 상태가 없다.
--   막은 것이 결함이 아니라, **세부유형을 안 지운 것**이 결함이다.
--
-- 세부유형은 **그 유형 안에서만 뜻이 있다.** 유형이 바뀌면 옛 세부유형은 뜻을 잃는다.
-- 그러니 유형이 바뀔 때 NULL 로 지운다. `person` 에 세부유형이 하나(`real`) 있긴 하지만
-- 자동으로 넣지 않는다 — 세부유형 확정은 `classification_evidence_id` 를 요구하는
-- 별개의 절차다(0123 kentity_entities_verified_classification). 반쯤 해두지 않는다.
--
-- 규모: kdb 소유 중 subtype 이 찬 행 1,476건(전부 work). 앞으로 유형을 고칠 때마다
--       같은 자리에서 막힌다.

BEGIN;

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
 -- ★유형이 바뀌면 옛 세부유형은 뜻을 잃는다. 남겨 두면 (person, drama) 같은 짝이 생겨
 --   kentity_entities_subtype_fk 에 막히고, 막히지 않았다면 거짓 분류가 남는다.
 subtype = CASE WHEN EXCLUDED.entity_type IS DISTINCT FROM kentity_entities.entity_type
                THEN NULL ELSE kentity_entities.subtype END,
 revision=kentity_entities.revision+1,updated_at=now()
 WHERE kentity_entities.write_owner='kdb' AND (kentity_entities.entity_type,kentity_entities.canonical_ko,kentity_entities.status,kentity_entities.operator_locked,kentity_entities.qualifier_ko)
 IS DISTINCT FROM (EXCLUDED.entity_type,EXCLUDED.canonical_ko,EXCLUDED.status,EXCLUDED.operator_locked,EXCLUDED.qualifier_ko);
 RETURN NEW;
END $sync$;

-- 불변식: 지금 원장에 (유형, 세부유형)이 사전에 없는 짝이 있으면 안 된다.
-- FK 가 이미 강제하지만, 이 파일이 그 성질을 건드리므로 여기서 한 번 확인하고 넘어간다.
DO $chk$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM kentity_entities k
   WHERE k.subtype IS NOT NULL
     AND NOT EXISTS (SELECT 1 FROM kentity_subtypes s
                      WHERE s.entity_type = k.entity_type AND s.code = k.subtype);
  IF n <> 0 THEN
    RAISE EXCEPTION '사전에 없는 (유형, 세부유형) 짝이 %건 있다', n;
  END IF;
END $chk$;

COMMIT;
