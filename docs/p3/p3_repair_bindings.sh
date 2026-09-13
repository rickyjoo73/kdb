#!/usr/bin/env bash
# D-37 복구 — 지어낸 shadow 를 실제 관측 경로가 만든 것으로 바꾼다
#
# 승인 근거: docs/KDB_P3_FIRST_BATCH_PLAN.md §11 안 A (운영자 승인 2026-09-13).
#
# 왜 이렇게 하나: 흡수가 shadow 를 만든 것이 잘못이었다. shadow 는 원본을 아는 관측
# 경로가 만들어야 실제 지문을 갖는다. 만든 엔티티·이름·외부 ID 는 옳으므로 버리지 않고,
# 연결만 진짜 관측 위에서 다시 확정한다.
#
# 왜 100건씩인가: import 배치 상한이 100이고, 그 뒤 워커가 shadow 하나당 wikidata 를
# 한 번 부른다. 외부 호출 상한이 provider 당 **일 100회**(한도 §5.4)라 하루 한 판이다.
# 몰아서 부르면 상한을 넘긴다 — 워커는 5초당 1건이라 스스로 멈추지 않는다.
set -euo pipefail
N=${1:-100}
PG=kdb-db
q()  { docker exec "$PG" psql -qAtX -U kdb -d kdb -c "$1"; }

echo "=== 1) 이번 판 대상 고르기 (아직 conflict 이고 지어낸 shadow 를 쓰는 것) ==="
docker exec "$PG" psql -qAtX -U kdb -d kdb -c "
DROP TABLE IF EXISTS p3_repair_batch;
CREATE TABLE p3_repair_batch AS
SELECT c.source_id::uuid AS tdb_id, c.entity_id, c.source_binding_id AS old_shadow_id,
       x.external_id AS qid
  FROM kentity_crosswalks c
  JOIN kentity_tdb_shadows s ON s.id = c.source_binding_id AND s.created_by = 'operator'
  JOIN kentity_external_ids x ON x.entity_id = c.entity_id AND x.provider = 'wikidata'
 WHERE c.source_system='tdb' AND c.source_table='tdb_places' AND c.status='conflict'
 ORDER BY c.source_id
 LIMIT $N" >/dev/null
echo "   대상 $(q "SELECT count(*) FROM p3_repair_batch")건"
[ "$(q "SELECT count(*) FROM p3_repair_batch")" = "0" ] && { echo "   복구할 것이 없다."; exit 0; }

echo "=== 2) 원본에서 실제 binding 을 읽어 배치 JSON 을 만든다 ==="
q "SELECT string_agg(tdb_id::text, ',') FROM p3_repair_batch" > /tmp/p3_ids.txt
IDS=$(cat /tmp/p3_ids.txt)
docker exec tdb-db psql -qAtX -U tdb -d tdb -c "
SELECT json_build_object(
  'policy','tdb-wikidata-bindings-v1','source','wikidata','license','CC0','state','live',
  'enabled', true, 'generated', false,
  'observed_at', to_char(now() AT TIME ZONE 'UTC','YYYY-MM-DD\"T\"HH24:MI:SS\"Z\"'),
  'bindings', json_agg(json_build_object(
      'tdb_id', p.id, 'qid', l.external_id, 'method', l.method,
      'score', LEAST(GREATEST(COALESCE(l.score,1.0),0),1), 'locked', p.operator_locked,
      'type', p.place_type)))::text
  FROM tdb_places p
  JOIN tdb_place_links l ON l.place_id = p.id AND l.source_code='wikidata'
 WHERE p.id = ANY(string_to_array('$IDS',',')::uuid[])" > /tmp/p3_bindings.json
echo "   JSON $(wc -c < /tmp/p3_bindings.json) bytes / bindings $(grep -o '\"tdb_id\"' /tmp/p3_bindings.json | wc -l)"

echo "=== 3) 지어낸 shadow 와 그 연결을 걷어낸다 ==="
docker exec "$PG" psql -qAtX -U kdb -d kdb -v ON_ERROR_STOP=1 -c "
BEGIN;
DELETE FROM kentity_crosswalks c USING p3_repair_batch b
 WHERE c.source_system='tdb' AND c.source_table='tdb_places' AND c.source_id=b.tdb_id::text;
DELETE FROM kentity_tdb_shadow_events e USING p3_repair_batch b WHERE e.shadow_id=b.old_shadow_id;
DELETE FROM kentity_tdb_shadows s USING p3_repair_batch b WHERE s.id=b.old_shadow_id;
COMMIT;" >/dev/null
echo "   남은 지어낸 shadow: $(q "SELECT count(*) FROM kentity_tdb_shadows WHERE created_by='operator'")"

echo "=== 4) 실제 import 경로로 shadow 를 만든다 (외부 호출 없음) ==="
docker exec -i "$PG" true  # 연결 확인
docker exec -i kdb-app kdb-app tdb-shadow-import --apply < /tmp/p3_bindings.json

echo "=== 5) 새 관측 근거로 연결을 다시 확정한다 ==="
docker exec "$PG" psql -qAtX -U kdb -d kdb -v ON_ERROR_STOP=1 -c "
BEGIN;
-- 새 정체성 근거. 철회된 옛 근거는 이력으로 남기고 되살리지 않는다
-- (설계: '철회하고 새 관측을 기록한다').
CREATE TEMP TABLE rp ON COMMIT DROP AS
SELECT b.*, s.id AS shadow_id, s.source_fingerprint, gen_random_uuid() AS ev_id
  FROM p3_repair_batch b
  JOIN kentity_tdb_shadows s ON s.tdb_id=b.tdb_id AND s.qid=b.qid
                            AND s.created_by <> 'operator';

INSERT INTO kentity_evidence
 (id, entity_id, provider, source_record_id, source_url, claim_type, status,
  license_code, export_allowed, verified_by, verified_at, summary,
  claim_fingerprint, source_observation_hash, independent_origin, source_policy_id)
SELECT r.ev_id, r.entity_id, 'wikidata', r.qid,
       'https://www.wikidata.org/wiki/' || r.qid, 'identity', 'verified',
       'CC0-1.0', true, 'operator', now(),
       '실제 관측 경로가 만든 원본 binding 위의 정체성 근거',
       left(r.source_fingerprint, 32), right(r.source_fingerprint, 32), 'wikidata',
       (SELECT id FROM kentity_source_policies WHERE provider='wikidata' AND status='approved' LIMIT 1)
  FROM rp r;

-- 외부 ID 를 새 근거로 되살린다(철회는 옛 근거가 죽어서였다).
UPDATE kentity_external_ids x SET status='verified', evidence_id=r.ev_id,
       revision=x.revision+1, observed_at=now()
  FROM rp r WHERE x.entity_id=r.entity_id AND x.provider='wikidata' AND x.external_id=r.qid;

INSERT INTO kentity_crosswalks
 (source_system, source_table, source_id, entity_id, status, target_identity_revision,
  mapping_policy_version, reason, evidence_id, decided_by, decided_at,
  source_binding_id, source_state, source_observed_at)
SELECT 'tdb','tdb_places', r.tdb_id::text, r.entity_id, 'confirmed',
       (SELECT identity_revision FROM kentity_entities WHERE id=r.entity_id),
       'tdb-premap-v1',
       'D-37 복구: 실제 관측이 만든 binding 위에서 다시 확정. 옛 근거는 철회 이력으로 남는다.',
       r.ev_id, 'operator', now(), r.shadow_id, 'present', now()
  FROM rp r;
COMMIT;" >/dev/null

echo "=== 6) 확인 ==="
docker exec "$PG" psql -qAX -U kdb -d kdb -c "
SELECT '연결 confirmed' 항목, count(*)::text 값 FROM kentity_crosswalks WHERE source_system='tdb' AND status='confirmed'
UNION ALL SELECT '연결 conflict(남은 복구 대상)', count(*)::text FROM kentity_crosswalks WHERE source_system='tdb' AND status='conflict'
UNION ALL SELECT '지어낸 shadow(0이어야)', count(*)::text FROM kentity_tdb_shadows WHERE created_by='operator'
UNION ALL SELECT '실제 shadow', count(*)::text FROM kentity_tdb_shadows WHERE created_by<>'operator'
UNION ALL SELECT '외부ID verified', count(*)::text FROM kentity_external_ids x JOIN kentity_entities e ON e.id=x.entity_id WHERE e.origin_system='tdb' AND x.status='verified'
UNION ALL SELECT '★한 외부 키를 둘이 예약(0)', count(*)::text FROM (SELECT provider,external_id FROM kentity_id_reservations GROUP BY 1,2 HAVING count(DISTINCT entity_id)>1) z"
docker exec "$PG" psql -qAtX -U kdb -d kdb -c "DROP TABLE IF EXISTS p3_repair_batch" >/dev/null
