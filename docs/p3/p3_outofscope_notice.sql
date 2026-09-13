-- 범위 밖으로 판정했던 대상이 TDB 로 다시 도착한 것을 기록한다
--
-- 배경: 흡수분 185건이 `audit_outofscope` guard 가 걸린 QID 를 들고 있다.
-- 차단급 **오연결**(MISLINK) 재유입은 **0** 이다 — 연결이 틀린 것이 아니라, 그 대상을
-- KDB 편집 범위 밖으로 판정했던 것이다. 같은 사람이 TDB 를 통해 들어왔다.
--
-- 지우지 않는다. 외부 ID 는 `unverified` 라 아무것도 공급하지 않고, 대상의 정체성은
-- 자기 ID 로 묶여 있어 QID 가 없어도 성립한다. 다만 **운영자의 이전 판정이 있었다는
-- 사실은 보여야 한다** — 범위 판단은 운영자 몫이지 흡수가 대신 뒤집을 일이 아니다.

\set ON_ERROR_STOP on
BEGIN;

INSERT INTO kentity_audit_events (entity_id, actor, action, reason, after_value)
SELECT e.id, 'operator', 'outofscope_subject_rearrived',
       '전에 범위 밖으로 판정한 대상이 TDB 흡수로 다시 도착했다. 범위 판단을 다시 할지는 운영자가 정한다.',
       jsonb_build_object('qid', x.external_id,
                          'prior_guard_id', g.id,
                          'prior_rejected_entity_id', g.rejected_entity_id,
                          'external_id_status', x.status)
  FROM kentity_external_ids x
  JOIN kentity_entities e ON e.id = x.entity_id AND e.origin_system = 'tdb'
  JOIN kentity_source_guards g ON g.subject_pk->>'qid' = x.external_id
                              AND g.state = 'active' AND g.reason_code = 'audit_outofscope'
 WHERE NOT EXISTS (SELECT 1 FROM kentity_audit_events a
                    WHERE a.action = 'outofscope_subject_rearrived' AND a.entity_id = e.id);

COMMIT;

SELECT '범위밖 재도착 기록' AS 항목, count(*)::text AS 값
  FROM kentity_audit_events WHERE action = 'outofscope_subject_rearrived';
