-- 0098: 근거 원문 URL 대장.
--
-- 문제(2026-07-31 실측): 뉴스근거 승급 레인(cand-evidence)은 gemma 가 읽은 기사의
-- 제목+스니펫만 프롬프트로 쓰고 URL 은 gatherEvidenceQuery 에서 그 자리에 버렸다.
-- 승급 후 엔티티에 남는 건 verification_evidence 의 산문 요약("search+gemma: ...")뿐이라
--   verification_tier='evidenced' 인데 그 근거를 되짚을 방법이 없다.
-- 규모: active 4,346 건이 앵커·근거링크 없이 서빙 중이고 최근 7일 유입 886 중 845(95%)가
-- 이 경로다. 뉴스검색 FP 는 실측 ~33%(candidate_evidence.go 주석) — 사후검증이 필수인 축.
--
-- ★source_urls 에 넣지 않은 이유: source_urls 는 "권위 소스" 칸이다.
--   provenanceExpr / provenanceVerifiedExpr(verified_only 게이트)의 판정 입력이자
--   /v1 응답에 그대로 나가는 소비자 필드(api.go:2334-2345, 81/246/301).
--   기사 URL 을 거기 넣으면 근거 등급이 위조되고, active-no-anchor 지표만 0 으로 떨어진다.
--   근거(evidence)와 앵커(authoritative anchor)는 다른 명제다 — 칸을 분리해 둘 다 정직하게 센다.
CREATE TABLE IF NOT EXISTS kwave_kdb_evidence_refs (
  id         bigserial PRIMARY KEY,
  entity_id  uuid NOT NULL,
  lane       text NOT NULL DEFAULT '',   -- cand-evidence | (향후 다른 근거 레인)
  url        text NOT NULL,
  title      text NOT NULL DEFAULT '',
  provider   text NOT NULL DEFAULT '',   -- naver-news | searxng
  created_at timestamptz NOT NULL DEFAULT now()
);

-- 같은 엔티티에 같은 기사가 재수집돼도 1행(재판정·재승급 시 중복 방지).
CREATE UNIQUE INDEX IF NOT EXISTS kwave_kdb_evidence_refs_entity_url_key
  ON kwave_kdb_evidence_refs (entity_id, url);
CREATE INDEX IF NOT EXISTS kwave_kdb_evidence_refs_entity_idx
  ON kwave_kdb_evidence_refs (entity_id, created_at DESC);
CREATE INDEX IF NOT EXISTS kwave_kdb_evidence_refs_recent_idx
  ON kwave_kdb_evidence_refs (created_at DESC);
