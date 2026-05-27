-- 0051_kdb_can_replace_function.sql — replace decision helper.
--
-- internal/kdb/source_priority.go::ShouldReplace 와 1:1 정합.
-- cascade UPDATE 의 CASE WHEN 가독성 향상.

BEGIN;

CREATE OR REPLACE FUNCTION can_replace_canonical(
  is_locked       boolean,
  current_source  text,
  incoming_source text
) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
  SELECT
    NOT is_locked
    AND current_source NOT IN ('operator-locked', 'operator')
    AND kdb_source_priority(incoming_source) < kdb_source_priority(current_source)
$$;

COMMENT ON FUNCTION can_replace_canonical(boolean, text, text) IS
  'cascade UPDATE 의 priority 가드. operator-locked 보존 + 새 source priority 가 더 높을 (낮은 숫자) 때만 true.';

COMMIT;
