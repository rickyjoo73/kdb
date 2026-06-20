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

\echo '=== [7] quarantine(typed 보류 — 운영자 검토 대기): 소비자 typed 요청·외부증거 미확보 ==='
SELECT count(*) AS quarantined_typed
FROM kwave_entities WHERE COALESCE(notes,'') LIKE '%[kdb:q:typed]%' AND status='candidate';

\echo '=== [8] canonical_en 충돌(같은 영문+type active 2+): 동명이인/중복 — 운영자 정식 해소 대상 ==='
-- WF-2 가 가시화만 하는 충돌. match 응답엔 locale_ambiguous=true 로 소비자 통지됨.
SELECT lower(canonical_en) AS en, entity_type::text AS type, count(*) AS n,
       string_agg(DISTINCT canonical_ko, ' | ') AS canonical_kos
FROM kwave_entities
WHERE status='active' AND canonical_en IS NOT NULL AND canonical_en <> ''
  AND disambig IS NULL AND operator_locked = false
GROUP BY lower(canonical_en), entity_type
HAVING count(*) > 1
ORDER BY n DESC, en LIMIT 40;

\echo '=== [9] 저신뢰 active(conf<0.70) 분해: 자동 bump 가능 vs 검증불가 잔량(운영자/재enrich 필요) ==='
SELECT
  count(*) AS lowconf_total,
  count(*) FILTER (WHERE EXISTS(SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=kwave_entities.id AND r.provider IN ('tmdb','musicbrainz','kofic','kmdb'))) AS tier2_extref_bumpable,
  count(*) FILTER (WHERE COALESCE(array_length(source_domains,1),0) >= 2
        AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=kwave_entities.id AND r.provider IN ('tmdb','musicbrainz','kofic','kmdb','wikidata'))) AS media_only_not_bumped,
  count(*) FILTER (WHERE COALESCE(array_length(source_domains,1),0) < 2
        AND NOT EXISTS(SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=kwave_entities.id AND r.provider IN ('tmdb','musicbrainz','kofic','kmdb','wikidata'))) AS unverifiable
FROM kwave_entities WHERE status='active' AND entity_type<>'unknown' AND confidence<0.70 AND operator_locked=false;

\echo '=== [10] 오염 의심(검수): person 표기에 팬호칭(언니/오빠/누나/형님) — 운영자 확인/정리 대상 ==='
-- 유입 PreGate 가 "고은언니 한고은" 류(이름+결합호칭+둘째이름)는 하드 차단하지만,
-- "오드리 누나"(분리호칭) 등 그레이존은 오탐 방지로 여기 검수 큐에 노출한다.
SELECT id, status, canonical_ko, confidence
FROM kwave_entities
WHERE entity_type='person' AND status IN ('active','candidate') AND operator_locked=false
  AND (canonical_ko ~ '(언니|오빠|누나|형님)'
       OR EXISTS(SELECT 1 FROM unnest(COALESCE(aliases_ko,'{}'::text[])) a WHERE a ~ '(언니|오빠|누나|형님)'))
ORDER BY status, confidence DESC LIMIT 40;

\echo '=== [11] K-범위 의심(자율 scope-QA 발굴): 비-K 인물 의심 — 운영자 확인 후 reject ==='
-- stepScopeReview(Gemma, 매 cycle)가 자동 발굴해 [scope:review] 마킹한 active person.
SELECT id, canonical_ko, confidence,
       substring(notes from '\[scope:review\][^·]*') AS scope_reason
FROM kwave_entities
WHERE status='active' AND entity_type='person' AND COALESCE(notes,'') LIKE '%[scope:review]%'
ORDER BY confidence DESC LIMIT 40;
SQL
