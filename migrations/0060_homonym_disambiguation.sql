-- 0060_homonym_disambiguation.sql
-- 동명이인 (same-name people) disambiguation.
--
-- 목적: 같은 한글 canonical_ko 를 가진 서로 다른 실존 인물 (예: 윤성호 감독 vs
--       윤성호 가수) 을 별개 entity 로 공존시키되, 소속사(agency)/작품(notable_works)/
--       역할(primary_role)/출생연도(birth_year)로 구분한다.
--
-- 핵심: 기존 kwave_entities.canonical_ko UNIQUE 제약이 homonym 을 차단하므로 제거.
--       대신 "같은 사람의 진짜 중복"만 막는 partial UNIQUE index 로 교체한다.
--
-- 적용 안전성: 전부 additive/reversible. autopilot 가동 중에도 적용 가능하나,
--   DROP CONSTRAINT 와 CREATE UNIQUE INDEX 사이의 짧은 창에서 autopilot 의 옛
--   candidates.Observe() ON CONFLICT (canonical_ko) upsert 가 일시적으로
--   "no unique constraint matching ON CONFLICT" 에러를 낼 수 있다.
--   → 본 마이그레이션과 함께 배포되는 candidates.go 는 ON CONFLICT 대신 explicit
--     lookup→insert 로 바뀌므로, "마이그레이션 적용 + 새 바이너리 배포"를 같은
--     윈도우에서 처리하면 안전하다. 보수적으로는 적용 중 autopilot 일시정지 권장.
--     적용 자체는 수 초.
--
-- =====================================================================
-- UP
-- =====================================================================

BEGIN;

-- 1) homonym 차단 UNIQUE 제약 제거 + 조회 성능용 non-unique btree index 추가.
ALTER TABLE kwave_entities DROP CONSTRAINT IF EXISTS kwave_entities_canonical_ko_key;
CREATE INDEX IF NOT EXISTS kwave_entities_canonical_ko_btree
    ON kwave_entities (canonical_ko);

-- 2) disambig: 운영자/enrichment 가 채우는 사람-친화 구분 라벨.
--    예: '(배우)', '(가수)', '(YG)', '(1990)'. 표시/API/구분키 용도.
ALTER TABLE kwave_entities ADD COLUMN IF NOT EXISTS disambig text;

-- 3) needs_disambig: 같은 이름이 충돌(agency/works/role 불일치)로 신규 분리될 때
--    운영자 리뷰로 라우팅하는 신호.
ALTER TABLE kwave_entities ADD COLUMN IF NOT EXISTS needs_disambig boolean NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS kwave_entities_needs_disambig_idx
    ON kwave_entities (needs_disambig) WHERE needs_disambig;

-- 4) "같은 사람의 진짜 중복"만 막는 partial UNIQUE index.
--    키 선택: (canonical_ko, entity_type, coalesce(disambig,'')).
--    근거:
--      - agency/birth_year 는 kwave_entity_person_details 에 있어 kwave_entities
--        index 표현식에 직접 넣을 수 없다 (cross-table). 비정규화 컬럼을 새로
--        추가하는 것보다, 본 마이그레이션이 추가하는 disambig 를 행별 구분자로
--        쓰는 편이 단순하고 운영자 직관(라벨)과 일치한다.
--      - 같은 이름+같은 type 의 두 entity 는 반드시 서로 다른 disambig 를 가져야
--        한다 → 진짜 중복(둘 다 disambig='')은 한 행만 허용, 정당한 homonym
--        (윤성호 '(감독)' vs 윤성호 '(가수)')은 공존 허용.
--      - 단일 행(disambig NULL/'')은 자유롭게 1개 생성 가능 — 기존 흐름 무변경.
CREATE UNIQUE INDEX IF NOT EXISTS kwave_entities_homonym_key
    ON kwave_entities (canonical_ko, entity_type, coalesce(disambig,''));

COMMIT;

-- notable_works/agency/birth_year 정규화 테이블은 추가하지 않는다.
-- 이유: kwave_entity_person_details.notable_works text[] / agency text /
--       birth_year int 가 이미 존재하고 충분하다. 별도 kdb_entity_works 정규화
--       테이블은 join 비용/관리 비용만 늘리고 현 구분 로직(disjoint works 비교,
--       agency 비교)에 이점이 없다. text[] 재사용. (필요 시 후속 마이그레이션.)

-- =====================================================================
-- DOWN (rollback) — 아래를 수동 실행하면 원복.
-- =====================================================================
-- BEGIN;
-- DROP INDEX IF EXISTS kwave_entities_homonym_key;
-- DROP INDEX IF EXISTS kwave_entities_needs_disambig_idx;
-- ALTER TABLE kwave_entities DROP COLUMN IF EXISTS needs_disambig;
-- ALTER TABLE kwave_entities DROP COLUMN IF EXISTS disambig;
-- DROP INDEX IF EXISTS kwave_entities_canonical_ko_btree;
-- -- 주의: UNIQUE 제약 원복은 현재 데이터에 homonym 이 없을 때만 성공한다.
-- ALTER TABLE kwave_entities ADD CONSTRAINT kwave_entities_canonical_ko_key UNIQUE (canonical_ko);
-- COMMIT;
