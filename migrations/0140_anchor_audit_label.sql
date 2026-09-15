-- 0140: 앵커 판정에 **QID 의 영문 라벨**을 같이 적는다.
--
-- 2026-09-15 실측으로 갈린 자리다. P31 만 보면 "우리 유형과 QID 유형이 다르다"까지만
-- 알고, **어느 쪽이 틀렸는지는 모른다.** 영문 라벨을 우리 en 과 나란히 놓으면 갈린다:
--
--   황해     우리 en `The Yellow Sea`(tmdb)   QID 라벨 `Hwang Hae`(배우)  → 다른 대상 · 앵커가 틀림
--   씨스타19  우리 en `Sistar19`               QID 라벨 `Sistar19`        → 같은 대상 · 유형이 틀림
--
-- ★한국어 라벨은 안 쓴다. 동명 함정이다 — 무용가 `가비` 와 2012년 영화 `가비` 가 같은 한글이다.
-- ★우리 en 이 wikidata 에서 온 것이면 일치는 순환이라 증거가 못 된다. 그 판정은 화면이
--   `canonical_en_source` 를 같이 보고 한다 — 여기서는 관측만 적는다(D-37).

BEGIN;
ALTER TABLE kwave_kdb_anchor_audit
  ADD COLUMN IF NOT EXISTS label_en text NOT NULL DEFAULT '';
COMMIT;
