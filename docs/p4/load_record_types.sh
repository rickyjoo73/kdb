#!/usr/bin/env bash
# P4.01 원천 레코드 코드 적재 — TDB(읽기 전용) → KDB 적재대.
# 유형 미상 대상이 어떤 원천 코드를 갖는지. 대상당 여러 코드가 있을 수 있다.
set -u
KDBC=${1:-kdb-db}; KDBD=${2:-kdb}

docker exec "$KDBC" psql -qAtX -U kdb -d "$KDBD" -c "
CREATE TABLE IF NOT EXISTS p4_tdb_record_type (
  tdb_id      uuid NOT NULL,
  source_code text NOT NULL,
  type_code   text NOT NULL,
  PRIMARY KEY (tdb_id, source_code, type_code));
CREATE INDEX IF NOT EXISTS p4_tdb_record_type_code ON p4_tdb_record_type (source_code, type_code);" >/dev/null

have=$(docker exec "$KDBC" psql -qAtX -U kdb -d "$KDBD" -c "SELECT count(*) FROM p4_tdb_record_type")
echo "$(date +%H:%M:%S) 적재대 기존 행수 $have"
[ "$have" != "0" ] && { echo "  이미 적재됨 — 건너뛴다(멱등)"; exit 0; }

docker exec tdb-db psql -qAtX -U tdb -d tdb -c "COPY (
  SELECT DISTINCT l.place_id, r.source_code, r.type_code
    FROM tdb_places p
    JOIN tdb_place_links l ON l.place_id = p.id
    JOIN tdb_src_records r ON r.source_code = l.source_code AND r.external_id = l.external_id
   WHERE p.status = 'active' AND r.type_code IS NOT NULL AND r.type_code <> ''
) TO STDOUT WITH (FORMAT csv)" \
 | docker exec -i "$KDBC" psql -qAtX -U kdb -d "$KDBD" -c "
     COPY p4_tdb_record_type (tdb_id, source_code, type_code) FROM STDIN WITH (FORMAT csv)"

docker exec "$KDBC" psql -qAX -F'|' -U kdb -d "$KDBD" -c "
SELECT count(*) AS 행수, count(DISTINCT tdb_id) AS 대상수 FROM p4_tdb_record_type"
