-- 0103_kdb_bigsmile_en_revert — 빅스마일엔터테인먼트 en 을 빈칸으로 되돌린다(1차 출처 부재).
--
-- 0101 이 넣은 "Big Smile Entertainment" 는 3자 출처 기반이었고, 0102 에서 evidence provider
-- 를 'third-party' 로 낮춰 표시만 해뒀다. 추가 조사 결과 값을 유지할 근거가 없다고 판단한다.
--
-- ★1차 출처가 존재하지 않는다. 가능한 경로를 전부 시도했다:
--   · 공식 도메인 bigsmile-ent.com — DNS 에서 소멸(getaddrinfo ENOTFOUND)
--   · web.archive.org — 스냅샷 **0건**(archived_snapshots: {})
--   · 공식 인스타그램 @bigsmile_ent — 표시명이 "빅스마일 엔터테인먼트" 로 한글뿐
--   · 회사가 직접 등록한 jobkorea 기업정보 — 영문 회사명 항목 없음
--
-- ★그리고 근처의 유일한 영문 대문자 표기는 **다른 회사 것**이다. @bigsmiles_ent
-- ("BIG SMILE ENTERTAINMENT")·facebook/BigSmileEntertainment 는 미국 매사추세츠 Manchester
-- 소재 DJ·공연 회사다(eventective/yourtheater411 등록). 한국 배우 매니지먼트사와 상호만
-- 같다. 이건 "근거 없음"보다 나쁜 상태 — 적극적인 오매칭 위험이고, 이 저장소가 가드까지
-- 만들어 막아온 유형이다(wikidata SearchAndFetch 의 박보검→허성진 사례).
--
-- ★값의 형태 자체도 틀렸을 공산이 크다. 0102 에서 1차 출처로 확정한 10곳이 **전부 대문자**
-- 였다(GREAT M · BEATS ENTERTAINMENT · IOKENM · EVERMORE ENTERTAINMENT · WAVE ENTERTAINMENT ·
-- INNIT ENTERTAINMENT · J,WIDE-COMPANY · CHOI CREATIVE LAB · PACIFIC MUSIC GROUP ·
-- AM ENTERTAINMENT). title case 인 "Big Smile Entertainment" 는 단어가 맞더라도 표기가 틀렸을
-- 가능성이 높은데, 대문자로 고치면 그건 추측이고 하필 그 대문자형이 위의 미국 회사 것이다.
--
-- 비용은 1건 5셀(en + Latin 4종)이다. 그리고 0100 의 입력지문 규칙 덕에 자기치유된다 —
-- 앵커나 wikidata 항목이 생겨 입력이 바뀌면 자동으로 다시 집힌다.

BEGIN;

UPDATE kwave_entities
   SET canonical_en        = NULL, canonical_en_source    = NULL,
       canonical_vi        = CASE WHEN canonical_vi    = 'Big Smile Entertainment' THEN NULL ELSE canonical_vi    END,
       canonical_vi_source = CASE WHEN canonical_vi    = 'Big Smile Entertainment' THEN NULL ELSE canonical_vi_source END,
       canonical_es        = CASE WHEN canonical_es    = 'Big Smile Entertainment' THEN NULL ELSE canonical_es    END,
       canonical_es_source = CASE WHEN canonical_es    = 'Big Smile Entertainment' THEN NULL ELSE canonical_es_source END,
       canonical_id        = CASE WHEN canonical_id    = 'Big Smile Entertainment' THEN NULL ELSE canonical_id    END,
       canonical_id_source = CASE WHEN canonical_id    = 'Big Smile Entertainment' THEN NULL ELSE canonical_id_source END,
       canonical_pt_br        = CASE WHEN canonical_pt_br = 'Big Smile Entertainment' THEN NULL ELSE canonical_pt_br END,
       canonical_pt_br_source = CASE WHEN canonical_pt_br = 'Big Smile Entertainment' THEN NULL ELSE canonical_pt_br_source END,
       updated_at = now()
 WHERE canonical_ko = '빅스마일엔터테인먼트'
   AND entity_type  = 'agency'
   AND operator_locked = false
   AND canonical_en = 'Big Smile Entertainment'
   AND canonical_en_source = 'local-search';

-- 값이 사라졌으므로 그 값의 근거도 지운다. 대신 "왜 비웠는지"를 남긴다 — 다음 사람이
-- 같은 조사를 처음부터 반복하지 않도록.
DELETE FROM kwave_kdb_evidence_refs r
 USING kwave_entities e
 WHERE r.entity_id = e.id AND r.lane = 'agency-en-search'
   AND e.canonical_ko = '빅스마일엔터테인먼트';

INSERT INTO kwave_kdb_evidence_refs (entity_id, lane, url, title, provider, snippet)
SELECT e.id, 'agency-en-unresolved', 'https://www.instagram.com/bigsmile_ent/',
       '빅스마일엔터테인먼트 — 영문 표기 확인 불가', 'none',
       '공식 도메인 bigsmile-ent.com DNS 소멸 · 웨이백 스냅샷 0건 · 공식 IG 표시명 한글뿐 · '
       || 'jobkorea 기업정보에 영문 없음. 검색에 나오는 "BIG SMILE ENTERTAINMENT"(@bigsmiles_ent)는 '
       || '미국 Manchester, MA 소재 동명 DJ·공연 회사로 별개 법인이다. 1차 출처가 생기기 전까지 빈칸 유지(2026-08-06).'
  FROM kwave_entities e
 WHERE e.canonical_ko = '빅스마일엔터테인먼트' AND e.entity_type = 'agency'
ON CONFLICT (entity_id, url) DO NOTHING;

COMMIT;
