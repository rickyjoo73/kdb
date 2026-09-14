-- supply_wikidata_labels.sql — 검증된 QID 항목의 **현재 라벨**을 표기로 넣는다.
--
-- 왜 이것부터인가. 흡수분의 로케일 결손 실측(2026-09-14):
--     zh-Hant 135,988 · zh-Hans 114,044 · ja 110,689 · en 72,272
--   그리고 en 결손 72,272 중 **51,662건은 "없다"가 아니라 기계 음역만 있다** —
--   `Johyeonmin`(조현민) · `Gimdoha`(김도하) · `Iseolgu`(이설구) 같은 것들이다.
--   공급 경로가 `form='recorded'` 만 내보내므로 서빙되진 않지만, 채워진 것도 아니다.
--
-- ★왜 보강 워커(common-anchored-fill-v1)를 안 쓰는가.
--   그것은 **요청 기반**이다 — `kentity_preparations` 에 소비자가 요청한 (대상, 로케일)만
--   채운다. 400만 건을 훑지 않는 것은 올바른 설계다(아무도 안 물어본 것을 위해
--   위키데이터를 두드리지 않는다). 다만 **지금 필요한 것은 일괄 보충**이고,
--   재료는 이미 받아 뒀다 — verify_premapped_qids 조회에서 라벨 76,704행이 함께 왔다.
--   같은 항목을 두 번 두드리지 않는다.
--
-- ★무엇을 근거로 적는가. 항목 자신이다. source_record_id = QID 이므로
--   「출처 항목 지목」 가드를 그대로 만족한다. 그 항목이 우리 대상이라는 것은
--   verify_premapped_qids.sql 이 ko 라벨/별칭 일치 + 유형 일치로 개별 확인했다.
--
-- 하지 않는 것
--   · **기존 표기를 덮지 않는다.** 그 로케일에 canonical 표기가 하나라도 있으면 건너뛴다.
--     blocked·withdrawn 도 마찬가지다 — 지운 데는 이유가 있다(보강 워커와 같은 규칙).
--   · 기계 음역(form='generated')을 지우지 않는다. 관측값을 **옆에** 놓을 뿐이고,
--     공급은 recorded 만 나가므로 관측값이 이긴다. 이력은 남긴다.
--   · 검증되지 않은 QID 의 라벨은 넣지 않는다.
--   · ko 는 넣지 않는다 — 정본은 우리 것이다.
--
-- 입력  :labels  entity_id,locale,label   (scripts/wdverify-batch.js 산출)
-- 실행
--   psql -v ON_ERROR_STOP=1 -v labels=/work/labels.csv -f supply_wikidata_labels.sql
--   -v dry=1 을 주면 되돌린다.

\set ON_ERROR_STOP on
\timing on
BEGIN;
SET LOCAL statement_timeout = '3600s';

CREATE TEMP TABLE lab(entity_id uuid, locale text, label text) ON COMMIT DROP;
\set copy_l '\\copy lab FROM ' :'labels' ' WITH (FORMAT csv)'
:copy_l

DO $$
BEGIN
  IF (SELECT count(*) FROM lab) = 0 THEN
    RAISE EXCEPTION 'labels 가 비었다 — 빈 입력을 성공으로 끝내지 않는다';
  END IF;
END $$;

\echo '=== 입력 라벨 ==='
SELECT count(*) AS 라벨행, count(DISTINCT entity_id) AS 대상, count(DISTINCT locale) AS 로케일 FROM lab;

-- ① 넣을 것을 고른다.
CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT l.entity_id, l.locale, btrim(l.label) AS label, x.external_id AS qid,
       gen_random_uuid() AS ev_id
  FROM lab l
  JOIN kentity_entities e ON e.id = l.entity_id AND e.write_owner = 'native' AND e.status <> 'rejected'
  -- ⑴ 검증된 앵커가 **정확히 이 대상의 것**이어야 한다.
  JOIN kentity_external_ids x ON x.entity_id = l.entity_id AND x.provider = 'wikidata'
                             AND x.status = 'verified'
  JOIN kentity_evidence iv ON iv.id = x.evidence_id AND iv.entity_id = x.entity_id
                          AND iv.claim_type = 'identity' AND iv.status = 'verified' AND iv.export_allowed
 WHERE l.locale <> 'ko'                       -- ⑵ 정본은 우리 것이다
   AND btrim(l.label) <> ''
   AND l.locale IN (SELECT code FROM kentity_locales)   -- ⑶ FK 를 미리 막는다
   -- ⑷ **어떤 형태로든** 그 로케일 canonical 이 있으면 건너뛴다. 덮지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kentity_names n
                    WHERE n.entity_id = l.entity_id AND n.locale = l.locale
                      AND n.kind = 'canonical' AND n.form = 'recorded')
   -- ⑸ 멱등. 같은 값을 같은 출처로 이미 넣었으면 다시 넣지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kentity_names n
                    WHERE n.entity_id = l.entity_id AND n.locale = l.locale
                      AND n.value = btrim(l.label) AND n.kind = 'canonical'
                      AND n.source_code = 'wikidata');

-- ⑹ 한 대상·한 로케일에 두 줄이 들어가면 안 된다. 입력에 중복이 있어도 하나만 남긴다.
DELETE FROM tgt a USING tgt b
 WHERE a.entity_id = b.entity_id AND a.locale = b.locale AND a.ctid > b.ctid;

\echo '=== 넣을 것 ==='
SELECT count(*) AS 표기수, count(DISTINCT entity_id) AS 대상수 FROM tgt;
SELECT locale AS 로케일, count(*) FROM tgt GROUP BY 1 ORDER BY 2 DESC;

-- ② 표기 근거. 항목 자신을 지목한다.
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, observed_at, summary,
   claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_id, t.entity_id, 'wikidata', t.qid,
       'https://www.wikidata.org/wiki/' || t.qid,
       'name', 'CC0-1.0', true, 'verified', 'policy:wd-batch-label-v1', now(), now(),
       '항목 ' || t.qid || ' 의 현재 ' || t.locale || ' 라벨. 대상=항목 확인은 '
         || 'verify_premapped_qids.sql(ko 라벨/별칭 일치 + 유형 일치)이 개별로 했다.',
       md5(t.qid || '|name|' || t.locale), md5(t.qid || '|label|' || t.locale || '|' || t.label),
       'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider = 'wikidata' AND status = 'approved'
         ORDER BY created_at DESC LIMIT 1)
  FROM tgt t;

-- ③ 표기. **관측**이다 — form='recorded'.
INSERT INTO kentity_names
  (entity_id, locale, value, kind, form, status, source_code, evidence_id, policy_version)
SELECT t.entity_id, t.locale, t.label, 'canonical', 'recorded', 'verified', 'wikidata',
       t.ev_id, 'wd-batch-label-v1'
  FROM tgt t;

\echo '=== 결과 ==='
SELECT (SELECT count(*) FROM kentity_names) AS 표기총수,
       (SELECT count(*) FROM kentity_names WHERE status='blocked') AS 차단표기,
       (SELECT count(*) FROM kentity_names WHERE status='verified' AND evidence_id IS NULL) AS 근거없는검증;

-- ④ 불변식 둘. 하나라도 어기면 되돌린다.
DO $$
DECLARE dup int; bad int;
BEGIN
  -- 한 대상·한 로케일에 관측 canonical 은 하나뿐이어야 한다(제약이 아니라 규율이다).
  SELECT count(*) INTO dup FROM (
    SELECT entity_id, locale FROM kentity_names
     WHERE kind='canonical' AND form='recorded' AND status='verified'
     GROUP BY 1,2 HAVING count(*) > 1) d;
  IF dup > 0 THEN RAISE EXCEPTION '한 대상·한 로케일에 관측 표기가 둘인 경우가 % 건 생겼다', dup; END IF;

  SELECT count(*) INTO bad FROM kentity_names n
   WHERE n.status='verified'
     AND NOT EXISTS (SELECT 1 FROM kentity_evidence v
                      WHERE v.id=n.evidence_id AND v.entity_id=n.entity_id
                        AND v.status='verified' AND v.export_allowed);
  IF bad > 0 THEN RAISE EXCEPTION '근거가 받치지 않는 검증 표기가 % 건 생겼다', bad; END IF;
END $$;

\if :{?dry}
\echo '*** dry=1 — 되돌린다 ***'
ROLLBACK;
\else
COMMIT;
\endif
