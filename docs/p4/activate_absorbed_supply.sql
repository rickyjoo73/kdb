-- activate_absorbed_supply.sql — 흡수분을 실제 공급 상태로 올린다 (P4.02)
--
-- 왜 이 스크립트가 필요한가. 실측 2026-09-14:
--   공통 경로(common-reviewed-names-v1)로 공급되는 대상은 **0건**이다.
--   흡수분 537,841건은 전부 status='candidate' 라 evaluateCommon 이 policy_blocked
--   ("common identity is not active with reusable verified evidence") 를 돌려주고,
--   기존 원장 12,688건은 write_owner='kdb' 라 unverified 를 돌려준다.
--   표기는 이미 다 있다 — 범위 안 흡수분 기준 en 341,796 · ja 303,379 ·
--   zh-Hans 300,024 · zh-Hant 278,080. 승인된 정책, recorded, https 근거다.
--   **막고 있는 것은 출처가 아니라 대상 상태 두 가지다.**
--
-- 두 가지는 DB 가 이미 의존관계로 강제한다(0116/0118 트리거):
--     'activation requires verified reusable identity evidence'
--   즉 ① 정체성 근거의 export_allowed 를 올리지 않으면 ② active 로 올릴 수 없다.
--
-- ★그런데 ①을 올리는 코드 경로가 하나뿐이고, 그것이 wikidata 를 요구한다.
--   resolution_approve.go 의 ApproveResearch 는 QID·wikidata-label 표기를 전제로 한다.
--   흡수분의 정체성 근거는 provider='tdb'(자체 ID 관측)라 그 경로에 들어가지 못한다.
--   이는 이 계획서 자신의 원칙과 어긋난다 — "wikidata QID 는 보조다. 자체 ID 가 주
--   앵커다"(식별 계약 I03), p3_expand.sql 주석 "외부 식별자가 아니라 우리 원본의
--   관측이 정체성 근거다". 그 원칙대로 여는 경로가 이 스크립트다.
--
-- 무엇을 하지 않는가
--   · **일괄 활성화하지 않는다.** 실수요 실측이 그것을 금한다(아래).
--   · 표기를 만들지 않는다. 이미 관측된 표기를 공급 가능 상태로 바꿀 뿐이다.
--   · 분류를 확정하지 않는다. classification_status 는 손대지 않는다.
--   · 범위 밖(rejected)은 건드리지 않는다. 되돌리려면 먼저 범위를 되돌려야 한다.
--   · 유형 미상(unknown)은 올리지 않는다. 무엇인지 모르는 것을 공급하지 않는다(I02).
--
-- ★왜 일괄이 아닌가 — 실수요 3개월치(요청 표제어 6,530개) 실측이 이유다.
--   여행 유형과 겹치는 요청은 tourist_spot 2 · heritage_site 6 · cultural_facility 6 뿐이고,
--   그 겹침의 대부분이 **동명 함정**이었다:
--     JYP엔터테인먼트 → location.cultural_facility(건물)
--     신동(슈퍼주니어)  → location.legal_dong 6건
--     대치·태산·미도·구천·이현 → location.natural_feature(하천/산)
--     신데렐라 → 펜션 · 쉼 → "Swim" · 앨리스/슈퍼스타 → 식당
--   이들은 기존 원장에 이미 **활성 대상이 있어** 올바르게 답해지고 있다.
--   일괄로 열면 뉴스 표제어가 식당·펜션 이름으로 답해질 자리를 만든다.
--
-- 동명 함정 방어 두 겹
--   ① 이 스크립트: 같은 ko 이름의 **활성 기존 원장 대상이 있으면 올리지 않는다.**
--   ② 공통 계약 자체: commonCandidates 는 발견 전용이고, 소비자가 entity_id 를
--      명시해 묶지 않으면 identity='ambiguous' 다. 이름만으로는 답이 나가지 않는다.
--
-- 되돌리는 법 (시험 완료, 아래 §되돌리기)
--   정체성 근거의 export_allowed 를 false 로 내리면
--   kentity_invalidate_withdrawn_evidence 트리거가 대상을 candidate 로 되돌린다.
--
-- 실행:
--   psql -v ON_ERROR_STOP=1 -v ids='uuid,uuid,...' -f activate_absorbed_supply.sql
--   psql -v ON_ERROR_STOP=1 -v ids='...' -v dry=1 -f activate_absorbed_supply.sql   (점검만)

\set ON_ERROR_STOP on
\if :{?ids}
\else
  -- ★`\quit 1` 로 끝내면 안 된다 — psql 의 \quit 은 인자를 무시하고 **종료코드 0** 이다.
  --   호출측이 `psql -f ... && echo OK` 로 묶으면 아무것도 안 한 실행이 성공으로 보고된다.
  --   실제로 배포에서 같은 종류의 "조용한 성공"에 당한 적이 있다(3f8af1b). 예외로 끝낸다.
  \set ON_ERROR_STOP on
  DO $$ BEGIN RAISE EXCEPTION
    '-v ids=<uuid,uuid,...> 가 필요하다. 대상을 고르는 일은 이 스크립트의 몫이 아니다.'; END $$;
\endif
\if :{?dry}
\else
  \set dry 0
\endif

SET lock_timeout = '5s';
SET statement_timeout = '600s';

BEGIN;

-- ① 요청된 ID 중 **모든 조건을 통과하는 것만** 고른다. 통과하지 못한 것은 ②에서 이유와
--    함께 보여 주고 건드리지 않는다. 조용히 빠지는 것이 없어야 한다.
-- 중복 ID 가 들어오면 감사 기록이 겹쳐 찍힌다. DISTINCT 로 한 번만 받는다.
-- 공백이 섞인 목록('a, b')도 받아 준다 — 부르는 쪽 손글씨를 이유로 실패시키지 않는다.
CREATE TEMP TABLE req ON COMMIT DROP AS
SELECT DISTINCT btrim(x)::uuid AS id
  FROM unnest(string_to_array(:'ids', ',')) AS x
 WHERE btrim(x) <> '';

DO $$ BEGIN
  IF (SELECT count(*) FROM req) = 0 THEN
    RAISE EXCEPTION 'ids 가 비어 있다. 빈 실행을 성공으로 끝내지 않는다.';
  END IF;
END $$;

CREATE TEMP TABLE chk ON COMMIT DROP AS
SELECT r.id,
       e.id IS NOT NULL                                        AS 존재,
       coalesce(e.write_owner = 'native', false)               AS 흡수분,
       coalesce(e.status = 'candidate', false)                 AS 후보상태,
       coalesce(NOT e.operator_locked, false)                  AS 잠금없음,
       coalesce(e.entity_type <> 'unknown', false)             AS 유형확정,
       -- ★공급 자격은 **자체 ID 관측**이 준다. QID 는 보조다(I03).
       --   외부 항목을 가리키는 근거(= kentity_external_ids 가 정체성을 정의하는 출처)는
       --   여기 세지 않는다. 그러지 않으면 대상이 **QID 때문에** 열린다 —
       --   실제로 회수 스크립트가 한때 wikidata 정체성 근거를 export_allowed=true 로
       --   넣어 434건이 `tdb export=false / wikidata export=true` 가 됐다. 앵커가 뒤집힌 것이다.
       EXISTS (SELECT 1 FROM kentity_evidence v
                 JOIN kentity_source_policies p ON p.id = v.source_policy_id
                                               AND p.status = 'approved'
                                               AND (p.valid_until IS NULL OR p.valid_until > now())
                WHERE v.entity_id = r.id AND v.claim_type = 'identity'
                  AND v.status = 'verified'
                  AND v.provider NOT IN (SELECT DISTINCT provider FROM kentity_external_ids)) AS 자체ID근거,
       EXISTS (SELECT 1 FROM kentity_names n
                 JOIN kentity_evidence v ON v.id = n.evidence_id AND v.entity_id = n.entity_id
                 JOIN kentity_source_policies p ON p.provider = v.provider
                                               AND p.status = 'approved' AND p.name_export_allowed
                                               AND (p.valid_until IS NULL OR p.valid_until > now())
                WHERE n.entity_id = r.id AND n.locale <> 'ko'
                  AND n.kind = 'canonical' AND n.form = 'recorded' AND n.status = 'verified'
                  AND v.status = 'verified' AND v.export_allowed)  AS 외국어표기,
       NOT EXISTS (SELECT 1 FROM kentity_entities k
                    WHERE k.canonical_ko = e.canonical_ko
                      AND k.write_owner = 'kdb' AND k.status = 'active') AS 동명함정없음,
       -- ★출처가 "항목"을 가진 곳이면 그 항목이 지목되어 있어야 한다. (P4.03 실측 2026-09-14)
       --   흡수분의 표기 근거 65,972건이 provider='wikidata' · license='CC0-1.0' ·
       --   export_allowed=true 인데, 그중 **65,487건의 source_record_id 가 TDB 레코드
       --   UUID** 다. source_url 도 우리 admin 페이지다. 즉 "위키데이터가 그렇게 말했다"는
       --   주장은 있는데 **어느 항목인지가 없다.**
       --   이 상태로 올리면 확인할 수 없는 CC0 권리 주장과 출처 주장을 공급하게 된다.
       --   흡수분 인물 31,377명 중 16,875명은 TDB 에도 QID 가 아예 없고(원본 재확인),
       --   QID 가 있는 14,502명조차 그 QID 가 근거로 옮겨지지 않았다.
       --
       --   ★항목 식별이 필요한 출처를 손으로 적지 않는다 — `kentity_external_ids` 가
       --   정체성을 정의하는 출처가 곧 그것이다(현재 데이터에선 wikidata 하나).
       --   출처가 늘면 규칙이 저절로 따라간다.
       NOT EXISTS (
         SELECT 1
           FROM kentity_names n
           JOIN kentity_evidence v ON v.id = n.evidence_id AND v.entity_id = n.entity_id
          WHERE n.entity_id = r.id AND n.locale <> 'ko'
            AND n.kind = 'canonical' AND n.form = 'recorded' AND n.status = 'verified'
            AND v.status = 'verified' AND v.export_allowed
            AND v.provider IN (SELECT DISTINCT provider FROM kentity_external_ids)
            AND NOT EXISTS (SELECT 1 FROM kentity_external_ids x
                             WHERE x.entity_id = n.entity_id AND x.provider = v.provider
                               AND x.status = 'verified'
                               AND x.external_id = v.source_record_id)
       ) AS 출처항목지목,
       e.canonical_ko
  FROM req r LEFT JOIN kentity_entities e ON e.id = r.id;

CREATE TEMP TABLE tgt ON COMMIT DROP AS
SELECT id, canonical_ko FROM chk
 WHERE 존재 AND 흡수분 AND 후보상태 AND 잠금없음 AND 유형확정
   AND 자체ID근거 AND 외국어표기 AND 동명함정없음 AND 출처항목지목;

-- ② 떨어진 것을 이유와 함께 보여 준다.
SELECT c.id, coalesce(c.canonical_ko, '(원장에 없음)') AS 이름,
       concat_ws(', ',
         CASE WHEN NOT c.존재         THEN '원장에 없음' END,
         CASE WHEN c.존재 AND NOT c.흡수분      THEN '흡수분이 아님(기존 원장 소유)' END,
         CASE WHEN c.존재 AND NOT c.후보상태    THEN '후보 상태가 아님(이미 활성이거나 범위 밖)' END,
         CASE WHEN c.존재 AND NOT c.잠금없음    THEN '운영자 잠금' END,
         CASE WHEN c.존재 AND NOT c.유형확정    THEN '유형 미상 — 무엇인지 모르는 것을 공급하지 않는다' END,
         CASE WHEN c.존재 AND NOT c.자체ID근거  THEN '자체 ID 관측 근거가 없다 — QID 만으로는 열지 않는다(I03)' END,
         CASE WHEN c.존재 AND NOT c.외국어표기  THEN '공급할 외국어 표기 없음 — 올려도 답할 것이 없다' END,
         CASE WHEN c.존재 AND NOT c.동명함정없음 THEN '같은 이름의 활성 기존 원장 대상이 있다 — 개별 검수 필요' END,
         CASE WHEN c.존재 AND NOT c.출처항목지목 THEN '공급할 표기의 근거가 외부 출처를 말하면서 그 항목을 지목하지 못한다 — 확인할 수 없는 권리·출처 주장' END
       ) AS 제외사유
  FROM chk c
 WHERE c.id NOT IN (SELECT id FROM tgt)
 ORDER BY 2;

-- ③ **자체 ID 관측 근거**를 재사용 가능으로 올린다. 이것이 DB 트리거가 요구하는 전제다.
--    관측 자체는 이미 verified 다 — 바꾸는 것은 "이 관측을 공급 근거로 쓸 수 있다"는 표시뿐이다.
--    ★외부 항목 근거(wikidata 등)는 올리지 않는다. 보조가 문을 열면 안 된다(I03).
UPDATE kentity_evidence v
   SET export_allowed = true
  FROM tgt t
 WHERE v.entity_id = t.id
   AND v.claim_type = 'identity'
   AND v.status = 'verified'
   AND NOT v.export_allowed
   AND v.provider NOT IN (SELECT DISTINCT provider FROM kentity_external_ids)
   AND EXISTS (SELECT 1 FROM kentity_source_policies p
                WHERE p.id = v.source_policy_id AND p.status = 'approved'
                  AND (p.valid_until IS NULL OR p.valid_until > now()));

-- ④ 활성으로 올린다. ③이 실패했다면 여기서 트리거가 막는다(조용히 지나가지 않는다).
UPDATE kentity_entities e
   SET status     = 'active',
       revision   = e.revision + 1,
       updated_at = now()
  FROM tgt t
 WHERE e.id = t.id AND e.status = 'candidate';

-- ⑤ 감사 기록. 무엇을 근거로 열었는지 남긴다.
INSERT INTO kentity_audit_events (entity_id, actor, action, reason, before_value, after_value)
SELECT t.id, 'policy:absorbed-own-id-activation-v1', 'absorbed_identity_activated',
       '자체 ID 관측을 정체성 근거로 공급 개시 (P4.02, 2026-09-14). 표기를 만들지 않았고 분류를 확정하지 않았다.',
       jsonb_build_object('status', 'candidate'),
       jsonb_build_object(
         'status', 'active',
         'identity_basis', 'tdb-own-record-observation',
         'wikidata_anchor_required', false,
         'foreign_locales', (SELECT coalesce(jsonb_agg(DISTINCT n.locale ORDER BY n.locale), '[]'::jsonb)
                               FROM kentity_names n
                              WHERE n.entity_id = t.id AND n.locale <> 'ko'
                                AND n.kind = 'canonical' AND n.form = 'recorded' AND n.status = 'verified'),
         'official_name_asserted', false)
  FROM tgt t;

SELECT ' 요청' AS 구분, count(*) AS 수 FROM req
UNION ALL SELECT ' 활성으로 올림', count(*) FROM tgt
UNION ALL SELECT ' 제외', (SELECT count(*) FROM req) - (SELECT count(*) FROM tgt);

\if :dry
  \echo '>>> dry=1 — 되돌린다. 아무것도 바뀌지 않았다.'
  ROLLBACK;
\else
  COMMIT;
\endif

-- ────────────────────────── 되돌리기 ──────────────────────────
-- 근거의 공급 표시만 내리면 트리거가 나머지를 한다.
--
--   UPDATE kentity_evidence SET export_allowed = false
--    WHERE entity_id IN ('<uuid>', ...) AND claim_type = 'identity' AND status = 'verified';
--
-- kentity_invalidate_withdrawn_evidence 가
--   · 그 근거에 달린 표기를 blocked 로 내리고 (정체성 근거엔 표기가 달려 있지 않다)
--   · 재사용 가능한 정체성 근거가 남지 않으면 대상을 candidate 로 되돌리고
--   · kentity_audit_events 에 evidence_invalidated 를 남긴다.
-- 실측으로 확인한 동작이다 — 문서에만 적어 둔 처방이 실제로는 실패한 전례가 있다.
