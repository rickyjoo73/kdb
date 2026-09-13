-- D-37 복구 — 실제 지문 위에서 연결을 다시 확정한다
--
-- 승인 근거: docs/KDB_P3_FIRST_BATCH_PLAN.md §11 안 A (운영자 승인 2026-09-13).
--
-- ★실측으로 계획이 바뀌었다. 관측자가 이미 242건의 shadow 에 **실제 지문**을 써넣었다
--   (import dry-run 전수 unchanged 242/242). 즉 지어낸 값은 이미 교체됐고, 남은 문제는
--   그 교체가 일으킨 무효화 뒷정리뿐이다. 따라서:
--     · shadow 를 지웠다 다시 만들 필요가 없다
--     · **외부 호출이 한 건도 필요 없다** (원래 우려했던 wikidata 242회가 0회)
--     · 하루 100건으로 나눌 이유도 없다
--
-- 하는 것: 새 관측 근거를 기록하고, 그 위에서 외부 ID 와 연결을 되살린다.
-- 안 하는 것: 철회된 옛 근거를 되살리지 않는다. 설계가 "철회하고 새 관측을 기록한다"
--            이므로 withdrawn 을 verified 로 되돌리는 일은 하지 않는다 — 이력으로 남긴다.

\set ON_ERROR_STOP on
BEGIN;

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '600s';

CREATE TEMP TABLE rp ON COMMIT DROP AS
SELECT c.source_id::uuid AS tdb_id, c.entity_id, s.id AS shadow_id, s.source_fingerprint,
       x.external_id AS qid, gen_random_uuid() AS ev_id
  FROM kentity_crosswalks c
  JOIN kentity_tdb_shadows s ON s.id = c.source_binding_id
  JOIN kentity_external_ids x ON x.entity_id = c.entity_id AND x.provider = 'wikidata'
 WHERE c.source_system='tdb' AND c.source_table='tdb_places' AND c.status='conflict';

DO $$
BEGIN
  IF (SELECT count(*) FROM rp) = 0 THEN RAISE EXCEPTION '복구할 conflict 연결이 없다'; END IF;
END $$;

-- ① shadow 의 정책 표기를 실제 값으로 맞춘다.
--    지문은 이미 관측자가 실제 값으로 바꿨으므로 건드리지 않는다 —
--    건드리면 무효화 트리거가 또 돈다(트리거는 fingerprint/locked/generation 에 걸린다).
--    state 는 review 로 둔다: 이 연결은 운영자 batch 가 이미 판정했고, 워커가 다시
--    가져가 wikidata 를 부를 이유가 없다(외부 호출 상한 보호).
UPDATE kentity_tdb_shadows s
   SET policy_version = 'tdb-wikidata-bindings-v1',
       state = 'review',
       reason = 'P3 파일럿 B: 운영자 batch 가 판정한 연결. 관측자가 실제 지문으로 교체 완료.',
       updated_at = now()
  FROM rp r WHERE s.id = r.shadow_id;

-- ② 새 정체성 근거 — 실제 지문을 주장 지문으로 삼는다.
INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT r.ev_id, r.entity_id, 'wikidata', r.qid,
       'https://www.wikidata.org/wiki/' || r.qid, 'identity', 'verified',
       'CC0-1.0', true, 'operator', now(),
       '실제 원본 binding 지문 위에서 다시 기록한 정체성 근거 (D-37 복구)',
       left(r.source_fingerprint, 32), right(r.source_fingerprint, 32), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider='wikidata' AND status='approved' LIMIT 1)
  FROM rp r;

-- ③ 외부 ID 를 새 근거로 되살린다. 철회는 옛 근거가 죽어서 일어난 것이다.
UPDATE kentity_external_ids x
   SET status='verified', evidence_id=r.ev_id, revision=x.revision+1, observed_at=now()
  FROM rp r
 WHERE x.entity_id=r.entity_id AND x.provider='wikidata' AND x.external_id=r.qid;

-- ④ 연결을 다시 확정한다.
UPDATE kentity_crosswalks c
   SET status='confirmed', evidence_id=r.ev_id, decided_by='operator', decided_at=now(),
       target_identity_revision=(SELECT identity_revision FROM kentity_entities WHERE id=r.entity_id),
       reason='D-37 복구: 실제 관측 지문 위에서 다시 확정. 옛 근거는 철회 이력으로 남는다.',
       source_state='present', source_observed_at=now(),
       revision=c.revision+1, updated_at=now()
  FROM rp r
 WHERE c.source_system='tdb' AND c.source_table='tdb_places' AND c.source_id=r.tdb_id::text;

INSERT INTO kentity_audit_events (entity_id, actor, action, reason, after_value)
SELECT r.entity_id, 'operator', 'tdb_mapping_reconfirmed',
       'D-37: 지어낸 지문을 관측자가 실제 값으로 교체했고, 그 위에서 다시 확정했다.',
       jsonb_build_object('shadow_id', r.shadow_id, 'evidence_id', r.ev_id, 'qid', r.qid)
  FROM rp r;

COMMIT;

-- 확인
SELECT '연결 confirmed' AS 항목, count(*)::text AS 값 FROM kentity_crosswalks WHERE source_system='tdb' AND status='confirmed'
UNION ALL SELECT '연결 conflict(남은 것)', count(*)::text FROM kentity_crosswalks WHERE source_system='tdb' AND status='conflict'
UNION ALL SELECT '외부ID verified', count(*)::text FROM kentity_external_ids x JOIN kentity_entities e ON e.id=x.entity_id WHERE e.origin_system='tdb' AND x.status='verified'
UNION ALL SELECT '근거 verified(identity)', count(*)::text FROM kentity_evidence v JOIN kentity_entities e ON e.id=v.entity_id WHERE e.origin_system='tdb' AND v.claim_type='identity' AND v.status='verified'
UNION ALL SELECT '근거 withdrawn(이력)', count(*)::text FROM kentity_evidence v JOIN kentity_entities e ON e.id=v.entity_id WHERE e.origin_system='tdb' AND v.status='withdrawn'
UNION ALL SELECT '★한 외부 키를 둘이 예약(0)', count(*)::text FROM (SELECT provider,external_id FROM kentity_id_reservations GROUP BY 1,2 HAVING count(DISTINCT entity_id)>1) z
UNION ALL SELECT '★근거가 다른 Entity 를 가리킴(0)', count(*)::text FROM kentity_external_ids x JOIN kentity_evidence v ON v.id=x.evidence_id WHERE v.entity_id<>x.entity_id;
