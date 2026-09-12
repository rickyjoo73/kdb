-- 미지 Entity의 외부 조사 원장. 검색 결과를 공식 표기/동일인 승인으로 오인하지 않는다.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='15s';
CREATE TABLE kentity_resolution_jobs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),entity_id uuid NOT NULL REFERENCES kentity_entities(id),
 entity_revision bigint NOT NULL,query_ko text NOT NULL,entity_type text NOT NULL,
 policy_version text NOT NULL DEFAULT 'candidate-research-v1',
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','review','approved','no_match','blocked','failed','cancelled')),
 attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 4),generation bigint NOT NULL DEFAULT 0,
 lease_token uuid,lease_until timestamptz,next_attempt_at timestamptz DEFAULT now(),
 reason text NOT NULL DEFAULT 'new candidate awaiting source research',
 result jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(result)='array'),
 requested_by text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(entity_id,entity_revision,policy_version),
 CHECK(state<>'running' OR (lease_token IS NOT NULL AND lease_until IS NOT NULL))
);
CREATE INDEX kentity_resolution_due ON kentity_resolution_jobs(next_attempt_at,created_at) WHERE state IN ('pending','failed','running');

-- New common identities and the legacy writer must serialize on the same ID.
-- Existing legacy duplicate claims remain reviewable; new native approvals may
-- not take an ID already claimed in legacy or assigned to another common UUID.
CREATE TABLE kentity_id_reservations (
 provider text NOT NULL,external_id text NOT NULL,native_owner uuid REFERENCES kentity_entities(id),
 PRIMARY KEY(provider,external_id)
);
CREATE FUNCTION kentity_reserve_legacy_external_id() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE owner_id uuid;
BEGIN
 IF NEW.provider<>'wikidata' OR NEW.external_id IS NULL OR NEW.external_id='' THEN RETURN NEW; END IF;
 INSERT INTO kentity_id_reservations(provider,external_id) VALUES(NEW.provider,NEW.external_id)
 ON CONFLICT(provider,external_id) DO UPDATE SET external_id=EXCLUDED.external_id
 RETURNING native_owner INTO owner_id;
 IF owner_id IS NOT NULL AND owner_id<>NEW.entity_id THEN
  RAISE EXCEPTION 'external ID already reserved by a common Entity; identity review required';
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_legacy_external_id_reservation BEFORE INSERT OR UPDATE ON kwave_entity_external_refs
 FOR EACH ROW EXECUTE FUNCTION kentity_reserve_legacy_external_id();

-- The same source can be reviewed again after withdrawal. Keep observations
-- separate instead of rewriting an earlier evidence record's provenance.
ALTER TABLE kentity_evidence DROP CONSTRAINT kentity_evidence_entity_id_provider_source_record_id_source_key;
CREATE INDEX kentity_evidence_source_lookup ON kentity_evidence(entity_id,provider,source_record_id);
CREATE FUNCTION kentity_guard_evidence_observation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status='verified' AND NEW.status='verified' AND
 (OLD.entity_id,OLD.provider,OLD.source_record_id,OLD.source_url,OLD.claim_type,OLD.license_code,OLD.observed_at,OLD.verified_by,OLD.verified_at,OLD.summary)
 IS DISTINCT FROM
 (NEW.entity_id,NEW.provider,NEW.source_record_id,NEW.source_url,NEW.claim_type,NEW.license_code,NEW.observed_at,NEW.verified_by,NEW.verified_at,NEW.summary)
 THEN RAISE EXCEPTION 'approved evidence is immutable; withdraw and record a new observation'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_evidence_observation BEFORE UPDATE ON kentity_evidence
 FOR EACH ROW EXECUTE FUNCTION kentity_guard_evidence_observation();
CREATE FUNCTION kentity_invalidate_withdrawn_evidence() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF OLD.status='verified' AND OLD.export_allowed AND (NEW.status<>'verified' OR NOT NEW.export_allowed) THEN
  UPDATE kentity_names SET status='blocked',revision=revision+1,updated_at=now() WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_external_ids SET status='withdrawn' WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_entities e SET status='candidate',revision=revision+1,updated_at=now()
   WHERE e.id=NEW.entity_id AND e.origin_system<>'kdb' AND e.status='active' AND NOT EXISTS (
    SELECT 1 FROM kentity_evidence v WHERE v.entity_id=e.id AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed);
  INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value)
   VALUES(NEW.entity_id,'source-policy','evidence_invalidated','verified evidence withdrawn or reuse permission removed',jsonb_build_object('evidence_id',NEW.id));
 END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_evidence_invalidation AFTER UPDATE OF status,export_allowed ON kentity_evidence
 FOR EACH ROW EXECUTE FUNCTION kentity_invalidate_withdrawn_evidence();
COMMIT;
