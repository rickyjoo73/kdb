-- TDB 원본은 유지하고 공통 UUID 연결의 검수/철회를 기록한다.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='15s';
ALTER TABLE kentity_tdb_shadows ADD source_type text NOT NULL DEFAULT '';
ALTER TABLE kentity_tdb_shadows ADD manual_rechecks int NOT NULL DEFAULT 0;
ALTER TABLE kentity_tdb_shadows ADD last_manual_recheck_at timestamptz;
ALTER TABLE kentity_crosswalks ADD source_binding_id uuid REFERENCES kentity_tdb_shadows(id);
ALTER TABLE kentity_crosswalks ADD source_generation bigint NOT NULL DEFAULT 0;
ALTER TABLE kentity_crosswalks ADD target_revision bigint NOT NULL DEFAULT 0;
ALTER TABLE kentity_crosswalks ADD CONSTRAINT kentity_tdb_binding_required CHECK(source_system<>'tdb' OR source_binding_id IS NOT NULL);
CREATE INDEX kentity_crosswalk_binding ON kentity_crosswalks(source_binding_id) WHERE source_binding_id IS NOT NULL;
CREATE FUNCTION kentity_invalidate_tdb_binding() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE link kentity_crosswalks%ROWTYPE;
BEGIN
 IF NEW.source_fingerprint=OLD.source_fingerprint AND NEW.source_locked=OLD.source_locked AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 FOR link IN SELECT * FROM kentity_crosswalks WHERE source_system='tdb' AND source_binding_id=NEW.id AND status<>'rejected' FOR UPDATE LOOP
  IF link.evidence_id IS NOT NULL THEN UPDATE kentity_evidence SET status='withdrawn' WHERE id=link.evidence_id AND status='verified'; END IF;
  UPDATE kentity_crosswalks SET status='conflict',reason='TDB source binding changed; mapping requires fresh review',revision=revision+1,updated_at=now()
   WHERE source_system='tdb' AND source_id=link.source_id;
  INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value)
   VALUES(link.entity_id,'tdb-source-policy','tdb_mapping_invalidated','Source fingerprint or protection changed',
    jsonb_build_object('status',link.status,'revision',link.revision,'source_id',link.source_id),
    jsonb_build_object('status','conflict','revision',link.revision+1,'shadow_id',NEW.id));
  INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,after_value)
   VALUES(NEW.id,'tdb-source-policy','mapping_invalidated',jsonb_build_object('reason','원본 변경으로 연결 재검토 필요','revision',link.revision+1));
 END LOOP;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_tdb_binding_invalidation AFTER UPDATE OF source_fingerprint,source_locked,generation ON kentity_tdb_shadows
 FOR EACH ROW EXECUTE FUNCTION kentity_invalidate_tdb_binding();
COMMIT;
