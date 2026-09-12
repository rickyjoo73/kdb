-- 요청의 종료와 언어별 실제 표기 준비를 분리한다. 과거 요청을 소급 추정하지 않는다.
BEGIN;
SET LOCAL lock_timeout = '2s';
SET LOCAL statement_timeout = '30s';

CREATE TABLE kentity_preparations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 owner_key text NOT NULL CHECK(length(owner_key) BETWEEN 1 AND 150),
 idempotency_key text NOT NULL CHECK(length(idempotency_key) BETWEEN 1 AND 200),
 payload_hash text NOT NULL,
 policy_version text NOT NULL,
 requested_locales text[] NOT NULL CHECK(cardinality(requested_locales) BETWEEN 1 AND 16),
 source_url text NOT NULL DEFAULT '', article_id text NOT NULL DEFAULT '', article_version text NOT NULL DEFAULT '',
 status text NOT NULL DEFAULT 'preparing' CHECK(status IN ('preparing','review','ready','cancelled')),
 revision bigint NOT NULL DEFAULT 1,
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 next_check_at timestamptz NOT NULL DEFAULT now(), cancelled_at timestamptz,
 UNIQUE(owner_key,idempotency_key)
);
CREATE INDEX kentity_preparations_due ON kentity_preparations(next_check_at,id) WHERE cancelled_at IS NULL;
CREATE INDEX kentity_preparations_owner ON kentity_preparations(owner_key,created_at DESC);

CREATE TABLE kentity_preparation_items (
 preparation_id uuid NOT NULL REFERENCES kentity_preparations(id) ON DELETE CASCADE,
 ordinal int NOT NULL CHECK(ordinal>=0), term text NOT NULL, entity_type text NOT NULL DEFAULT '',
 context_hint text NOT NULL DEFAULT '', supplied_entity_id uuid, resolved_entity_id uuid, bound_entity_id uuid,
 candidate_ids uuid[] NOT NULL DEFAULT '{}', identity_state text NOT NULL DEFAULT 'pending',
 PRIMARY KEY(preparation_id,ordinal)
);
-- Entity 外部 ID 는 소스별 호환 계층에서 확인한다. 레거시 이름 UNIQUE를 재사용하지 않는다.
CREATE TABLE kentity_locale_readiness (
 preparation_id uuid NOT NULL, ordinal int NOT NULL, locale text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ready','ambiguous','no_evidence','policy_blocked','failed','unverified','cancelled')),
 value text NOT NULL DEFAULT '', source text NOT NULL DEFAULT '', reason text NOT NULL DEFAULT '',
 fallback_value text NOT NULL DEFAULT '', input_fingerprint text NOT NULL DEFAULT '',
 observed_at timestamptz NOT NULL DEFAULT now(), first_ready_at timestamptz, ready_at timestamptz,
 PRIMARY KEY(preparation_id,ordinal,locale),
 FOREIGN KEY(preparation_id,ordinal) REFERENCES kentity_preparation_items(preparation_id,ordinal) ON DELETE CASCADE,
 CHECK((state='ready')=(ready_at IS NOT NULL)), CHECK(state<>'ready' OR value<>'')
);
CREATE TABLE kentity_readiness_events (
 id bigserial PRIMARY KEY, preparation_id uuid NOT NULL REFERENCES kentity_preparations(id) ON DELETE CASCADE,
 ordinal int, locale text NOT NULL DEFAULT '', state text NOT NULL, reason text NOT NULL,
 snapshot jsonb NOT NULL DEFAULT '{}', observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kentity_readiness_events_request ON kentity_readiness_events(preparation_id,id);

CREATE TABLE kentity_locale_fill_jobs (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), entity_id uuid NOT NULL, locale text NOT NULL,
 input_fingerprint text NOT NULL, policy_version text NOT NULL, qid text NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','running','complete','no_evidence','policy_blocked','failed','stale')),
 attempts int NOT NULL DEFAULT 0, generation bigint NOT NULL DEFAULT 0,
 manual_retries int NOT NULL DEFAULT 0, last_manual_retry_at timestamptz,
 lease_token uuid, lease_until timestamptz, next_retry_at timestamptz DEFAULT now(),
 last_reason text NOT NULL DEFAULT '', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(entity_id,locale,input_fingerprint,policy_version)
);
CREATE INDEX kentity_locale_fill_due ON kentity_locale_fill_jobs(next_retry_at,created_at)
 WHERE state IN ('pending','failed','no_evidence','running');

COMMENT ON COLUMN kentity_locale_readiness.first_ready_at IS '원장 관측상 최초 준비 시각. 과거 DB 저장 시각을 추정한 값이 아님.';
COMMENT ON COLUMN kentity_locale_readiness.fallback_value IS '사용 가능한 영문 대체값. 해당 언어의 실제 준비 완료로 세지 않음.';
COMMIT;
