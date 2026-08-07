-- 0108_kdb_tmdb_anchor_season_collapse_revert — 시즌 엔티티를 상위 시리즈에 붙인 앵커 3건과
-- 그 앵커가 만든 프랜차이즈 제목 6칸을 되돌린다.
--
-- ★무엇이 잘못됐나(2026-08-07). 새 tmdb-anchor 레인(3b326a9)이 붙인 25건을 배포 후 검수하니
-- 3건이 **상위 시리즈 항목**이었다. TMDb 는 시리즈의 시즌·속편을 **항목 하나로 접고** 각
-- 시즌의 한국어 제목을 alternative_titles 에 등록한다 — 실제 응답으로 확인했다:
--
--     tv/71908 "크라임씬"      KR alt: 크라임씬 리턴즈
--     tv/62411 "식샤를 합시다"  KR alt: 식샤를 합시다 2 / 식샤를 합시다 3: 비긴즈
--     tv/18495 "논스톱"        KR alt: 논스톱3 / 논스톱 4 / 논스톱5 / 뉴 논스톱
--
-- 그래서 alt-title 관용화(2026-07-23 Phase1)가 시즌 엔티티를 프랜차이즈 항목에 붙였고,
-- tmdb-locale 이 **프랜차이즈 제목을 시즌 칸에** 써넣었다. 가장 선명한 증거: 크라임씬
-- 리턴즈의 zh 가 `犯罪现场`(=Crime Scene)로 채워졌는데, 정작 그 항목의 CN alt 에는
-- 시즌용 `犯罪现场归来`(=Crime Scene Returns)가 **따로 등록돼 있다**.
--
-- §4.5(별칭 함정)와 같은 계열이다. **alt 표기는 "그 이름으로도 불린다"이지 "이 항목이 그
-- 작품이다"가 아니다.** 한 층 위에서 같은 착각을 했다.
--
-- 코드 쪽 재유입 차단: tmdb/tmdb.go 의 searchExactMatches 에 프랜차이즈 접힘 가드를 넣었다
-- (회차 표지가 우리 쪽에만 있고 상대 주제목에 없으면 보류, verdict='season-only').
-- 승급 경로(SearchExactID)도 같은 본체를 쓰므로 함께 막힌다.
--
-- ★논스톱5 의 zh/zh_hant `男生女生向前走 第五部` 는 **지우지 않는다.** 값 자체는 5기의
-- 중국어 제목이 맞다(TMDb 의 zh 번역이 하필 5기 기준으로 등록돼 있었다). 앵커만 뗀다 —
-- 앵커를 두면 en/vi/es 등 나머지 로케일에 프랜차이즈 제목이 들어올 자리가 남는다.
--
-- ★되돌리는 칸은 source='tmdb' 인 것만이다. tmdb-locale 은 gtranslate·romanization 등
-- 하위 출처를 덮어쓸 수 있어(tmdbOverwritableSources) 일부 칸은 덮기 전 기계번역 값이
-- 있었고 그 값은 복구되지 않는다. 빈칸으로 두면 mt-fill 레인이 다시 채운다 —
-- 오너 원칙 "빈칸 > 틀린값" 이므로 틀린 채로 두는 것보다 낫다.

BEGIN;

-- 이번 레인이 붙인 앵커 중 프랜차이즈 접힘에 해당하는 3건.
-- entity_id 를 함께 고정한다: tv/18495 에는 **다른 엔티티('논스톱' 2건)의 선행 앵커**가
-- 따로 걸려 있고 그건 이 마이그레이션의 대상이 아니다.
CREATE TEMP TABLE _season_collapse ON COMMIT DROP AS
SELECT e.id, e.canonical_ko, r.external_id
  FROM kwave_kdb_enrich_attempts t
  JOIN kwave_entities e ON e.id = t.entity_id
  JOIN kwave_entity_external_refs r ON r.entity_id = e.id AND r.provider = 'tmdb'
 WHERE t.field = 'tmdb-anchor'
   AND t.last_source = 'anchored'
   AND e.id IN ('e0a28849-ec04-4658-bc87-5421cbd4b960'::uuid,   -- 논스톱5        → tv/18495
                '3c9e4229-af2f-4241-b891-301e6a554ec5'::uuid,   -- 식샤를 합시다 3 → tv/62411
                '06a073a7-f4f8-4240-bb51-dd7f362d023b'::uuid);  -- 크라임씬 리턴즈 → tv/71908

DO $$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n FROM _season_collapse;
  IF n <> 3 THEN
    RAISE EXCEPTION '대상이 3건이어야 하는데 %건이다 — 상태가 예상과 다르므로 중단', n;
  END IF;
END $$;

-- ── 1) 프랜차이즈 제목이 들어간 칸을 비운다(논스톱5 제외 — 위 주석 참조) ──
--     실측 6칸: 식샤를 합시다 3(zh·vi·es·pt_br), 크라임씬 리턴즈(zh·zh_hant).
CREATE TEMP TABLE _value_revert ON COMMIT DROP AS
SELECT id FROM _season_collapse
 WHERE id <> 'e0a28849-ec04-4658-bc87-5421cbd4b960'::uuid;

UPDATE kwave_entities e SET canonical_zh = NULL, canonical_zh_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_zh_source = 'tmdb';
UPDATE kwave_entities e SET canonical_zh_hant = NULL, canonical_zh_hant_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_zh_hant_source = 'tmdb';
UPDATE kwave_entities e SET canonical_ja = NULL, canonical_ja_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_ja_source = 'tmdb';
UPDATE kwave_entities e SET canonical_vi = NULL, canonical_vi_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_vi_source = 'tmdb';
UPDATE kwave_entities e SET canonical_es = NULL, canonical_es_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_es_source = 'tmdb';
UPDATE kwave_entities e SET canonical_id = NULL, canonical_id_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_id_source = 'tmdb';
UPDATE kwave_entities e SET canonical_pt_br = NULL, canonical_pt_br_source = NULL, updated_at = now()
  FROM _value_revert v WHERE e.id = v.id AND e.canonical_pt_br_source = 'tmdb';

-- ── 2) 앵커를 뗀다 ──
-- §4.5 는 앵커를 일부러 남겼지만(선행 레인들이 이미 그 앵커에 기대고 있었다), 여기는
-- 다르다 — 이 3건은 오늘 이 레인이 처음 붙인 것이고 5시간밖에 안 됐다. 내가 만든 것을
-- 되돌리는 것이라 다른 판단에 영향이 없다.
DELETE FROM kwave_entity_external_refs r
 USING _season_collapse s
 WHERE r.entity_id = s.id AND r.provider = 'tmdb' AND r.external_id = s.external_id;

-- ── 3) 원장을 정직하게 고친다 ──
-- 'anchored'(성공) 로 남겨두면 이력이 거짓이 된다. ref 삭제로 fill_input_hash 가 바뀌므로
-- 어차피 재선정되고, 그때는 새 가드가 'season-only' 로 보류한다.
UPDATE kwave_kdb_enrich_attempts a
   SET last_source = 'season-only',
       last_reason = 'TMDb 가 시즌을 상위 시리즈 항목으로 접어 alt 표기로만 일치 — 앵커 회수(0108)',
       last_attempt_at = now()
  FROM _season_collapse s
 WHERE a.entity_id = s.id AND a.field = 'tmdb-anchor';

-- tmdb-locale 원장의 'filled' 도 정리한다. ref 가 없어 재선정되지 않으므로 남겨두면
-- "TMDb 로 채운 적 있음"이라는 잘못된 이력만 남는다.
DELETE FROM kwave_kdb_enrich_attempts a
 USING _value_revert v
 WHERE a.entity_id = v.id AND a.field = 'tmdb-locale';

COMMIT;
