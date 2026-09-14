-- correct_unpinned_source_claims.sql — 근거가 **아는 것만 말하게** 한다 (P4.03 선결 ⑶)
--
-- 무엇이 문제인가. P4.03-a 감사 실측:
--   흡수분의 표기 근거 **65,487건**이 `provider='wikidata'` · `license='CC0-1.0'` ·
--   `independent_origin='wikidata'` 라고 적혀 있는데, `source_record_id` 는 QID 가 아니라
--   **TDB/우리 UUID** 이고 `source_url` 은 우리 admin 페이지다.
--   "위키데이터가 이 표기를 말했다"는 주장에 **어느 항목인지가 없다.**
--   이 근거가 받치는 표기는 134,615건, 대상은 34,101건이다.
--
-- 원본에도 없다. TDB 인물 31,378명 중 16,685명은 wikidata 링크가 **아예 없는데**
-- 전원이 `source_code='wikidata'` 표기를 갖고 있고, `tdb_src_records` 에 wikidata
-- 원본이 보관돼 있지 않아 QID 를 되찾을 곳이 없다.
--
-- ★왜 조회로 해결하지 않는가. 15,092건을 위키데이터에 물어 확인하는 길도 있고 실제로
--   4,000건까지 돌려 봤다(표본 300건 OK 299). 그런데 ① 대량 조회는 차단 위험이 있고
--   ② 16,685명은 조회할 QID 자체가 없다. **우리가 아는 것으로 먼저 정리한다.**
--
-- 무엇으로 바꾸는가. 우리가 실제로 아는 것은 이것이다:
--   "TDB 가 자기 레코드에 이 표기를 갖고 있었고, 출처를 위키데이터라고 적어 두었다.
--    어느 항목인지는 원본에 남아 있지 않다."
--   그래서 provider 를 **TDB 자신**으로 바꾼다. TDB 는 운영자 소유이고
--   원천 정책이 승인돼 있다(`internal-operator-owned`, `name_export_allowed=true`).
--   TDB 가 적어 둔 출처 주장은 **지우지 않고** claim_payload 에 남긴다 —
--   나중에 항목을 확인하면 그때 다시 wikidata 로 올린다.
--
-- 하지 않는 것
--   · 표기를 만들거나 지우지 않는다. 값은 그대로다.
--   · **항목을 제대로 지목한 근거는 건드리지 않는다**(실측 919건). 그건 이미 참이다.
--   · 대상을 활성화하지 않는다. 활성화는 activate_absorbed_supply.sql 의 몫이다.
--   · 라이선스를 넓히지 않는다. CC0 → internal-operator-owned 는 **좁히는** 쪽이다.
--
-- ★되돌릴 수 있다. 바꾸기 전 값을 claim_payload 에 통째로 남기므로
--   나중에 항목이 확인되면 recover_wikidata_item_identity.sql 이 다시 올린다.
--
-- 실행: psql -v ON_ERROR_STOP=1 -v batch=20000 -f correct_unpinned_source_claims.sql
--       -v dry=1 을 주면 되돌린다.

\set ON_ERROR_STOP on
\if :{?batch}
\else
  \set batch 20000
\endif
\if :{?dry}
\else
  \set dry 0
\endif

SET lock_timeout = '5s';
SET statement_timeout = '900s';

BEGIN;

-- ① 교정 대상 — 외부 항목을 말하면서 그 항목을 지목하지 못하는 표기 근거.
--    항목 출처 집합을 손으로 적지 않는다: kentity_external_ids 가 정체성을 정의하는
--    출처가 곧 그것이다(activate_absorbed_supply.sql 의 가드와 같은 도출).
CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT v.id, gen_random_uuid() AS new_id, v.entity_id, v.provider AS old_provider, v.license_code AS old_license,
       v.source_record_id AS old_record, v.source_url AS old_url,
       v.independent_origin AS old_origin, v.source_policy_id AS old_policy
  FROM kentity_evidence v
  JOIN kentity_entities e ON e.id = v.entity_id AND e.write_owner = 'native'
 WHERE v.claim_type = 'name'
   -- ★`status='verified' AND export_allowed` 가 없으면 **이미 철회한 근거를 다시 잡는다.**
   --   그러면 같은 옛 근거에서 같은 새 행을 또 만들어 kentity_evidence_claim_key 에
   --   부딪힌다(시연에서 실제로 났다). 멱등이 아니면 배치로 돌릴 수 없다.
   --   가드도 verified+export_allowed 만 보므로 철회분은 애초에 막지 않는다.
   AND v.status = 'verified' AND v.export_allowed
   AND v.provider IN (SELECT DISTINCT provider FROM kentity_external_ids)
   AND NOT EXISTS (SELECT 1 FROM kentity_external_ids x
                    WHERE x.entity_id = v.entity_id AND x.provider = v.provider
                      AND x.status = 'verified' AND x.external_id = v.source_record_id)
 ORDER BY v.id
 LIMIT :batch;

DO $$ BEGIN
  IF (SELECT count(*) FROM tgt) = 0 THEN
    RAISE NOTICE '교정할 근거가 없다 — 이미 전부 정리됐거나 대상이 아니다.';
  END IF;
END $$;

-- ★승인된 근거는 **불변**이다. 처음엔 UPDATE 로 provider 를 바꾸려다 트리거에 막혔다:
--     'approved evidence is immutable; withdraw and record a new observation'
--   계약이 맞다. 지난 관측의 출처를 덧칠하지 않고 **철회하고 새 관측을 기록**한다.
--   순서가 중요하다 — 표기를 새 근거로 옮긴 **뒤에** 옛 근거를 철회해야 한다.
--   먼저 철회하면 kentity_invalidate_withdrawn_evidence 가 그 표기를 blocked 로 내린다.

-- ② 새 관측을 기록한다. 우리가 실제로 아는 것만 적는다.
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, observed_at, summary,
   claim_fingerprint, source_observation_hash, independent_origin, source_policy_id, claim_payload)
SELECT t.new_id, t.entity_id, 'tdb', t.old_record, t.old_url, 'name',
       'internal-operator-owned', true, 'verified', 'policy:unpinned-source-claim-v1', now(), now(),
       'TDB 가 자기 레코드에 갖고 있던 표기. TDB 는 출처를 ' || t.old_provider
         || ' 라고 적어 두었으나 **어느 항목인지는 원본에 없다** — 확인되지 않은 출처 주장을 공급 근거로 쓰지 않는다.',
       md5(t.old_record || '|name|tdb|' || t.id::text),
       md5(t.id::text || '|corrected'),
       'tdb',
       (SELECT id FROM kentity_source_policies WHERE provider = 'tdb' AND status = 'approved'
         ORDER BY created_at DESC LIMIT 1),
       jsonb_build_object(
         'correction', 'unpinned-source-claim-v1',
         'tdb_claimed_source', t.old_provider,
         'item_identified', false,
         'withdrawn_evidence_id', t.id,
         'before', jsonb_build_object(
           'provider', t.old_provider, 'license_code', t.old_license,
           'source_record_id', t.old_record, 'source_url', t.old_url,
           'independent_origin', t.old_origin,
           'source_policy_id', coalesce(t.old_policy::text, '')))
  FROM tgt t;

-- ③ 표기를 새 근거로 옮긴다. 값은 건드리지 않는다.
--    source_code 도 맞춘다 — 소비자 응답의 `source` 가 이 값이라, 항목을 못 대면서
--    'wikidata' 로 보이면 같은 과장을 반복하는 것이다.
UPDATE kentity_names n
   SET evidence_id = t.new_id,
       source_code = 'tdb',
       revision    = n.revision + 1,
       updated_at  = now()
  FROM tgt t
 WHERE n.evidence_id = t.id AND n.entity_id = t.entity_id;

-- ④ 옛 관측을 철회한다. 지우지 않는다 — 무엇을 믿었는지가 기록으로 남아야 한다.
UPDATE kentity_evidence v
   SET status = 'withdrawn', export_allowed = false
  FROM tgt t
 WHERE v.id = t.id;

-- ⑤ 감사 기록 — 대상 단위로 한 줄.
INSERT INTO kentity_audit_events (entity_id, actor, action, reason, before_value, after_value)
SELECT t.entity_id, 'policy:unpinned-source-claim-v1', 'source_claim_corrected',
       '외부 출처를 말하면서 항목을 지목하지 못하는 표기 근거를 철회하고 TDB 자체 관측으로 다시 기록했다. 표기 값은 바꾸지 않았다.',
       jsonb_build_object('provider', min(t.old_provider), 'license_code', min(t.old_license)),
       jsonb_build_object('provider', 'tdb', 'license_code', 'internal-operator-owned',
                          'evidence_rows', count(*), 'item_identified', false)
  FROM tgt t
 GROUP BY t.entity_id;

SELECT ' 교정한 근거' AS 구분, count(*) AS 수 FROM tgt
UNION ALL SELECT ' 영향받은 대상', count(DISTINCT entity_id) FROM tgt
UNION ALL SELECT ' 남은 미지목 근거',
  -- ★`status='verified'` 를 빼면 방금 철회한 것까지 세어 "하나도 안 줄었다"로 보인다.
  --   가드는 verified+export_allowed 만 보므로 철회분은 이미 막지 않는다.
  (SELECT count(*) FROM kentity_evidence v JOIN kentity_entities e ON e.id=v.entity_id AND e.write_owner='native'
    WHERE v.claim_type='name' AND v.status='verified' AND v.export_allowed
      AND v.provider IN (SELECT DISTINCT provider FROM kentity_external_ids)
      AND NOT EXISTS (SELECT 1 FROM kentity_external_ids x WHERE x.entity_id=v.entity_id AND x.provider=v.provider
                        AND x.status='verified' AND x.external_id=v.source_record_id));

\if :dry
  \echo '>>> dry=1 — 되돌린다. 아무것도 바뀌지 않았다.'
  ROLLBACK;
\else
  COMMIT;
\endif

-- ────────────────────────── 되돌리기 ──────────────────────────
-- 옛 관측이 `status='withdrawn'` 으로 그대로 남아 있고, 새 근거의 claim_payload 에
-- `withdrawn_evidence_id` 로 연결돼 있다. 순서를 지킨다 — 표기를 먼저 옮긴다.
--
--   BEGIN;
--   UPDATE kentity_evidence SET status='verified', export_allowed=true
--    WHERE id IN (SELECT (claim_payload->>'withdrawn_evidence_id')::uuid FROM kentity_evidence
--                  WHERE claim_payload->>'correction' = 'unpinned-source-claim-v1');
--   UPDATE kentity_names n SET evidence_id = (v.claim_payload->>'withdrawn_evidence_id')::uuid,
--          source_code = v.claim_payload->'before'->>'provider',
--          revision = n.revision+1, updated_at = now()
--     FROM kentity_evidence v
--    WHERE n.evidence_id = v.id AND v.claim_payload->>'correction' = 'unpinned-source-claim-v1';
--   DELETE FROM kentity_evidence WHERE claim_payload->>'correction' = 'unpinned-source-claim-v1';
--   COMMIT;
--
-- ★되돌리기는 반드시 격리 사본에서 실행해 확인하고 쓴다.
