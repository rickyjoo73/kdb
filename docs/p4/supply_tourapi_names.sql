-- supply_tourapi_names.sql — TDB 가 이미 받아 둔 관광공사 **언어별 레코드**에서
-- 외국어 표기를 회수한다. 외부 조회 0회.
--
-- ★어디서 오는가. TDB `tdb_src_records` 의 tourapi_en/ja/zh_hans/zh_hant/fr/es/de/ru.
--   이 레코드들은 제목에 **한국어 이름을 괄호로 달고 있다**:
--     tourapi_en  `Nakhwaam Rock (낙화암)`
--     tourapi_ja  `清渓川博物館（청계천박물관）`
--   그래서 괄호 안 한국어로 우리 대상과 붙일 수 있다. 실측 115,452행이 그 꼴이다.
--
-- ★왜 contentid 로 안 붙이나. 언어별 서비스가 **번호 체계를 공유하지 않는다.**
--   실측: tourapi_ko 68,993건 중 tourapi_en 에도 같은 id 가 있는 것 **0건**.
--   좌표도 못 쓴다 — 소수 4자리로 묶으면 68,993건이 501,618쌍이 된다(너무 거칠다).
--
-- ★왜 재조회가 아니라 보유분인가. EngService2 를 직접 불러 보니 totalCount=25,409 —
--   TDB 가 가진 25,409건과 **같다.** 포털의 "약 8만 건"은 14종 전체 합계였다.
--   영문 서비스가 덮는 곳이 국문(68,993)의 3분의 1뿐이라, 나머지는 공공데이터에도
--   영어 이름이 없다. 재조회로 새로 나올 것이 없으므로 같은 곳을 두 번 두드리지 않는다.
--
-- ★동명이인 가드 (I05). canonical_ko 로 붙이므로, 같은 한국어 이름을 가진 대상이
--   둘 이상이면 **붙이지 않는다.** 어느 쪽 이름인지는 이 스크립트가 정할 일이 아니다 —
--   동일인 판정(P4.07)의 몫이다. 붙였다가 틀리면 두 대상이 같은 외국어 이름을 갖는다.
--
-- 입력  :pairs  source_code,foreign_name,ko_name,external_id
-- 실행
--   psql -v ON_ERROR_STOP=1 -v pairs=/tmp/tourpairs.csv -f supply_tourapi_names.sql
--   -v dry=1 을 주면 되돌린다.

\set ON_ERROR_STOP on
\timing on
BEGIN;
SET LOCAL statement_timeout = '3600s';

CREATE TEMP TABLE tp(source_code text, foreign_name text, ko_name text, external_id text) ON COMMIT DROP;
\set copy_t '\\copy tp FROM ' :'pairs' ' WITH (FORMAT csv)'
:copy_t
CREATE INDEX ON tp(ko_name);
ANALYZE tp;

DO $$
BEGIN
  IF (SELECT count(*) FROM tp) = 0 THEN
    RAISE EXCEPTION 'pairs 가 비었다 — 빈 입력을 성공으로 끝내지 않는다';
  END IF;
END $$;

\echo '=== 입력 ==='
SELECT count(*) AS 쌍, count(DISTINCT ko_name) AS 한국어이름, count(DISTINCT source_code) AS 출처 FROM tp;

-- 출처코드 → 로케일. 승인된 정책이 있는 것만 쓴다.
CREATE TEMP TABLE loc_map(source_code text PRIMARY KEY, locale text) ON COMMIT DROP;
INSERT INTO loc_map VALUES
  ('tourapi_en','en'), ('tourapi_ja','ja'),
  ('tourapi_zh_hans','zh-Hans'), ('tourapi_zh_hant','zh-Hant'),
  ('tourapi_fr','fr'), ('tourapi_es','es'), ('tourapi_de','de'), ('tourapi_ru','ru');

-- ① 붙일 것.
CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT DISTINCT ON (e.id, m.locale)
       e.id AS entity_id, m.locale, btrim(t.foreign_name) AS value,
       t.source_code, t.external_id, gen_random_uuid() AS ev_id
  FROM tp t
  JOIN loc_map m ON m.source_code = t.source_code
  JOIN kentity_entities e ON e.canonical_ko = t.ko_name
   AND e.write_owner = 'native' AND e.status <> 'rejected'
 WHERE btrim(t.foreign_name) <> '' AND t.ko_name IS NOT NULL AND btrim(t.ko_name) <> ''
   -- ⑴ 동명 가드(I05) — 같은 한국어 이름의 대상이 둘 이상이면 붙이지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kentity_entities k
                    WHERE k.canonical_ko = t.ko_name AND k.write_owner='native'
                      AND k.status <> 'rejected' AND k.id <> e.id)
   -- ⑵ 그 로케일의 현재 정본 자리가 비어 있어야 한다(유일 인덱스와 같은 조건).
   AND NOT EXISTS (SELECT 1 FROM kentity_names n
                    WHERE n.entity_id = e.id AND n.locale = m.locale
                      AND n.kind = 'canonical' AND n.status = 'verified'
                      AND n.valid_until IS NULL)
   -- ⑶ 멱등.
   AND NOT EXISTS (SELECT 1 FROM kentity_names n
                    WHERE n.entity_id = e.id AND n.locale = m.locale
                      AND n.value = btrim(t.foreign_name) AND n.kind='canonical'
                      AND n.source_code = t.source_code)
 ORDER BY e.id, m.locale, t.external_id;

\echo '=== 붙일 것 ==='
SELECT locale AS 로케일, count(*) FROM tgt GROUP BY 1 ORDER BY 2 DESC;
SELECT count(*) AS 표기수, count(DISTINCT entity_id) AS 대상수 FROM tgt;

-- ② 근거 — **관광공사 항목 자신을 지목한다**(contentid).
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, observed_at, summary,
   claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_id, t.entity_id, t.source_code, t.external_id,
       'https://api.visitkorea.or.kr/#/contentId/' || t.external_id,
       'name', 'KOGL-1', true, 'verified', 'policy:tourapi-paren-ko-v1', now(), now(),
       '한국관광공사 ' || t.source_code || ' 레코드 ' || t.external_id
         || ' 의 표기. 대상 연결은 그 레코드 제목의 **괄호 안 한국어 이름**이 우리 '
         || 'canonical_ko 와 정확히 같다는 것에 근거한다(동명 대상이 있으면 붙이지 않았다).',
       md5(t.source_code || '|' || t.external_id || '|name'),
       md5(t.source_code || '|' || t.external_id || '|' || t.value),
       t.source_code,
       (SELECT id FROM kentity_source_policies WHERE provider = t.source_code AND status = 'approved'
         ORDER BY created_at DESC LIMIT 1)
  FROM tgt t
 WHERE EXISTS (SELECT 1 FROM kentity_source_policies p
                WHERE p.provider = t.source_code AND p.status='approved');

-- ③ 표기.
INSERT INTO kentity_names
  (entity_id, locale, value, kind, form, status, source_code, evidence_id, policy_version)
SELECT t.entity_id, t.locale, t.value, 'canonical', 'recorded', 'verified', t.source_code,
       t.ev_id, 'tourapi-paren-ko-v1'
  FROM tgt t
 WHERE EXISTS (SELECT 1 FROM kentity_evidence v WHERE v.id = t.ev_id);

\echo '=== 결과 ==='
SELECT (SELECT count(*) FROM kentity_names) AS 표기총수,
       (SELECT count(*) FROM kentity_names WHERE status='blocked') AS 차단,
       (SELECT count(*) FROM kentity_names WHERE status='verified' AND evidence_id IS NULL) AS 근거없음;

-- ④ 불변식.
DO $$
DECLARE dup int; bad int;
BEGIN
  SELECT count(*) INTO dup FROM (
    SELECT entity_id, locale FROM kentity_names
     WHERE kind='canonical' AND form='recorded' AND status='verified'
     GROUP BY 1,2 HAVING count(*) > 1) d;
  IF dup > 0 THEN RAISE EXCEPTION '한 대상·한 로케일에 관측 표기가 둘인 경우 % 건', dup; END IF;
  SELECT count(*) INTO bad FROM kentity_names n
   WHERE n.status='verified'
     AND NOT EXISTS (SELECT 1 FROM kentity_evidence v
                      WHERE v.id=n.evidence_id AND v.entity_id=n.entity_id
                        AND v.status='verified' AND v.export_allowed);
  IF bad > 0 THEN RAISE EXCEPTION '근거가 받치지 않는 검증 표기 % 건', bad; END IF;
END $$;

\if :{?dry}
\echo '*** dry=1 — 되돌린다 ***'
ROLLBACK;
\else
COMMIT;
\endif
