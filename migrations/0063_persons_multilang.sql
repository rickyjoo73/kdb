-- 0063_persons_multilang.sql
-- 인물DB(kwave_persons)를 9개 언어로 확장.
--
-- 기존 컬럼: name_ko / name_en / name_ja / name_vi (4개).
-- KDB 가치는 global localization 이므로 인물 이름도 entities 와 동일하게
-- 9개 언어(ko + 8 외국어)를 보유해야 한다. 부족하던 5개 컬럼 추가:
--   es / id / pt-br / zh(간체) / zh-hant(번체)
--
-- 값 채움은 autopilot stepSyncPersons 가 kwave_entities.canonical_* 에서
-- mirror 한다 (entity 가 enrich cascade 로 채워지면 persons 도 따라 채워짐).
-- 추가만 (idempotent) — 기존 데이터 영향 X.
BEGIN;

ALTER TABLE kwave_persons ADD COLUMN IF NOT EXISTS name_es      TEXT;
ALTER TABLE kwave_persons ADD COLUMN IF NOT EXISTS name_id      TEXT;
ALTER TABLE kwave_persons ADD COLUMN IF NOT EXISTS name_pt_br   TEXT;
ALTER TABLE kwave_persons ADD COLUMN IF NOT EXISTS name_zh      TEXT;
ALTER TABLE kwave_persons ADD COLUMN IF NOT EXISTS name_zh_hant TEXT;

COMMIT;
