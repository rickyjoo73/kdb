-- TDB 연결 주장의 비교 원장. 기존 DB 삭제·이름 복사·동일인 승인이 아니다.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='15s';
CREATE TABLE kentity_tdb_shadows (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),tdb_id uuid NOT NULL,qid text NOT NULL CHECK(qid~'^Q[1-9][0-9]*$'),
 source_fingerprint text NOT NULL,link_method text NOT NULL,link_score numeric NOT NULL CHECK(link_score BETWEEN 0 AND 1),
 source_observed_at timestamptz NOT NULL,last_seen_at timestamptz NOT NULL DEFAULT now(),
 source_locked boolean NOT NULL DEFAULT false,policy_version text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','review','failed','blocked')),
 attempts int NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 4),generation bigint NOT NULL DEFAULT 0,
 lease_token uuid,lease_until timestamptz,next_attempt_at timestamptz DEFAULT now(),
 result jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(result)='object'),
 reason text NOT NULL DEFAULT '',created_by text NOT NULL,created_at timestamptz NOT NULL DEFAULT now(),updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(tdb_id,qid)
);
CREATE INDEX kentity_tdb_shadow_due ON kentity_tdb_shadows(next_attempt_at,id) WHERE state IN ('pending','running','failed');
CREATE TABLE kentity_tdb_shadow_events (
 id bigserial PRIMARY KEY,shadow_id uuid NOT NULL REFERENCES kentity_tdb_shadows(id),actor text NOT NULL,action text NOT NULL,
 before_value jsonb NOT NULL DEFAULT '{}',after_value jsonb NOT NULL DEFAULT '{}',created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kentity_tdb_shadow_history ON kentity_tdb_shadow_events(shadow_id,id);
COMMIT;
