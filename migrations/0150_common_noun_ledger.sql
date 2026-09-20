-- 0150: **일반명사에 제 구간을 준다.**
--
-- ★왜 (2026-09-20 실측). 판정기는 일반명사를 잘 가려낸다. 오늘 배포 뒤 찍힌 것들:
--     무지개  "기상 현상"      왠지  "부사(adverb)"
--     명의    "계좌의 소유자"  헤엄  "일반 명사(수영 동작)"
--   문제는 **그 판정을 적을 자리가 없다**는 것이다. 지금은 세 가지가 전부 같은 곳
--   (notes 라는 자유 문장)에 같은 낱말로 적힌다:
--
--     ① «연예가 아니다»        → 0143 으로 **죽은** 명제
--     ② «일반명사다»           → 지금도 **살아 있는** 명제
--     ③ «해외 대상이다»        → 지금도 **살아 있는** 명제
--
--   모델은 ②를 이렇게 쓴다: "…일반 명사로서의 '무지개'를 다루고 있으며 **K-콘텐츠**와 무관".
--   그래서 ①을 되살리려고 만든 레인(DrainDeadScopeFlags)이 ②를 같이 걷었다 —
--   396건 중 118건이 진짜 일반명사였고, 43건은 이미 «걷힘 → 재판정 → 같은 결론»
--   왕복을 마쳤다. 낱말로 가르려는 한 이 왕복은 끝나지 않는다.
--
-- ★그래서 구간을 나눈다. 일반명사는 «기각된 엔티티»가 아니라 **등재된 일반명사**다.
--
--     kwave_kdb_common_nouns   낱말 하나가 한 행. 재요청이 와도 LLM 없이 즉답한다.
--     entity_type='common_noun' 원장 쪽 표시. «미상(term)»과 구분된다.
--
--   term 은 changelog 가 이미 못 박았다 — «일반어다»가 아니라 «어느 칸인지
--   모르겠다»는 뜻이다. 그런데 세 레인이 term 을 «일반어»로 읽어 왔고(rejudge 제외 ·
--   cand-evidence 제외 · romanize 제외), 그래서 서울대학교·연세대학교가 term 으로
--   죽은 채 어느 레인에도 안 걸린다(요청 7일 20건·14건). 칸을 나누면 term 은 다시
--   «미상»만 뜻하게 되고, 그 행들은 재심 대상으로 돌아온다.
--
-- ★불변식: common_noun 은 **절대 active 가 아니다.** 아래 CHECK 로 못 박는다.
--   서빙 쿼리 스무 곳에 조건을 더 적는 대신 한 자리에서 막는다 — 같은 목록을 두 벌
--   적다가 화면이 몇 달째 틀려 있던 일(0143 → entity_types.go)을 반복하지 않는다.

BEGIN;

ALTER TYPE kwave_entity_type ADD VALUE IF NOT EXISTS 'common_noun';  -- 일반명사·일상어·범주어

COMMIT;

-- ── 일반명사 원장 ──────────────────────────────────────────────────────────
--
-- norm_key 는 인테이크 정규화 키와 **같은 규칙**이다(공백·문장부호 제거 + 소문자):
--   lower(regexp_replace(btrim(term), '[[:space:][:punct:]]+', '', 'g'))
-- 같은 규칙을 쓰는 곳: rejudge.go 의 intake_normalized_key 대조.
CREATE TABLE IF NOT EXISTS kwave_kdb_common_nouns (
  id             bigserial PRIMARY KEY,
  norm_key       text NOT NULL UNIQUE,
  surface        text NOT NULL,                        -- 처음 본 표기 그대로
  kind           text NOT NULL DEFAULT 'common_noun',  -- common_noun | category | product | foreign_subject
  reason         text NOT NULL DEFAULT '',             -- 판정기가 적은 사유
  decided_by     text NOT NULL DEFAULT '',             -- cand-evidence | gatekeeper | operator | backfill
  evidence       text NOT NULL DEFAULT '',             -- 근거 스니펫/URL
  entity_id      uuid,                                 -- 그 판정이 붙은 원장 행(있으면)
  status         text NOT NULL DEFAULT 'confirmed',    -- confirmed | revoked
  revoked_reason text NOT NULL DEFAULT '',
  revoked_by     text NOT NULL DEFAULT '',
  hit_count      int  NOT NULL DEFAULT 1,              -- 같은 낱말이 다시 들어온 횟수
  first_seen_at  timestamptz NOT NULL DEFAULT now(),
  last_seen_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT kwave_kdb_common_nouns_kind_ck
    CHECK (kind IN ('common_noun','category','product','foreign_subject')),
  CONSTRAINT kwave_kdb_common_nouns_status_ck
    CHECK (status IN ('confirmed','revoked'))
);

COMMENT ON TABLE kwave_kdb_common_nouns IS
  '일반명사 구간. «기각»이 아니라 «등재»다 — 재요청에 LLM 없이 즉답하고, 범위 회수
   레인이 이 목록을 보고 건드리지 않는다. revoked 는 고유명사로 밝혀진 되돌림.';

CREATE INDEX IF NOT EXISTS kwave_kdb_common_nouns_live_idx
  ON kwave_kdb_common_nouns (status, last_seen_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_common_nouns_entity_idx
  ON kwave_kdb_common_nouns (entity_id) WHERE entity_id IS NOT NULL;

-- ── 불변식: 일반명사는 서빙되지 않는다 ────────────────────────────────────
--
-- ★왜 CHECK 인가. 「일반명사가 가끔 올라온다」는 것이 오너가 말한 혼돈의 실체다.
--   지금 active 에 `타이틀곡 → title track` · `휴가 → Our Season` 같은 것이 남아
--   있다(11행). 조건을 쿼리마다 적으면 새 경로가 생길 때마다 빠뜨린다 —
--   **넣는 쪽을 막는다.** 위반하면 조용히 나가는 대신 쓰기가 실패하고 로그가 남는다.
--
-- NOT VALID 로 붙인다: 기존 행 검사를 건너뛰고 **앞으로의 쓰기만** 막는다. 지금은
-- common_noun 행이 0건이라 차이가 없지만, 운영 테이블에 전체 잠금을 걸지 않는다.
ALTER TABLE kwave_entities
  DROP CONSTRAINT IF EXISTS kwave_entities_common_noun_not_active_ck;
ALTER TABLE kwave_entities
  ADD CONSTRAINT kwave_entities_common_noun_not_active_ck
  CHECK (NOT (entity_type = 'common_noun' AND status = 'active')) NOT VALID;
