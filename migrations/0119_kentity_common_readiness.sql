-- Common ready is a reviewed exact-locale observation, not a catalog hit.
BEGIN;
SET LOCAL lock_timeout='2s';
SET LOCAL statement_timeout='15s';
ALTER TABLE kentity_locale_readiness ADD proof jsonb NOT NULL DEFAULT '{}' CHECK(jsonb_typeof(proof)='object');
COMMENT ON COLUMN kentity_locale_readiness.proof IS 'Versioned common name/identity evidence IDs. Readiness is revalidated on common API reads; this is not an automatic publication grant.';
COMMIT;
