-- verify_premapped_qids.sql — 흡수 때 딸려 온 **미검증 QID 17,237건**을 항목 원문과
-- 대조해 검증으로 올린다 (P4.03 선결 ⑴).
--
-- 무엇이 문제였나. P3 확대 흡수(docs/p3/p3_expand.sql:160)는 QID 를 이렇게 적었다:
--     INSERT INTO kentity_external_ids ... VALUES (..., 'unverified', 'tdb-premap-v1')
-- **evidence_id 없이** 넣었다. 미검증 17,237건 전부 evidence_id 가 NULL 이다.
-- 값은 있는데 **무엇을 보고 그렇게 적었는지가 없다.** 그래서 쓸 수가 없다.
--
-- ★왜 "TDB 기록과 일치한다"로 끝내지 않는가.
--   external_ids 의 QID 와 p2_tdb_source.qid 는 17,237건 전부 일치한다. 세어 봤다.
--   그런데 **둘 다 같은 자동 연결에서 나온 값**이다. 자기 자신과 일치하는 것뿐이다.
--   앞선 감사가 이미 같은 함정을 적어 뒀다 —
--     "링크 점수는 근거가 아니다 — 14,504건 전부 0.90 인 고정값이다."
--     "집단이 좋다는 것이 개별 행이 옳다는 뜻은 아니다."
--   그래서 **항목 원문을 받아 개별로 확인한다**(scripts/wdverify-batch.js).
--
-- ★조회량. 운영자가 "qid를 너무 조회하면 차단되지 않을까?" 라고 물었다.
--   Special:EntityData 는 QID 당 요청 1회라 17,237회다. wbgetentities 는 ids 에 50개를
--   받는다 → **345회**. 실측 1회 2.0초. 같은 확인을 요청 50분의 1로 한다.
--
-- ★QID 를 verified 로 올리면 주 앵커가 되는 것 아닌가 — 아니다(I03).
--   공급 자격은 activate_absorbed_supply.sql 의 `자체ID근거` 가 준다. 그 가드는
--   **external_ids 에 등장하지 않는 provider** 의 identity 근거를 요구한다.
--   wikidata 근거는 그 조건을 영원히 만족하지 못한다. 그래서 QID 가 아무리 많아도
--   공급은 열리지 않는다. 앞서 export_allowed=false 로 막으려다 트리거가 외부ID 를
--   철회해 회수분을 무너뜨렸다(677→243). **막을 곳은 근거가 아니라 게이트다.**
--
-- 입력
--   :verdicts  entity_id,qid,verdict,ko_label,en_label,p31,entity_type
--
-- 하지 않는 것
--   · 표기를 만들지 않는다. 이 스크립트는 **정체성만** 다룬다(라벨은 별도 스크립트).
--   · 대상을 활성화하지 않는다.
--   · OK 가 아닌 판정은 건드리지 않는다 — 미검증으로 남긴다.
--   · 다른 대상이 이미 검증으로 쥔 QID 는 붙이지 않는다(I01) → 동일인 판정(P4.07)으로.
--
-- 실행
--   psql -v ON_ERROR_STOP=1 -v verdicts=/work/verdicts.csv -f verify_premapped_qids.sql
--   -v dry=1 을 주면 되돌린다.

\set ON_ERROR_STOP on
\timing on
BEGIN;
SET LOCAL statement_timeout = '1800s';

CREATE TEMP TABLE v(
  entity_id uuid, qid text, verdict text,
  ko_label text, en_label text, p31 text, entity_type text
) ON COMMIT DROP;

-- ★\copy 는 변수를 치환하지 않는다. 명령 자체를 \set 으로 만들어 실행한다.
--   (처음엔 `\copy v FROM :verdicts` 라 썼다가 파일 이름이 ":verdicts" 가 됐다.)
\set copy_v '\\copy v FROM ' :'verdicts' ' WITH (FORMAT csv)'
:copy_v

-- 빈 입력을 성공으로 끝내지 않는다.
DO $$
BEGIN
  IF (SELECT count(*) FROM v) = 0 THEN
    RAISE EXCEPTION 'verdicts 가 비었다 — 조회 결과 없이 진행하지 않는다';
  END IF;
END $$;

\echo '=== 입력 판정 분포 ==='
SELECT verdict, count(*) FROM v GROUP BY 1 ORDER BY 2 DESC;

-- ① 올릴 대상. 세 가지를 동시에 만족해야 한다.
CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT v.entity_id, v.qid, gen_random_uuid() AS ev_identity
  FROM v
  JOIN kentity_external_ids x
    ON x.entity_id = v.entity_id AND x.provider = 'wikidata' AND x.external_id = v.qid
 WHERE v.verdict = 'OK'
   -- ⑴ 아직 미검증인 행만. **멱등성은 여기서 나온다** — 두 번 돌려도 같은 행을 다시 안 잡는다.
   AND x.status = 'unverified'
   -- ⑵ I01. 같은 항목을 다른 대상이 이미 검증으로 쥐고 있으면 붙이지 않는다.
   --    한 대상 = 한 항목이고, 겹치면 그것은 동일인 판정(P4.07)의 일이지 여기서 정할 일이 아니다.
   AND NOT EXISTS (SELECT 1 FROM kentity_external_ids y
                    WHERE y.provider = 'wikidata' AND y.external_id = v.qid
                      AND y.status = 'verified' AND y.entity_id <> v.entity_id)
   -- ⑶ 같은 주장을 담은 근거가 이미 있으면 넣지 않는다(UNIQUE kentity_evidence_claim_key).
   --    앞서 이 가드를 빼먹어 철회한 근거를 다시 잡고 제약에 부딪혔다.
   AND NOT EXISTS (SELECT 1 FROM kentity_evidence e
                    WHERE e.entity_id = v.entity_id AND e.provider = 'wikidata'
                      AND e.source_record_id = v.qid AND e.claim_type = 'identity');

\echo '=== 올릴 대상 ==='
SELECT count(*) AS 검증승격대상 FROM tgt;

-- ② 정체성 근거. **항목 자신을 지목한다** — source_record_id 가 QID 다.
--    이것이 「출처 항목 지목」 가드가 요구하는 모양이다.
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, observed_at, summary,
   claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_identity, t.entity_id, 'wikidata', t.qid,
       'https://www.wikidata.org/wiki/' || t.qid,
       'identity', 'CC0-1.0', true, 'verified', 'policy:wd-batch-verified-v1', now(), now(),
       '보조 앵커 — 흡수 때 딸려 온 미검증 QID 를 항목 원문과 대조해 확인(ko 라벨/별칭 일치 + 유형 일치). '
         || '공급 자격은 자체 ID 관측이 준다(I03) — activate_absorbed_supply.sql 의 자체ID근거 가드가 강제한다.',
       md5(t.qid || '|identity'), md5(t.qid || '|wdbatch|' || t.entity_id::text), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider = 'wikidata' AND status = 'approved'
         ORDER BY created_at DESC LIMIT 1)
  FROM tgt t;

-- ③ 외부 ID 를 검증으로. 이제 "이 대상은 이 항목이다"에 근거가 붙는다.
UPDATE kentity_external_ids x
   SET status = 'verified', evidence_id = t.ev_identity
  FROM tgt t
 WHERE x.entity_id = t.entity_id AND x.provider = 'wikidata' AND x.external_id = t.qid
   AND x.status = 'unverified';

\echo '=== 결과 ==='
SELECT (SELECT count(*) FROM kentity_external_ids WHERE provider='wikidata' AND status='verified')   AS 검증됨,
       (SELECT count(*) FROM kentity_external_ids WHERE provider='wikidata' AND status='unverified') AS 남은미검증,
       (SELECT count(*) FROM kentity_external_ids WHERE provider='wikidata' AND status='verified'
                                                    AND evidence_id IS NULL)                          AS 근거없는검증;

\echo '=== 올리지 못한 이유 ==='
SELECT CASE WHEN v.verdict <> 'OK' THEN '판정 ' || v.verdict
            WHEN x.status = 'verified' THEN '이미 검증됨'
            WHEN EXISTS (SELECT 1 FROM kentity_external_ids y
                          WHERE y.provider='wikidata' AND y.external_id=v.qid
                            AND y.status='verified' AND y.entity_id<>v.entity_id)
                 THEN '다른 대상이 쥔 항목 — 동일인 판정(P4.07)으로'
            ELSE '기타' END AS 이유, count(*)
  FROM v LEFT JOIN kentity_external_ids x
    ON x.entity_id=v.entity_id AND x.provider='wikidata' AND x.external_id=v.qid
 WHERE NOT EXISTS (SELECT 1 FROM tgt t WHERE t.entity_id=v.entity_id AND t.qid=v.qid)
 GROUP BY 1 ORDER BY 2 DESC;

-- ④ 불변식. 검증된 외부 ID 는 반드시 살아 있는 근거를 물고 있어야 한다.
DO $$
DECLARE bad int;
BEGIN
  SELECT count(*) INTO bad FROM kentity_external_ids x
   WHERE x.status='verified'
     AND (x.evidence_id IS NULL
          OR NOT EXISTS (SELECT 1 FROM kentity_evidence e
                          WHERE e.id=x.evidence_id AND e.status='verified' AND e.export_allowed));
  IF bad > 0 THEN
    RAISE EXCEPTION '근거 없는 검증 외부ID 가 % 건 생겼다 — 되돌린다', bad;
  END IF;
END $$;

\if :{?dry}
\echo '*** dry=1 — 되돌린다 ***'
ROLLBACK;
\else
COMMIT;
\endif
