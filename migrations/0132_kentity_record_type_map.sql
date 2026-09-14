-- 0132_kentity_record_type_map — 원천 레코드 코드 → 유형 매핑 (P4.01)
--
-- 0126 의 kentity_source_type_map 은 TDB `place_type` 단위다. 이 표는 한 단계 아래,
-- **원천이 준 레코드 코드**(TourAPI contenttypeid · AI Hub 범주명 · NGII 지명코드 ·
-- 국가유산 종목코드) 단위다. 유형 미상 106,602건 중 106,536건(99.94%)이 이 코드를 갖는다.
--
-- 대응을 어떻게 얻었는가 — **추측하지 않았다.**
-- API 문서를 기억으로 옮기지 않고, TDB 가 **이미 분류한 대상들**이 어떤 코드를 가졌는지
-- 교차집계해 다수 유형과 일치율을 구했다. 다국어 TourAPI 코드는 같은 대상이 양쪽 API 에
-- 있을 때의 공존 관측으로 한국어 코드와의 동치를 확인했다(38↔79 10,338쌍 · 12↔76 3,078쌍).
-- 근거 전문: docs/KDB_P4_TYPE_CODE_EVIDENCE.md
--
-- 기준선
--   부모 ≥99% 및 세부 ≥99% → 세부유형까지 확정 (46 코드)
--   부모 ≥99% 및 세부 <99% → **부모만**, 세부는 NULL   (38 코드)
--   부모 <99%              → hold. 코드가 유형을 정하지 못한다 (32 코드)
--
-- 성급할 뻔한 지점을 남긴다: tourapi_ko 12(관광지)를 tourist_spot 으로 밀 뻔했다.
-- 이미 분류된 2,033건 중 heritage 1,170 · nature 739 라 세부 일치율이 57.6%다.
-- 코드는 "장소다"까지만 말한다.
--
-- 기준선 밖의 예외 하나 — aihub_tour 편의오락 (운영자 결정 2026-09-14 "응 올려").
-- 유형 미상의 82%(94,024건)인데 TDB 가 분류한 적 있는 표본이 362건뿐이라 부모 일치율
-- 97.8% 로 기준선(99%)에 못 미친다. 운영자가 근거를 보고 올리기로 했다:
--   · 표본 362건의 비-location 은 8건(2.2%) 뿐이고 나머지는 전부 location 세부유형이다
--   · 미상 106,602건의 99.97%가 주소를 갖는다 (제닉스아레나PC 배곧점 · 올레노래방 …)
--   · 같은 원본 표의 음식점 145,433건을 이미 location 으로 흡수한 선례가 있다
-- 대가는 약 2%(1,900건 내외) 오분류이고, decided_by='operator' 로 표시해 **코드 단위로
-- 골라 되돌릴 수 있게** 한다. 세부유형은 주지 않는다 — 362건이 shopping·restaurant·
-- accommodation 으로 흩어지므로 하나로 밀면 그건 추측이다.

SET lock_timeout = '5s';
SET statement_timeout = '300s';

BEGIN;

CREATE TABLE IF NOT EXISTS kentity_record_type_map (
  provider           text        NOT NULL CHECK (btrim(provider) <> ''),
  type_code          text        NOT NULL CHECK (btrim(type_code) <> ''),
  disposition        text        NOT NULL CHECK (disposition IN ('map','hold')),
  target_entity_type text        REFERENCES kentity_types(code) ON DELETE RESTRICT,
  target_subtype     text,
  sample_size        integer     NOT NULL CHECK (sample_size >= 0),
  parent_pct         numeric(4,1) NOT NULL,
  subtype_pct        numeric(4,1) NOT NULL,
  decided_by         text        NOT NULL DEFAULT 'derived' CHECK (decided_by IN ('derived','operator')),
  reason             text        NOT NULL CHECK (btrim(reason) <> ''),
  policy_version     text        NOT NULL,
  created_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (provider, type_code),
  FOREIGN KEY (target_entity_type, target_subtype)
    REFERENCES kentity_subtypes (entity_type, code) ON DELETE RESTRICT,
  CONSTRAINT kentity_record_type_map_shape CHECK (
    CASE disposition WHEN 'map' THEN target_entity_type IS NOT NULL
                     ELSE target_entity_type IS NULL AND target_subtype IS NULL END));

INSERT INTO kentity_record_type_map
 (provider, type_code, disposition, target_entity_type, target_subtype,
  sample_size, parent_pct, subtype_pct, reason, policy_version)
VALUES
  ('aihub_tour','음식점','map','location','restaurant',135876,99.9,99.4,'TDB 자체 분류 135876건 교차집계: 부모 99.9% · 세부 99.4%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','C0109','map','location','district',74032,100.0,99.4,'TDB 자체 분류 74032건 교차집계: 부모 100.0% · 세부 99.4%. 세부까지 확정.','p4-record-type-v1'),
  ('aihub_tour','관광지','map','location',NULL,38146,99.9,94.6,'TDB 자체 분류 38146건 교차집계: 부모 99.9% 확정, 세부는 94.6% 라 주지 않는다.','p4-record-type-v1'),
  ('aihub_tour','숙박','map','location',NULL,24957,100.0,95.4,'TDB 자체 분류 24957건 교차집계: 부모 100.0% 확정, 세부는 95.4% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ko','39','map','location','restaurant',19770,100.0,99.9,'TDB 자체 분류 19770건 교차집계: 부모 100.0% · 세부 99.9%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ko','38','map','location','shopping',16636,100.0,99.8,'TDB 자체 분류 16636건 교차집계: 부모 100.0% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('aihub_tour','레포츠','map','location','sports_facility',16243,100.0,99.8,'TDB 자체 분류 16243건 교차집계: 부모 100.0% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('aihub_tour','쇼핑','map','location','shopping',11276,99.9,99.4,'TDB 자체 분류 11276건 교차집계: 부모 99.9% · 세부 99.4%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_en','79','map','location','shopping',10305,100.0,99.8,'TDB 자체 분류 10305건 교차집계: 부모 100.0% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ja','79','map','location','shopping',10238,100.0,99.8,'TDB 자체 분류 10238건 교차집계: 부모 100.0% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_zh_hans','79','map','location','shopping',10222,99.9,99.7,'TDB 자체 분류 10222건 교차집계: 부모 99.9% · 세부 99.7%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_zh_hant','79','map','location','shopping',10085,100.0,99.7,'TDB 자체 분류 10085건 교차집계: 부모 100.0% · 세부 99.7%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ko','32','map','location','accommodation',5096,100.0,99.8,'TDB 자체 분류 5096건 교차집계: 부모 100.0% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ko','28','map','location','sports_facility',5067,100.0,99.9,'TDB 자체 분류 5067건 교차집계: 부모 100.0% · 세부 99.9%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','21','hold',NULL,NULL,4951,57.8,57.8,'TDB 자체 분류 4951건 교차집계: 부모 일치율 57.8% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('ngii_gazetteer','C0101','map','location','district',4950,100.0,99.6,'TDB 자체 분류 4950건 교차집계: 부모 100.0% · 세부 99.6%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0202','map','location','natural_feature',4369,100.0,99.3,'TDB 자체 분류 4369건 교차집계: 부모 100.0% · 세부 99.3%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','C0102','map','location','district',4360,100.0,99.9,'TDB 자체 분류 4360건 교차집계: 부모 100.0% · 세부 99.9%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0207','map','location',NULL,4324,100.0,98.8,'TDB 자체 분류 4324건 교차집계: 부모 100.0% 확정, 세부는 98.8% 라 주지 않는다.','p4-record-type-v1'),
  ('aihub_tour','문화시설','map','location',NULL,3519,99.6,96.8,'TDB 자체 분류 3519건 교차집계: 부모 99.6% 확정, 세부는 96.8% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ko','15','map','event','festival_series',3323,100.0,100.0,'TDB 자체 분류 3323건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','31','hold',NULL,NULL,3318,77.8,77.8,'TDB 자체 분류 3318건 교차집계: 부모 일치율 77.8% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('legal_dong','동','map','location',NULL,3221,100.0,91.5,'TDB 자체 분류 3221건 교차집계: 부모 100.0% 확정, 세부는 91.5% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ko','14','map','location','cultural_facility',3151,100.0,99.8,'TDB 자체 분류 3151건 교차집계: 부모 100.0% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ja','82','map','location','restaurant',2604,99.9,99.8,'TDB 자체 분류 2604건 교차집계: 부모 99.9% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_en','82','map','location','restaurant',2573,99.9,99.7,'TDB 자체 분류 2573건 교차집계: 부모 99.9% · 세부 99.7%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_zh_hant','82','map','location','restaurant',2570,99.8,99.5,'TDB 자체 분류 2570건 교차집계: 부모 99.8% · 세부 99.5%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','12','hold',NULL,NULL,2562,69.4,69.4,'TDB 자체 분류 2562건 교차집계: 부모 일치율 69.4% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_zh_hans','82','map','location','restaurant',2535,99.8,99.3,'TDB 자체 분류 2535건 교차집계: 부모 99.8% · 세부 99.3%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0603','map','location','natural_feature',2521,100.0,99.0,'TDB 자체 분류 2521건 교차집계: 부모 100.0% · 세부 99.0%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','23','hold',NULL,NULL,2131,98.5,98.5,'TDB 자체 분류 2131건 교차집계: 부모 일치율 98.5% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_ko','12','hold',NULL,NULL,2033,96.6,57.6,'TDB 자체 분류 2033건 교차집계: 부모 일치율 96.6% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_es','82','map','location','restaurant',1907,99.8,99.3,'TDB 자체 분류 1907건 교차집계: 부모 99.8% · 세부 99.3%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_fr','82','map','location','restaurant',1859,99.9,99.5,'TDB 자체 분류 1859건 교차집계: 부모 99.9% · 세부 99.5%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_de','82','map','location','restaurant',1833,99.9,99.8,'TDB 자체 분류 1833건 교차집계: 부모 99.9% · 세부 99.8%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0212','map','location','natural_feature',1745,100.0,99.3,'TDB 자체 분류 1745건 교차집계: 부모 100.0% · 세부 99.3%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0302','map','location','natural_feature',1452,100.0,99.3,'TDB 자체 분류 1452건 교차집계: 부모 100.0% · 세부 99.3%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0203','map','location','natural_feature',1351,100.0,99.6,'TDB 자체 분류 1351건 교차집계: 부모 100.0% · 세부 99.6%. 세부까지 확정.','p4-record-type-v1'),
  ('legal_dong','면','map','location','legal_dong',1174,100.0,100.0,'TDB 자체 분류 1174건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ko','25','map','location','district',1080,100.0,100.0,'TDB 자체 분류 1080건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0102','map','location','natural_feature',1072,100.0,99.7,'TDB 자체 분류 1072건 교차집계: 부모 100.0% · 세부 99.7%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','79','hold',NULL,NULL,1003,95.1,95.1,'TDB 자체 분류 1003건 교차집계: 부모 일치율 95.1% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('ngii_gazetteer','C0202','map','location',NULL,903,100.0,98.6,'TDB 자체 분류 903건 교차집계: 부모 100.0% 확정, 세부는 98.6% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0403','map','location','natural_feature',874,100.0,99.9,'TDB 자체 분류 874건 교차집계: 부모 100.0% · 세부 99.9%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ja','80','map','location',NULL,839,99.9,97.9,'TDB 자체 분류 839건 교차집계: 부모 99.9% 확정, 세부는 97.9% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_en','80','map','location',NULL,777,99.9,97.7,'TDB 자체 분류 777건 교차집계: 부모 99.9% 확정, 세부는 97.7% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_zh_hant','80','map','location',NULL,737,99.9,97.7,'TDB 자체 분류 737건 교차집계: 부모 99.9% 확정, 세부는 97.7% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_zh_hans','80','map','location',NULL,726,99.9,97.5,'TDB 자체 분류 726건 교차집계: 부모 99.9% 확정, 세부는 97.5% 라 주지 않는다.','p4-record-type-v1'),
  ('heritage_khs','22','hold',NULL,NULL,725,98.3,98.3,'TDB 자체 분류 725건 교차집계: 부모 일치율 98.3% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0205','map','location',NULL,684,100.0,98.4,'TDB 자체 분류 684건 교차집계: 부모 100.0% 확정, 세부는 98.4% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_en','76','hold',NULL,NULL,640,96.4,34.2,'TDB 자체 분류 640건 교차집계: 부모 일치율 96.4% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('heritage_khs','16','map','location','heritage_site',613,100.0,100.0,'TDB 자체 분류 613건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','13','map','location','heritage_site',610,100.0,100.0,'TDB 자체 분류 610건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_es','80','map','location',NULL,607,99.7,97.4,'TDB 자체 분류 607건 교차집계: 부모 99.7% 확정, 세부는 97.4% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ja','78','map','location',NULL,599,99.0,96.2,'TDB 자체 분류 599건 교차집계: 부모 99.0% 확정, 세부는 96.2% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_en','78','map','location',NULL,592,99.3,97.0,'TDB 자체 분류 592건 교차집계: 부모 99.3% 확정, 세부는 97.0% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_zh_hant','78','map','location',NULL,581,99.5,96.7,'TDB 자체 분류 581건 교차집계: 부모 99.5% 확정, 세부는 96.7% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_zh_hans','78','map','location',NULL,532,99.2,96.1,'TDB 자체 분류 532건 교차집계: 부모 99.2% 확정, 세부는 96.1% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ja','76','hold',NULL,NULL,528,94.5,31.8,'TDB 자체 분류 528건 교차집계: 부모 일치율 94.5% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_fr','80','map','location',NULL,526,99.8,97.5,'TDB 자체 분류 526건 교차집계: 부모 99.8% 확정, 세부는 97.5% 라 주지 않는다.','p4-record-type-v1'),
  ('heritage_khs','24','hold',NULL,NULL,522,97.3,97.3,'TDB 자체 분류 522건 교차집계: 부모 일치율 97.3% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_zh_hans','76','hold',NULL,NULL,509,95.9,29.9,'TDB 자체 분류 509건 교차집계: 부모 일치율 95.9% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_zh_hant','76','hold',NULL,NULL,477,96.6,38.2,'TDB 자체 분류 477건 교차집계: 부모 일치율 96.6% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_fr','76','hold',NULL,NULL,434,97.2,40.6,'TDB 자체 분류 434건 교차집계: 부모 일치율 97.2% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('legal_dong','가','map','location','legal_dong',432,100.0,100.0,'TDB 자체 분류 432건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_en','85','hold',NULL,NULL,419,95.5,95.5,'TDB 자체 분류 419건 교차집계: 부모 일치율 95.5% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_fr','78','map','location',NULL,419,99.3,95.9,'TDB 자체 분류 419건 교차집계: 부모 99.3% 확정, 세부는 95.9% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_es','78','map','location',NULL,405,99.3,96.5,'TDB 자체 분류 405건 교차집계: 부모 99.3% 확정, 세부는 96.5% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_es','85','hold',NULL,NULL,399,98.0,98.0,'TDB 자체 분류 399건 교차집계: 부모 일치율 98.0% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_ru','76','hold',NULL,NULL,389,95.9,35.5,'TDB 자체 분류 389건 교차집계: 부모 일치율 95.9% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_de','78','hold',NULL,NULL,388,98.7,95.9,'TDB 자체 분류 388건 교차집계: 부모 일치율 98.7% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('heritage_khs','11','hold',NULL,NULL,372,74.2,74.2,'TDB 자체 분류 372건 교차집계: 부모 일치율 74.2% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_ja','85','hold',NULL,NULL,368,96.7,96.7,'TDB 자체 분류 368건 교차집계: 부모 일치율 96.7% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_ru','78','map','location',NULL,365,99.7,97.8,'TDB 자체 분류 365건 교차집계: 부모 99.7% 확정, 세부는 97.8% 라 주지 않는다.','p4-record-type-v1'),
  ('aihub_tour','편의오락','hold',NULL,NULL,362,97.8,72.1,'TDB 자체 분류 362건 교차집계: 부모 일치율 97.8% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_zh_hant','85','hold',NULL,NULL,359,95.0,95.0,'TDB 자체 분류 359건 교차집계: 부모 일치율 95.0% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('heritage_khs','18','hold',NULL,NULL,334,97.6,97.6,'TDB 자체 분류 334건 교차집계: 부모 일치율 97.6% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_de','76','hold',NULL,NULL,332,97.9,45.2,'TDB 자체 분류 332건 교차집계: 부모 일치율 97.9% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_es','76','hold',NULL,NULL,328,97.9,42.4,'TDB 자체 분류 328건 교차집계: 부모 일치율 97.9% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_de','85','hold',NULL,NULL,300,98.3,98.3,'TDB 자체 분류 300건 교차집계: 부모 일치율 98.3% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_zh_hans','85','hold',NULL,NULL,291,95.5,95.5,'TDB 자체 분류 291건 교차집계: 부모 일치율 95.5% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('wikidata','admin_region','map','location','admin_region',249,100.0,100.0,'TDB 자체 분류 249건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_en','75','map','location',NULL,249,100.0,98.8,'TDB 자체 분류 249건 교차집계: 부모 100.0% 확정, 세부는 98.8% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_zh_hant','75','map','location',NULL,249,100.0,98.8,'TDB 자체 분류 249건 교차집계: 부모 100.0% 확정, 세부는 98.8% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0506','map','location',NULL,248,100.0,98.4,'TDB 자체 분류 248건 교차집계: 부모 100.0% 확정, 세부는 98.4% 라 주지 않는다.','p4-record-type-v1'),
  ('legal_dong','읍','map','location','legal_dong',237,100.0,99.6,'TDB 자체 분류 237건 교차집계: 부모 100.0% · 세부 99.6%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0213','map','location','natural_feature',225,100.0,99.1,'TDB 자체 분류 225건 교차집계: 부모 100.0% · 세부 99.1%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ja','75','map','location',NULL,221,100.0,96.8,'TDB 자체 분류 221건 교차집계: 부모 100.0% 확정, 세부는 96.8% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_zh_hans','75','map','location',NULL,219,100.0,97.7,'TDB 자체 분류 219건 교차집계: 부모 100.0% 확정, 세부는 97.7% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0204','map','location','natural_feature',213,100.0,100.0,'TDB 자체 분류 213건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','55','map','location','heritage_site',200,100.0,100.0,'TDB 자체 분류 200건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_ru','82','hold',NULL,NULL,198,98.5,94.9,'TDB 자체 분류 198건 교차집계: 부모 일치율 98.5% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_ru','85','hold',NULL,NULL,195,98.5,98.5,'TDB 자체 분류 195건 교차집계: 부모 일치율 98.5% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('heritage_khs','17','hold',NULL,NULL,173,98.8,98.8,'TDB 자체 분류 173건 교차집계: 부모 일치율 98.8% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_es','79','map','location',NULL,171,100.0,97.7,'TDB 자체 분류 171건 교차집계: 부모 100.0% 확정, 세부는 97.7% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0304','map','location','natural_feature',161,100.0,100.0,'TDB 자체 분류 161건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('tourapi_de','79','map','location',NULL,160,100.0,96.3,'TDB 자체 분류 160건 교차집계: 부모 100.0% 확정, 세부는 96.3% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_fr','79','hold',NULL,NULL,158,98.7,94.9,'TDB 자체 분류 158건 교차집계: 부모 일치율 98.7% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_fr','85','hold',NULL,NULL,158,93.7,93.7,'TDB 자체 분류 158건 교차집계: 부모 일치율 93.7% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_es','75','map','location',NULL,155,100.0,97.4,'TDB 자체 분류 155건 교차집계: 부모 100.0% 확정, 세부는 97.4% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ru','80','map','location',NULL,153,100.0,98.7,'TDB 자체 분류 153건 교차집계: 부모 100.0% 확정, 세부는 98.7% 라 주지 않는다.','p4-record-type-v1'),
  ('heritage_khs','15','map','location','heritage_site',145,100.0,100.0,'TDB 자체 분류 145건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1'),
  ('heritage_khs','25','hold',NULL,NULL,136,86.8,86.8,'TDB 자체 분류 136건 교차집계: 부모 일치율 86.8% — 코드가 유형을 정하지 못한다.','p4-record-type-v1'),
  ('tourapi_de','75','map','location',NULL,133,99.2,97.7,'TDB 자체 분류 133건 교차집계: 부모 99.2% 확정, 세부는 97.7% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_fr','75','map','location',NULL,130,99.2,95.4,'TDB 자체 분류 130건 교차집계: 부모 99.2% 확정, 세부는 95.4% 라 주지 않는다.','p4-record-type-v1'),
  ('legal_dong','통칭','map','location',NULL,125,100.0,98.4,'TDB 자체 분류 125건 교차집계: 부모 100.0% 확정, 세부는 98.4% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ru','75','map','location',NULL,124,100.0,98.4,'TDB 자체 분류 124건 교차집계: 부모 100.0% 확정, 세부는 98.4% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_ru','79','map','location','shopping',115,100.0,99.1,'TDB 자체 분류 115건 교차집계: 부모 100.0% · 세부 99.1%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0502','map','location',NULL,102,100.0,98.0,'TDB 자체 분류 102건 교차집계: 부모 100.0% 확정, 세부는 98.0% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0402','map','location','natural_feature',102,100.0,99.0,'TDB 자체 분류 102건 교차집계: 부모 100.0% · 세부 99.0%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','C0203','map','location','tourist_spot',99,100.0,99.0,'TDB 자체 분류 99건 교차집계: 부모 100.0% · 세부 99.0%. 세부까지 확정.','p4-record-type-v1'),
  ('ngii_gazetteer','B0101','map','location',NULL,97,100.0,97.9,'TDB 자체 분류 97건 교차집계: 부모 100.0% 확정, 세부는 97.9% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0309','map','location',NULL,95,100.0,96.8,'TDB 자체 분류 95건 교차집계: 부모 100.0% 확정, 세부는 96.8% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0301','map','location',NULL,78,100.0,96.2,'TDB 자체 분류 78건 교차집계: 부모 100.0% 확정, 세부는 96.2% 라 주지 않는다.','p4-record-type-v1'),
  ('tourapi_de','80','map','location',NULL,76,100.0,98.7,'TDB 자체 분류 76건 교차집계: 부모 100.0% 확정, 세부는 98.7% 라 주지 않는다.','p4-record-type-v1'),
  ('ngii_gazetteer','B0404','map','location','natural_feature',64,100.0,100.0,'TDB 자체 분류 64건 교차집계: 부모 100.0% · 세부 100.0%. 세부까지 확정.','p4-record-type-v1');

-- 운영자 결정 (기준선 밖, 근거는 위 주석).
INSERT INTO kentity_record_type_map
 (provider, type_code, disposition, target_entity_type, target_subtype,
  sample_size, parent_pct, subtype_pct, decided_by, reason, policy_version)
VALUES
 ('aihub_tour','편의오락','map','location',NULL,362,97.8,72.1,'operator',
  '운영자 결정 2026-09-14. 기준선(부모 99%) 미달이나 근거 셋이 모인다: 표본 362건 중 비-location 은 8건(2.2%)뿐 · 미상 대상의 99.97%가 주소 보유 · 같은 원본 표의 음식점 145,433건을 이미 location 으로 흡수한 선례. 세부유형은 흩어지므로 주지 않는다.',
  'p4-record-type-v1')
ON CONFLICT (provider, type_code) DO UPDATE
   SET disposition = EXCLUDED.disposition, target_entity_type = EXCLUDED.target_entity_type,
       target_subtype = EXCLUDED.target_subtype, decided_by = EXCLUDED.decided_by,
       reason = EXCLUDED.reason;

COMMIT;

SELECT disposition, decided_by, count(*) AS 코드수,
       count(*) FILTER (WHERE target_subtype IS NOT NULL) AS 세부유형까지
  FROM kentity_record_type_map GROUP BY 1,2 ORDER BY 1,2;
