-- 0074_kdb_corrections.sql
-- 클라이언트(외부 소비자) 정정 신고 큐 + 감사.
--
-- 흐름(신뢰 모델: 근거기반 자동 + 미달은 큐):
--   POST /v1/corrections {entity_id|ko, locale, returned, suggested, evidence_url}
--     → suggested 가 locale 문자셋 가드 통과 + 현재 값이 교체가능(저신뢰 source)
--       + Wikidata 가 독립 확인(label/sitelink 일치) → 자동 반영(status=auto_applied,
--       source=wikidata-label, 원값은 returned_value 에 스냅샷되어 revert 가능).
--     → 그 외(근거 부족/보호된 값/operator_locked) → status=pending → /admin 또는
--       `kdb-app corrections approve|reject` 로 운영자 심사.
--
-- 단일 클라이언트의 주장만으로는 절대 자동 반영되지 않는다(권위 외부소스 교차검증
-- 필수) — 오남용 저항.
BEGIN;

CREATE TABLE IF NOT EXISTS kwave_kdb_corrections (
    id             BIGSERIAL   PRIMARY KEY,
    entity_id      uuid        NOT NULL REFERENCES kwave_entities(id) ON DELETE CASCADE,
    locale         text        NOT NULL,
    returned_value text        NOT NULL DEFAULT '',  -- 신고 시점의 KDB 값(=교체 시 원값 스냅샷)
    suggested_value text       NOT NULL,             -- 클라이언트가 제안한 현지 통용 표기
    evidence_url   text        NOT NULL DEFAULT '',
    reporter       text        NOT NULL DEFAULT '',  -- 신고 소비자 식별(키 해시 prefix/도메인)
    reason         text        NOT NULL DEFAULT '',
    status         text        NOT NULL DEFAULT 'pending',  -- pending|auto_applied|approved|rejected
    resolution     text        NOT NULL DEFAULT '',         -- 처리 근거 한 줄
    created_at     timestamptz NOT NULL DEFAULT now(),
    resolved_at    timestamptz
);

CREATE INDEX IF NOT EXISTS kwave_kdb_corrections_pending_idx
    ON kwave_kdb_corrections (created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS kwave_kdb_corrections_entity_idx
    ON kwave_kdb_corrections (entity_id);

COMMIT;
