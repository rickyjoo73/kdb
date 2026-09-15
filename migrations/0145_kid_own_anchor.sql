-- 0145: `kid` — **우리 자체 ID.** 소비자가 들고 다니는 주 앵커다.
--
-- ★운영자 지시 (2026-09-15): "qid는 보조로 사용한다고 분명히 말했고, 자체 id를
--   구축하라고도 아주 명확하게 지시했다." / "우리는 kid 로 해."
--
-- ★왜 필요한가. 소비자는 지금 **이름**으로 묻는다. 그래서 같은 이름의 대상이 둘이면
--   고를 수가 없다 — 실측으로 24시간 요청 802개 중 37개(3.9%)가 "동명 여럿"으로 나갔다.
--   한 번 고른 뒤 그 대상을 가리킬 방법이 없으면 다음에 또 같은 질문을 한다.
--
--   kid 를 주면 그 다음부터는 이름이 아니라 **대상**을 묻는다. 동명이인이 근본적으로
--   풀린다. 이름이 바뀌어도(개명·활동명 변경) 같은 kid 다.
--
-- ★왜 UUID 를 그냥 안 쓰나. UUID 는 내부 기본키이고 그대로 둔다(I02: 불변).
--   다만 36자 난수라 사람이 기사·시트·로그에 옮겨 적기 어렵고, 눈으로 대조가 안 된다.
--   kid 는 짧고 읽을 수 있고, **UUID 와 1:1** 이다. 둘 중 무엇으로 물어도 같은 행이다.
--
-- ★kid 에 바뀔 수 있는 사실을 담지 않는다. 유형(person/company…)도 이름도 넣지 않는다.
--   유형은 재분류되고 이름은 개명된다. 그때 ID 가 따라 바뀌면 ID 가 아니다.
--   그래서 순번뿐이다 — `K` + 7자리.
--
-- ★한 번 준 kid 는 회수하지 않는다. 병합돼 퇴역한 행의 kid 도 남겨 둔다. 소비자가
--   그것을 들고 다시 물으면 "그 대상은 이제 저쪽"이라고 답할 수 있어야 하기 때문이다.

BEGIN;

CREATE SEQUENCE IF NOT EXISTS kwave_kid_seq;

ALTER TABLE kwave_entities
  ADD COLUMN IF NOT EXISTS kid text;

-- 기존 행에 순번을 준다. **created_at 순서**로 매긴다 — 오래된 대상이 작은 번호를
-- 갖는 편이 사람이 볼 때 자연스럽고, 순서 자체에 뜻이 생기지 않게 그 이상은 안 담는다.
UPDATE kwave_entities e
   SET kid = 'K' || lpad(s.n::text, 7, '0')
  FROM (SELECT id, row_number() OVER (ORDER BY created_at, id) AS n
          FROM kwave_entities) s
 WHERE s.id = e.id AND e.kid IS NULL;

SELECT setval('kwave_kid_seq', GREATEST((SELECT count(*) FROM kwave_entities), 1));

ALTER TABLE kwave_entities
  ALTER COLUMN kid SET DEFAULT 'K' || lpad(nextval('kwave_kid_seq')::text, 7, '0'),
  ALTER COLUMN kid SET NOT NULL;

CREATE UNIQUE INDEX IF NOT EXISTS kwave_entities_kid_key ON kwave_entities (kid);

COMMENT ON COLUMN kwave_entities.kid IS
  'KDB 자체 ID. 주 앵커(I03)이며 소비자가 들고 다니는 값. 불변이고 회수하지 않는다. QID 는 보조.';

COMMIT;
