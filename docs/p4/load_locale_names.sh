#!/usr/bin/env bash
# P4.01 다국어 표기 적재 — TDB(읽기 전용) → KDB 적재대.
#
# TDB 는 536,322개 대상에 11개 로케일 2,685,232건의 표기를 **이미 관측해 두었다**.
# 흡수(P3)는 그중 한국어 대표명만 가져왔다. 만들 데이터가 아니라 안 가져온 데이터다.
#
# 두 DB 는 서로 조인할 수 없으므로 P2/P3 와 같은 방식으로 적재대를 거친다.
# tdb-db 는 **읽기만** 한다(SELECT ... TO STDOUT).
set -u
KDBC=${1:-kdb-db}; KDBD=${2:-kdb}

docker exec "$KDBC" psql -qAtX -U kdb -d "$KDBD" -c "
CREATE TABLE IF NOT EXISTS p4_tdb_name_source (
  tdb_id          uuid        NOT NULL,
  locale          text        NOT NULL,
  name            text        NOT NULL,
  kind            text        NOT NULL,
  source_code     text        NOT NULL,
  confidence      numeric,
  operator_locked boolean     NOT NULL DEFAULT false,
  verified_at     timestamptz,
  absorbed_at     timestamptz,
  PRIMARY KEY (tdb_id, locale, name, kind, source_code));
CREATE INDEX IF NOT EXISTS p4_tdb_name_source_todo
  ON p4_tdb_name_source (tdb_id, source_code) WHERE absorbed_at IS NULL;" >/dev/null

have=$(docker exec "$KDBC" psql -qAtX -U kdb -d "$KDBD" -c "SELECT count(*) FROM p4_tdb_name_source")
echo "$(date +%H:%M:%S) 적재대 기존 행수 $have"
if [ "$have" != "0" ]; then echo "  이미 적재됨 — 건너뛴다(멱등)"; exit 0; fi

docker exec tdb-db psql -qAtX -U tdb -d tdb -c "COPY (
  SELECT n.place_id, n.locale, n.name, n.kind, n.source_code,
         n.confidence, n.operator_locked, n.verified_at
    FROM tdb_place_names n
    JOIN tdb_places p ON p.id = n.place_id AND p.status = 'active'
) TO STDOUT WITH (FORMAT csv)" \
 | docker exec -i "$KDBC" psql -qAtX -U kdb -d "$KDBD" -c "
     COPY p4_tdb_name_source (tdb_id, locale, name, kind, source_code, confidence, operator_locked, verified_at)
     FROM STDIN WITH (FORMAT csv)"

docker exec "$KDBC" psql -qAX -F'|' -U kdb -d "$KDBD" -c "
SELECT locale, count(*) AS 표기수, count(DISTINCT tdb_id) AS 대상수
  FROM p4_tdb_name_source GROUP BY 1 ORDER BY 2 DESC"
