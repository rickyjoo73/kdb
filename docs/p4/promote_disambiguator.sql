-- promote_disambiguator.sql — 흡수 때 잡아 둔 **구분값을 문장에서 컬럼으로** 옮긴다 (P4.07).
--
-- ★무엇이 문제인가.
--   동명 대기열을 세면 22,231개 이름 / 98,908 대상이 나온다. 무서운 수다.
--   그런데 열어 보면 대부분이 **이미 구분돼 있다**:
--     중앙동  구분값=인천 제물포구
--     중앙동  구분값=경남 창원시 성산구
--     중앙동  구분값=전남 여수시
--   서로 다른 법정동이고, 우리는 그걸 흡수할 때 이미 알았다(p3_expand 의 built_disambig).
--   **동일인 판정이 필요한 게 아니라 이미 판정돼 있다.**
--
--   다만 그 값이 `classification_reason` **문장 안에** 들어가 있다. 사람은 읽을 수 있지만
--   조회도 공급도 안 된다. 그래서 화면은 "동명 98,908건"이라고 말하고, 운영자는
--   9만 건을 검수해야 하는 것처럼 보인다. **실제로 판단이 필요한 것은 866건이다.**
--
--   실측:
--     ① 구분값 있음(문장에 묻힘)   78,272   ← 이 스크립트가 옮긴다
--     ② 구분값 미상 — 검수 필요       866   ← 진짜 P4.07 대기열
--     ③ 구분값 언급 없음           19,770   ← 별도 조사
--
-- ★왜 qualifier_ko 인가. 마이그레이션 0123 이 이 컬럼을 이렇게 정의했다:
--     "D-04 (§16). 표시 한정어이며 **정체성 키가 아니다**."
--   정확히 이 용도로 만들어졌고 414,068건 중 **0건** 채워져 있었다.
--   정체성 키가 아니라는 점이 중요하다 — 구분값이 붙는다고 ID 가 갈리지 않고,
--   구분값이 같다고 합쳐지지도 않는다. 사람이 **어느 것인지 알아보게** 할 뿐이다.
--
-- ★지어내지 않는다(D-37). `구분값 미상` 인 행은 건드리지 않는다. 흡수 때 "모른다"고
--   정직하게 적어 둔 것이고, 그것이 곧 검수 대기열이다.
--
-- ★동명 무리만이 아니라 값이 있는 **전부**에 채운다. 절반만 채우면 "왜 이건 있고
--   저건 없나"라는 물음이 생기고, 그 물음의 답이 "동명이라서"가 되면 컬럼의 의미가
--   정체성으로 오해된다. 표시 한정어는 있으면 적는다.
--
-- 실행
--   psql -v ON_ERROR_STOP=1 -f promote_disambiguator.sql
--   -v dry=1 을 주면 되돌린다.

\set ON_ERROR_STOP on
\timing on
BEGIN;
SET LOCAL statement_timeout = '1800s';

CREATE TEMP TABLE ext ON COMMIT DROP AS
SELECT e.id,
       btrim((regexp_match(e.classification_reason, '구분값=([^/]+?)(?: /|$)'))[1]) AS q
  FROM kentity_entities e
 WHERE e.write_owner = 'native'
   AND COALESCE(e.qualifier_ko,'') = ''
   AND e.classification_reason ~ '구분값='
   AND e.classification_reason !~ '구분값 미상';

DELETE FROM ext WHERE q IS NULL OR btrim(q) = '' OR q ~ '미상';

\echo '=== 옮길 것 ==='
SELECT count(*) AS 건수, count(DISTINCT q) AS 서로다른구분값 FROM ext;

\echo '=== 표본 ==='
SELECT e.canonical_ko, COALESCE(e.subtype,'-') AS subtype, x.q AS 구분값
  FROM ext x JOIN kentity_entities e ON e.id = x.id
 ORDER BY md5(x.id::text) LIMIT 8;

DO $$
BEGIN
  IF (SELECT count(*) FROM ext) = 0 THEN
    RAISE EXCEPTION '옮길 것이 0건이다 — 빈 실행을 성공으로 끝내지 않는다';
  END IF;
  -- 구분값이 canonical_ko 와 같으면 구분이 되지 않는다. 그런 것이 있으면 멈춘다.
  IF EXISTS (SELECT 1 FROM ext x JOIN kentity_entities e ON e.id=x.id WHERE x.q = e.canonical_ko) THEN
    RAISE EXCEPTION '구분값이 이름과 같은 행이 있다 — 추출이 잘못됐다';
  END IF;
END $$;

UPDATE kentity_entities e
   SET qualifier_ko = x.q, revision = e.revision + 1, updated_at = now()
  FROM ext x
 WHERE e.id = x.id AND COALESCE(e.qualifier_ko,'') = '';

\echo '=== 결과 — 동명 무리가 얼마나 풀렸나 ==='
WITH dup AS (
  SELECT e.id, e.canonical_ko, e.qualifier_ko, e.classification_reason
    FROM kentity_entities e
    JOIN (SELECT canonical_ko FROM kentity_entities
           WHERE write_owner='native' AND status<>'rejected'
           GROUP BY 1 HAVING count(*)>1) g USING (canonical_ko)
   WHERE e.write_owner='native' AND e.status<>'rejected')
SELECT CASE WHEN COALESCE(qualifier_ko,'')<>'' THEN '① 구분됨'
            WHEN classification_reason ~ '구분값 미상' THEN '② 구분값 미상 — 검수 대기'
            ELSE '③ 구분값 없음 — 조사 필요' END AS 구분, count(*)
  FROM dup GROUP BY 1 ORDER BY 1;

\echo '=== 한 이름 안에서 구분값이 겹치는 무리 (진짜 동일인 후보) ==='
SELECT count(*) AS 겹치는무리 FROM (
  SELECT canonical_ko, COALESCE(qualifier_ko,'') q
    FROM kentity_entities
   WHERE write_owner='native' AND status<>'rejected' AND COALESCE(qualifier_ko,'')<>''
   GROUP BY 1,2 HAVING count(*)>1) t;

-- 불변식: 표시 한정어는 정체성 키가 아니다 — 대상 수가 변하면 안 된다.
DO $$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n FROM kentity_entities WHERE write_owner='native';
  IF n <> 414068 THEN
    RAISE EXCEPTION '흡수분 대상 수가 변했다: % (기대 414068)', n;
  END IF;
END $$;

\if :{?dry}
\echo '*** dry=1 — 되돌린다 ***'
ROLLBACK;
\else
COMMIT;
\endif
