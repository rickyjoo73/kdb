-- 0147: **등록된 소비자의 자기 기사 URL 을 신뢰 출처로 인정한다.**
--
-- ★계기 (2026-09-15). 범위를 넓히고 게이트를 풀었는데도 SK하이닉스·국민의힘·
--   연세대학교가 `review` 에 묶였다. 사유는 `missing_source_evidence` 였다.
--
--   파 보니 isTrustedIntakeSource 가 `discovery_enabled=true` 를 묻고 있었다.
--   그 칸의 뜻은 "우리가 이 사이트를 크롤링한다"이고, 물어야 하는 것은 "이 출처를
--   믿는가"다 — **다른 질문**인데 같은 칸으로 답하고 있었다.
--
--   그래서 등록된 소비자가 자기 기사 URL 을 보내도 '출처 근거 없음'이 됐다. 실측:
--     www.mediafine.co.kr    6,545회 요청   ← 화이트리스트 밖
--     issuetalk.co.kr        4,126회        ← 밖
--     kstory.aiinplanet.com  3,974회        ← 밖
--     www.nbntv.co.kr          682회 · specialtimes 338 · famtimes 210 · ksw-news 148
--
--   발행사가 자기 기사를 가리키며 "이 고유명사가 여기 나온다"고 하는 것보다 더 나은
--   인입 근거는 없다. 그것을 못 믿으면 API 를 줄 이유도 없다.
--
-- ★크롤링은 안 한다. discovery_enabled=false 로 넣는다 — **믿는 것과 긁는 것은 다르다.**
--   이 도메인들은 한국어 원문 출처라 외국어 표기를 찾을 곳이 아니다.

BEGIN;

INSERT INTO kwave_news_whitelist (domain, locale, trust, enabled, discovery_enabled, category, added_by, added_at)
VALUES
  ('mediafine.co.kr',       'ko', 0.90, false, false, 'consumer', 'migration-0147', now()),
  ('presslocale.com',       'ko', 0.90, false, false, 'consumer', 'migration-0147', now()),
  ('issuetalk.co.kr',       'ko', 0.90, false, false, 'consumer', 'migration-0147', now()),
  ('kstory.aiinplanet.com', 'ko', 0.90, false, false, 'consumer', 'migration-0147', now()),
  ('nbntv.co.kr',           'ko', 0.85, false, false, 'consumer', 'migration-0147', now()),
  ('specialtimes.co.kr',    'ko', 0.85, false, false, 'consumer', 'migration-0147', now()),
  ('famtimes.co.kr',        'ko', 0.85, false, false, 'consumer', 'migration-0147', now()),
  ('ksw-news.com',          'ko', 0.85, false, false, 'consumer', 'migration-0147', now()),
  ('trendbiz.co.kr',        'ko', 0.85, false, false, 'consumer', 'migration-0147', now())
ON CONFLICT (domain, locale) DO NOTHING;   -- 기본키가 (domain, locale) 이다

COMMIT;
