-- 공통 UUID/이름/분야/관계 레이어. 기존 KDB UUID와 기존 API/worker 쓰기 책임을 보존한다.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='30s';

CREATE TABLE kentity_entities (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 entity_type text NOT NULL CHECK(entity_type IN ('person','organization','company','location','work','team','league','event','product','concept','unknown')),
 subtype text NOT NULL DEFAULT '', canonical_ko text NOT NULL CHECK(btrim(canonical_ko)<>''),
 origin_system text NOT NULL CHECK(origin_system IN ('kdb','native','tdb')),
 status text NOT NULL CHECK(status IN ('active','candidate','rejected','retired')),
 operator_locked boolean NOT NULL DEFAULT false,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kentity_entities_name ON kentity_entities(canonical_ko,entity_type);
CREATE INDEX kentity_entities_status ON kentity_entities(status,updated_at);

CREATE TABLE kentity_domains(code text PRIMARY KEY,label_ko text NOT NULL);
INSERT INTO kentity_domains VALUES ('politics','정치'),('government','행정'),('economy','경제'),('society','사회'),('entertainment','연예'),('sports','스포츠'),('travel','여행'),('culture','문화');
CREATE TABLE kentity_entity_domains (
 entity_id uuid NOT NULL REFERENCES kentity_entities(id),domain text NOT NULL REFERENCES kentity_domains(code),
 assigned_by text NOT NULL,reason text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(entity_id,domain)
);

CREATE TABLE kentity_evidence (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), entity_id uuid NOT NULL REFERENCES kentity_entities(id),
 provider text NOT NULL,source_record_id text NOT NULL DEFAULT '',source_url text NOT NULL,
 claim_type text NOT NULL DEFAULT 'identity' CHECK(claim_type IN ('identity','name','relation')),
 license_code text NOT NULL DEFAULT 'unreviewed',export_allowed boolean NOT NULL DEFAULT false,
 status text NOT NULL DEFAULT 'unverified' CHECK(status IN ('unverified','verified','withdrawn','blocked')),
 observed_at timestamptz NOT NULL DEFAULT now(),verified_by text,verified_at timestamptz,
 summary text NOT NULL DEFAULT '',UNIQUE(entity_id,provider,source_record_id,source_url),UNIQUE(id,entity_id),
 CHECK(NOT export_allowed OR license_code<>'unreviewed'),
 CHECK(status<>'verified' OR (verified_by IS NOT NULL AND verified_at IS NOT NULL))
);

CREATE TABLE kentity_names (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),entity_id uuid NOT NULL REFERENCES kentity_entities(id),
 locale text NOT NULL, value text NOT NULL CHECK(btrim(value)<>''),
 kind text NOT NULL CHECK(kind IN ('canonical','alias','transliteration')),
 form text NOT NULL CHECK(form IN ('recorded','generated','translated','unknown')),
 status text NOT NULL DEFAULT 'unverified' CHECK(status IN ('unverified','verified','withdrawn','blocked')),
 evidence_id uuid, source_code text NOT NULL,
 valid_from date,valid_until date,revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 CHECK(valid_until IS NULL OR valid_from IS NULL OR valid_until>=valid_from),
 CHECK(status<>'verified' OR (evidence_id IS NOT NULL AND form<>'unknown')),
 UNIQUE(entity_id,locale,value,kind,source_code),
 FOREIGN KEY(evidence_id,entity_id) REFERENCES kentity_evidence(id,entity_id)
);
CREATE INDEX kentity_names_lookup ON kentity_names(locale,value);
CREATE UNIQUE INDEX kentity_names_current_canonical ON kentity_names(entity_id,locale)
 WHERE kind='canonical' AND status='verified' AND valid_until IS NULL;

CREATE TABLE kentity_relations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),subject_id uuid NOT NULL REFERENCES kentity_entities(id),
 predicate text NOT NULL,object_id uuid NOT NULL REFERENCES kentity_entities(id),role_label text NOT NULL DEFAULT '',
 valid_from date,valid_until date,evidence_id uuid,
 status text NOT NULL DEFAULT 'unverified' CHECK(status IN ('unverified','verified','withdrawn')),
 CHECK(subject_id<>object_id),CHECK(valid_until IS NULL OR valid_from IS NULL OR valid_until>=valid_from),
 CHECK(status<>'verified' OR evidence_id IS NOT NULL),
 FOREIGN KEY(evidence_id,subject_id) REFERENCES kentity_evidence(id,entity_id)
);
CREATE INDEX kentity_relations_subject ON kentity_relations(subject_id,predicate,valid_from);
CREATE INDEX kentity_relations_object ON kentity_relations(object_id,predicate,valid_from);

CREATE TABLE kentity_external_ids (
 entity_id uuid NOT NULL REFERENCES kentity_entities(id),provider text NOT NULL,external_id text NOT NULL,
 status text NOT NULL DEFAULT 'unverified' CHECK(status IN ('unverified','verified','conflict','withdrawn')),
 evidence_id uuid,PRIMARY KEY(entity_id,provider,external_id),
 FOREIGN KEY(evidence_id,entity_id) REFERENCES kentity_evidence(id,entity_id),
 CHECK(status<>'verified' OR evidence_id IS NOT NULL)
);
CREATE INDEX kentity_external_ids_claims ON kentity_external_ids(provider,external_id);
CREATE UNIQUE INDEX kentity_external_ids_verified ON kentity_external_ids(provider,external_id) WHERE status='verified';

CREATE TABLE kentity_crosswalks (
 source_system text NOT NULL,source_id text NOT NULL,entity_id uuid REFERENCES kentity_entities(id),
 status text NOT NULL DEFAULT 'review' CHECK(status IN ('review','confirmed','conflict','rejected')),
 candidate_ids uuid[] NOT NULL DEFAULT '{}',reason text NOT NULL, evidence_id uuid,
 decided_by text,decided_at timestamptz,source_fingerprint text NOT NULL DEFAULT '',
 revision bigint NOT NULL DEFAULT 1,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(source_system,source_id),
 CHECK(status<>'confirmed' OR (entity_id IS NOT NULL AND evidence_id IS NOT NULL AND decided_by IS NOT NULL AND decided_at IS NOT NULL)),
 FOREIGN KEY(evidence_id,entity_id) REFERENCES kentity_evidence(id,entity_id)
);
CREATE INDEX kentity_crosswalks_review ON kentity_crosswalks(status,source_system);
CREATE TABLE kentity_audit_events (
 id bigserial PRIMARY KEY,entity_id uuid REFERENCES kentity_entities(id),actor text NOT NULL,
 action text NOT NULL,reason text NOT NULL,before_value jsonb NOT NULL DEFAULT '{}',after_value jsonb NOT NULL DEFAULT '{}',
 created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kentity_audit_entity ON kentity_audit_events(entity_id,id);
CREATE TABLE kentity_candidate_requests (
 owner_key text NOT NULL,request_key text NOT NULL,payload_hash text NOT NULL,
 entity_id uuid NOT NULL REFERENCES kentity_entities(id) DEFERRABLE INITIALLY DEFERRED,
 created_at timestamptz NOT NULL DEFAULT now(),PRIMARY KEY(owner_key,request_key)
);

CREATE FUNCTION kentity_guard_legacy_owner() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' AND OLD.origin_system='kdb' THEN RAISE EXCEPTION 'legacy identity cannot be deleted through common layer'; END IF;
 IF TG_OP<>'DELETE' AND NEW.origin_system='kdb' AND pg_trigger_depth()<2 THEN
  RAISE EXCEPTION 'legacy identity is owned by existing KDB writer';
 END IF;
 IF TG_OP='UPDATE' AND NEW.origin_system<>OLD.origin_system THEN RAISE EXCEPTION 'source ownership transfer requires migration'; END IF;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF;
 IF NEW.origin_system<>'kdb' AND NEW.status='active' AND NOT EXISTS (
  SELECT 1 FROM kentity_evidence WHERE entity_id=NEW.id AND claim_type='identity' AND status='verified' AND export_allowed
 ) THEN RAISE EXCEPTION 'native activation requires verified reusable identity evidence'; END IF;
 RETURN NEW;
END $$;

CREATE FUNCTION kentity_guard_verified_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.entity_id AND origin_system='kdb') THEN
  RAISE EXCEPTION 'legacy names are owned by the original KDB writer';
 END IF;
 IF NEW.status='verified' AND NOT EXISTS(SELECT 1 FROM kentity_evidence
  WHERE id=NEW.evidence_id AND entity_id=NEW.entity_id AND status='verified' AND export_allowed
 ) THEN RAISE EXCEPTION 'name requires verified reusable evidence'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_verified_name BEFORE INSERT OR UPDATE ON kentity_names
 FOR EACH ROW EXECUTE FUNCTION kentity_guard_verified_name();

CREATE FUNCTION kentity_legacy_type(t text) RETURNS text LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN t='person' THEN 'person' WHEN t IN ('group','agency','organization') THEN 'organization'
 WHEN t IN ('company','location','event','product') THEN t WHEN t='unknown' THEN 'unknown' ELSE 'work' END
$$;

-- KDB 언어값은 복사본을 다시 쓰지 않고 live view로 읽는다. 출처 라벨을 검증으로 승격하지 않는다.
CREATE VIEW kentity_name_catalog AS
 SELECT e.id AS entity_id,n.locale,n.value,'canonical'::text AS kind,'unknown'::text AS form,
 'legacy'::text AS verification_status,n.source_code,NULL::uuid AS evidence_id,'kdb'::text AS write_owner
 FROM kwave_entities e CROSS JOIN LATERAL (VALUES
 ('ko',e.canonical_ko,'legacy-kdb'),('en',e.canonical_en,e.canonical_en_source),
 ('ja',e.canonical_ja,e.canonical_ja_source),('vi',e.canonical_vi,e.canonical_vi_source),
 ('id',e.canonical_id,e.canonical_id_source),('es',e.canonical_es,e.canonical_es_source),
 ('pt-BR',e.canonical_pt_br,e.canonical_pt_br_source),('zh-Hans',e.canonical_zh,e.canonical_zh_source),
 ('zh-Hant',e.canonical_zh_hant,e.canonical_zh_hant_source)) n(locale,value,source_code)
 WHERE COALESCE(n.value,'')<>''
 UNION ALL
 SELECT n.entity_id,n.locale,n.value,n.kind,n.form,n.status,n.source_code,n.evidence_id,e.origin_system
 FROM kentity_names n JOIN kentity_entities e ON e.id=n.entity_id WHERE e.origin_system<>'kdb';

CREATE FUNCTION kentity_sync_legacy_identity() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='DELETE' THEN
  UPDATE kentity_entities SET status='retired',revision=revision+1,updated_at=now() WHERE id=OLD.id AND origin_system='kdb';
  RETURN OLD;
 END IF;
 IF TG_OP='UPDATE' AND (OLD.entity_type,OLD.canonical_ko,OLD.status,OLD.operator_locked)
 IS NOT DISTINCT FROM (NEW.entity_type,NEW.canonical_ko,NEW.status,NEW.operator_locked) THEN RETURN NEW; END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND origin_system<>'kdb') THEN
  RAISE EXCEPTION 'Entity UUID belongs to another source';
 END IF;
 INSERT INTO kentity_entities(id,entity_type,subtype,canonical_ko,origin_system,status,operator_locked,created_at,updated_at)
 VALUES(NEW.id,kentity_legacy_type(NEW.entity_type::text),NEW.entity_type::text,NEW.canonical_ko,'kdb',
 CASE WHEN NEW.status IN ('active','candidate','rejected') THEN NEW.status ELSE 'retired' END,NEW.operator_locked,NEW.created_at,NEW.updated_at)
 ON CONFLICT(id) DO UPDATE SET entity_type=EXCLUDED.entity_type,subtype=EXCLUDED.subtype,canonical_ko=EXCLUDED.canonical_ko,
 status=EXCLUDED.status,operator_locked=EXCLUDED.operator_locked,revision=kentity_entities.revision+1,updated_at=now()
 WHERE (kentity_entities.entity_type,kentity_entities.subtype,kentity_entities.canonical_ko,kentity_entities.status,kentity_entities.operator_locked)
 IS DISTINCT FROM (EXCLUDED.entity_type,EXCLUDED.subtype,EXCLUDED.canonical_ko,EXCLUDED.status,EXCLUDED.operator_locked);
 RETURN NEW;
END $$;
CREATE TRIGGER kentity_legacy_identity AFTER INSERT OR UPDATE OR DELETE ON kwave_entities
 FOR EACH ROW EXECUTE FUNCTION kentity_sync_legacy_identity();

INSERT INTO kentity_entities(id,entity_type,subtype,canonical_ko,origin_system,status,operator_locked,created_at,updated_at)
 SELECT id,kentity_legacy_type(entity_type::text),entity_type::text,canonical_ko,'kdb',
 CASE WHEN status IN ('active','candidate','rejected') THEN status ELSE 'retired' END,operator_locked,created_at,updated_at FROM kwave_entities;

-- 이름은 매핑 키가 아니다. 자동 confirmed 없이 후보 UUID 전체를 검수 자료로 보관한다.
INSERT INTO kentity_crosswalks(source_system,source_id,candidate_ids,reason)
 SELECT 'legacy_person',p.id::text,COALESCE((SELECT array_agg(e.id ORDER BY e.id) FROM kwave_entities e
 WHERE e.entity_type='person' AND e.status IN ('active','candidate') AND e.canonical_ko=p.name_ko),'{}'::uuid[]),
 '이름 일치 후보일 뿐 동일인 확정이 아님. 외부 식별자와 프로필 근거 검수 필요.' FROM kwave_persons p;

-- Backfill 완료 뒤 설치한다. 이후 legacy 투영 트리거만 legacy identity를 쓸 수 있다.
CREATE TRIGGER kentity_legacy_owner BEFORE INSERT OR UPDATE OR DELETE ON kentity_entities
 FOR EACH ROW EXECUTE FUNCTION kentity_guard_legacy_owner();

COMMIT;
