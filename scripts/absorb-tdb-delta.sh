#!/usr/bin/env bash
# TDB 증분 흡수 — 9/13 전량 흡수 이후 원본이 더 쓴 것을 따라잡는다.
#
# 왜 필요한가. P3.06 은 **스냅샷 1회**였고 적재기(load_locale_names.sh·load_record_types.sh)는
# "적재대에 행이 있으면 건너뛴다"로 끝난다. 그래서 그날 이후 tdb-worker 가 쓴 것은
# 들어올 길이 없었다. 못 하는 것이 아니라 길이 없었던 것이다.
#
# 무엇을 기준으로 「안 들어온 것」이라 하는가. 시각이 아니라 **원장 대조**다 —
# `kentity_crosswalks` 에 없는 원본 ID, `p4_tdb_name_source` 에 없는 표기 키.
# 시각 기준은 과거에 보류로 빠진 행을 영원히 놓친다(실측: 39건 중 7건이 그런 행이다).
#
# 사용법:  MODE=dry bash absorb-tdb-delta.sh     (기본 — 세기만 한다)
#          MODE=go  bash absorb-tdb-delta.sh     (적재대 갱신 + 흡수 실행)
set -euo pipefail

MODE=${MODE:-dry}
REPO=${REPO:-/home/aiin/kdb/repo}
KDB="docker exec kdb-db psql -qAtX -U kdb -d kdb"
KDBI="docker exec -i kdb-db psql -qAtX -U kdb -d kdb"
TDBI="docker exec -i tdb-db psql -qAtX -U tdb -d tdb"
say() { echo "$(date +%H:%M:%S) $*"; }

# ─────────────────────────────────────────────── 1. 장소: 원장에 없는 원본 ID
say "① 원본 ID 전량을 뽑아 원장과 대조한다"
docker exec tdb-db psql -qAtX -U tdb -d tdb \
  -c "COPY (SELECT id, status FROM tdb_places) TO STDOUT WITH (FORMAT csv)" > /tmp/tdb_all_ids.csv
docker cp /tmp/tdb_all_ids.csv kdb-db:/tmp/tdb_all_ids.csv >/dev/null

$KDBI <<'SQL'
CREATE TEMP TABLE t(id uuid, status text);
\copy t FROM '/tmp/tdb_all_ids.csv' WITH (FORMAT csv)
\o /tmp/missing_ids.txt
SELECT t.id FROM t
  LEFT JOIN kentity_crosswalks c
    ON c.source_system='tdb' AND c.source_table='tdb_places' AND c.source_id=t.id::text
 WHERE c.source_id IS NULL AND t.status='active';
\o
SQL
docker cp kdb-db:/tmp/missing_ids.txt /tmp/missing_ids.txt >/dev/null
say "   원장에 없는 활성 원본: $(wc -l < /tmp/missing_ids.txt) 건"

# ─────────────────────────────────────────────── 2. 그 ID 의 원본 행만 되가져온다
docker cp /tmp/missing_ids.txt tdb-db:/tmp/missing_ids.txt >/dev/null
$TDBI <<'SQL' >/dev/null
CREATE TEMP TABLE want(id uuid);
\copy want FROM '/tmp/missing_ids.txt'
\copy (SELECT p.id, p.place_type, p.name_ko, coalesce(p.disambiguator,''), p.status, p.merged_into, p.operator_locked, coalesce(p.addr_ko,''), coalesce(p.sido_code,''), coalesce(p.sigungu_code,''), coalesce(p.ldong_code,''), coalesce(p.heritage_no,''), p.lat, p.lon, p.confidence, coalesce((SELECT l.external_id FROM tdb_place_links l WHERE l.place_id=p.id AND l.source_code='wikidata' ORDER BY l.linked_at LIMIT 1),''), coalesce((SELECT string_agg(DISTINCT n.locale,',') FROM tdb_place_names n WHERE n.place_id=p.id),''), coalesce((SELECT string_agg(DISTINCT l.source_code,',') FROM tdb_place_links l WHERE l.place_id=p.id),''), p.updated_at FROM tdb_places p JOIN want w ON w.id=p.id) TO '/tmp/delta_places.csv' WITH (FORMAT csv)
SQL
docker cp tdb-db:/tmp/delta_places.csv /tmp/delta_places.csv >/dev/null
docker cp /tmp/delta_places.csv kdb-db:/tmp/delta_places.csv >/dev/null

# ─────────────────────────────────────────────── 3. 표기: 적재대에 없는 표기 키
say "② 표기 키 전량을 대조한다 (시각이 아니라 키로 — 값이 바뀐 행도 새 키다)"
docker exec tdb-db psql -qAtX -U tdb -d tdb -c "COPY (
  SELECT n.place_id, n.locale, n.name, n.kind, n.source_code, n.confidence, n.operator_locked, n.verified_at
    FROM tdb_place_names n JOIN tdb_places p ON p.id=n.place_id AND p.status='active'
) TO STDOUT WITH (FORMAT csv)" > /tmp/tdb_all_names.csv
docker cp /tmp/tdb_all_names.csv kdb-db:/tmp/tdb_all_names.csv >/dev/null

$KDBI <<'SQL'
CREATE TEMP TABLE n(tdb_id uuid, locale text, name text, kind text, source_code text,
                    confidence numeric, operator_locked boolean, verified_at timestamptz);
\copy n FROM '/tmp/tdb_all_names.csv' WITH (FORMAT csv)
CREATE INDEX ON n (tdb_id, locale, name, kind, source_code);
\o /tmp/name_delta.txt
SELECT count(*) FROM n LEFT JOIN p4_tdb_name_source s USING (tdb_id, locale, name, kind, source_code)
 WHERE s.tdb_id IS NULL;
\o
\copy (SELECT n.* FROM n LEFT JOIN p4_tdb_name_source s USING (tdb_id, locale, name, kind, source_code) WHERE s.tdb_id IS NULL) TO '/tmp/delta_names.csv' WITH (FORMAT csv)
SQL
docker cp kdb-db:/tmp/name_delta.txt /tmp/name_delta.txt >/dev/null
say "   적재대에 없는 표기: $(cat /tmp/name_delta.txt) 건"

if [ "$MODE" != "go" ]; then
  say "dry — 여기까지. 실행하려면 MODE=go"
  exit 0
fi

# ─────────────────────────────────────────────── 4. 적재대 갱신
say "③ 적재대 갱신"
$KDBI <<'SQL'
BEGIN;
CREATE TEMP TABLE dp (LIKE p2_tdb_source) ON COMMIT DROP;
\copy dp FROM '/tmp/delta_places.csv' WITH (FORMAT csv)
INSERT INTO p2_tdb_source SELECT * FROM dp
  ON CONFLICT (tdb_id) DO UPDATE SET
    place_type=EXCLUDED.place_type, name_ko=EXCLUDED.name_ko, disambiguator=EXCLUDED.disambiguator,
    status=EXCLUDED.status, merged_into=EXCLUDED.merged_into, operator_locked=EXCLUDED.operator_locked,
    addr_ko=EXCLUDED.addr_ko, sido_code=EXCLUDED.sido_code, sigungu_code=EXCLUDED.sigungu_code,
    ldong_code=EXCLUDED.ldong_code, heritage_no=EXCLUDED.heritage_no, lat=EXCLUDED.lat, lon=EXCLUDED.lon,
    confidence=EXCLUDED.confidence, qid=EXCLUDED.qid, name_locales=EXCLUDED.name_locales,
    source_codes=EXCLUDED.source_codes, source_updated_at=EXCLUDED.source_updated_at;
CREATE TEMP TABLE dn (tdb_id uuid, locale text, name text, kind text, source_code text,
                      confidence numeric, operator_locked boolean, verified_at timestamptz) ON COMMIT DROP;
\copy dn FROM '/tmp/delta_names.csv' WITH (FORMAT csv)
INSERT INTO p4_tdb_name_source (tdb_id, locale, name, kind, source_code, confidence, operator_locked, verified_at)
SELECT tdb_id, locale, name, kind, source_code, confidence, operator_locked, verified_at FROM dn
  ON CONFLICT DO NOTHING;
COMMIT;
SQL

# ─────────────────────────────────────────────── 5. 흡수 — 유형별로 p3_expand
say "④ 장소 흡수 (유형별)"
types=$($KDB -c "SELECT DISTINCT s.place_type FROM p2_tdb_source s
                   LEFT JOIN kentity_crosswalks c
                     ON c.source_system='tdb' AND c.source_table='tdb_places' AND c.source_id=s.tdb_id::text
                  WHERE c.source_id IS NULL AND s.status='active'")
for pt in $types; do
  n=$($KDB -c "SELECT count(*) FROM p2_tdb_source s
                 LEFT JOIN kentity_crosswalks c
                   ON c.source_system='tdb' AND c.source_table='tdb_places' AND c.source_id=s.tdb_id::text
                WHERE c.source_id IS NULL AND s.status='active' AND s.place_type='$pt'")
  rid=$($KDB -c "SELECT gen_random_uuid()")
  say "   $pt: $n 건 (run $rid)"
  docker exec -i kdb-db psql -qAtX -U kdb -d kdb -v ON_ERROR_STOP=1 \
    -v ptype="$pt" -v lim="$n" -v runid="$rid" < "$REPO/docs/p3/p3_expand.sql"
done

say "⑤ 표기 흡수"
docker exec -i kdb-db psql -qAtX -U kdb -d kdb -v ON_ERROR_STOP=1 -v batch=5000 \
  < "$REPO/docs/p4/absorb_locale_names.sql"

# ─────────────────────────────────────────────── 6. 대조 — 누락 0 인가
say "⑥ 대조"
docker cp /tmp/tdb_all_ids.csv kdb-db:/tmp/tdb_all_ids.csv >/dev/null
docker exec -i kdb-db psql -qAX -F'|' -U kdb -d kdb <<'SQL'
CREATE TEMP TABLE t(id uuid, status text);
\copy t FROM '/tmp/tdb_all_ids.csv' WITH (FORMAT csv)
SELECT '원본 활성' AS 항목, count(*)::text AS 값 FROM t WHERE status='active'
UNION ALL SELECT '원장에 없는 활성 원본', count(*)::text FROM t
  LEFT JOIN kentity_crosswalks c ON c.source_system='tdb' AND c.source_table='tdb_places' AND c.source_id=t.id::text
 WHERE c.source_id IS NULL AND t.status='active'
UNION ALL SELECT '미흡수 표기', count(*)::text FROM p4_tdb_name_source WHERE absorbed_at IS NULL;
SQL

docker exec kdb-db rm -f /tmp/tdb_all_ids.csv /tmp/tdb_all_names.csv /tmp/delta_places.csv /tmp/delta_names.csv /tmp/missing_ids.txt /tmp/name_delta.txt 2>/dev/null || true
docker exec tdb-db rm -f /tmp/missing_ids.txt /tmp/delta_places.csv 2>/dev/null || true
rm -f /tmp/tdb_all_ids.csv /tmp/tdb_all_names.csv /tmp/delta_places.csv /tmp/delta_names.csv /tmp/missing_ids.txt /tmp/name_delta.txt
say "끝"
