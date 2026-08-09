-- 0111 — 저신뢰 en 2건 승격 + ko 따옴표 불균형 1건 교정 (2026-08-09)
--
-- 45차 §3-1·§3-3 의 미해결분 중 **근거가 확실한 것만** 처리한다. 나머지는 왜 안 했는지
-- 아래에 남긴다 — "처리 못 함"과 "일부러 안 함"은 다르다.
--
-- ═══ ① 저신뢰 en 2건 — 형제 locale 에 이미 더 높은 값이 있다 ═══
--
-- 45차 §2 에서 매큔-라이샤워(MR) 로마자 5건의 파생값을 되돌렸지만 en 원본은 안 건드렸다.
-- 이번에 소스를 보니 **5건 전부 `wikidata-label`(prio 5)** 이다. 우리가 만든 값이 아니라
-- 위키데이터가 MR 로 갖고 있는 것이고, **이 저장소에는 개정 로마자 변환기가 없다**
-- (internal/kdb/hangul 은 검증 전용). 즉 손으로 로마자를 지어내는 것 외에 방법이 없다.
--
-- 그래서 **이미 DB 안에 있는 더 높은 값만 승격**한다. `local-usage`(prio 1, 검색그라운드
-- 현지표기·확정)가 형제 라틴 locale 에 있는데 en 이 prio 5 를 들고 있는 상태였다:
--
--   양수경  en=Yang Su-kyŏng(wikidata,5) ← id·pt_br=Yang Soo Kyung(local-usage,1) 두 칸 일치
--   이수형  en=Yi Suhyŏng(wikidata,5)    ← id=Lee Su-hyung(local-usage,1)
--
-- ★`이수형` 의 vi 는 일부러 안 썼다. `Lý Tú Huỳnh` 는 로마자가 아니라 한자 李秀亨 의
--   **베트남 한자음**이다 — en 에 넣으면 다른 사람 이름이 된다. **vi 는 로마자 공급원이
--   아니다.** 같은 계열 208건을 일괄 승격하면 이 함정을 그대로 밟는다(아래 미처리 참고).
--
-- 안영민·양현아·유성원 3건은 **안 건드린다.** 형제 locale 에도 local-usage 가 없어
-- 승격할 값이 없고, 내가 로마자를 지어내면 wikidata(prio5)를 근거 없는 값으로 덮는 것이다.
-- 오너 원칙 "빈칸 > 틀린값" 은 여기서 "근거 있는 값 > 내가 지어낸 값"으로 읽어야 한다.
--
-- ═══ ② ko 따옴표 불균형 1건 ═══
--
-- `2026 LE SSERAFIM TOUR ‘PUREFLOW’’` — 여는 `‘` 하나에 닫는 `’` 가 둘이다. 명백한 오타다.
--
-- ★어제 라틴 원제 승계 레인이 이 값을 **8개 locale 로 퍼뜨렸다**(전부 source=romanization).
--   ko 만 고치면 파생 8칸이 옛 오타를 든 채 남는다 — 44차 §1 의 "내가 만든 것이 무엇을
--   먹이는가"가 이번엔 내 레인에 적용된다. 같은 트랜잭션에서 함께 고친다.
--
-- `2024 Hongseok Fanmeeting［Photo by Hongseok］` 의 전각 대괄호는 **안 고친다.**
-- source_urls 가 없어 공식 표기를 확인할 방법이 없고, 팬미팅 제목의 전각 괄호는 실제
-- 스타일링일 수 있다. 불균형 따옴표처럼 "그 자체로 틀린" 것이 아니다.

BEGIN;

-- ① 저신뢰 en 승격 (값 출처는 전부 DB 안의 local-usage)
UPDATE kwave_entities
   SET canonical_en = 'Yang Soo Kyung', canonical_en_source = 'local-usage', updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND canonical_ko = '양수경' AND canonical_en = 'Yang Su-kyŏng'
   AND canonical_id = 'Yang Soo Kyung' AND canonical_pt_br = 'Yang Soo Kyung';

UPDATE kwave_entities
   SET canonical_en = 'Lee Su-hyung', canonical_en_source = 'local-usage', updated_at = now()
 WHERE status='active' AND operator_locked = false
   AND canonical_ko = '이수형' AND canonical_en = 'Yi Suhyŏng'
   AND canonical_id = 'Lee Su-hyung';

-- ② ko 오타 + 그 값을 승계한 파생 8칸을 함께 교정
CREATE TEMP TABLE pf AS
SELECT id, canonical_ko AS bad, regexp_replace(canonical_ko, '’’$', '’') AS good
  FROM kwave_entities
 WHERE status='active' AND operator_locked = false AND canonical_ko LIKE '%PUREFLOW’’';

UPDATE kwave_entities e SET canonical_en      = p.good FROM pf p WHERE e.id=p.id AND e.canonical_en      = p.bad;
UPDATE kwave_entities e SET canonical_ja      = p.good FROM pf p WHERE e.id=p.id AND e.canonical_ja      = p.bad;
UPDATE kwave_entities e SET canonical_zh      = p.good FROM pf p WHERE e.id=p.id AND e.canonical_zh      = p.bad;
UPDATE kwave_entities e SET canonical_zh_hant = p.good FROM pf p WHERE e.id=p.id AND e.canonical_zh_hant = p.bad;
UPDATE kwave_entities e SET canonical_vi      = p.good FROM pf p WHERE e.id=p.id AND e.canonical_vi      = p.bad;
UPDATE kwave_entities e SET canonical_es      = p.good FROM pf p WHERE e.id=p.id AND e.canonical_es      = p.bad;
UPDATE kwave_entities e SET canonical_id      = p.good FROM pf p WHERE e.id=p.id AND e.canonical_id      = p.bad;
UPDATE kwave_entities e SET canonical_pt_br   = p.good FROM pf p WHERE e.id=p.id AND e.canonical_pt_br   = p.bad;
-- ko 는 맨 마지막 — 먼저 바꾸면 위 비교(= p.bad)가 어긋난다.
UPDATE kwave_entities e SET canonical_ko = p.good, updated_at = now() FROM pf p WHERE e.id = p.id;

DO $$
DECLARE r record;
BEGIN
  FOR r IN
    SELECT canonical_ko, canonical_en,
           (canonical_ko LIKE '%’’')::int AS 오타잔존,
           (SELECT count(*) FROM (VALUES (canonical_ja),(canonical_zh),(canonical_zh_hant),
                                         (canonical_vi),(canonical_es),(canonical_id),
                                         (canonical_pt_br)) t(v) WHERE t.v LIKE '%’’') AS 파생잔존
      FROM kwave_entities
     WHERE canonical_ko IN ('양수경','이수형') OR canonical_ko LIKE '%PUREFLOW%'
     ORDER BY canonical_ko
  LOOP
    RAISE NOTICE '0111: % · en=% · ko오타잔존=% · 파생잔존=%',
                 r.canonical_ko, r.canonical_en, r.오타잔존, r.파생잔존;
  END LOOP;
END $$;

COMMIT;
