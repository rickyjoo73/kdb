-- recover_wikidata_item_identity.sql — 흡수분의 위키데이터 항목 식별을 회수한다 (P4.03 선결 ⑴)
--
-- 무엇을 고치는가. P4.03-a 감사(docs/KDB_P4_03_PERSON_SUPPLY_AUDIT.md)가 이것을 쟀다:
--   표기 근거 65,972건이 `provider='wikidata'` · CC0-1.0 · export_allowed=true 인데
--   **65,487건의 source_record_id 가 QID 가 아니라 TDB/우리 UUID** 다.
--   "위키데이터가 말했다"는 주장에 **어느 항목인지가 없다.**
--   그래서 activate_absorbed_supply.sql 의 가드가 29,169건을 막고 있다.
--
-- ★왜 TDB 의 시각으로 복원하지 않는가.
--   TDB 링크는 전부 2026-08-15 에 생겼고 라벨 읽기는 08-18 인데, wikidata 표기 73,320건은
--   그 **이전**(08-06·08-10)에 만들어졌다. 그런데 KDB 의 '회수' 근거는 **한 대상당 한 행**이고
--   그 한 행이 08-06 표기와 08-18 표기를 **함께 받친다**. 시각으로는 가를 수 없다.
--
--   그래서 시각을 쓰지 않는다. **항목의 현재 라벨과 우리 표기를 직접 대조한다.**
--   항목 Q 의 locale L 라벨이 우리가 가진 값과 같으면, 그 값은 Q 의 L 라벨이다 —
--   TDB 가 언제 적었는지와 무관하게 참이다. 같지 않으면 붙이지 않는다(보수적).
--
-- ★무엇을 근거로 "이 대상 = 이 항목"이라 하는가. 링크 점수는 근거가 아니다 —
--   14,504건 전부 0.90 인 **고정값**이다. 대신 항목 원문을 받아 두 가지를 확인한다:
--     ① 항목의 ko 라벨 또는 ko 별칭이 우리 canonical_ko 와 같다
--     ② 항목이 instance of Q5(사람)다
--   표본 300건 실측: OK 299 · KO_MISMATCH 0 · NOT_HUMAN 0 · NO_ITEM 1
--   (en 라벨까지 일치 298). 자동 연결의 품질은 좋았지만, **집단이 좋다는 것이
--   개별 행이 옳다는 뜻은 아니다.** 그래서 올리는 행은 전부 개별 조회로 확인한다.
--
-- 입력 (wdverify.js 가 만든다)
--   :verdicts  entity_id,qid,verdict,ko_label,en_label,p31,entity_type
--   :labels    entity_id,locale,label      — 항목의 현재 라벨
--
-- 하지 않는 것
--   · 표기를 만들지 않는다. 이미 있는 표기에 **출처를 정확히 붙일 뿐**이다.
--   · 대상을 활성화하지 않는다. 활성화는 activate_absorbed_supply.sql 의 몫이다.
--   · 라벨이 일치하지 않는 표기는 건드리지 않는다. 계속 가드에 막힌다.
--   · 다른 대상이 이미 쓰는 QID 는 붙이지 않는다(I01) — 동일성 판정(P5)으로 간다.
--
-- 실행:
--   psql -v ON_ERROR_STOP=1 -v verdicts=/work/verdicts.csv -v labels=/work/labels.csv \
--        -f recover_wikidata_item_identity.sql
--   -v dry=1 을 주면 되돌린다.

\set ON_ERROR_STOP on
\if :{?verdicts}
\else
  \set ON_ERROR_STOP on
  DO $$ BEGIN RAISE EXCEPTION '-v verdicts=<csv> 가 필요하다'; END $$;
\endif
\if :{?labels}
\else
  DO $$ BEGIN RAISE EXCEPTION '-v labels=<csv> 가 필요하다'; END $$;
\endif
\if :{?dry}
\else
  \set dry 0
\endif

SET lock_timeout = '5s';
SET statement_timeout = '900s';

BEGIN;

-- ★`\copy` 는 psql 변수를 치환하지 않는다. 그대로 쓰면 파일명이 ":'verdicts'" 가 되어
--   "No such file or directory" 로 죽는다. 명령 자체를 \set 으로 조립해서 부른다.
CREATE TEMP TABLE v (entity_id uuid, qid text, verdict text, ko_label text, en_label text, p31 text, entity_type text) ON COMMIT DROP;
\set copy_v '\\copy v FROM ' :'verdicts' ' WITH (FORMAT csv)'
:copy_v

CREATE TEMP TABLE lab (entity_id uuid, locale text, label text) ON COMMIT DROP;
\set copy_l '\\copy lab FROM ' :'labels' ' WITH (FORMAT csv)'
:copy_l

CREATE INDEX ON lab (entity_id, locale);

DO $$ BEGIN
  IF (SELECT count(*) FROM v) = 0 THEN
    RAISE EXCEPTION 'verdicts 가 비었다. 빈 실행을 성공으로 끝내지 않는다.';
  END IF;
END $$;

-- ① 올릴 대상 — 조회로 확인됐고 모든 정체성 가드를 통과하는 것만.
CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT v.entity_id, v.qid,
       gen_random_uuid() AS ev_identity,
       gen_random_uuid() AS ev_name
  FROM v
  JOIN kentity_entities e ON e.id = v.entity_id
 WHERE v.verdict = 'OK'
   AND e.write_owner = 'native'
   AND e.status <> 'rejected'
   AND NOT e.operator_locked
   -- 대상이 그 QID 를 실제로 들고 있어야 한다(우리가 조회한 것과 원장이 같아야 한다)
   AND EXISTS (SELECT 1 FROM kentity_external_ids x
                WHERE x.entity_id = v.entity_id AND x.provider = 'wikidata' AND x.external_id = v.qid)
   -- ★한 대상 = 하나의 ID (I01). 그 항목을 이미 쓰는 곳이 있으면 붙이지 않는다.
   AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                    WHERE r.provider = 'wikidata' AND r.external_id = v.qid)
   AND NOT EXISTS (SELECT 1 FROM kentity_external_ids x2
                    WHERE x2.provider = 'wikidata' AND x2.external_id = v.qid
                      AND x2.entity_id <> v.entity_id AND x2.status = 'verified')
   AND NOT EXISTS (SELECT 1 FROM kentity_id_reservations r
                    WHERE r.provider = 'wikidata' AND r.external_id = v.qid
                      AND r.entity_id IS NOT NULL AND r.entity_id <> v.entity_id)
   -- 이미 이 항목으로 정체성 근거가 있으면 다시 만들지 않는다(멱등).
   AND NOT EXISTS (SELECT 1 FROM kentity_evidence ev
                    WHERE ev.entity_id = v.entity_id AND ev.provider = 'wikidata'
                      AND ev.claim_type = 'identity' AND ev.source_record_id = v.qid);

-- ② 떨어진 것을 이유와 함께 보여 준다. 조용히 빠지는 것이 없어야 한다.
--    유형을 함께 본다 — TDB 링커의 품질이 유형마다 다르다(인물은 정확, 장소는 역 이름이
--    든 상호를 그 역에 걸어 놓았다: `농협안심축산 고덕역` → `고덕역`).
SELECT coalesce(v.entity_type,'?') AS 유형,
       coalesce(nullif(v.verdict,'OK'),'가드') AS 사유,
       count(*) AS 수
  FROM v
 WHERE v.entity_id NOT IN (SELECT entity_id FROM tgt)
 GROUP BY 1,2 ORDER BY 3 DESC;

-- ③ 정체성 근거 — **항목을 지목하고 그 항목의 주소를 적는다.** p3_batch1 이 하던 방식이다.
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, observed_at, summary,
   claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_identity, t.entity_id, 'wikidata', t.qid,
       'https://www.wikidata.org/wiki/' || t.qid,
       'identity', 'CC0-1.0', true, 'verified', 'policy:tdb-link-verified-v1', now(), now(),
       'TDB 자동 연결(kowiki+en-fp)을 항목 원문과 대조해 확인 — ko 라벨/별칭 일치 + instance of Q5',
       md5(t.qid || '|identity'), md5(t.qid || '|verified|' || t.entity_id::text), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider = 'wikidata' AND status = 'approved'
         ORDER BY created_at DESC LIMIT 1)
  FROM tgt t;

-- ④ 외부 ID 를 verified 로. 이제 "이 대상은 이 항목이다"가 원장에 적힌다.
UPDATE kentity_external_ids x
   SET status = 'verified', evidence_id = t.ev_identity
  FROM tgt t
 WHERE x.entity_id = t.entity_id AND x.provider = 'wikidata' AND x.external_id = t.qid
   AND x.status <> 'verified';

-- ⑤ 표기 근거 — 항목을 지목하는 새 근거를 만든다. 라벨이 실제로 일치하는 표기만 옮긴다.
INSERT INTO kentity_evidence
  (id, entity_id, provider, source_record_id, source_url, claim_type, license_code,
   export_allowed, status, verified_by, verified_at, observed_at, summary,
   claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT t.ev_name, t.entity_id, 'wikidata', t.qid,
       'https://www.wikidata.org/wiki/' || t.qid,
       'name', 'CC0-1.0', true, 'verified', 'policy:tdb-link-verified-v1', now(), now(),
       '항목 원문의 현재 라벨과 값이 같은 표기 — 출처 항목을 지목해 다시 받친다',
       md5(t.qid || '|name'), md5(t.qid || '|labels|' || t.entity_id::text), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider = 'wikidata' AND status = 'approved'
         ORDER BY created_at DESC LIMIT 1)
  FROM tgt t
 WHERE EXISTS (SELECT 1 FROM kentity_names n JOIN lab l
                 ON l.entity_id = n.entity_id AND l.locale = n.locale
                WHERE n.entity_id = t.entity_id AND n.kind = 'canonical' AND n.form = 'recorded'
                  AND n.status = 'verified'
                  AND lower(regexp_replace(n.value,  '\s', '', 'g'))
                    = lower(regexp_replace(l.label,  '\s', '', 'g')));

-- ⑥ 값이 항목 라벨과 **같은** 표기만 새 근거로 옮긴다. 다른 것은 그대로 둔다.
UPDATE kentity_names n
   SET evidence_id = t.ev_name,
       source_code = 'wikidata',
       revision    = n.revision + 1,
       updated_at  = now()
  FROM tgt t
  JOIN lab l ON l.entity_id = t.entity_id
 WHERE n.entity_id = t.entity_id AND n.locale = l.locale
   AND n.kind = 'canonical' AND n.form = 'recorded' AND n.status = 'verified'
   AND lower(regexp_replace(n.value, '\s', '', 'g')) = lower(regexp_replace(l.label, '\s', '', 'g'))
   AND EXISTS (SELECT 1 FROM kentity_evidence ev WHERE ev.id = t.ev_name);

-- ⑦ 감사 기록
INSERT INTO kentity_audit_events (entity_id, actor, action, reason, before_value, after_value)
SELECT t.entity_id, 'policy:tdb-link-verified-v1', 'wikidata_item_identity_recovered',
       'TDB 자동 연결을 항목 원문과 대조해 확인하고 출처 항목을 근거에 적었다 (P4.03 선결 ⑴, 2026-09-14). 표기를 만들지 않았고 대상을 활성화하지 않았다.',
       jsonb_build_object('external_id_status', 'unverified'),
       jsonb_build_object('qid', t.qid, 'external_id_status', 'verified',
                          'checks', jsonb_build_array('ko_label_or_alias_matches_canonical_ko', 'instance_of_Q5'),
                          'names_reattributed',
                          (SELECT count(*) FROM kentity_names n JOIN lab l
                             ON l.entity_id = n.entity_id AND l.locale = n.locale
                            WHERE n.entity_id = t.entity_id AND n.evidence_id = t.ev_name),
                          'official_name_asserted', false)
  FROM tgt t;

SELECT ' 조회 결과' AS 구분, count(*) AS 수 FROM v
UNION ALL SELECT ' 회수 대상', count(*) FROM tgt
UNION ALL SELECT ' 표기 재귀속', (SELECT count(*) FROM kentity_names n JOIN tgt t ON t.ev_name = n.evidence_id);

\if :dry
  \echo '>>> dry=1 — 되돌린다. 아무것도 바뀌지 않았다.'
  ROLLBACK;
\else
  COMMIT;
\endif

-- ────────────────────────── 되돌리기 ──────────────────────────
-- ★아래는 격리 사본에서 **실제로 실행해 확인한** 처방이다(2026-09-14).
--   500건 회수분에 대해 표기 1,751 복구 · 외부ID 434 해제 · 근거 868 삭제로
--   정확히 적용 전 수치(verified 외부ID 243 · QID 지목 표기근거 485)로 돌아갔고,
--   근거 없는 verified 표기는 0건이었다.
--   ★순서를 지켜야 한다 — 표기를 먼저 옮기지 않고 근거를 지우면 FK 가 막는다.
--
--   BEGIN;
--   -- ① 표기를 옛 근거로 되돌린다
--   UPDATE kentity_names n SET evidence_id = (
--      SELECT ev.id FROM kentity_evidence ev
--       WHERE ev.entity_id = n.entity_id AND ev.provider='wikidata' AND ev.claim_type='name'
--         AND ev.verified_by <> 'policy:tdb-link-verified-v1'
--       ORDER BY ev.id LIMIT 1),
--      revision = n.revision + 1, updated_at = now()
--    WHERE n.evidence_id IN (SELECT id FROM kentity_evidence
--       WHERE verified_by='policy:tdb-link-verified-v1' AND claim_type='name');
--   -- ② 외부 ID 확정을 내린다
--   UPDATE kentity_external_ids SET status='unverified', evidence_id=NULL
--    WHERE evidence_id IN (SELECT id FROM kentity_evidence
--       WHERE verified_by='policy:tdb-link-verified-v1' AND claim_type='identity');
--   -- ③ 새 근거 제거
--   DELETE FROM kentity_evidence WHERE verified_by='policy:tdb-link-verified-v1';
--   COMMIT;
