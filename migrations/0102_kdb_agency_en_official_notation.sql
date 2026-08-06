-- 0102_kdb_agency_en_official_notation — 소속사 영문명을 각 회사 **공식 사이트의 자기 표기**로 정정.
--
-- ★0101 의 결함(오너 지적, 2026-08-06): 0101 은 Kpop Wiki/kpopping 같은 3자 위키의 표기를
-- 섞어 썼고, 대소문자를 내가 임의로 title case 로 정규화했다. 기준은 그게 아니라 **그 회사가
-- 자기 사이트에 쓴 표기**여야 한다. 1차 출처로 전건 재확인한 결과 11건 중 9건이 틀렸다.
--
--   그레이트엠엔터테인먼트  Great M Entertainment   → GREAT M
--   비츠엔터테인먼트        BEATS Entertainment     → BEATS ENTERTAINMENT
--   에버모어엔터테인먼트    Evermore Entertainment  → EVERMORE ENTERTAINMENT
--   에이엠엔터테인먼트      AM Entertainment        → AM ENTERTAINMENT
--   웨이브엔터테인먼트      Wave Entertainment      → WAVE ENTERTAINMENT
--   이닛엔터테인먼트        INNIT Entertainment     → INNIT ENTERTAINMENT
--   제이와이드컴퍼니        J,WIDE-Company          → J,WIDE-COMPANY
--   초이크리에이티브랩      Choi Creative Lab       → CHOI CREATIVE LAB
--   퍼시픽뮤직그룹          Pacific Music Group     → PACIFIC MUSIC GROUP
--   아이오케이이엔엠        IOKENM                  → 변경 없음(공식 로고 alt 와 이미 일치)
--
-- 확인 방법: 각 사 공식 도메인의 <title> · 로고 alt · 푸터 저작권 문구를 직접 조회.
--   greatment.co.kr        <title>그레이트엠 엔터테인먼트 GREAT M</title> · 푸터 "© GREAT M"
--                          (단 로고 alt 는 "GREATM entertainment" 로 띄어쓰기가 다르다 — 회사
--                           자체 표기가 갈리는 유일한 건. <title>·저작권 2곳이 일치하는
--                           "GREAT M" 을 택했다. prio 7 이라 나중에 교정 가능.)
--   evmore.kr              본문·저작권 "EVERMORE ENTERTAINMENT"
--   waveseoul.com          로고·헤더·회사소개·푸터 전부 "WAVE ENTERTAINMENT"
--   innitent.com           <title>INNIT ENTERTAINMENT</title> · 로고 alt 동일
--   choicreativelab.com    <title>CHOI CREATIVE LAB</title> · 로고 alt 동일 · 저작권 동일
--   iokenm.com             로고 alt "IOKENM"
--   jwide.co.kr            푸터 "J,WIDE-COMPANY. ALL RIGHTS RESERVED"
--   team-pmg.com           로고·헤딩 "PACIFIC MUSIC GROUP"(법인명 Pacific Music Group Ltd.)
--   비츠엔터테인먼트       공식 사이트 폐쇄 → 공식 X 계정 표시명 "BEATS ENTERTAINMENT"
--
-- ★빅스마일엔터테인먼트는 손대지 않는다. 공식 도메인(bigsmile-ent.com)이 DNS 에서 사라졌고
-- 공식 인스타그램 표시명도 한글뿐이라 **1차 출처가 존재하지 않는다**. 현재 값
-- "Big Smile Entertainment" 는 3자 출처 기반이므로 evidence provider 를 'third-party' 로
-- 낮춰 표시만 정정한다 — 어느 값이 1차 확인분이고 어느 게 아닌지 구분되게 남긴다.
--
-- Latin locale(vi/es/id/pt_br)은 DrainRomanizeLatin 이 canonical_en 을 복사한 것이라
-- 옛 값이 그대로 남아 있다. 같은 트랜잭션에서 함께 정정한다.

BEGIN;

CREATE TEMP TABLE _fix (ko text PRIMARY KEY, old_en text, new_en text, url text) ON COMMIT DROP;

INSERT INTO _fix (ko, old_en, new_en, url) VALUES
  ('그레이트엠엔터테인먼트', 'Great M Entertainment',   'GREAT M',              'http://greatment.co.kr/'),
  ('비츠엔터테인먼트',       'BEATS Entertainment',     'BEATS ENTERTAINMENT',  'https://x.com/BEATS_ent_twt'),
  ('에버모어엔터테인먼트',   'Evermore Entertainment',  'EVERMORE ENTERTAINMENT','https://evmore.kr/'),
  ('에이엠엔터테인먼트',     'AM Entertainment',        'AM ENTERTAINMENT',     'https://www.youtube.com/@ament_official'),
  ('웨이브엔터테인먼트',     'Wave Entertainment',      'WAVE ENTERTAINMENT',   'https://waveseoul.com/about/'),
  ('이닛엔터테인먼트',       'INNIT Entertainment',     'INNIT ENTERTAINMENT',  'https://www.innitent.com/'),
  ('제이와이드컴퍼니',       'J,WIDE-Company',          'J,WIDE-COMPANY',       'http://www.jwide.co.kr/'),
  ('초이크리에이티브랩',     'Choi Creative Lab',       'CHOI CREATIVE LAB',    'https://www.choicreativelab.com/'),
  ('퍼시픽뮤직그룹',         'Pacific Music Group',     'PACIFIC MUSIC GROUP',  'https://team-pmg.com/');

-- ① canonical_en 정정. 0101 이 넣은 값 그대로일 때만 — 그 사이 권위소스가 올려쳤으면 존중.
UPDATE kwave_entities e
   SET canonical_en = f.new_en, updated_at = now()
  FROM _fix f
 WHERE e.canonical_ko = f.ko AND e.entity_type = 'agency'
   AND e.operator_locked = false
   AND e.canonical_en = f.old_en
   AND e.canonical_en_source = 'local-search';

-- ② Latin locale 4종 정정(en 을 복사한 파생값). 옛 en 값 그대로인 것만.
UPDATE kwave_entities e
   SET canonical_vi    = CASE WHEN e.canonical_vi    = f.old_en THEN f.new_en ELSE e.canonical_vi    END,
       canonical_es    = CASE WHEN e.canonical_es    = f.old_en THEN f.new_en ELSE e.canonical_es    END,
       canonical_id    = CASE WHEN e.canonical_id    = f.old_en THEN f.new_en ELSE e.canonical_id    END,
       canonical_pt_br = CASE WHEN e.canonical_pt_br = f.old_en THEN f.new_en ELSE e.canonical_pt_br END,
       updated_at = now()
  FROM _fix f
 WHERE e.canonical_ko = f.ko AND e.entity_type = 'agency'
   AND e.operator_locked = false
   AND (e.canonical_vi = f.old_en OR e.canonical_es = f.old_en
     OR e.canonical_id = f.old_en OR e.canonical_pt_br = f.old_en);

-- ③ 근거를 1차 출처로 교체. 3자 위키 근거(0101)는 지우고 공식 도메인만 남긴다.
DELETE FROM kwave_kdb_evidence_refs r
 USING kwave_entities e, _fix f
 WHERE r.entity_id = e.id AND r.lane = 'agency-en-search'
   AND e.canonical_ko = f.ko AND r.url <> f.url;

INSERT INTO kwave_kdb_evidence_refs (entity_id, lane, url, title, provider, snippet)
SELECT e.id, 'agency-en-search', f.url, f.new_en, 'official-site',
       f.ko || ' 공식 사이트 자기표기 확인: ' || f.new_en || ' (2026-08-06)'
  FROM kwave_entities e JOIN _fix f ON f.ko = e.canonical_ko
 WHERE e.entity_type = 'agency' AND e.canonical_en = f.new_en
ON CONFLICT (entity_id, url) DO NOTHING;

-- ④ 1차 출처가 없는 건은 그렇게 표시해 둔다(값은 유지, 구분만 가능하게).
UPDATE kwave_kdb_evidence_refs r
   SET provider = 'third-party',
       snippet  = '빅스마일엔터테인먼트: 공식 도메인 소멸(DNS 없음)·공식 SNS 한글 표기뿐 — 1차 출처 없음. 3자 출처 기반 잠정값(2026-08-06)'
  FROM kwave_entities e
 WHERE r.entity_id = e.id AND r.lane = 'agency-en-search'
   AND e.canonical_ko = '빅스마일엔터테인먼트';

COMMIT;
