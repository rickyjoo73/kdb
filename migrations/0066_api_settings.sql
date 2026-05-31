-- 0066_api_settings.sql
-- admin "API 설정" 페이지용 키 저장소. 운영자 지시(2026-05-31): 사용되는 API를
-- 모두 관리자 페이지에 등록 + 키 입력/편집(DB 우선, .env fallback).
--
-- 보안: 세션 인증된 admin 만 접근. 값은 평문 저장(DB 접근 자체가 자격증명 필요).
-- 런타임은 apikeys.Resolve 로 DB 값 우선, 없으면 환경변수(.env) 사용.
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_api_settings (
    name       TEXT        PRIMARY KEY,   -- 환경변수명과 동일 (예: KDB_TMDB_API_TOKEN)
    value      TEXT        NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_by TEXT        NOT NULL DEFAULT 'admin'
);

COMMIT;
