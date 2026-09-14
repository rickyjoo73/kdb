-- extract_chain_brands.sql — 지점명에서 체인 브랜드를 뽑아 등록한다 (P4.01)
--
-- 운영자 결정 2026-09-14: "브랜드만 추출해줘 / 5개 이상으로 해줘."
--
-- 지점은 노이즈지만 **브랜드 자체는 KDB 가 담아야 할 고유명사**다. 기사에 "편의점 CU에서"는
-- 나온다. 지점 92,159건을 브랜드 2,298개로 압축하는 것이지 버리는 것이 아니다.
--
-- 어떻게 뽑는가 — 지어내지 않는다.
--   같은 앞머리가 **5개 이상의 서로 다른 지점**에 반복되면 체인이고, 그 앞머리가 브랜드다.
--   문자열은 원천 레코드에 그대로 들어 있던 것이고(만든 글자가 아니다), 우리가 한 판단은
--   "이 앞머리가 브랜드다"는 것이다. 그 판단의 근거(지점 수·예시)를 근거 행에 남긴다.
--
-- ★이미 원장에 있는 이름은 만들지 않는다.
--   실측 2,298개 중 778개가 이미 있다 — 이디야커피는 location.restaurant(native) 와
--   work(kdb) 로 **이미 둘**이다. 여기서 brand 를 또 만들면 셋이 된다.
--   "한 대상 = 하나의 ID"(I01)에 정면으로 걸리므로 만들지 않고 남긴다. 기존 중복의 통합은
--   P5 의 동일성 판정 몫이고, kdb 소유 행은 원 작성자 몫이다(I04).
--
-- 등록 형태
--   entity_type='brand', subtype='commercial_brand' (0129 가 연 것), status='candidate'.
--   확정이 아니라 후보다 — classification_status 는 'pending' 으로 둔다.
--
-- 실행: psql -v ON_ERROR_STOP=1 -v batch=500 -f extract_chain_brands.sql

\set ON_ERROR_STOP on
\if :{?batch}
\else
  \set batch 500
\endif

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

CREATE TEMP TABLE brands ON COMMIT DROP AS
WITH branch AS (
  SELECT e.id, e.canonical_ko,
         btrim(split_part(e.canonical_ko, ' ', 1)) AS head
    FROM kentity_entities e
   WHERE e.write_owner = 'native'
     AND e.canonical_ko LIKE '%점'
     AND e.canonical_ko NOT LIKE '%거점'
     AND position(' ' in e.canonical_ko) > 0
     AND (e.subtype IN ('restaurant','shopping','accommodation','sports_facility')
          OR e.entity_type = 'unknown')
), agg AS (
  SELECT head,
         count(*) AS branch_count,
         (array_agg(canonical_ko ORDER BY canonical_ko))[1:3] AS samples
    FROM branch
   WHERE length(head) >= 2
   GROUP BY head
  HAVING count(*) >= 5
), src AS (   -- 지점들이 가장 많이 온 원천을 브랜드 표기의 출처로 삼는다
  SELECT a.head, a.branch_count, a.samples,
         (SELECT n.source_code FROM branch b2
            JOIN kentity_names n ON n.entity_id = b2.id
           WHERE b2.head = a.head AND n.locale='ko'
           GROUP BY n.source_code ORDER BY count(*) DESC LIMIT 1) AS source_code
    FROM agg a
)
SELECT s.head, s.branch_count, s.samples, s.source_code,
       gen_random_uuid() AS entity_id, gen_random_uuid() AS ev_id
  FROM src s
  JOIN kentity_source_policies p
    ON p.provider = s.source_code AND p.status = 'approved' AND p.name_export_allowed
 WHERE NOT EXISTS (SELECT 1 FROM kentity_entities x WHERE x.canonical_ko = s.head)
 ORDER BY s.branch_count DESC, s.head
 LIMIT :batch;

INSERT INTO kentity_entities
  (id, entity_type, subtype, canonical_ko, origin_system, write_owner, status, classification_reason)
SELECT b.entity_id, 'brand', 'commercial_brand', b.head, 'tdb', 'native', 'candidate',
       '체인 지점 ' || b.branch_count || '개에서 반복되는 앞머리로 추출 (P4.01, 2026-09-14). 세부 검수 대기.'
  FROM brands b;

INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, summary,
   claim_fingerprint, source_observation_hash, claim_payload, independent_origin, source_policy_id)
SELECT b.ev_id, b.entity_id, b.source_code, '', '', 'name', p.license_code,
       true, 'verified', 'policy:chain-brand-extraction-v1', now(),
       '지점명에 반복되는 앞머리에서 체인 브랜드를 추출',
       md5('brand|' || b.head), '',
       jsonb_build_object('brand', b.head::text, 'branch_count', b.branch_count,
                          'samples', to_jsonb(b.samples)),
       b.source_code, p.id
  FROM brands b
  JOIN kentity_source_policies p ON p.provider = b.source_code AND p.status = 'approved';

INSERT INTO kentity_names
  (entity_id, locale, value, kind, form, status, evidence_id, source_code,
   verification_method, policy_version)
SELECT b.entity_id, 'ko', b.head, 'canonical', 'recorded', 'verified', b.ev_id, b.source_code,
       'source-policy-approved', 'chain-brand-v1'
  FROM brands b;

SELECT ' 브랜드 등록' AS 구분, count(*) AS 수 FROM brands;

COMMIT;
