-- 0114 — TTL 종결 뒤 rejudge 가 되살려 영구 고착된 candidate 2건을 원래 판정으로 되돌린다
-- (2026-08-14)
--
-- backlog-watch 의 candidate-stuck 사유는 "TTL 이 도는 한 초과는 0 이어야 한다 → 초과 =
-- TTL 고장"이다. over_21d 이 1 → 2 로 늘어 파본 결과, TTL 은 정상이었고 **다른 레인이
-- TTL 의 결론을 되돌리고 있었다.**
--
--   ① candidate_ttl 이 기각(status=rejected, notes 에 [ttl-expire:reject] 스탬프)
--   ② rejudge 가 "wikidata 존재(Q…) 복원 — 게이트키퍼 재심" 으로 candidate 로 되살림
--   ③ 그런데 TTL 의 재선정 조건이 `notes NOT LIKE '%[ttl-expire:%'` 라 **영영 다시 못 집는다**
--
-- 결과는 한 방향 덫이다. candidate 로 돌아왔지만 어떤 종결 규칙도 적용되지 않는다 —
-- candidate_ttl.go 머리말이 "모든 레인이 '내 것 아님'이라 말할 수 있어서 플래그가 붙는 순간
-- 잔여물이 되어 무한 누적된다"고 쓴 바로 그 상태를, TTL 자신의 산출물로 다시 만들었다.
--
-- 두 기각의 명제가 다른 게 원인이다. rejudge 가 회복하려는 것은 **옛 게이트키퍼가 Wikidata
-- veto 없이 잘못 거부한 건**이고, TTL 기각은 "기한 내 실증되지 않았다"이며 그 재진입 경로는
-- 소비자 재요청이다(그래서 api.go:2603 이 ttl-expire 를 tombstone 근거에서 뺐다).
-- 근본원인은 rejudge.go 선정에서 ttl-expire 건을 제외해 막았다. 이 마이그레이션은 **이미
-- 갇힌 2건**만 되돌린다.
--
-- ★값 판단이 아니라 이미 내려진 판정의 복원이다. 둘 다 노트에 K-엔터가 아니라는 근거가
-- 이미 적혀 있어 재기각이 실질적으로도 맞다:
--
--   인아    "주상욱·차예련의 딸(일반인)로 언급되며 K-엔터테인먼트 종사자가 아님"
--   한예진  "사단법인의 약어로 사용되었거나 동명이인 일반인"
--
-- 되돌릴 수 있게 kwave_kdb_recheck_log 에 남긴다(TTL 레인과 같은 방식). 이름 기준
-- tombstone 이 아니므로 소비자가 다시 요청하면 정상적으로 재발굴된다.

BEGIN;

CREATE TEMP TABLE trapped AS
SELECT id, canonical_ko
  FROM kwave_entities
 WHERE status='candidate'
   AND operator_locked=false
   AND COALESCE(notes,'') LIKE '%[ttl-expire:reject]%';

INSERT INTO kwave_kdb_recheck_log (entity_id, term_ko, verdict, models, agreed, evidence)
SELECT id, canonical_ko, 'ttl-expire', 'migration-0114', true,
       'TTL 종결 후 rejudge 복원으로 재선정 불가 상태였음 — 원 판정 복원(rejudge 선정에서 ttl-expire 제외 조치와 함께)'
  FROM trapped;

UPDATE kwave_entities e
   SET status='rejected', confidence=0.000,
       notes = COALESCE(NULLIF(notes,'') || ' · ','') ||
               '[ttl-expire:reject] 0114 — rejudge 복원으로 TTL 재선정이 막혀 있던 건. 원 판정 복원. 재요청 시 재발굴됨',
       updated_at=now()
  FROM trapped t
 WHERE e.id = t.id AND e.status='candidate' AND e.operator_locked=false;

DO $$
DECLARE n int; r record;
BEGIN
  SELECT count(*) INTO n FROM trapped;
  RAISE NOTICE '0114: 갇혀 있던 candidate %건', n;
  FOR r IN SELECT canonical_ko FROM trapped ORDER BY 1
  LOOP RAISE NOTICE '  · %', r.canonical_ko; END LOOP;

  SELECT count(*) INTO n FROM kwave_entities
   WHERE status='candidate' AND COALESCE(notes,'') LIKE '%[ttl-expire:reject]%';
  RAISE NOTICE '0114: 남은 갇힌 건 %건 (0이어야 정상)', n;

  SELECT count(*) INTO n FROM kwave_entities
   WHERE status='candidate' AND operator_locked=false AND created_at < now() - interval '21 days';
  RAISE NOTICE '0114: created_at 21일 초과 미결 candidate %건 (0이어야 정상)', n;
END $$;

COMMIT;
