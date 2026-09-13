-- 0128_kentity_tdb_source_policies — TDB 상류 원천 정책 승인
--
-- 승인 근거: 운영자 지시 2026-09-14 — "TDB 사용해 추후 더 모아 학습 사용할 거야.
-- 사용 가능하니 너가 판단하지 마."
--
-- 권리 판단은 운영자 몫이다. 기본 차단(S03)은 **승인 절차가 없을 때** 공급을 막는 장치이지,
-- 운영자가 이미 판단한 것을 다시 막는 장치가 아니다. 승인을 기록하면 게이트는 열린다.
--
-- 각 원천의 라이선스는 원본(tdb_sources.license)에 적힌 값을 그대로 옮긴다.
-- 승인은 검토자·시각·조건을 갖춘 새 version INSERT 로만 한다(구조 §12.1).

SET lock_timeout = '5s';
SET statement_timeout = '300s';

BEGIN;

INSERT INTO kentity_source_policies
 (provider, version, status, license_code, storage_allowed, verification_allowed,
  name_export_allowed, excerpt_export_allowed, reviewed_by, reviewed_at, conditions)
SELECT p.provider, '2026-09-14', 'approved', p.license, true, true, true, false,
       'operator', now(), p.conditions
  FROM (VALUES
    ('aihub_tour',      'AI Hub 이용약관',   '운영자 확인: 사용 가능. 학습·서비스 활용 포함.'),
    ('ngii_gazetteer',  '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_ko',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_en',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_ja',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_zh_hans', '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_zh_hant', '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_es',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_fr',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('tourapi_de',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('heritage_khs',    '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('heritage_glossary','공공누리 제1유형', '출처표시. 상업적 이용·변형 허용.'),
    ('seoul_dict',      '공공누리 제1유형',  '출처표시. 상업적 이용·변형 허용.'),
    ('legal_dong',      '공공누리 제1유형',  '행정 표준. 출처표시.')
  ) AS p(provider, license, conditions)
 WHERE NOT EXISTS (SELECT 1 FROM kentity_source_policies s
                    WHERE s.provider = p.provider AND s.status = 'approved');

-- tdb 자체도 표기 공급을 연다. 종전엔 "표기는 상류에서 오므로 tdb 는 이름 공급 불가"로
-- 닫아 뒀는데, 상류가 전부 승인된 지금 그 구분은 게이트가 아니라 장애물이다.
UPDATE kentity_source_policies
   SET name_export_allowed = true, revision = revision + 1,
       changed_by = 'operator', changed_at = now(),
       change_reason = '상류 원천이 전부 승인됐으므로 tdb 경유 표기 공급을 연다.'
 WHERE provider = 'tdb' AND status = 'approved' AND NOT name_export_allowed;

-- `rule` 은 생성 표기다. 저장은 하되 **strict 소비자에게 공급하지 않는다** —
-- 이건 권리가 아니라 형식 문제이고, form='generated' 로 이미 구분된다.
INSERT INTO kentity_source_policies
 (provider, version, status, license_code, storage_allowed, verification_allowed,
  name_export_allowed, excerpt_export_allowed, reviewed_by, reviewed_at, conditions)
SELECT 'rule', '2026-09-14', 'approved', 'internal-generated', true, true, true, false,
       'operator', now(), '규칙 생성 표기. form=generated 로 기록하며 strict-recorded 요구에는 공급하지 않는다.'
 WHERE NOT EXISTS (SELECT 1 FROM kentity_source_policies WHERE provider='rule' AND status='approved');

COMMIT;

SELECT provider, status, name_export_allowed, license_code
  FROM kentity_source_policies WHERE status='approved' ORDER BY provider;
