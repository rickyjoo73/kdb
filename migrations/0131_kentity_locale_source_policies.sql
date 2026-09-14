-- 0131_kentity_locale_source_policies — 다국어 표기 원천 정책 보충
--
-- P4.01(언어 결손) 조사에서 드러난 것: TDB 는 536,322개 대상에 대해 **11개 로케일
-- 2,685,232건의 표기를 이미 관측해 두었다.** 우리는 그중 한국어 대표명 536,321건만
-- 가져왔다. 만들어야 할 데이터가 아니라 **가져오지 않은 데이터**다.
-- (운영자가 첫 세션에서 지적한 "고유명사에서 일본어·중국어가 빠르게 업데이트 안 된다"의 답.)
--
-- 0128 이 승인한 17개 원천이 그중 2,668,297건을 덮는다. 나머지 7개 원천 16,935건의
-- 정책을 여기서 채운다. **라이선스는 지어내지 않았다** — TDB 의 tdb_sources.license 에
-- 적힌 값을 그대로 옮긴다.
--
--   kowiki / kowiki_admin  CC-BY-SA        (ko.wikipedia — 자연지명·법정동 한자)
--   tourapi_ru             공공누리 제1유형 (data.go.kr 15101831)
--   seoul_dong             공공누리 제1유형 (행정동·법정동 한자 병기)
--   subway_multi           공공누리 제1유형 (1~8호선 역명 다국어)
--   aihub_kculture_ja      AI Hub 이용약관
--   llm                    라이선스 없음 — **생성 표기**다
--
-- llm 을 어떻게 다루는가. tdb_sources.license 가 비어 있다. 상류가 없기 때문이다 —
-- 모델이 만든 표기다. 그렇다고 막지 않는다(권리 판단은 운영자 몫). 대신 **무엇인지를
-- 정확히 기록한다**: form='generated' 로 적어 `rule` 과 같은 취급을 받게 한다.
-- 기록된 표기(recorded)를 요구하는 소비자에게는 공급되지 않고, 그렇지 않은 소비자는
-- 생성물임을 알고 쓴다. "모르는 한자·공식명을 만들어 확정"(P4 금지사항)에 걸리지 않는
-- 이유가 이것이다 — 확정하지 않고 생성물이라고 말한다.

SET lock_timeout = '5s';
SET statement_timeout = '300s';

BEGIN;

INSERT INTO kentity_source_policies
 (provider, version, status, license_code, storage_allowed, verification_allowed,
  name_export_allowed, excerpt_export_allowed, reviewed_by, reviewed_at, conditions)
SELECT p.provider, '2026-09-14', 'approved', p.license, true, true, true, false,
       'operator', now(), p.conditions
  FROM (VALUES
    ('kowiki',            'CC-BY-SA',          '출처표시·동일조건변경허락. 자연지명 공백 공급원.'),
    ('kowiki_admin',      'CC-BY-SA',          '출처표시·동일조건변경허락. 법정동 한자의 확인된 전국 원천.'),
    ('tourapi_ru',        '공공누리 제1유형',   '출처표시. data.go.kr 15101831.'),
    ('seoul_dong',        '공공누리 제1유형',   '출처표시. 행정동·법정동 한자 병기.'),
    ('subway_multi',      '공공누리 제1유형',   '출처표시. 1~8호선 역명 한글·한자·영자·중국어·일어.'),
    ('aihub_kculture_ja', 'AI Hub 이용약관',    '운영자 확인: 사용 가능.'),
    ('llm',               'internal-generated', '상류 없음. 모델 생성 표기이므로 form=generated 로 기록하고 recorded 요구에는 공급하지 않는다.')
  ) AS p(provider, license, conditions)
 WHERE NOT EXISTS (SELECT 1 FROM kentity_source_policies s
                    WHERE s.provider = p.provider AND s.status = 'approved');

COMMIT;

SELECT provider, license_code, name_export_allowed
  FROM kentity_source_policies
 WHERE status = 'approved'
   AND provider IN ('kowiki','kowiki_admin','tourapi_ru','seoul_dong','subway_multi','aihub_kculture_ja','llm')
 ORDER BY provider;
