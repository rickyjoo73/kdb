-- 0107_kdb_wdlocale_bad_anchor_revert — 별칭 매칭으로 통과한 잘못된 앵커의 회수분을 되돌린다.
--
-- ★무엇이 잘못됐나(2026-08-07). 새 wd-locale 레인(d877e6e)이 채운 11칸을 배포 후 검수하니
-- 4건이 틀렸다. 넷 다 위키데이터 ko **레이블**은 우리 정본과 다르고 **별칭**에만 우리
-- 이름이 있어서 앵커 가드를 통과한 것이다:
--
--     바이브(group)   ← Q87730005 "네이버 VIBE"  = 음악 스트리밍 서비스
--     DK(SEVENTEEN)  ← Q85976326 "Dplus Kia"   = 이스포츠 구단
--     제이(ENHYPEN)   ← Q26220991 "Jae"        = DAY6 제이 박(다른 인물)
--     라미            ← Q114690838 "김성경"      = 표기 불일치
--
-- 별칭은 "그 이름으로도 불릴 수 있다"는 사전적 사실이지 동일성이 아니다. 짧은 활동명은
-- 별칭 목록에 흔해서, 별칭을 인정하면 동명이인 가드가 사실상 없는 것과 같다. 코드 쪽은
-- wikidataAnchorMatches 에서 별칭 분기를 제거해 재유입을 막았다.
--
-- ★앵커 자체는 이 마이그레이션이 지우지 않는다. 앞의 3건은 명백히 다른 대상이지만, 앵커를
-- 떼는 건 다른 레인들의 판단에도 영향을 주는 별개 결정이다. 여기서는 **이번에 잘못 쓴 값만**
-- 되돌리고, 앵커는 lane='wdlocale-bad-anchor' 로 표시해 다음 세션이 판정하게 둔다.
-- 오너 원칙 "빈칸 > 틀린값" — 값은 지금 비운다.
--
-- 라미는 앵커가 맞을 수도 있다(김성경이 본명일 가능성). 그래도 canonical_ja 가
-- "キム・ソンギョン" 인 것은 틀렸다 — 이 엔티티의 이름은 라미다. 그래서 같이 비운다.

BEGIN;

CREATE TEMP TABLE _bad_anchor ON COMMIT DROP AS
SELECT e.id, e.canonical_ko, r.external_id AS qid
  FROM kwave_entities e
  JOIN kwave_entity_external_refs r ON r.entity_id = e.id AND r.provider = 'wikidata'
 WHERE r.external_id IN ('Q87730005', 'Q85976326', 'Q26220991', 'Q114690838');

-- 이번 레인이 쓴 값만 되돌린다. 다른 출처가 이미 덮었으면 건드리지 않는다.
UPDATE kwave_entities e
   SET canonical_ja = NULL, canonical_ja_source = NULL, updated_at = now()
  FROM _bad_anchor b
 WHERE e.id = b.id AND e.canonical_ja_source = 'wikidata-label';
UPDATE kwave_entities e
   SET canonical_zh = NULL, canonical_zh_source = NULL, updated_at = now()
  FROM _bad_anchor b
 WHERE e.id = b.id AND e.canonical_zh_source = 'wikidata-label';
UPDATE kwave_entities e
   SET canonical_zh_hant = NULL, canonical_zh_hant_source = NULL, updated_at = now()
  FROM _bad_anchor b
 WHERE e.id = b.id AND e.canonical_zh_hant_source = 'wikidata-label';

-- 다음 세션이 앵커 자체를 판정하도록 표시를 남긴다.
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, last_source, last_attempt_at, attempts)
SELECT id, 'wdlocale-bad-anchor', left(canonical_ko || ' ← ' || qid, 200), now(), 1
  FROM _bad_anchor
ON CONFLICT (entity_id, field) DO UPDATE
  SET last_source = EXCLUDED.last_source, last_attempt_at = now(),
      attempts = kwave_kdb_enrich_attempts.attempts + 1;

-- 지문이 바뀌었으니 wd-locale 원장을 지워 재선택 가능하게 둔다. 새 가드가 이제 막을 것이다.
DELETE FROM kwave_kdb_enrich_attempts a
 USING _bad_anchor b
 WHERE a.entity_id = b.id AND a.field = 'wd-locale';

DO $chk$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n FROM kwave_entities e JOIN _bad_anchor b ON b.id = e.id
   WHERE COALESCE(e.canonical_ja_source,'') = 'wikidata-label'
      OR COALESCE(e.canonical_zh_source,'') = 'wikidata-label'
      OR COALESCE(e.canonical_zh_hant_source,'') = 'wikidata-label';
  IF n > 0 THEN
    RAISE EXCEPTION '잘못된 앵커에서 온 값이 % 칸 남았다', n;
  END IF;
  RAISE NOTICE '잘못된 앵커 회수분 되돌림 완료 — 잔여 0칸';
END $chk$;

COMMIT;
