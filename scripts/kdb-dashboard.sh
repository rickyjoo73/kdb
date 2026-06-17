#!/bin/sh
# KDB 상태 대시보드 — 요청 대비 답변 커버리지/미해결을 한눈에.
# 사용: ./scripts/kdb-dashboard.sh
set -e
cd "$(dirname "$0")/.."
docker exec -i kdb-db psql -U kdb -d kdb <<'SQL'
SET TIME ZONE 'Asia/Seoul';

\echo '=== [1] 외부 API 요청 (오늘 / 누적, 엔드포인트별) ==='
SELECT path AS endpoint,
       count(*) FILTER (WHERE created_at>=date_trunc('day',now())) AS today,
       count(*) AS total,
       count(*) FILTER (WHERE status>=400) AS errors_total
FROM kwave_kdb_api_requests GROUP BY path ORDER BY total DESC;

\echo '=== [2] 키워드 리서치 큐 처리상태 ==='
SELECT status,
       count(*) AS cnt,
       count(*) FILTER (WHERE created_at>=date_trunc('day',now())) AS today
FROM kwave_entity_research_queue GROUP BY status ORDER BY cnt DESC;

\echo '=== [3] *커버리지: 요청된 키워드를 답할 수 있나 (핵심, 합계=requested) ==='
-- 키워드 1개당 단일 분류: active > rejected > candidate > none
WITH q AS (SELECT DISTINCT entity_ko AS k FROM kwave_entity_research_queue WHERE length(entity_ko)<=60),
cls AS (
  SELECT k,
    CASE
      WHEN EXISTS(SELECT 1 FROM kwave_entities e WHERE e.status='active'    AND (e.canonical_ko=q.k OR q.k=ANY(e.aliases_ko))) THEN 'answerable_active'
      WHEN EXISTS(SELECT 1 FROM kwave_entities e WHERE e.status='rejected'  AND (e.canonical_ko=q.k OR q.k=ANY(e.aliases_ko))) THEN 'intentionally_rejected'
      WHEN EXISTS(SELECT 1 FROM kwave_entities e WHERE e.status='candidate' AND (e.canonical_ko=q.k OR q.k=ANY(e.aliases_ko))) THEN 'candidate_pending_promo'
      ELSE 'unresolved_no_entity'
    END AS bucket
  FROM q
)
SELECT bucket, count(*) AS cnt FROM cls GROUP BY bucket ORDER BY cnt DESC;

\echo '=== [4] 미해결(엔티티 자체 없음) 분해: 진짜 갭(6월+) vs 초기노이즈(5월) ==='
WITH q AS (SELECT entity_ko, max(created_at) lr FROM kwave_entity_research_queue WHERE length(entity_ko)<=60 GROUP BY entity_ko)
SELECT CASE WHEN lr>='2026-06-01' THEN 'real_gap_jun+' ELSE 'noise_may_legacy' END AS bucket, count(*) AS cnt
FROM q
WHERE NOT EXISTS(SELECT 1 FROM kwave_entities e WHERE (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))
GROUP BY 1 ORDER BY cnt DESC;

\echo '=== [5] 즉시 처리대상: 6월+ active 아님 (candidate 승급대기 / 거부 점검) ==='
WITH q AS (SELECT entity_ko, max(requested_entity_type::text) typ, max(created_at) lr FROM kwave_entity_research_queue WHERE length(entity_ko)<=60 GROUP BY entity_ko)
SELECT q.entity_ko, q.typ AS type, q.lr::date AS last_req,
       coalesce((SELECT e.status FROM kwave_entities e WHERE (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)) ORDER BY (e.status='active') DESC LIMIT 1),'(none)') AS db_status
FROM q
WHERE q.lr>='2026-06-01'
  AND NOT EXISTS(SELECT 1 FROM kwave_entities e WHERE e.status='active' AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)))
ORDER BY q.lr DESC;

\echo '=== [6] DB 규모 (entities 상태별) ==='
SELECT status, count(*) FROM kwave_entities GROUP BY status ORDER BY count DESC;
SQL
