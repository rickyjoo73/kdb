-- scope_branch_pois.sql — 체인 지점 POI 를 편집 범위 밖으로 (P4.01)
--
-- 운영자 결정 2026-09-14: "지점형은 전부 빼고 브랜드만 추출해줘."
--
-- 왜. KDB 는 K-Wave 기사 번역에 쓰는 고유명사 사전이다. "GS25 석촌시장점"이 기사에
-- 나올 일은 없는데, 이런 지점이 12만건 들어와 ① 분류 검수 대기 수를 부풀리고
-- ② 동명 판정 후보 집합에 노이즈를 넣고 ③ 인덱스·ID 공간을 먹는다.
--
-- **지우지 않는다.** status 를 'rejected'(편집 범위 밖)로 내릴 뿐이다.
--   · 흡수의 "누락 0" 대조가 그대로 유지된다
--   · basis_record_id(자체 ID 앵커)가 ON DELETE RESTRICT 라 삭제는 연쇄 정리를 부른다
--   · 되돌리려면 status 하나만 바꾸면 된다. 삭제는 ID·구분값 작업을 다시 해야 한다
--
-- ★규칙을 정규식만으로 세우면 문화재를 밀어낸다. 실제로 확인한 것:
--     익산 구 신신백화점 · 옛 제일은행 본점 · 부여 구 홍산저포조합 본점
--   전부 '점'으로 끝나지만 heritage_site 다. 그래서 **상업 POI 유형에만** 규칙을 건다.
--   이름이 '점'으로 끝나도 tourist_spot·district·cultural_facility·heritage_site·
--   natural_feature·real·festival_series 면 건드리지 않는다(실측 954건이 이렇게 보호된다).
--   '거점'으로 끝나는 지원센터류도 뺀다(경기남부해바라기센터 거점).
--
-- 대상 실측(2026-09-14 12:35, 브랜드 추출 뒤 재측정):
--   restaurant 56,590 · unknown 45,815 · shopping 17,332 ·
--   sports_facility 3,381 · accommodation 667 = 123,785
--   여기서 체인 머리 12건을 빼 **123,773** 이 실제 대상이다.
--
-- 실행: psql -v ON_ERROR_STOP=1 -v batch=20000 -f scope_branch_pois.sql

\set ON_ERROR_STOP on
\if :{?batch}
\else
  \set batch 20000
\endif

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

-- ★체인 머리 자체는 지점이 아니다 — 빼낸다.
--   실측 12건이 여기 걸린다: 보배반점(지점 44) · 개성손만두요리전문점(8) ·
--   아이스크림할인점(14) · SM아이스크림할인점(12).
--   이들은 이름이 '점'으로 끝나는 탓에 지점 규칙에 걸리지만, 실제로는 **체인의 머리**다.
--   extract_chain_brands.sql 은 "이미 원장에 있는 이름은 만들지 않는다"(I01)는 이유로
--   이들의 brand 행을 만들지 않았다. 그러므로 여기서 이 행을 범위 밖으로 내리면
--   ③이 남겨 둔 유일한 표현까지 사라져 **브랜드가 원장에서 통째로 없어진다.**
--   ③과 ④가 각자로는 옳은데 이어 붙이면 대상을 잃는 자리다.
CREATE TEMP TABLE chain_head ON COMMIT DROP AS
WITH branch AS (
  SELECT btrim(split_part(e.canonical_ko, ' ', 1)) AS head
    FROM kentity_entities e
   WHERE e.write_owner = 'native'
     AND e.canonical_ko LIKE '%점'
     AND e.canonical_ko NOT LIKE '%거점'
     AND position(' ' in e.canonical_ko) > 0
     AND (e.subtype IN ('restaurant','shopping','accommodation','sports_facility')
          OR e.entity_type = 'unknown')
)
SELECT head FROM branch WHERE length(head) >= 2 GROUP BY head HAVING count(*) >= 5;

CREATE INDEX ON chain_head (head);

CREATE TEMP TABLE oos ON COMMIT DROP AS
SELECT e.id
  FROM kentity_entities e
 WHERE e.write_owner = 'native'
   AND e.status <> 'rejected'
   AND NOT e.operator_locked
   AND (e.subtype IN ('restaurant','shopping','accommodation','sports_facility')
        OR e.entity_type = 'unknown')
   AND e.canonical_ko LIKE '%점'
   AND e.canonical_ko NOT LIKE '%거점'
   AND NOT EXISTS (SELECT 1 FROM chain_head c WHERE c.head = e.canonical_ko)
 ORDER BY e.id
 LIMIT :batch;

UPDATE kentity_entities e
   SET status = 'rejected',
       classification_reason = '편집 범위 밖 — 체인 지점 POI (운영자 결정 2026-09-14). '
                            || '되돌리려면 status 만 바꾸면 된다.',
       revision = e.revision + 1,
       updated_at = now()
  FROM oos
 WHERE e.id = oos.id;

SELECT ' 범위 밖 처리' AS 구분, count(*) AS 수 FROM oos;

COMMIT;
