-- promote_absorbed_names.sql — 흡수된 표기를 공급 가능 상태로 올린다 (P4.00)
--
-- 배경. TDB 전량 흡수(P3)가 끝난 시점에 표기 536,079건이 status='unverified',
-- evidence_id IS NULL 로 남아 있었다. 근거가 없으니 kentity_guard_verified_name 이
-- 승격을 막고, 승격이 막히니 소비자에게 공급되는 표기는 242건뿐이었다.
-- 즉 데이터는 다 들어왔는데 쓸 수가 없었다.
--
-- 근거가 없던 이유는 관측이 없어서가 아니다. 흡수 당시 상류 원천의 정책이
-- 승인돼 있지 않아 근거를 'verified + export_allowed' 로 쓸 수 없었기 때문이다.
-- 0128 이 그 정책들을 승인했다(운영자 판단). 이제 관측 사실을 근거로 적는다.
--
-- 무엇을 적는가 — D-37 의 교훈을 지킨다: **관측하지 않은 값을 지어내지 않는다.**
--   · source_record_id / source_url / source_observation_hash
--       → 흡수 때 관측자가 실제로 기록한 identity 근거에서 그대로 가져온다.
--         우리가 읽은 레코드는 그것이고, 그 표기는 같은 관측에서 나왔다.
--   · claim_fingerprint → 주장 자체에서 계산한다(파생이지 날조가 아니다).
--   · independent_origin → 표기를 공급한 상류 원천. 교차확인 계산에 쓰인다.
--   · verified_by = 'policy:tdb-absorption-name-v1'
--       → 사람이 한 건씩 본 게 아니라 정책 승인으로 올렸다는 사실을 남긴다.
--         (common-anchored-fill-v1 과 다른 값이어야 한다. 그 값은
--          kentity_guard_automatic_name 의 의존성 요구를 켠다.)
--
-- 무엇을 올리지 않는가
--   · form='unknown' 인 표기 (kentity_names_check1 위반). 이건 권리 문제가 아니라
--     표기 형식을 모르는 것이므로 P4 보강 대상이지 승격 대상이 아니다.
--   · 승인 정책이 없거나 name_export_allowed=false 인 출처.
--   · write_owner='kdb' (레거시). 그쪽은 원 작성자의 몫이다(I04).
--   · 운영자 잠금·활성 가드에 걸린 표기 — 애초에 대상에서 뺀다.
--
-- 실행. 한 번에 :batch 건씩. 멱등하므로 중단돼도 다시 돌리면 이어진다.
--   psql -v ON_ERROR_STOP=1 -v batch=5000 -f promote_absorbed_names.sql

\set ON_ERROR_STOP on
\if :{?batch}
\else
  \set batch 5000
\endif

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

CREATE TEMP TABLE promo ON COMMIT DROP AS
SELECT n.id            AS name_id,
       n.entity_id     AS entity_id,
       n.locale, n.value, n.kind, n.form,
       n.source_code,
       p.id            AS policy_id,
       p.license_code  AS license_code,
       gen_random_uuid() AS ev_id
  FROM kentity_names n
  JOIN kentity_entities t
    ON t.id = n.entity_id AND t.write_owner = 'native'
  JOIN kentity_source_policies p
    ON p.provider = n.source_code AND p.status = 'approved' AND p.name_export_allowed
 WHERE n.status = 'unverified'
   AND n.evidence_id IS NULL
   AND n.form <> 'unknown'
   AND NOT n.operator_locked
   AND NOT EXISTS (
         SELECT 1 FROM kentity_source_guards g
          WHERE g.state = 'active' AND g.scope_kind = 'name_slot'
            AND g.entity_id = n.entity_id AND g.locale = n.locale
            AND ( (g.guard_kind IN ('withdrawn_name','operator_correction')
                   AND g.claim_fingerprint = kentity_name_claim_fingerprint(n.locale, n.value, n.kind, n.form))
               OR (g.guard_kind = 'empty_slot' AND n.kind = 'canonical') ))
 ORDER BY n.id
 LIMIT :batch;

-- 흡수 당시의 실제 관측을 붙인다. 없으면 빈 문자열이지, 지어낸 값이 아니다.
CREATE TEMP TABLE promo_obs ON COMMIT DROP AS
SELECT p.*,
       coalesce(i.source_record_id, '')        AS obs_record_id,
       coalesce(i.source_url, '')              AS obs_url,
       coalesce(i.source_observation_hash, '') AS obs_hash
  FROM promo p
  LEFT JOIN LATERAL (
        SELECT e.source_record_id, e.source_url, e.source_observation_hash
          FROM kentity_evidence e
         WHERE e.entity_id = p.entity_id
           AND e.claim_type = 'identity'
           AND e.status = 'verified'
         ORDER BY (e.provider = 'tdb') DESC, e.observed_at
         LIMIT 1) i ON true;

INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, summary,
   claim_fingerprint, source_observation_hash, claim_payload, independent_origin, source_policy_id)
SELECT o.ev_id, o.entity_id, o.source_code, o.obs_record_id, o.obs_url, 'name', o.license_code,
       true, 'verified', 'policy:tdb-absorption-name-v1', now(),
       'TDB 흡수 표기 — 상류 원천 정책 승인(0128)에 따라 공급 가능으로 기록',
       kentity_name_claim_fingerprint(o.locale, o.value, o.kind, o.form),
       o.obs_hash,
       jsonb_build_object('locale', o.locale::text, 'value', o.value::text,
                          'kind', o.kind::text, 'form', o.form::text,
                          'source_code', o.source_code::text),
       o.source_code, o.policy_id
  FROM promo_obs o;

UPDATE kentity_names n
   SET evidence_id         = o.ev_id,
       status              = 'verified',
       verification_method = 'source-policy-approved',
       policy_version      = 'tdb-absorb-name-v1',
       revision            = n.revision + 1,
       updated_at          = now()
  FROM promo_obs o
 WHERE n.id = o.name_id
   AND n.status = 'unverified'
   AND n.evidence_id IS NULL;

SELECT ' 승격' AS 구분, count(*) AS 건수 FROM promo_obs;

COMMIT;
