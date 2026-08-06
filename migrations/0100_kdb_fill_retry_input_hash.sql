-- 0100: 채움 재시도 규칙을 "시간"에서 "입력 변화"로 통합 (2026-08-06).
--
-- 문제 — 같은 질문에 규칙이 6개였다. "이 칸을 다시 채워볼까?" 에 대해
--   mt-fill:<loc>      30일 쿨다운 + 3회 exhausted
--   gt-fill:en         30일 쿨다운 + 3회 exhausted
--   tmdb-locale        30일 쿨다운 (성공·실패 무관 마킹)
--   enrichground       7일 쿨다운 (+exhausted 면 30일)
--   ground-strict-skip 7일 억제
--   cascade canonical_* 7일 창 안의 exhausted
-- 전부 "시간이 지났나"만 보고 "입력이 바뀌었나"는 보지 않았다.
--
-- 그래서 무슨 일이 났나 (2026-08-06 실측):
--   07-26 대량 백필 1회가 백로그 전체의 시도권을 하루에 소진시켰다 — zh 1,415건 ·
--   ja 841건이 같은 날 원장에 적재됐고, 07-31 에 tmdb-locale 636건이 뒤따랐다.
--   그 결과 상시 cron(kdb-locale-fill.sh)이 예산 620 을 받고 6건을 처리한다:
--     시작 zh=250 ja=250 en=120 → zh filled=2 /3 · ja filled=0 /3 · en total=0
--   레인별 실제 선정 건수는 전부 0 이다:
--     mt-fill:zh 대상 1,568 → 0 | mt-fill:ja 1,014 → 0
--     gt-fill:en   대상    79 → 0 | tmdb-locale  255 → 0
--   원장은 exhausted=0 · max(attempts)=1 — 영구 포기도 아니고 재시도도 아닌 채로
--   08-25~09-05 까지 잠겨 있다. 그리고 해제돼도 같은 ko 를 같은 구글번역에 넣으므로
--   같은 판정이 나오고 다시 30일이 걸린다. 사실상 영구 정지다.
--   (핸드오프 42차 §11 이 en 85건에서 지목한 함정과 같은 것 — 규모가 30배다.)
--
-- 처방 — 재시도 조건을 하나로 합친다.
--   input_hash = 채움 레인이 볼 수 있는 그 엔티티의 모든 입력에 대한 지문.
--     · 같은 지문으로 이미 실패했으면 다시 하지 않는다 (재시도해도 결과가 같다).
--     · 지문이 달라지면(앵커가 붙거나 다른 로케일이 채워지면) 쿨다운을 기다리지 않고
--       즉시 재시도한다 — 지금은 새 사실이 들어와도 30일을 기다린다.
--     · 지문이 그대로여도 90일이 지나면 1회 재방문한다. 우리 입력은 안 변해도 외부
--       소스(Wikidata·TMDb)가 그 사이 값을 얻었을 수 있다 — 자기치유 안전판이다.
--   이 술어 하나가 위 6개 규칙을 대체한다. 로직이 늘지 않고 줄어든다.
--
-- ★기존 행의 input_hash 는 '' 이고 현재 지문과 절대 같을 수 없다. 즉 이 마이그레이션을
--   적용하는 즉시 07-26 잠금이 전부 풀린다(의도된 것). 풀린 뒤 첫 스윕은 같은 커밋의
--   "전송실패를 판정으로 기록하지 않는다" 수정이 적용된 상태로 돌기 때문에, gemma 장애
--   구간에 'bad-title' 로 잘못 낙인찍힌 건들이 여기서 회수된다.
--   (gemma 는 08-06 하루에만 두 번 OFFLINE 이었다 — 02:14 deadline, 02:45 DNS, 34분.)

ALTER TABLE kwave_kdb_enrich_attempts
  ADD COLUMN IF NOT EXISTS input_hash text NOT NULL DEFAULT '';

-- 판정 근거. 지금은 last_source 에 판정 종류(bad-title/literal/…)만 남고 이유가 없어서,
-- "게이트가 나쁘다고 했다" 와 "게이트를 못 불렀다" 를 사후에 구분할 수 없다. 실제로
-- bad-title zh 1,327 · ja 1,049 중 몇 %가 gemma 장애분인지 원장으로는 판별 불가다.
ALTER TABLE kwave_kdb_enrich_attempts
  ADD COLUMN IF NOT EXISTS last_reason text NOT NULL DEFAULT '';

COMMENT ON COLUMN kwave_kdb_enrich_attempts.input_hash IS
  '이 시도가 어떤 입력에 대한 것이었는지의 지문(kdb_fill_input_hash). 선정 쿼리는 '
  '"현재 지문과 같은 실패 기록이 있는가"만 본다 — 같으면 재시도 무의미, 다르면 즉시 재시도.';

COMMENT ON COLUMN kwave_kdb_enrich_attempts.last_reason IS
  '마지막 판정의 근거 한 줄. 결정적 실패만 기록된다 — 전송실패(LLM/외부 API 장애)는 '
  '애초에 원장에 들어오지 않는다.';

-- kdb_fill_input_hash — 채움 레인이 참조할 수 있는 입력 전부의 지문.
--
-- 무엇을 넣는가: 값이 바뀌면 "다시 시도해볼 가치가 생기는" 것만 넣는다.
--   canonical_ko/entity_type  번역·게이트의 직접 입력
--   disambig/category_hint    gtranslate 맥락 문장(loadGTranslateContext)의 입력
--   aliases_ko                검색·채움 힌트
--   canonical_*               L4 의 Known 집합. 한 로케일이 채워지면 나머지 추론이 달라진다
--   person_details            FillEN 맥락(역할·소속·그룹·대표작)
--   external_refs             ★가장 중요 — 앵커가 새로 붙으면 권위 조회가 가능해진다
--
-- 배열은 정렬해서 넣는다(순서 변경만으로 지문이 바뀌면 헛 재시도가 생긴다).
-- COALESCE 로 NULL 을 '' 로 눕힌다 — concat_ws 는 NULL 을 건너뛰어 자리를 밀기 때문에
-- (a=NULL,b='x') 와 (a='x',b=NULL) 이 같은 지문이 될 수 있다.
CREATE OR REPLACE FUNCTION kdb_fill_input_hash(p_entity uuid) RETURNS text
LANGUAGE sql STABLE AS $$
  SELECT md5(concat_ws('|',
      COALESCE(e.canonical_ko, ''),
      COALESCE(e.entity_type::text, ''),
      COALESCE(e.disambig, ''),
      COALESCE(e.category_hint, ''),
      COALESCE(array_to_string(ARRAY(SELECT unnest(e.aliases_ko) ORDER BY 1), ','), ''),
      COALESCE(e.canonical_en, ''),
      COALESCE(e.canonical_ja, ''),
      COALESCE(e.canonical_vi, ''),
      COALESCE(e.canonical_id, ''),
      COALESCE(e.canonical_es, ''),
      COALESCE(e.canonical_pt_br, ''),
      COALESCE(e.canonical_zh, ''),
      COALESCE(e.canonical_zh_hant, ''),
      COALESCE(d.primary_role::text, ''),
      COALESCE(d.agency, ''),
      COALESCE(array_to_string(ARRAY(SELECT unnest(d.groups) ORDER BY 1), ','), ''),
      COALESCE(array_to_string(ARRAY(SELECT unnest(d.notable_works) ORDER BY 1), ','), ''),
      COALESCE((SELECT string_agg(r.provider || ':' || r.external_id, ','
                             ORDER BY r.provider, r.external_id)
                  FROM kwave_entity_external_refs r WHERE r.entity_id = e.id), '')
  ))
    FROM kwave_entities e
    LEFT JOIN kwave_entity_person_details d ON d.entity_id = e.id
   WHERE e.id = p_entity
$$;

COMMENT ON FUNCTION kdb_fill_input_hash(uuid) IS
  '채움 재시도 판단의 유일한 기준. 선정 쿼리와 마킹이 같은 함수를 쓰므로 Go 쪽에 '
  '해시 계산이 중복되지 않는다 — 규칙이 한 곳에만 있다.';
