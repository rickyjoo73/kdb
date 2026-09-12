-- 출생 원천(origin)과 현재 쓰기 책임(owner)을 분리한다. 기존 UUID는 바꾸지 않는다.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='30s';
ALTER TABLE kentity_entities ADD write_owner text NOT NULL DEFAULT 'kdb' CHECK(write_owner IN ('kdb','native','tdb'));
UPDATE kentity_entities SET write_owner=origin_system WHERE origin_system<>'kdb';
ALTER TABLE kentity_entities ALTER write_owner SET DEFAULT 'native';

CREATE OR REPLACE FUNCTION kentity_guard_legacy_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  IF OLD.origin_system='kdb' THEN RAISE EXCEPTION 'legacy-origin identity UUID must be preserved'; END IF;
  RETURN OLD;
 END IF;
 IF TG_OP='UPDATE' AND NEW.origin_system<>OLD.origin_system THEN RAISE EXCEPTION 'identity origin is immutable'; END IF;
 IF TG_OP='UPDATE' AND NEW.write_owner<>OLD.write_owner AND pg_trigger_depth()<2 THEN
  RAISE EXCEPTION 'write ownership requires an audited transition';
 END IF;
 IF NEW.write_owner='kdb' AND pg_trigger_depth()<2 THEN RAISE EXCEPTION 'legacy identity belongs to original KDB writer'; END IF;
 IF NEW.write_owner<>'kdb' AND NEW.status='active' AND NOT EXISTS(
  SELECT 1 FROM kentity_evidence WHERE entity_id=NEW.id AND claim_type='identity' AND status='verified' AND export_allowed
 ) THEN RAISE EXCEPTION 'activation requires verified reusable identity evidence'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION kentity_guard_verified_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.entity_id AND write_owner='kdb') THEN
  RAISE EXCEPTION 'legacy names belong to original KDB writer';
 END IF;
 IF NEW.status='verified' AND NOT EXISTS(SELECT 1 FROM kentity_evidence WHERE id=NEW.evidence_id AND entity_id=NEW.entity_id AND status='verified' AND export_allowed)
 THEN RAISE EXCEPTION 'name requires verified reusable evidence'; END IF;
 RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION kentity_sync_legacy_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  UPDATE kentity_entities SET status='retired',revision=revision+1,updated_at=now() WHERE id=OLD.id AND write_owner='kdb';
  RETURN OLD;
 END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND origin_system<>'kdb') THEN
  RAISE EXCEPTION 'Entity UUID belongs to another source';
 END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND write_owner<>'kdb') THEN
  -- A lock from the preserved legacy record remains a safety stop. Unlocking
  -- it must not silently release a lock set independently in the common DB.
  IF NEW.operator_locked THEN UPDATE kentity_entities SET operator_locked=true,revision=revision+1,updated_at=now() WHERE id=NEW.id AND NOT operator_locked; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='UPDATE' AND (OLD.entity_type,OLD.canonical_ko,OLD.status,OLD.operator_locked)
 IS NOT DISTINCT FROM (NEW.entity_type,NEW.canonical_ko,NEW.status,NEW.operator_locked) THEN RETURN NEW; END IF;
 INSERT INTO kentity_entities(id,entity_type,subtype,canonical_ko,origin_system,write_owner,status,operator_locked,created_at,updated_at)
 VALUES(NEW.id,kentity_legacy_type(NEW.entity_type::text),NEW.entity_type::text,NEW.canonical_ko,'kdb','kdb',
 CASE WHEN NEW.status IN ('active','candidate','rejected') THEN NEW.status ELSE 'retired' END,NEW.operator_locked,NEW.created_at,NEW.updated_at)
 ON CONFLICT(id) DO UPDATE SET entity_type=EXCLUDED.entity_type,subtype=EXCLUDED.subtype,canonical_ko=EXCLUDED.canonical_ko,
 status=EXCLUDED.status,operator_locked=EXCLUDED.operator_locked,revision=kentity_entities.revision+1,updated_at=now()
 WHERE kentity_entities.write_owner='kdb' AND (kentity_entities.entity_type,kentity_entities.subtype,kentity_entities.canonical_ko,kentity_entities.status,kentity_entities.operator_locked)
 IS DISTINCT FROM (EXCLUDED.entity_type,EXCLUDED.subtype,EXCLUDED.canonical_ko,EXCLUDED.status,EXCLUDED.operator_locked);
 RETURN NEW;
END $$;

CREATE OR REPLACE VIEW kentity_name_catalog AS
 SELECT e.id AS entity_id,n.locale,n.value,'canonical'::text AS kind,'unknown'::text AS form,
 'legacy'::text AS verification_status,n.source_code,NULL::uuid AS evidence_id,'kdb'::text AS write_owner
 FROM kwave_entities e JOIN kentity_entities c ON c.id=e.id AND c.write_owner='kdb'
 CROSS JOIN LATERAL (VALUES
 ('ko',e.canonical_ko,'legacy-kdb'),('en',e.canonical_en,e.canonical_en_source),
 ('ja',e.canonical_ja,e.canonical_ja_source),('vi',e.canonical_vi,e.canonical_vi_source),
 ('id',e.canonical_id,e.canonical_id_source),('es',e.canonical_es,e.canonical_es_source),
 ('pt-BR',e.canonical_pt_br,e.canonical_pt_br_source),('zh-Hans',e.canonical_zh,e.canonical_zh_source),
 ('zh-Hant',e.canonical_zh_hant,e.canonical_zh_hant_source)) n(locale,value,source_code)
 WHERE COALESCE(n.value,'')<>''
 UNION ALL
 SELECT n.entity_id,n.locale,n.value,n.kind,n.form,n.status,n.source_code,n.evidence_id,c.write_owner
 FROM kentity_names n JOIN kentity_entities c ON c.id=n.entity_id WHERE c.write_owner<>'kdb';

CREATE TABLE kentity_ownership_decisions (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),entity_id uuid NOT NULL REFERENCES kentity_entities(id),
 expected_revision bigint NOT NULL,source_fingerprint text NOT NULL,
 selected_type text NOT NULL,domains text[] NOT NULL,
 actor text NOT NULL CHECK(btrim(actor)<>''),reason text NOT NULL CHECK(length(btrim(reason))>=10),
 source_url text NOT NULL CHECK(source_url LIKE 'https://%'),created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(entity_id)
);
CREATE FUNCTION kentity_adopt_rejected_legacy() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE old_source kwave_entities%ROWTYPE; old_core kentity_entities%ROWTYPE; d text; old_domains jsonb;
BEGIN
 SELECT * INTO STRICT old_source FROM kwave_entities WHERE id=NEW.entity_id FOR UPDATE;
 SELECT * INTO STRICT old_core FROM kentity_entities WHERE id=NEW.entity_id FOR UPDATE;
 IF old_source.status<>'rejected' OR old_core.status<>'rejected' OR old_source.operator_locked OR old_core.operator_locked OR old_core.write_owner<>'kdb'
 OR old_core.revision<>NEW.expected_revision OR md5(to_jsonb(old_source)::text)<>NEW.source_fingerprint
 THEN RAISE EXCEPTION 'legacy transition input or lock changed'; END IF;
 IF cardinality(NEW.domains)<1 OR cardinality(NEW.domains)>8 THEN RAISE EXCEPTION 'transition requires reviewed domains'; END IF;
 SELECT COALESCE(jsonb_agg(domain ORDER BY domain),'[]'::jsonb) INTO old_domains FROM kentity_entity_domains WHERE entity_id=NEW.entity_id;
 UPDATE kentity_entities SET write_owner='native',status='candidate',entity_type=NEW.selected_type,subtype='',revision=revision+1,updated_at=now() WHERE id=NEW.entity_id;
 DELETE FROM kentity_entity_domains WHERE entity_id=NEW.entity_id;
 FOREACH d IN ARRAY NEW.domains LOOP
  INSERT INTO kentity_entity_domains(entity_id,domain,assigned_by,reason) VALUES(NEW.entity_id,d,NEW.actor,NEW.reason)
  ON CONFLICT(entity_id,domain) DO UPDATE SET assigned_by=EXCLUDED.assigned_by,reason=EXCLUDED.reason;
 END LOOP;
 INSERT INTO kentity_names(entity_id,locale,value,kind,form,source_code) VALUES(NEW.entity_id,'ko',old_core.canonical_ko,'canonical','unknown','legacy-scope-review');
 INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value)
 VALUES(NEW.entity_id,NEW.actor,'legacy_scope_adopted',NEW.reason,
  jsonb_build_object('write_owner','kdb','status',old_core.status,'revision',old_core.revision,'entity_type',old_core.entity_type,'subtype',old_core.subtype,'domains',old_domains),
  jsonb_build_object('write_owner','native','status','candidate','revision',old_core.revision+1,'origin','kdb','legacy_status_unchanged',true,'source_url',NEW.source_url,'domains',NEW.domains));
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_ownership_decision AFTER INSERT ON kentity_ownership_decisions
 FOR EACH ROW EXECUTE FUNCTION kentity_adopt_rejected_legacy();

CREATE OR REPLACE FUNCTION kentity_reserve_legacy_external_id() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id uuid;
BEGIN
 IF NEW.provider<>'wikidata' OR NEW.external_id IS NULL OR NEW.external_id='' THEN RETURN NEW; END IF;
 INSERT INTO kentity_id_reservations(provider,external_id) VALUES(NEW.provider,NEW.external_id)
 ON CONFLICT(provider,external_id) DO UPDATE SET external_id=EXCLUDED.external_id RETURNING native_owner INTO owner_id;
 IF owner_id IS NOT NULL AND owner_id<>NEW.entity_id THEN RAISE EXCEPTION 'external ID reserved by another common Entity'; END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities c JOIN kentity_external_ids x ON x.entity_id=c.id
  WHERE c.id=NEW.entity_id AND c.write_owner='native' AND x.provider=NEW.provider AND x.status='verified' AND x.external_id<>NEW.external_id)
 THEN RAISE EXCEPTION 'legacy writer cannot replace common verified identity anchor'; END IF;
 RETURN NEW;
END $$;
CREATE OR REPLACE FUNCTION kentity_invalidate_withdrawn_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status='verified' AND OLD.export_allowed AND (NEW.status<>'verified' OR NOT NEW.export_allowed) THEN
  UPDATE kentity_names SET status='blocked',revision=revision+1,updated_at=now() WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_external_ids SET status='withdrawn' WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_entities e SET status='candidate',revision=revision+1,updated_at=now()
   WHERE e.id=NEW.entity_id AND e.write_owner<>'kdb' AND e.status='active' AND NOT EXISTS (
    SELECT 1 FROM kentity_evidence v WHERE v.entity_id=e.id AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed);
  INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value)
   VALUES(NEW.entity_id,'source-policy','evidence_invalidated','verified evidence withdrawn or reuse permission removed',jsonb_build_object('evidence_id',NEW.id));
 END IF;
 RETURN NEW;
END $$;
COMMIT;
