-- 0067_api_consumers.sql
-- 외부 매체(소비자)용 KDB 검색 API 키 저장소.
-- 운영자 지시(2026-06-01): 미디어파인 등 외부 매체가 /v1/lookup 을 호출하려면
-- 인바운드 키가 필요하다. admin /admin/settings 에서 발급/회수하며, 런타임
-- 인증(kdbapi)은 .env KDB_API_KEYS(정적) ∪ 이 테이블의 active 키를 합집합으로
-- 허용한다. 키 포맷: "kdb_" + 32 hex. 발급 시 admin 화면에서 1회만 평문 노출.
--
-- 0066(kwave_kdb_api_settings)은 KDB 가 외부로 호출하는 아웃바운드 키 저장소이고,
-- 이 테이블은 외부가 KDB 로 들어오는 인바운드 키 저장소로 역할이 다르다.
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_api_consumers (
    id           UUID        PRIMARY KEY,
    label        TEXT        NOT NULL,
    key          TEXT        NOT NULL UNIQUE,
    active       BOOLEAN     NOT NULL DEFAULT true,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_by   TEXT        NOT NULL DEFAULT 'admin',
    last_used_at TIMESTAMPTZ
);

-- 런타임 인증이 active 키만 짧은 TTL 로 조회.
CREATE INDEX IF NOT EXISTS kwave_kdb_api_consumers_active_idx
    ON kwave_kdb_api_consumers (key) WHERE active;

COMMIT;
