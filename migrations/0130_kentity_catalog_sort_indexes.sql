-- 0130_kentity_catalog_sort_indexes — 목록 정렬 인덱스 (관리자 대시보드 성능)
--
-- 카탈로그 목록은 created_at 또는 updated_at 내림차순으로 상위 몇 건만 뽑는다.
-- 그런데 두 열 어느 쪽에도 단독 인덱스가 없었다. 기존 인덱스는 선두 열이 status 라
-- (kentity_entities_list = status, updated_at, id) 필터 없는 정렬에는 쓸 수 없다.
-- 그래서 8건을 얻으려고 555,877행을 전부 정렬했다 — 목록 한 번에 294ms.
--
-- 인덱스를 주면 Index Only Scan 으로 바뀌어 **5.3ms** 가 된다(55배, 운영 실측).
-- 크기는 각 22MB — 3.8GB DB 에서 감당할 만하고, 대시보드는 가장 자주 열리는 화면이다.
--
-- 운영에는 CREATE INDEX CONCURRENTLY 로 먼저 만들었다(쓰기 차단 0). 이 파일은 그것을
-- git 에 남겨 새 환경에서도 같은 인덱스가 서게 한다. 이미 있으면 아무 일도 하지 않는다.
--
-- 함께 시도했다가 **뺀 것**: (status, classification_status, entity_type, created_at, id)
-- 커버링 인덱스. 집계에서 상관 서브쿼리를 걷어낸 뒤로는 있으나 없으나 160ms 로 같았다
-- (병렬 순차 스캔이 같은 속도). 41MB 와 쓰기 비용만 들고 이득이 0이라 만들지 않는다.
-- 재보지 않고 "인덱스는 많을수록 좋다"로 남겨 두면 쓰기가 계속 손해를 본다.

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

CREATE INDEX IF NOT EXISTS kentity_entities_created_desc
  ON kentity_entities (created_at DESC, id);

CREATE INDEX IF NOT EXISTS kentity_entities_updated_desc
  ON kentity_entities (updated_at DESC, id);

COMMIT;

SELECT indexname FROM pg_indexes
 WHERE tablename = 'kentity_entities'
   AND indexname IN ('kentity_entities_created_desc', 'kentity_entities_updated_desc')
 ORDER BY indexname;
