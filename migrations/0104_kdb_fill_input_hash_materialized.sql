-- 0104_kdb_fill_input_hash_materialized — 입력 지문을 컬럼으로 실체화하고 트리거로 유지한다.
--
-- ★왜 필요한가(2026-08-07 실측): 0100 이 도입한 입력지문 규칙은 mt-fill · gt-fill ·
-- tmdb-locale 3개 레인에만 들어갔다. 가장 큰 생산자인 enricher 캐스케이드는 아직 옛
-- "7일 창 + exhausted 플래그" 모델이고, 그 때문에 다음 상태다:
--
--     앵커(wikidata QID) 보유 · CJK 빈칸 엔티티   192건
--       그중 옛 7일 규칙에 막혀 선정 불가          186건 (96.9%)
--
-- 즉 어제 배포한 L3.2(Wikipedia langlink)가 값을 만들 수 있는 대상이 통째로 선정에서
-- 빠져 있었다. langlink 채움이 계속 0건이던 실제 원인이다.
--
-- ★그런데 술어를 그대로 이식할 수 없었다. 캐스케이드 선정은 전체 엔티티를 훑는데,
-- kdb_fill_input_hash(uuid) 를 그 안에서 호출하면 원장 보유 엔티티 15,491건에 대해
-- 해시가 계산돼 **선정 쿼리가 11초**가 된다(다른 레인은 안티조인이라 240ms). 함수 자체가
-- 1건 0.17ms 로 싸지 않다 — person_details LEFT JOIN + external_refs string_agg 때문이다.
--
-- 그래서 지문을 컬럼으로 들고, 트리거로 유지한다. 비교가 컬럼 대 컬럼이 되어 사실상
-- 공짜가 되고, 기존 3개 레인도 같이 빨라진다.
--
-- ★기존 함수 kdb_fill_input_hash(uuid) 는 정의를 **바꾸지 않는다**. 구동 중인 바이너리가
-- 그걸 호출하고 있으므로, 이 마이그레이션만으로는 운영 동작이 변하지 않는다(0100 과 같은
-- 무중단 원칙). 새 컬럼을 쓰는 건 다음 배포부터다.

BEGIN;

ALTER TABLE kwave_entities
  ADD COLUMN IF NOT EXISTS fill_input_hash text NOT NULL DEFAULT '';

COMMENT ON COLUMN kwave_entities.fill_input_hash IS
  '채움 재시도 판정용 입력 지문. kdb_fill_input_hash(id) 와 동일한 값을 트리거로 유지한다. '
  '원장(kwave_kdb_enrich_attempts.input_hash)과 이 값을 비교해 "같은 입력으로 이미 실패했나"를 판정.';

-- 행을 인자로 받는 변형. 기존 함수와 **필드 목록·순서가 완전히 동일**해야 한다 —
-- 다르면 원장에 쌓인 지문이 전부 불일치가 되어 백로그 전체가 한 번 더 재시도된다.
-- (아래 검증 쿼리로 전건 일치를 확인한 뒤 커밋한다.)
CREATE OR REPLACE FUNCTION kdb_fill_input_hash_row(e kwave_entities) RETURNS text
LANGUAGE sql STABLE AS $fn$
  SELECT md5(concat_ws('|',
      COALESCE(e.canonical_ko, ''),
      COALESCE(e.entity_type::text, ''),
      COALESCE(e.disambig, ''),
      COALESCE(e.category_hint, ''),
      COALESCE(array_to_string(ARRAY(SELECT unnest(e.aliases_ko) ORDER BY 1), ','), ''),
      COALESCE(e.canonical_en, ''),
      COALESCE(e.canonical_ja, ''),
      COALESCE(e.canonical_vi, ''),
      COALESCE(e.canonical_id, ''),
      COALESCE(e.canonical_es, ''),
      COALESCE(e.canonical_pt_br, ''),
      COALESCE(e.canonical_zh, ''),
      COALESCE(e.canonical_zh_hant, ''),
      COALESCE(d.primary_role::text, ''),
      COALESCE(d.agency, ''),
      COALESCE(array_to_string(ARRAY(SELECT unnest(d.groups) ORDER BY 1), ','), ''),
      COALESCE(array_to_string(ARRAY(SELECT unnest(d.notable_works) ORDER BY 1), ','), ''),
      COALESCE((SELECT string_agg(r.provider || ':' || r.external_id, ','
                             ORDER BY r.provider, r.external_id)
                  FROM kwave_entity_external_refs r WHERE r.entity_id = e.id), '')
  ))
    FROM (SELECT 1) _one
    LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
$fn$;

-- ① 엔티티 자신의 변경 → BEFORE 트리거로 NEW 에 직접 써넣는다(재귀 없음).
CREATE OR REPLACE FUNCTION kdb_fill_hash_self() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
  NEW.fill_input_hash := kdb_fill_input_hash_row(NEW);
  RETURN NEW;
END $fn$;

DROP TRIGGER IF EXISTS trg_kdb_fill_hash_ins ON kwave_entities;
CREATE TRIGGER trg_kdb_fill_hash_ins
  BEFORE INSERT ON kwave_entities
  FOR EACH ROW EXECUTE FUNCTION kdb_fill_hash_self();

-- ★WHEN 절이 중요하다: 지문에 들어가는 컬럼이 실제로 바뀔 때만 재계산한다. 이게 없으면
-- 아래 ②가 fill_input_hash 만 갱신할 때도 트리거가 돌아 이중 계산이 되고, 모든 UPDATE
-- (updated_at 만 바뀌는 것 포함)마다 0.17ms 가 붙는다.
DROP TRIGGER IF EXISTS trg_kdb_fill_hash_upd ON kwave_entities;
CREATE TRIGGER trg_kdb_fill_hash_upd
  BEFORE UPDATE ON kwave_entities
  FOR EACH ROW WHEN (
       OLD.canonical_ko      IS DISTINCT FROM NEW.canonical_ko
    OR OLD.entity_type       IS DISTINCT FROM NEW.entity_type
    OR OLD.disambig          IS DISTINCT FROM NEW.disambig
    OR OLD.category_hint     IS DISTINCT FROM NEW.category_hint
    OR OLD.aliases_ko        IS DISTINCT FROM NEW.aliases_ko
    OR OLD.canonical_en      IS DISTINCT FROM NEW.canonical_en
    OR OLD.canonical_ja      IS DISTINCT FROM NEW.canonical_ja
    OR OLD.canonical_vi      IS DISTINCT FROM NEW.canonical_vi
    OR OLD.canonical_id      IS DISTINCT FROM NEW.canonical_id
    OR OLD.canonical_es      IS DISTINCT FROM NEW.canonical_es
    OR OLD.canonical_pt_br   IS DISTINCT FROM NEW.canonical_pt_br
    OR OLD.canonical_zh      IS DISTINCT FROM NEW.canonical_zh
    OR OLD.canonical_zh_hant IS DISTINCT FROM NEW.canonical_zh_hant)
  EXECUTE FUNCTION kdb_fill_hash_self();

-- ② 자식 테이블 변경 → 부모의 지문만 갱신한다. 위 WHEN 절이 이 UPDATE 를 걸러내므로
-- ①이 다시 돌지 않는다. updated_at 은 건드리지 않는다 — 여러 레인의 ORDER BY 가 그걸
-- 쓰고 있어서, 여기서 올리면 백로그 정렬이 통째로 흔들린다.
CREATE OR REPLACE FUNCTION kdb_fill_hash_child() RETURNS trigger
LANGUAGE plpgsql AS $fn$
DECLARE v_id uuid;
BEGIN
  v_id := COALESCE(NEW.entity_id, OLD.entity_id);
  IF v_id IS NOT NULL THEN
    UPDATE kwave_entities SET fill_input_hash = kdb_fill_input_hash(v_id) WHERE id = v_id;
  END IF;
  RETURN NULL;
END $fn$;

DROP TRIGGER IF EXISTS trg_kdb_fill_hash_person ON kwave_entity_person_details;
CREATE TRIGGER trg_kdb_fill_hash_person
  AFTER INSERT OR UPDATE OR DELETE ON kwave_entity_person_details
  FOR EACH ROW EXECUTE FUNCTION kdb_fill_hash_child();

DROP TRIGGER IF EXISTS trg_kdb_fill_hash_refs ON kwave_entity_external_refs;
CREATE TRIGGER trg_kdb_fill_hash_refs
  AFTER INSERT OR UPDATE OR DELETE ON kwave_entity_external_refs
  FOR EACH ROW EXECUTE FUNCTION kdb_fill_hash_child();

-- ③ 백필. fill_input_hash 만 쓰므로 위 WHEN 절에 걸려 ①은 돌지 않는다(이중계산 없음).
UPDATE kwave_entities SET fill_input_hash = kdb_fill_input_hash(id);

-- ④ 검증: 실체화한 값이 기존 함수와 전건 일치해야 한다. 하나라도 어긋나면 원장의 지문이
-- 무효가 되므로 트랜잭션을 중단한다.
DO $chk$
DECLARE n bigint;
BEGIN
  SELECT count(*) INTO n FROM kwave_entities
   WHERE fill_input_hash IS DISTINCT FROM kdb_fill_input_hash(id);
  IF n > 0 THEN
    RAISE EXCEPTION '실체화 지문 불일치 %건 — 필드 목록이 어긋났다', n;
  END IF;
END $chk$;

CREATE INDEX IF NOT EXISTS kwave_entities_fill_hash_idx ON kwave_entities (fill_input_hash);

COMMIT;
