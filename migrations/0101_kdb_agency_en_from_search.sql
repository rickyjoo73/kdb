-- 0101_kdb_agency_en_from_search — 소속사 11곳의 canonical_en 빈칸을 웹 검색 확인값으로 채운다.
--
-- ★배경(2026-08-06): active 소속사 11곳이 canonical_en 빈칸이었고, 자동 경로로는 채울 수
-- 없는 상태였다:
--   · 외부 앵커 0건 · source_urls 0건 · 소속 아티스트 기록에도 영문 단서 없음
--   · translate-fill(구글번역)은 11곳 전부 no-translation — 고유명사라 번역을 내놓지 않는다
--   · 사내 SearXNG 는 무관한 결과만 반환(초이크리에이티브랩→Poki 게임, 이닛→콘택트렌즈
--     회사, 에버모어→대만 복권). 대조군(에스엠→"SM Entertainment", 하이브→HYBE)은 정상이라
--     검색기 고장이 아니라 이 엔진 조합이 이 회사들을 못 찾는 것이다.
-- en 이 비면 Latin locale 4종도 함께 비므로(DrainRomanizeLatin 이 canonical_en 을 복사원본
-- 으로 쓴다) 1건당 5셀이 막혀 있었다.
--
-- ★왜 규칙으로 만들지 않았나: 음차 역변환은 검증 없이는 틀린다. 실제로 이번 확인에서
--   이닛엔터테인먼트 → "Init" 이 아니라 **INNIT**
--   비츠엔터테인먼트 → "Bits" 가 아니라 **BEATS**
--   아이오케이이엔엠 → "IOK ENM" 이 아니라 한 단어 **IOKENM**(공식 사이트 헤더)
-- 이었다. 규칙만으로 밀었으면 세 건 다 그럴듯한 오답이 들어갔을 자리다("빈칸>틀린값").
--
-- ★source='local-search'(prio 7)로 기록하는 이유: 검색 근거값이라 codex-fallback(prio 8,
-- 현재 다른 소속사 en 값은 **전부** 이것이다)보다는 우선하되, 대소문자·띄어쓰기 표기까지
-- 100% 확신할 수는 없으므로 wikidata(5)·wikipedia(6)·권위 API(4)가 나중에 업그레이드할 수
-- 있게 남겨둔다. operator(prio 1)로 넣으면 그 교정 경로가 막힌다.
--
-- 근거 URL 은 kwave_kdb_evidence_refs 에 함께 적재한다 — 되짚을 수 없는 확증 주장을
-- 금지하는 invariant(evidenced-unretrievable)를 지키기 위해서다.
--
-- 주의: 퍼시픽뮤직그룹은 홍콩 본사 Pacific Music Group 과 한국 법인 Pacific Music Group
-- Korea(PMG 코리아)가 별개다. 이 엔티티의 canonical_ko 가 '퍼시픽뮤직그룹'(코리아 없음)
-- 이므로 본사명을 넣는다. 한국 법인을 가리키는 것이었다면 canonical_ko 쪽을 고쳐야 한다.

BEGIN;

CREATE TEMP TABLE _agency_en (ko text PRIMARY KEY, en text, url text, provider text) ON COMMIT DROP;

INSERT INTO _agency_en (ko, en, url, provider) VALUES
  ('그레이트엠엔터테인먼트', 'Great M Entertainment',  'http://greatment.co.kr/',                        'official-site'),
  ('비츠엔터테인먼트',       'BEATS Entertainment',    'https://ko.wikipedia.org/wiki/비츠엔터테인먼트',  'wikipedia-ko'),
  ('빅스마일엔터테인먼트',   'Big Smile Entertainment','https://www.instagram.com/bigsmile_ent/',        'official-sns'),
  ('아이오케이이엔엠',       'IOKENM',                 'https://iokenm.com/',                            'official-site'),
  ('에버모어엔터테인먼트',   'Evermore Entertainment', 'https://evmore.kr/',                             'official-site'),
  ('에이엠엔터테인먼트',     'AM Entertainment',       'https://www.youtube.com/@ament_official',        'official-sns'),
  ('웨이브엔터테인먼트',     'Wave Entertainment',     'https://waveseoul.com/',                         'official-site'),
  ('이닛엔터테인먼트',       'INNIT Entertainment',    'https://kpop.fandom.com/wiki/INNIT_Entertainment','kpop-wiki'),
  ('제이와이드컴퍼니',       'J,WIDE-Company',         'https://www.facebook.com/jwidecompany/',         'official-sns'),
  ('초이크리에이티브랩',     'Choi Creative Lab',      'https://kpopping.com/companies/choi-creative-lab','kpop-db'),
  ('퍼시픽뮤직그룹',         'Pacific Music Group',    'https://team-pmg.com/',                          'official-site');

-- 빈칸일 때만 채운다. 그 사이 다른 레인이 채웠으면 건드리지 않는다.
UPDATE kwave_entities e
   SET canonical_en        = a.en,
       canonical_en_source = 'local-search',
       source_urls         = (SELECT ARRAY(SELECT DISTINCT u FROM unnest(COALESCE(e.source_urls, '{}') || a.url) u)),
       updated_at          = now()
  FROM _agency_en a
 WHERE e.canonical_ko = a.ko
   AND e.entity_type  = 'agency'
   AND e.status       = 'active'
   AND e.operator_locked = false
   AND COALESCE(e.canonical_en, '') = '';

-- 근거 적재(되짚기 가능성 확보). 같은 (entity,url) 재적재는 무시.
INSERT INTO kwave_kdb_evidence_refs (entity_id, lane, url, title, provider, snippet)
SELECT e.id, 'agency-en-search', a.url, a.en, a.provider,
       a.ko || ' 의 공식 영문명 ' || a.en || ' 확인(2026-08-06)'
  FROM kwave_entities e
  JOIN _agency_en a ON a.ko = e.canonical_ko
 WHERE e.entity_type = 'agency' AND e.canonical_en = a.en
ON CONFLICT (entity_id, url) DO NOTHING;

COMMIT;
