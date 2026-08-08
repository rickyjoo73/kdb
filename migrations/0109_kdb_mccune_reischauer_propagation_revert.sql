-- 0109 — 매큔-라이샤워 로마자가 라틴 4종으로 퍼진 14칸을 되돌린다 (2026-08-08)
--
-- ★무엇이 일어났나
-- 같은 날 라틴 원제 승계 가드를 `^[ -~]+$`(ASCII 전용) → 라틴 스크립트 허용목록으로 넓혔다.
-- 곡선따옴표(’) 하나 때문에 막혀 있던 18건을 열기 위한 확장이고 그건 의도대로 됐다.
--
-- 그런데 종전 ASCII 가드가 **우연히** 하고 있던 일이 하나 더 있었다: 매큔-라이샤워(MR)
-- 로마자의 ŏ/ŭ 를 걸러 canonical_en → vi/es/id/pt_br 전파에서 빼고 있었던 것이다.
-- 가드를 넓히면서 그 우연한 보호가 사라졌고, 한 번의 전파로 5건이 4종에 퍼졌다.
--
-- ★왜 되돌리나 — 어느 로마자가 옳으냐 이전에, 전파가 **한 인물을 locale 마다 다른 이름으로**
-- 만들었다. 더 신뢰되는 소스가 든 칸은 불가침이라 안 덮이고 빈칸만 MR 로 찼기 때문이다:
--
--   양수경  en=Yang Su-kyŏng · vi/es=Yang Su-kyŏng · id/pt_br=Yang Soo Kyung(local-usage)
--   이수형  en=Yi Suhyŏng    · pt_br=Yi Suhyŏng    · id=Lee Su-hyung(local-usage)
--                                                  · vi=Lý Tú Huỳnh(wikidata-label)
--
-- 게다가 이 DB 의 표준은 개정 로마자다(internal/kdb/romanize.go 머리말: 송강호=Song Kang-ho).
-- `안영민 → Yŏng-min An` 은 성·이름 순서까지 뒤집혀 있다.
--
-- ★범위 — canonical_en 은 건드리지 않는다. en 값 자체를 고치는 건 다른 레인의 판단이다
-- (44차 §4 가 선행 레인 오염에 그은 선과 같다). 여기서는 **내가 만든 파생값만** 되돌린다.
-- 빈칸으로 두면 en 이 개정 로마자로 고쳐지는 즉시 전파 레인이 자동으로 다시 채운다.
--
-- 재발 방지는 코드 쪽 `latinPropagateSQLGuard` — 우연한 보호를 의도적 가드로 바꿨다.
--
-- 대상: source='romanization' 이고 값이 canonical_en 과 동일한 칸만 = 실측 14칸.
-- (더 신뢰되는 소스가 든 칸은 조건에서 자동 제외된다.)

BEGIN;

CREATE TEMP TABLE mr_revert_before AS
SELECT id, canonical_ko, canonical_en,
       canonical_vi, canonical_es, canonical_id, canonical_pt_br
  FROM kwave_entities
 WHERE status = 'active' AND canonical_en ~ '[ŏŭŎŬ]';

UPDATE kwave_entities
   SET canonical_vi = NULL, canonical_vi_source = NULL, updated_at = now()
 WHERE status = 'active' AND canonical_en ~ '[ŏŭŎŬ]'
   AND canonical_vi_source = 'romanization' AND canonical_vi = canonical_en;

UPDATE kwave_entities
   SET canonical_es = NULL, canonical_es_source = NULL, updated_at = now()
 WHERE status = 'active' AND canonical_en ~ '[ŏŭŎŬ]'
   AND canonical_es_source = 'romanization' AND canonical_es = canonical_en;

UPDATE kwave_entities
   SET canonical_id = NULL, canonical_id_source = NULL, updated_at = now()
 WHERE status = 'active' AND canonical_en ~ '[ŏŭŎŬ]'
   AND canonical_id_source = 'romanization' AND canonical_id = canonical_en;

UPDATE kwave_entities
   SET canonical_pt_br = NULL, canonical_pt_br_source = NULL, updated_at = now()
 WHERE status = 'active' AND canonical_en ~ '[ŏŭŎŬ]'
   AND canonical_pt_br_source = 'romanization' AND canonical_pt_br = canonical_en;

-- 되돌린 결과를 남긴다 — 집계만 보면 무엇이 비었는지 알 수 없다(44차 §1 의 교훈).
DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT b.canonical_ko, b.canonical_en,
           (b.canonical_vi    IS NOT NULL AND a.canonical_vi    IS NULL)::int
         + (b.canonical_es    IS NOT NULL AND a.canonical_es    IS NULL)::int
         + (b.canonical_id    IS NOT NULL AND a.canonical_id    IS NULL)::int
         + (b.canonical_pt_br IS NOT NULL AND a.canonical_pt_br IS NULL)::int AS cleared
      FROM mr_revert_before b JOIN kwave_entities a ON a.id = b.id
     ORDER BY b.canonical_ko
  LOOP
    RAISE NOTICE '0109 revert: % (en=%) — 되돌린 칸 %', r.canonical_ko, r.canonical_en, r.cleared;
  END LOOP;
END $$;

COMMIT;
