-- P3 보호 규칙 선배포 — 알려진 의심 연결을 차단한다
--
-- 승인 근거: docs/KDB_P3_FIRST_BATCH_PLAN.md §3 (운영자 승인 2026-09-13).
-- batch 적재보다 **먼저** 실행한다. 순서가 뒤집히면 흡수가 오연결을 승계한다.
--
-- 왜 지우지 않는가: 지우면 다음 수집이 같은 근거로 같은 연결을 되살린다.
-- 차단하고 이유를 남기는 것이 재유입을 막는다(P2.05).

\set ON_ERROR_STOP on
BEGIN;

-- kdb_audit_suspects 에 적힌 의심 중, **지금도 그 QID 를 들고 있는 것**만 막는다.
-- 이미 풀린 것까지 막으면 나중에 왜 막혔는지 설명할 수 없다.
INSERT INTO kentity_source_guards
 (origin_system, origin_table, origin_pk, scope_key, scope_kind,
  subject_system, subject_table, subject_pk,
  rejected_entity_id, guard_kind, state, reason_code, source_fingerprint,
  decided_by, decided_at, decision_ref)
SELECT 'kdb', 'kwave_entity_external_refs',
       jsonb_build_object('entity_id', a.entity_id::text, 'provider', 'wikidata', 'external_id', a.qid),
       'wikidata/' || a.qid || '#' || a.entity_id::text,
       'source_binding',
       'wikidata', 'entity', jsonb_build_object('qid', a.qid),
       a.entity_id, 'rejected_binding', 'active',
       'audit_' || lower(a.category),
       md5(a.entity_id::text || a.qid) || md5(a.category || a.qid),
       'operator', now(), 'KDB_P3_FIRST_BATCH_PLAN.md#3'
  FROM kdb_audit_suspects a
 WHERE EXISTS (SELECT 1 FROM kwave_entity_external_refs r
                WHERE r.entity_id = a.entity_id AND r.provider = 'wikidata' AND r.external_id = a.qid)
   AND EXISTS (SELECT 1 FROM kentity_entities e WHERE e.id = a.entity_id)
ON CONFLICT (origin_system, origin_table, origin_pk, scope_key) DO NOTHING;

-- 감사 기록. 무엇을 왜 막았는지 대상별로 남긴다.
INSERT INTO kentity_audit_events (entity_id, actor, action, reason, after_value)
SELECT g.rejected_entity_id, 'operator', 'source_binding_rejected',
       '감사에서 의심으로 표시된 외부 ID 연결. 흡수가 승계하지 않도록 차단한다.',
       jsonb_build_object('guard_id', g.id, 'reason_code', g.reason_code,
                          'qid', g.subject_pk->>'qid')
  FROM kentity_source_guards g
 WHERE g.guard_kind = 'rejected_binding' AND g.decision_ref = 'KDB_P3_FIRST_BATCH_PLAN.md#3'
   AND NOT EXISTS (SELECT 1 FROM kentity_audit_events e
                    WHERE e.action = 'source_binding_rejected'
                      AND e.after_value->>'guard_id' = g.id::text);

COMMIT;

-- 확인
SELECT reason_code, count(*) FROM kentity_source_guards
 WHERE guard_kind='rejected_binding' AND decision_ref='KDB_P3_FIRST_BATCH_PLAN.md#3'
 GROUP BY 1 ORDER BY 2 DESC;
