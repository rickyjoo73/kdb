-- 0106_kdb_strip_disambig_suffix — 로케일 정본에 딸려 들어온 동음이의 괄호를 뗀다.
--
-- ★무엇이 잘못됐나(2026-08-07 실측). canonical_ja/zh/zh_hant 에 이름이 아니라 **분류
-- 딱지**가 붙어 있다:
--
--     아이비   ja  アイビー (歌手)          zh  Ivy (韓國歌手)
--     산이     ja  San E（ラッパー）
--     스피카   ja  Spica (音楽グループ)
--     초능력자 ja  超能力者 (映画)
--     유니콘   zh  Unicorn (女子团体)
--
-- 출처는 위키백과 문서 제목이 아니라 **위키데이터 레이블 자체**다. 봇이 문서 제목을
-- 레이블로 복사한 흔적이고, Q6099788(아이비) 의 ja 레이블이 실제로 "アイビー (歌手)" 다.
-- 그래서 문서 제목 전용 정제(cleanLanglinkTitle)를 통과하지 않고 그대로 들어왔다.
-- opencc/wikipedia-zh-variant 로 표시된 것들은 그렇게 오염된 값에서 파생된 2차 오염이다.
--
-- 오너 원칙 "빈칸 > 틀린값"에서 이건 틀린값이다. 다만 **지울 게 아니라 괄호만 떼면 맞는
-- 값**이라 비우지 않고 교정한다.
--
-- ★괄호를 무조건 떼지 않는다. 정상 표기에도 괄호가 쓰인다 — `티빙(TVING)` 은 맞는 값이다.
--
-- ★"괄호 안이 CJK 면 딱지"라는 규칙도 처음 써봤다가 폐기했다. 드라이런에서 그 조건에
-- 걸리는 것의 대부분이 분류가 아니라 **독음/한자 병기**였다:
--
--     4WARD(フォワード) · BINGO(ビンゴ) · Gnarly（ナーリー） · HAWWAH（夏渦）
--
-- 이건 이름의 일부고 떼면 정보가 사라진다. 그래서 분류어 **닫힌 목록**으로만 판정한다.
-- 목록에 없는 괄호는 무조건 보존 — 모르면 손대지 않는 쪽이 안전하다.
-- Go 쪽 wikidata.StripDisambig 과 같은 목록이고, 그쪽이 재유입을 막는다.
--
-- 되돌리기: 아래 kwave_kdb_enrich_attempts 의 lane='disambig-strip' 행에 이전 값이 남는다.

BEGIN;

CREATE TEMP TABLE _disambig_fix ON COMMIT DROP AS
WITH cand AS (
  SELECT id, 'ja'::text AS loc, canonical_ja AS v FROM kwave_entities WHERE COALESCE(canonical_ja,'') <> ''
  UNION ALL SELECT id, 'zh',      canonical_zh      FROM kwave_entities WHERE COALESCE(canonical_zh,'') <> ''
  UNION ALL SELECT id, 'zh_hant', canonical_zh_hant FROM kwave_entities WHERE COALESCE(canonical_zh_hant,'') <> ''
)
SELECT id, loc, v AS old_v,
       btrim(regexp_replace(v, '\s*[(（][^)）]*[)）]\s*$', '')) AS new_v
  FROM cand
 -- 끝에 붙은 괄호이고, 그 안에 분류어가 들어 있는 것만. 목록에 없으면 손대지 않는다.
 WHERE v ~ ('[(（][^)）]*(' ||
       '歌手|俳優|女優|声優|映画|ドラマ|グループ|バンド|ラッパー|アイドル|タレント|テレビ番組|楽曲|アルバム|' ||
       '組合|组合|團體|团体|樂隊|乐队|電視劇|电视剧|網路劇|网络剧|電影|电影|藝人|艺人|專輯|专辑|歌曲|綜藝|综艺|' ||
       '가수|배우|영화|드라마|그룹|밴드|아이돌' ||
       ')[^)）]*[)）]\s*$')
   -- 괄호를 뗀 뒤에도 이름이 남아야 한다("（하나）" 처럼 전체가 괄호면 제외).
   AND btrim(regexp_replace(v, '\s*[(（][^)）]*[)）]\s*$', '')) <> ''
   AND btrim(regexp_replace(v, '\s*[(（][^)）]*[)）]\s*$', '')) <> v;

-- 감사 흔적을 먼저 남긴다(교정 전 값 보존).
INSERT INTO kwave_kdb_enrich_attempts (entity_id, field, last_source, last_attempt_at, attempts)
SELECT id, 'disambig-strip:' || loc, left(old_v, 200), now(), 1
  FROM _disambig_fix
ON CONFLICT (entity_id, field) DO UPDATE
  SET last_source = EXCLUDED.last_source, last_attempt_at = now(),
      attempts = kwave_kdb_enrich_attempts.attempts + 1;

UPDATE kwave_entities e SET canonical_ja = f.new_v, updated_at = now()
  FROM _disambig_fix f WHERE f.id = e.id AND f.loc = 'ja';
UPDATE kwave_entities e SET canonical_zh = f.new_v, updated_at = now()
  FROM _disambig_fix f WHERE f.id = e.id AND f.loc = 'zh';
UPDATE kwave_entities e SET canonical_zh_hant = f.new_v, updated_at = now()
  FROM _disambig_fix f WHERE f.id = e.id AND f.loc = 'zh_hant';

-- 정직성 검사: 교정 후에도 같은 패턴이 남아 있으면 규칙이 안 먹은 것이다.
DO $chk$
DECLARE n int;
BEGIN
  SELECT count(*) INTO n FROM (
    SELECT canonical_ja AS v FROM kwave_entities WHERE COALESCE(canonical_ja,'')<>''
    UNION ALL SELECT canonical_zh FROM kwave_entities WHERE COALESCE(canonical_zh,'')<>''
    UNION ALL SELECT canonical_zh_hant FROM kwave_entities WHERE COALESCE(canonical_zh_hant,'')<>''
  ) t WHERE v ~ ('[(（][^)）]*(' ||
       '歌手|俳優|女優|声優|映画|ドラマ|グループ|バンド|ラッパー|アイドル|タレント|テレビ番組|楽曲|アルバム|' ||
       '組合|组合|團體|团体|樂隊|乐队|電視劇|电视剧|網路劇|网络剧|電影|电影|藝人|艺人|專輯|专辑|歌曲|綜藝|综艺|' ||
       '가수|배우|영화|드라마|그룹|밴드|아이돌' ||
       ')[^)）]*[)）]\s*$');
  IF n > 0 THEN
    RAISE EXCEPTION '동음이의 괄호가 % 칸 남았다 — 교정 규칙 확인 필요', n;
  END IF;
  RAISE NOTICE '동음이의 괄호 제거 완료 — 잔여 0칸';
END $chk$;

COMMIT;
