-- 검수된 정체성에 의존하는 자동 이름 근거. 정체성 철회를 이름까지 전파한다.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='15s';
CREATE TABLE kentity_evidence_dependencies (
 evidence_id uuid PRIMARY KEY,entity_id uuid NOT NULL,depends_on_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),CHECK(evidence_id<>depends_on_id),
 FOREIGN KEY(evidence_id,entity_id) REFERENCES kentity_evidence(id,entity_id),
 FOREIGN KEY(depends_on_id,entity_id) REFERENCES kentity_evidence(id,entity_id)
);
CREATE INDEX kentity_evidence_parent ON kentity_evidence_dependencies(depends_on_id);
CREATE FUNCTION kentity_guard_evidence_dependency() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' THEN RAISE EXCEPTION 'evidence dependency is immutable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM kentity_evidence p JOIN kentity_evidence c ON c.entity_id=p.entity_id
 WHERE p.id=NEW.depends_on_id AND c.id=NEW.evidence_id AND p.entity_id=NEW.entity_id
 AND p.claim_type='identity' AND c.claim_type='name' AND p.status='verified' AND p.export_allowed
 AND c.status='verified' AND c.export_allowed)
 THEN RAISE EXCEPTION 'name dependency requires reusable verified identity of the same Entity'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_evidence_dependency_guard BEFORE INSERT OR UPDATE ON kentity_evidence_dependencies
 FOR EACH ROW EXECUTE FUNCTION kentity_guard_evidence_dependency();
CREATE FUNCTION kentity_invalidate_dependent_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status='verified' AND OLD.export_allowed AND (NEW.status<>'verified' OR NOT NEW.export_allowed) THEN
  UPDATE kentity_evidence v SET status='blocked',export_allowed=false
  WHERE v.id IN(SELECT evidence_id FROM kentity_evidence_dependencies WHERE depends_on_id=NEW.id)
  AND v.status='verified';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_dependent_evidence_invalidation AFTER UPDATE OF status,export_allowed ON kentity_evidence
 FOR EACH ROW EXECUTE FUNCTION kentity_invalidate_dependent_evidence();
CREATE FUNCTION kentity_guard_automatic_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.status='verified' AND EXISTS(SELECT 1 FROM kentity_evidence WHERE id=NEW.evidence_id AND verified_by='policy:common-anchored-fill-v1')
 AND NOT EXISTS(SELECT 1 FROM kentity_evidence_dependencies d JOIN kentity_evidence p ON p.id=d.depends_on_id
 WHERE d.evidence_id=NEW.evidence_id AND d.entity_id=NEW.entity_id AND p.status='verified' AND p.export_allowed)
 THEN RAISE EXCEPTION 'automatic name requires its current reviewed identity dependency'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_automatic_name_guard BEFORE INSERT OR UPDATE ON kentity_names
 FOR EACH ROW EXECUTE FUNCTION kentity_guard_automatic_name();
COMMIT;
