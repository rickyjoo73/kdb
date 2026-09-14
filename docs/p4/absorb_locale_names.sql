-- absorb_locale_names.sql — TDB 가 이미 관측한 다국어 표기를 공통 원장으로 (P4.01)
--
-- 무엇을 가져오는가. TDB `tdb_place_names` 2,685,232건(11개 로케일). 흡수(P3)는 한국어
-- 대표명만 가져왔고 나머지 214만여 건은 원본에 남아 있었다. **생성이 아니라 회수다.**
--
-- 근거를 어떻게 적는가 — 관측 단위로 적는다.
--   우리가 한 관측은 "TDB 레코드 X 를 원천 S 에서 읽었고, 그 레코드가 이 표기들을 담고
--   있었다" 하나다. 표기마다 근거 행을 복제하면 **하지 않은 관측을 여러 번 한 것처럼**
--   보인다. 그래서 (대상, 원천)당 근거 한 행을 만들고 그 원천의 표기들이 함께 가리킨다.
--   claim_fingerprint 는 그 근거가 받치는 표기 집합의 지문이라 무엇을 받치는지 특정된다.
--   source_record_id·source_url·source_observation_hash 는 흡수 당시 관측자가 기록한
--   identity 근거에서 그대로 가져온다. 지어내지 않는다(D-37).
--
-- 무엇을 가져오지 않는가
--   · 승인 정책이 없거나 name_export_allowed=false 인 원천
--   · 이미 같은 (대상, 로케일, 값, 종류, 원천) 으로 들어와 있는 표기 (멱등)
--   · 그 대상·로케일에 **이미 검증된 대표명이 있는데 또 대표명인** 표기.
--     TDB 안에서는 (대상,로케일)당 대표명이 하나임을 실측했지만(충돌 0), P4.00 이 다른
--     원천에서 대표명을 이미 넣었을 수 있다. 그 경우 조용히 덮지 않고 **세어서 남긴다**
--     (kentity_names_current_canonical 이 막기도 한다).
--
-- 생성 표기. rule(309,246) 과 llm(883) 은 상류 관측이 아니라 만들어진 표기다.
-- 막지는 않되 form='generated' 로 **무엇인지 정확히 적는다** — recorded 를 요구하는
-- 소비자에게는 공급되지 않는다. 이것이 "모르는 한자·공식명을 만들어 확정"(P4 금지사항)에
-- 걸리지 않는 이유다: 확정하지 않고 생성물이라고 말한다.
--
-- 실행. 한 번에 :batch 개 (대상,원천) 그룹씩. 멱등이라 중단돼도 다시 돌리면 이어진다.
--   psql -v ON_ERROR_STOP=1 -v batch=2000 -f absorb_locale_names.sql

\set ON_ERROR_STOP on
\if :{?batch}
\else
  \set batch 2000
\endif

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

-- ① 처리할 (대상, 원천) 그룹.
CREATE TEMP TABLE grp ON COMMIT DROP AS
SELECT s.tdb_id,
       s.source_code,
       c.entity_id,
       p.id            AS policy_id,
       p.license_code  AS license_code,
       gen_random_uuid() AS ev_id
  FROM (SELECT DISTINCT tdb_id, source_code
          FROM p4_tdb_name_source
         WHERE absorbed_at IS NULL
         ORDER BY tdb_id, source_code
         LIMIT :batch) s
  JOIN kentity_crosswalks c
    ON c.source_system = 'tdb' AND c.source_table = 'tdb_places' AND c.source_id = s.tdb_id::text
  JOIN kentity_entities t
    ON t.id = c.entity_id AND t.write_owner = 'native'
  JOIN kentity_source_policies p
    ON p.provider = s.source_code AND p.status = 'approved' AND p.name_export_allowed;

-- ② 실제로 새로 넣을 표기. 이미 있는 것과 대표명 충돌은 뺀다.
CREATE TEMP TABLE ins ON COMMIT DROP AS
SELECT g.entity_id, g.ev_id, g.source_code, g.policy_id, g.license_code,
       n.locale, n.name AS value, n.kind,
       CASE WHEN g.source_code IN ('rule','llm') THEN 'generated' ELSE 'recorded' END AS form,
       n.operator_locked
  FROM grp g
  JOIN p4_tdb_name_source n
    ON n.tdb_id = g.tdb_id AND n.source_code = g.source_code
 WHERE NOT EXISTS (
         SELECT 1 FROM kentity_names x
          WHERE x.entity_id = g.entity_id AND x.locale = n.locale
            AND x.value = n.name AND x.kind = n.kind AND x.source_code = n.source_code)
   AND NOT (n.kind = 'canonical' AND EXISTS (
         SELECT 1 FROM kentity_names x
          WHERE x.entity_id = g.entity_id AND x.locale = n.locale
            AND x.kind = 'canonical' AND x.status = 'verified' AND x.valid_until IS NULL));

-- ③ 관측 단위 근거. 표기가 하나도 새로 안 들어가는 그룹에는 근거를 만들지 않는다.
CREATE TEMP TABLE ev ON COMMIT DROP AS
SELECT i.entity_id, i.ev_id, i.source_code, i.policy_id, i.license_code,
       md5(string_agg(i.locale || '|' || i.kind || '|' || i.value, E'\n'
                      ORDER BY i.locale, i.kind, i.value))       AS claim_fp,
       count(*)                                                  AS name_count,
       array_agg(DISTINCT i.locale ORDER BY i.locale)            AS locales
  FROM ins i
 GROUP BY i.entity_id, i.ev_id, i.source_code, i.policy_id, i.license_code;

INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, summary,
   claim_fingerprint, source_observation_hash, claim_payload, independent_origin, source_policy_id)
SELECT e.ev_id, e.entity_id, e.source_code,
       coalesce(o.source_record_id, ''), coalesce(o.source_url, ''),
       'name', e.license_code, true, 'verified',
       'policy:tdb-locale-names-v1', now(),
       'TDB 가 관측해 둔 다국어 표기 회수 — 한 레코드 관측이 이 표기들을 함께 받친다',
       e.claim_fp, coalesce(o.source_observation_hash, ''),
       jsonb_build_object('locales', to_jsonb(e.locales), 'name_count', e.name_count),
       e.source_code, e.policy_id
  FROM ev e
  LEFT JOIN LATERAL (
        SELECT v.source_record_id, v.source_url, v.source_observation_hash
          FROM kentity_evidence v
         WHERE v.entity_id = e.entity_id AND v.claim_type = 'identity' AND v.status = 'verified'
         ORDER BY (v.provider = 'tdb') DESC, v.observed_at
         LIMIT 1) o ON true;

-- ④ 표기.
INSERT INTO kentity_names
  (entity_id, locale, value, kind, form, status, evidence_id, source_code,
   operator_locked, verification_method, policy_version)
SELECT i.entity_id, i.locale, i.value, i.kind, i.form, 'verified', i.ev_id, i.source_code,
       i.operator_locked, 'source-policy-approved', 'tdb-locale-names-v1'
  FROM ins i
  JOIN ev e ON e.ev_id = i.ev_id;

-- ⑤ 처리 표시. 그룹 전체를 표시한다 — 새로 넣을 게 없던 그룹도 "봤다"는 뜻이다.
UPDATE p4_tdb_name_source s
   SET absorbed_at = now()
  FROM grp g
 WHERE s.tdb_id = g.tdb_id AND s.source_code = g.source_code AND s.absorbed_at IS NULL;

-- 정책이 없어 못 가져온 그룹도 무한 반복을 막기 위해 표시한다(세어서 남긴다).
UPDATE p4_tdb_name_source s
   SET absorbed_at = now()
 WHERE s.absorbed_at IS NULL
   AND NOT EXISTS (SELECT 1 FROM kentity_source_policies p
                    WHERE p.provider = s.source_code AND p.status = 'approved' AND p.name_export_allowed);

SELECT ' 그룹' AS 구분, count(*) AS 수 FROM grp
UNION ALL SELECT ' 표기 적재', count(*) FROM ins
UNION ALL SELECT ' 근거 생성', count(*) FROM ev;

COMMIT;
