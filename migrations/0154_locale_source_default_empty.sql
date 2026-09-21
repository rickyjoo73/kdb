-- 0154: 로케일 출처 컬럼의 기본값을 'wikidata-label' → '' 로 (2026-09-21).
--
-- ★왜. 0049 가 출처 컬럼 8개를 **추가하면서 기존 행을 채우려고** DEFAULT 'wikidata-label'
--   을 줬다. 그 기본값이 그대로 남아 이후의 모든 INSERT 에 붙었고, 개체를 만드는 세 경로는
--   출처를 넣지 않으므로 **새 개체는 값이 없는데 8칸이 전부 「위키데이터에서 왔다」고
--   주장했다.** 실측 9,527행 · 약 5만 4천 칸(locale_source_default.go 주석 참고).
--
-- ★이 파일은 **배포 경로를 절대 막지 않는다.** kwave_entities 는 레인이 쉬지 않고 쓰는
--   바쁜 테이블이라 ALTER 가 배타 잠금을 못 얻을 수 있다(다른 에이전트가 CHECK 제약을
--   12번 시도하고 접은 자리다). 여기서 실패하면 이 파일이 원장에 안 적혀 **다음 배포마다
--   다시 시도되고, 계속 실패하면 모든 배포가 멈춘다.** 그래서 잠금을 2초만 기다리고, 못
--   얻으면 NOTICE 만 남기고 넘어간다. 그래도 새 거짓은 막힌다 — 세 INSERT 가 출처를
--   '' 로 명시하도록 코드를 함께 고쳤다.
--
-- ★이미 쌓인 칸의 정리는 여기서 하지 않는다. 9,527행을 한 번에 잠그면 같은 이유로
--   배포를 막을 수 있다. 운영에서 작은 묶음으로 따로 돌린다(MONITOR 45회차).
DO $$
BEGIN
  SET LOCAL lock_timeout = '2s';
  ALTER TABLE kwave_entities
    ALTER COLUMN canonical_en_source      SET DEFAULT '',
    ALTER COLUMN canonical_ja_source      SET DEFAULT '',
    ALTER COLUMN canonical_vi_source      SET DEFAULT '',
    ALTER COLUMN canonical_zh_source      SET DEFAULT '',
    ALTER COLUMN canonical_zh_hant_source SET DEFAULT '',
    ALTER COLUMN canonical_es_source      SET DEFAULT '',
    ALTER COLUMN canonical_id_source      SET DEFAULT '',
    ALTER COLUMN canonical_pt_br_source   SET DEFAULT '';
  RAISE NOTICE '0154: 로케일 출처 기본값을 '''' 로 바꿨다';
EXCEPTION WHEN lock_not_available THEN
  RAISE NOTICE '0154: 잠금을 못 얻어 기본값 변경을 건너뛴다 — 코드의 INSERT 가 출처를 명시하므로 새 거짓은 막힌다';
END $$;
