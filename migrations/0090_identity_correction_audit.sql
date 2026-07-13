-- 0090: immutable snapshots for provider/entity resource type corrections.

CREATE TABLE IF NOT EXISTS kwave_kdb_identity_correction_audit (
  id                        bigserial PRIMARY KEY,
  entity_id                 uuid NOT NULL,
  canonical_ko              text NOT NULL,
  entity_type               text NOT NULL,
  provider                  text NOT NULL,
  external_id               text NOT NULL,
  external_url              text NOT NULL,
  external_confidence       numeric,
  external_raw_payload      jsonb,
  external_fetched_at       timestamptz,
  old_status                text NOT NULL,
  old_confidence            numeric,
  old_verification_tier     text,
  old_verification_evidence text,
  old_notes                 text,
  action                    text NOT NULL,
  reason                    text NOT NULL,
  corrected_at              timestamptz NOT NULL DEFAULT now(),
  UNIQUE (entity_id, provider, external_id, action)
);

CREATE INDEX IF NOT EXISTS kwave_kdb_identity_correction_entity_idx
  ON kwave_kdb_identity_correction_audit (entity_id, corrected_at DESC);
