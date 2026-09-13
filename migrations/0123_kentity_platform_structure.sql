-- 0123_kentity_platform_structure — P3.02 승격: docs/p1/p1_structure.sql
--
-- **운영 적용분.** 격리 복원본에서 P1/P2 전 과정을 통과한 구조를 그대로 옮긴다.
-- 원본 파일은 docs/p1/p1_structure.sql 에 남아 있고 내용은 동일하다(헤더와 타임아웃만 다르다).
-- 승인 근거: docs/KDB_P3_FIRST_BATCH_PLAN.md — 운영자 승인 2026-09-13.
--
-- ★타임아웃을 건다. ALTER TABLE 은 ACCESS EXCLUSIVE 를 잡으므로, 앞선 트랜잭션이
--   붙잡고 있으면 이 마이그레이션 뒤로 모든 질의가 줄을 선다. 기다리다 쌓이느니
--   빨리 실패하는 편이 낫다 — 실패하면 배포가 컨테이너를 건드리기 전에 멈춘다.
SET lock_timeout = '5s';
SET statement_timeout = '900s';

BEGIN;

-- ============================================================ 0. 확장 (D-05)
CREATE EXTENSION IF NOT EXISTS btree_gist;

-- ============================================================ 1. 분류·정책 사전 (§4, §10.6, §12.1)

CREATE TABLE kentity_types (
  code        text PRIMARY KEY CHECK (code ~ '^[a-z0-9_]{1,64}$'),
  label_ko    text NOT NULL CHECK (btrim(label_ko) <> ''),
  enabled     boolean NOT NULL DEFAULT false,
  sort_order  integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
  revision    bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE kentity_subtypes (
  entity_type text NOT NULL REFERENCES kentity_types(code) ON DELETE RESTRICT,
  code        text NOT NULL CHECK (code ~ '^[a-z0-9_]{1,64}$'),
  label_ko    text NOT NULL CHECK (btrim(label_ko) <> ''),
  parent_code text,
  enabled     boolean NOT NULL DEFAULT false,
  sort_order  integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
  revision    bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (entity_type, code),
  CHECK (parent_code IS NULL OR parent_code <> code),
  FOREIGN KEY (entity_type, parent_code)
    REFERENCES kentity_subtypes(entity_type, code) ON DELETE RESTRICT
);

CREATE TABLE kentity_role_types (
  code        text PRIMARY KEY CHECK (code ~ '^[a-z0-9_]{1,64}$'),
  label_ko    text NOT NULL CHECK (btrim(label_ko) <> ''),
  parent_code text REFERENCES kentity_role_types(code) ON DELETE RESTRICT,
  enabled     boolean NOT NULL DEFAULT false,
  sort_order  integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
  revision    bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now(),
  CHECK (parent_code IS NULL OR parent_code <> code)
);

-- §4.4 기존 보강
ALTER TABLE kentity_domains
  ADD COLUMN enabled    boolean NOT NULL DEFAULT false,
  ADD COLUMN sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
  ADD COLUMN revision   bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();

CREATE TABLE kentity_locales (
  code       text PRIMARY KEY CHECK (code ~ '^[A-Za-z]{2,3}(-[A-Za-z]{2,4})?$'),
  label_ko   text NOT NULL,
  enabled    boolean NOT NULL DEFAULT false,
  sort_order integer NOT NULL DEFAULT 0 CHECK (sort_order >= 0),
  revision   bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);

CREATE TABLE kentity_relation_types (
  code     text PRIMARY KEY CHECK (code ~ '^[a-z0-9_]{1,64}$'),
  label_ko text NOT NULL,
  enabled  boolean NOT NULL DEFAULT false,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);

CREATE TABLE kentity_relation_type_pairs (
  predicate    text NOT NULL REFERENCES kentity_relation_types(code) ON DELETE RESTRICT,
  subject_type text NOT NULL REFERENCES kentity_types(code) ON DELETE RESTRICT,
  object_type  text NOT NULL REFERENCES kentity_types(code) ON DELETE RESTRICT,
  PRIMARY KEY (predicate, subject_type, object_type)
);

CREATE TABLE kentity_position_types (
  code     text PRIMARY KEY CHECK (code ~ '^[a-z0-9_]{1,64}$'),
  label_ko text NOT NULL,
  enabled  boolean NOT NULL DEFAULT false,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0)
);

-- §12.1 + S03. 기본은 차단이다: 허용 플래그는 approved 일 때만 true 가 될 수 있다.
CREATE TABLE kentity_source_policies (
  id                     uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  provider               text NOT NULL CHECK (btrim(provider) <> ''),
  version                text NOT NULL CHECK (btrim(version) <> ''),
  license_code           text NOT NULL DEFAULT 'unreviewed',
  storage_allowed        boolean NOT NULL DEFAULT false,
  verification_allowed   boolean NOT NULL DEFAULT false,
  name_export_allowed    boolean NOT NULL DEFAULT false,
  excerpt_export_allowed boolean NOT NULL DEFAULT false,
  status                 text NOT NULL DEFAULT 'unreviewed'
                           CHECK (status IN ('unreviewed','approved','blocked','expired')),
  terms_url              text,
  reviewed_by            text,
  reviewed_at            timestamptz,
  valid_until            timestamptz,
  conditions             text NOT NULL DEFAULT '',
  created_at             timestamptz NOT NULL DEFAULT now(),
  revision               bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  changed_by             text,
  changed_at             timestamptz,
  change_reason          text,
  UNIQUE (provider, version),
  CONSTRAINT kentity_source_policies_approved_needs_reviewer
    CHECK (status <> 'approved' OR (reviewed_by IS NOT NULL AND reviewed_at IS NOT NULL)),
  CONSTRAINT kentity_source_policies_flags_need_approval
    CHECK (NOT (storage_allowed OR verification_allowed OR name_export_allowed OR excerpt_export_allowed)
           OR status = 'approved')
);

-- ============================================================ 2. 사전 seed (DIFF §6)
-- 출처: KDB_CLASSIFICATION_RULES.md §2(13유형)/§3(61세부유형)/§4(26직군)/§5(8분야),
--       KDB_NAME_READINESS_CONTRACT.md §2(13 locale), KDB_IDENTITY_CONTRACT.md §7(11 predicate),
--       KDB_TARGET_SCHEMA.md §10.6(10 직책).

-- 13유형. 현행 Go supportedTypes/DB CHECK 의 11개만 enabled=true 로 시작한다(D-14).
INSERT INTO kentity_types (code, label_ko, enabled, sort_order) VALUES
 ('person','실존 인물',true,10),('organization','기관·단체',true,20),('company','기업·사업체',true,30),
 ('team','스포츠팀',true,40),('league','리그',true,50),('location','장소',true,60),
 ('work','작품',true,70),('event','행사·대회',true,80),('product','상품',true,90),
 ('concept','명명된 개념·용어',true,100),('brand','브랜드',false,110),('character','캐릭터',false,120),
 ('unknown','유형 검수 대기',true,999);

-- 61세부유형. 1차는 2단계만 쓰므로 parent_code 는 전부 NULL 이다.
INSERT INTO kentity_subtypes (entity_type, code, label_ko, enabled, sort_order) VALUES
 ('person','real','실존 인물',true,10),
 ('organization','public_body','공공기관·행정기관',true,10),
 ('organization','political_party','정당',true,20),
 ('organization','school','학교 조직',true,30),
 ('organization','media_outlet','매체·채널 운영 단체',true,40),
 ('organization','association','협회·단체',true,50),
 ('organization','performance_group','음악·공연 그룹',true,60),
 ('organization','research_institute','연구기관',true,70),
 ('organization','religious_body','종교 조직',true,80),
 ('company','corporation','법인 기업',true,10),
 ('company','business','비법인 사업체',true,20),
 ('company','agency','상업적 매니지먼트·대행 사업체',true,30),
 ('team','sports_team','스포츠팀',true,10),
 ('league','sports_league','스포츠 리그',true,10),
 ('location','tourist_spot','관광 명소',true,10),
 ('location','accommodation','숙박 시설·지점',true,20),
 ('location','restaurant','음식점 지점',true,30),
 ('location','shopping','판매 시설·점포',true,40),
 ('location','cultural_facility','문화시설',true,50),
 ('location','sports_facility','체육·레포츠 시설',true,60),
 ('location','stadium','경기장',true,70),
 ('location','heritage_site','장소형 문화유산',true,80),
 ('location','natural_feature','자연 지명',true,90),
 ('location','admin_region','행정구역',true,100),
 ('location','legal_dong','법정동',true,110),
 ('location','district','상권·지구',true,120),
 ('location','road','도로',true,130),
 ('location','station','역·정류장',true,140),
 ('location','airport','공항',true,150),
 ('location','port','항구·터미널',true,160),
 ('location','transport_facility','그 밖의 확인된 교통시설',true,170),
 ('location','transit_route','명명된 교통 노선',true,180),
 ('location','travel_course','명명된 여행 코스',true,190),
 ('location','campus','학교 캠퍼스',true,200),
 ('work','film','영화',true,10),('work','drama','드라마',true,20),
 ('work','broadcast_program','방송 프로그램',true,30),('work','episode','개별 회차 작품',true,40),
 ('work','song','곡',true,50),('work','album','음반',true,60),
 ('work','book','도서·문학 작품',true,70),('work','artwork','미술·창작 작품',true,80),
 ('work','artifact','이동 가능한 유물',true,90),
 ('event','festival_series','축제 시리즈',true,10),('event','festival_edition','축제 회차',true,20),
 ('event','performance','공연 행사',true,30),('event','tour','공연 투어',true,40),
 ('event','competition_series','대회 시리즈',true,50),('event','competition_edition','대회 회차·시즌',true,60),
 ('event','election','특정 선거',true,70),('event','conference','회의·박람회 행사',true,80),
 ('product','named_product','고유명으로 구별되는 상품',true,10),
 ('product','vehicle_model','차량 모델',true,20),('product','software','소프트웨어 제품',true,30),
 ('product','food_product','명명된 식음료 상품',true,40),
 ('concept','named_term','번역 관리 대상의 고정 용어',true,10),
 ('concept','food_name','전통 음식·메뉴의 고정 명칭',true,20),
 ('concept','award','상의 명칭',true,30),('concept','program','정책·사업의 명칭',true,40),
 ('brand','commercial_brand','상업 브랜드',false,10),
 ('character','fictional','허구 캐릭터',false,10);

-- 26직군. 자기참조 FK 때문에 뿌리 → 자식 → 손자(rapper) 순서로 넣는다.
INSERT INTO kentity_role_types (code, label_ko, parent_code, enabled, sort_order) VALUES
 ('businessperson','기업인',NULL,true,10),('politician','정치인',NULL,true,20),
 ('public_official','공직자',NULL,true,30),('entertainer','연예인',NULL,true,40),
 ('sports_person','스포츠인',NULL,true,50),('journalist','언론인',NULL,true,60),
 ('researcher','연구자',NULL,true,70),('educator','교육자',NULL,true,80),
 ('writer','작가',NULL,true,90),('director','감독',NULL,true,100),
 ('producer','제작자·프로듀서',NULL,true,110),('creator','창작자',NULL,true,120),
 ('artist','예술가',NULL,true,130),('professional','전문직 종사자',NULL,true,140);
INSERT INTO kentity_role_types (code, label_ko, parent_code, enabled, sort_order) VALUES
 ('entrepreneur','창업가','businessperson',true,11),('executive','기업 경영자','businessperson',true,12),
 ('singer','가수','entertainer',true,41),('actor','배우','entertainer',true,42),
 ('idol','아이돌','entertainer',true,43),('broadcaster','방송인','entertainer',true,44),
 ('comedian','코미디언','entertainer',true,45),('model','모델','entertainer',true,46),
 ('athlete','선수','sports_person',true,51),('coach','지도자','sports_person',true,52),
 ('referee','심판','sports_person',true,53);
INSERT INTO kentity_role_types (code, label_ko, parent_code, enabled, sort_order) VALUES
 ('rapper','래퍼','singer',true,411);

-- 8분야 (기존 행 활성화)
UPDATE kentity_domains SET enabled = true, sort_order = CASE code
  WHEN 'politics' THEN 10 WHEN 'government' THEN 20 WHEN 'economy' THEN 30 WHEN 'society' THEN 40
  WHEN 'entertainment' THEN 50 WHEN 'sports' THEN 60 WHEN 'travel' THEN 70 WHEN 'culture' THEN 80 END;

-- 13 locale. TDB th 는 disabled 이고, 실제 요청이 없는 de/fr/ru 도 false 로 시작한다.
INSERT INTO kentity_locales (code, label_ko, enabled, sort_order) VALUES
 ('ko','한국어',true,10),('en','영어',true,20),('ja','일본어',true,30),
 ('zh-Hans','중국어 간체',true,40),('zh-Hant','중국어 번체',true,50),('vi','베트남어',true,60),
 ('id','인도네시아어',true,70),('es','스페인어',true,80),('pt-BR','포르투갈어(브라질)',true,90),
 ('de','독일어',false,100),('fr','프랑스어',false,110),('ru','러시아어',false,120),('th','태국어',false,130);
-- ★ D-17: 현행 kentity_names 에 모호한 legacy 태그 'zh' 가 1행 있다(그 밖의 표는 전부 정확 태그).
-- 표기 계약 §2 는 "새 공통 API 에서 모호한 zh 를 무조건 Hans 로 바꾸지 않는다"고 못박았으므로
-- 값을 zh-Hans 로 고쳐 쓰지 않는다. 대신 요청할 수 없는 비활성 호환 코드로 남겨 행을 보존하고
-- 정확 태그 판정은 검수(P2)로 보낸다. enabled=false 이므로 새 요청·공급에는 쓰이지 않는다.
INSERT INTO kentity_locales (code, label_ko, enabled, sort_order) VALUES
 ('zh','중국어(모호 · legacy 호환, 요청 불가)',false,900);

-- 11 predicate + 30 허용 조합 (IDENTITY_CONTRACT §7 의 정확 조합만)
INSERT INTO kentity_relation_types (code, label_ko, enabled) VALUES
 ('member_of','소속',true),('holds_position','직책 재임',true),('operates','운영',true),
 ('located_in','소재',true),('created_by','창작 주체',true),('appears_in','출연·수록',true),
 ('portrays','배역 연기',true),('manufactured_by','제조 주체',true),('branded_as','브랜드 귀속',true),
 ('edition_of','회차·판',true),('held_at','개최 장소',true);
INSERT INTO kentity_relation_type_pairs (predicate, subject_type, object_type) VALUES
 ('member_of','person','organization'),('member_of','person','company'),('member_of','person','team'),
 ('holds_position','person','organization'),('holds_position','person','company'),
 ('holds_position','person','team'),('holds_position','person','league'),
 ('operates','organization','location'),('operates','organization','team'),
 ('operates','organization','league'),('operates','organization','brand'),
 ('operates','company','location'),('operates','company','team'),
 ('operates','company','league'),('operates','company','brand'),
 ('located_in','location','location'),
 ('created_by','work','person'),('created_by','work','organization'),('created_by','work','company'),
 ('appears_in','person','work'),('appears_in','character','work'),
 ('portrays','person','character'),
 ('manufactured_by','product','company'),('manufactured_by','product','organization'),
 ('branded_as','product','brand'),('branded_as','location','brand'),
 ('edition_of','event','event'),('edition_of','event','league'),('edition_of','work','work'),
 ('held_at','event','location');

-- 10 직책
INSERT INTO kentity_position_types (code, label_ko, enabled) VALUES
 ('ceo','대표이사',true),('executive','임원',true),('legislator','국회의원',true),
 ('mayor','시장·단체장',true),('minister','장관',true),('chairperson','의장·회장',true),
 ('head_coach','감독',true),('coach','코치',true),('player','선수',true),('member','구성원',true);

-- ============================================================ 3. kentity_entities 보강 (§5, §10.1, §16)

-- 기존 subtype 은 NOT NULL DEFAULT ''. 미확인과 잘못된 조합을 구별하려면 NULL 이어야 한다.
-- 제약을 먼저 풀고 값을 바꾼다(순서를 뒤집으면 NOT NULL 위반으로 실패한다).
ALTER TABLE kentity_entities
  ALTER COLUMN subtype DROP NOT NULL,
  ALTER COLUMN subtype DROP DEFAULT,
  ALTER COLUMN status  SET DEFAULT 'candidate';           -- D-07
UPDATE kentity_entities SET subtype = NULL WHERE subtype = '';

ALTER TABLE kentity_entities
  ADD COLUMN qualifier_ko                  text,           -- D-04 (§16). 표시 한정어이며 정체성 키가 아니다.
  ADD COLUMN classification_status         text NOT NULL DEFAULT 'pending'
                                             CHECK (classification_status IN ('pending','conflict','verified')),
  ADD COLUMN classification_reason         text NOT NULL DEFAULT 'awaiting_classification'
                                             CHECK (btrim(classification_reason) <> ''),
  ADD COLUMN classification_evidence_id    uuid,
  ADD COLUMN classification_policy_version text NOT NULL DEFAULT 'classification-design-v1',
  ADD COLUMN classified_by                 text,
  ADD COLUMN classified_at                 timestamptz,
  ADD COLUMN identity_revision             bigint NOT NULL DEFAULT 1 CHECK (identity_revision > 0),
  ADD COLUMN dependency_epoch              bigint NOT NULL DEFAULT 1 CHECK (dependency_epoch > 0);

-- ★ P1.02 에서 드러난 설계↔현행 충돌 (D-16)
-- 현행 subtype 에는 목표 세부유형이 아니라 legacy 유형(song_album, term, group, brand_place …)이
-- 투영돼 있고(D-15), 그 투영을 **트리거가 계속 유지**한다:
--   kentity_sync_legacy_identity() 가 kwave_entities 변경마다
--   `subtype = NEW.entity_type::text` 로 legacy enum 을 덮어쓴다.
-- 따라서 subtype 을 NULL 로 내려도 legacy 쓰기 한 번이면 되돌아오고,
-- 그 순간 (entity_type,subtype)→kentity_subtypes FK 가 깨져 legacy 쓰기 자체가 실패한다.
-- 설계(§5 "기존 ''→선매핑/NULL, 복합 FK")는 이 트리거를 함께 고치지 않으면 성립하지 않는다.
--
-- 처리: (1) 값은 NULL(미확인)로 내리되 원문을 classification_reason 에 보존한다.
--       (2) 동기화 트리거가 더 이상 subtype 을 쓰지 않게 바꾼다. legacy 유형은
--           kwave_entities.entity_type 에 그대로 남아 있으므로 정보 손실이 없다.
-- 선매핑(legacy 유형 → 목표 세부유형)은 분류 규칙 §3·§6 대로 P2.02 에서 한다.

-- kentity_guard_legacy_owner 는 write_owner='kdb' 행을 트리거 밖에서 쓰지 못하게 막는다
-- (pg_trigger_depth()<2 → RAISE). 구조 변경은 migration 역할의 정당한 작업이므로
-- 이 UPDATE 동안만 끈다. 런타임 pool 에는 이 권한을 주지 않는다(WRITER_AUTHORITY A01).
ALTER TABLE kentity_entities DISABLE TRIGGER kentity_legacy_owner;
UPDATE kentity_entities e
   SET classification_reason = 'legacy_subtype=' || e.subtype || '; awaiting_p2_02_premapping',
       subtype = NULL
 WHERE e.subtype IS NOT NULL
   AND NOT EXISTS (SELECT 1 FROM kentity_subtypes s
                    WHERE s.entity_type = e.entity_type AND s.code = e.subtype);
ALTER TABLE kentity_entities ENABLE TRIGGER kentity_legacy_owner;

-- (2) 동기화 트리거 보강: subtype 투영을 제거한다. 나머지 동작은 0118 원본 그대로다.
CREATE OR REPLACE FUNCTION kentity_sync_legacy_identity() RETURNS trigger LANGUAGE plpgsql AS $sync$
BEGIN
 IF TG_OP='DELETE' THEN
  UPDATE kentity_entities SET status='retired',revision=revision+1,updated_at=now() WHERE id=OLD.id AND write_owner='kdb';
  RETURN OLD;
 END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND origin_system<>'kdb') THEN
  RAISE EXCEPTION 'Entity UUID belongs to another source';
 END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities WHERE id=NEW.id AND write_owner<>'kdb') THEN
  IF NEW.operator_locked THEN UPDATE kentity_entities SET operator_locked=true,revision=revision+1,updated_at=now() WHERE id=NEW.id AND NOT operator_locked; END IF;
  RETURN NEW;
 END IF;
 IF TG_OP='UPDATE' AND (OLD.entity_type,OLD.canonical_ko,OLD.status,OLD.operator_locked)
 IS NOT DISTINCT FROM (NEW.entity_type,NEW.canonical_ko,NEW.status,NEW.operator_locked) THEN RETURN NEW; END IF;
 -- subtype 은 더 이상 투영하지 않는다(D-16). 목표 사전 코드만 들어가야 하며 선매핑은 P2.02.
 INSERT INTO kentity_entities(id,entity_type,canonical_ko,origin_system,write_owner,status,operator_locked,created_at,updated_at)
 VALUES(NEW.id,kentity_legacy_type(NEW.entity_type::text),NEW.canonical_ko,'kdb','kdb',
 CASE WHEN NEW.status IN ('active','candidate','rejected') THEN NEW.status ELSE 'retired' END,NEW.operator_locked,NEW.created_at,NEW.updated_at)
 ON CONFLICT(id) DO UPDATE SET entity_type=EXCLUDED.entity_type,canonical_ko=EXCLUDED.canonical_ko,
 status=EXCLUDED.status,operator_locked=EXCLUDED.operator_locked,revision=kentity_entities.revision+1,updated_at=now()
 WHERE kentity_entities.write_owner='kdb' AND (kentity_entities.entity_type,kentity_entities.canonical_ko,kentity_entities.status,kentity_entities.operator_locked)
 IS DISTINCT FROM (EXCLUDED.entity_type,EXCLUDED.canonical_ko,EXCLUDED.status,EXCLUDED.operator_locked);
 RETURN NEW;
END $sync$;

ALTER TABLE kentity_entities
  ADD CONSTRAINT kentity_entities_id_entity_type_key UNIQUE (id, entity_type),
  ADD CONSTRAINT kentity_entities_type_fk
    FOREIGN KEY (entity_type) REFERENCES kentity_types(code) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_entities_subtype_fk
    FOREIGN KEY (entity_type, subtype) REFERENCES kentity_subtypes(entity_type, code) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_entities_classification_evidence_fk
    FOREIGN KEY (classification_evidence_id, id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_entities_verified_classification CHECK (
    classification_status <> 'verified'
    OR (entity_type <> 'unknown' AND subtype IS NOT NULL
        AND classification_evidence_id IS NOT NULL
        AND classified_by IS NOT NULL AND classified_at IS NOT NULL));

-- D-14 호환 전환 완료: 유형 허용값의 근거를 CHECK(11값 하드코딩)에서 사전 FK 로 옮긴다.
-- 사전은 13코드이고 brand/character 는 enabled=false 다. 즉 저장은 가능하되
-- 서비스 노출·활성은 enabled 로 가른다(§4.1 "승인된 seed만 true, 신규 코드 자동 노출 금지").
-- 이 gate 는 DB CHECK 로 표현할 수 없다(다른 행 조회 금지, §3). 읽기 경로에서 사전을 읽어 거른다.
ALTER TABLE kentity_entities DROP CONSTRAINT kentity_entities_entity_type_check;

CREATE INDEX kentity_entities_list          ON kentity_entities (status, updated_at, id);
CREATE INDEX kentity_entities_type_filter   ON kentity_entities (entity_type, subtype, status, id);
CREATE INDEX kentity_entities_class_queue   ON kentity_entities (classification_status, updated_at, id)
  WHERE classification_status <> 'verified';

-- ============================================================ 4. kentity_person_roles (§6.1 + S07)

CREATE TABLE kentity_person_roles (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  entity_id   uuid NOT NULL,
  entity_type text NOT NULL DEFAULT 'person' CHECK (entity_type = 'person'),
  role_code   text NOT NULL REFERENCES kentity_role_types(code) ON DELETE RESTRICT,
  status      text NOT NULL DEFAULT 'unverified'
                CHECK (status IN ('unverified','verified','withdrawn','blocked')),
  evidence_id uuid,
  valid_from  date, valid_until date,
  valid_from_precision  text NOT NULL DEFAULT 'unknown'
    CHECK (valid_from_precision IN ('unknown','open','year','month','day')),
  valid_until_precision text NOT NULL DEFAULT 'unknown'
    CHECK (valid_until_precision IN ('unknown','open','year','month','day')),
  valid_from_year  smallint, valid_until_year  smallint,
  valid_from_month smallint, valid_until_month smallint,
  assigned_by    text NOT NULL CHECK (btrim(assigned_by) <> ''),
  reason         text NOT NULL CHECK (btrim(reason) <> ''),
  policy_version text NOT NULL CHECK (btrim(policy_version) <> ''),
  verified_by    text, verified_at timestamptz,
  operator_locked boolean NOT NULL DEFAULT false,
  revision   bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  -- §14.7: 중복 검사 계산 전용 외피. 관측한 날짜로 표시하지 않는다.
  possible_validity daterange GENERATED ALWAYS AS (daterange(
    CASE valid_from_precision
      WHEN 'day'   THEN valid_from
      WHEN 'month' THEN make_date(valid_from_year::int, valid_from_month::int, 1)
      WHEN 'year'  THEN make_date(valid_from_year::int, 1, 1) ELSE NULL END,
    CASE valid_until_precision
      WHEN 'day'   THEN valid_until
      WHEN 'month' THEN (make_date(valid_until_year::int, valid_until_month::int, 1) + interval '1 month - 1 day')::date
      WHEN 'year'  THEN make_date(valid_until_year::int, 12, 31) ELSE NULL END,
    '[]')) STORED,
  UNIQUE (id, entity_id),
  FOREIGN KEY (entity_id, entity_type)
    REFERENCES kentity_entities(id, entity_type) ON DELETE RESTRICT,
  FOREIGN KEY (evidence_id, entity_id)
    REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  CONSTRAINT kentity_person_roles_verified_needs_evidence CHECK (
    status <> 'verified' OR (evidence_id IS NOT NULL AND verified_by IS NOT NULL AND verified_at IS NOT NULL)),
  CONSTRAINT kentity_person_roles_from_precision CHECK (COALESCE(
    CASE valid_from_precision
      WHEN 'unknown' THEN valid_from IS NULL AND valid_from_year IS NULL AND valid_from_month IS NULL
      WHEN 'open'    THEN valid_from IS NULL AND valid_from_year IS NULL AND valid_from_month IS NULL
      WHEN 'year'    THEN valid_from IS NULL AND valid_from_year IS NOT NULL AND valid_from_year BETWEEN 1 AND 9999 AND valid_from_month IS NULL
      WHEN 'month'   THEN valid_from IS NULL AND valid_from_year IS NOT NULL AND valid_from_year BETWEEN 1 AND 9999 AND valid_from_month IS NOT NULL AND valid_from_month BETWEEN 1 AND 12
      WHEN 'day'     THEN valid_from IS NOT NULL
                          AND (valid_from_year  IS NULL OR valid_from_year  = EXTRACT(YEAR  FROM valid_from))
                          AND (valid_from_month IS NULL OR valid_from_month = EXTRACT(MONTH FROM valid_from))
    END, false)),
  CONSTRAINT kentity_person_roles_until_precision CHECK (COALESCE(
    CASE valid_until_precision
      WHEN 'unknown' THEN valid_until IS NULL AND valid_until_year IS NULL AND valid_until_month IS NULL
      WHEN 'open'    THEN valid_until IS NULL AND valid_until_year IS NULL AND valid_until_month IS NULL
      WHEN 'year'    THEN valid_until IS NULL AND valid_until_year IS NOT NULL AND valid_until_year BETWEEN 1 AND 9999 AND valid_until_month IS NULL
      WHEN 'month'   THEN valid_until IS NULL AND valid_until_year IS NOT NULL AND valid_until_year BETWEEN 1 AND 9999 AND valid_until_month IS NOT NULL AND valid_until_month BETWEEN 1 AND 12
      WHEN 'day'     THEN valid_until IS NOT NULL
                          AND (valid_until_year  IS NULL OR valid_until_year  = EXTRACT(YEAR  FROM valid_until))
                          AND (valid_until_month IS NULL OR valid_until_month = EXTRACT(MONTH FROM valid_until))
    END, false)),
  CONSTRAINT kentity_person_roles_order CHECK (
    valid_until IS NULL OR valid_from IS NULL OR valid_until >= valid_from),
  -- M01: 다른 UUID 이면 같은 직업·같은 기간도 정상이다. entity_id 가 키에 있으므로 충돌하지 않는다.
  EXCLUDE USING gist (entity_id WITH =, role_code WITH =, possible_validity WITH &&)
    WHERE (status = 'verified')
);
CREATE INDEX kentity_person_roles_entity   ON kentity_person_roles (entity_id, status);
CREATE INDEX kentity_person_roles_by_role  ON kentity_person_roles (role_code, entity_id) WHERE status = 'verified';
CREATE INDEX kentity_person_roles_evidence ON kentity_person_roles (evidence_id, entity_id) WHERE evidence_id IS NOT NULL;

-- ============================================================ 5. kentity_entity_domains 보강 (§6.2)

ALTER TABLE kentity_entity_domains
  ADD COLUMN status text NOT NULL DEFAULT 'unverified'
    CHECK (status IN ('unverified','verified','withdrawn','blocked')),
  ADD COLUMN evidence_id uuid,
  ADD COLUMN policy_version text,
  ADD COLUMN verified_by text,
  ADD COLUMN verified_at timestamptz,
  ADD COLUMN operator_locked boolean NOT NULL DEFAULT false,
  ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
UPDATE kentity_entity_domains SET policy_version = 'legacy-import-v0' WHERE policy_version IS NULL;
ALTER TABLE kentity_entity_domains
  ALTER COLUMN policy_version SET NOT NULL,
  ADD CONSTRAINT kentity_entity_domains_evidence_fk
    FOREIGN KEY (evidence_id, entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_entity_domains_verified_needs_evidence CHECK (
    status <> 'verified' OR (evidence_id IS NOT NULL AND verified_by IS NOT NULL AND verified_at IS NOT NULL));
CREATE INDEX kentity_entity_domains_by_domain ON kentity_entity_domains (domain, entity_id) WHERE status = 'verified';
CREATE INDEX kentity_entity_domains_evidence  ON kentity_entity_domains (evidence_id, entity_id) WHERE evidence_id IS NOT NULL;

-- ============================================================ 6. 정체성 (§10.1~10.4 + S04/S05)

-- 6.1 crosswalk: PK 를 (source_system, source_table, source_id) 로 전환
ALTER TABLE kentity_crosswalks
  ADD COLUMN id uuid NOT NULL DEFAULT gen_random_uuid(),
  ADD COLUMN source_table text,
  ADD COLUMN source_state text NOT NULL DEFAULT 'unknown'
    CHECK (source_state IN ('present','deleted','merged','unknown')),
  ADD COLUMN source_merged_into text,
  ADD COLUMN source_observed_at timestamptz,
  ADD COLUMN mapping_policy_version text,
  ADD COLUMN target_identity_revision bigint NOT NULL DEFAULT 0 CHECK (target_identity_revision >= 0);
-- 확정된 source_system 매핑만 backfill 한다. 공통 기본값 'places' 를 넣지 않는다(IDENTITY_CONTRACT §3).
UPDATE kentity_crosswalks SET source_table = 'kwave_persons' WHERE source_system = 'legacy_person';
UPDATE kentity_crosswalks SET source_table = 'tdb_places'    WHERE source_system = 'tdb';
UPDATE kentity_crosswalks SET source_table = 'kwave_entities' WHERE source_system = 'kdb';
UPDATE kentity_crosswalks SET mapping_policy_version = 'legacy-import-v0' WHERE mapping_policy_version IS NULL;
ALTER TABLE kentity_crosswalks
  ALTER COLUMN source_table SET NOT NULL,
  ALTER COLUMN mapping_policy_version SET NOT NULL,
  DROP CONSTRAINT kentity_crosswalks_pkey,
  ADD PRIMARY KEY (source_system, source_table, source_id),
  ADD CONSTRAINT kentity_crosswalks_id_key UNIQUE (id),
  DROP CONSTRAINT kentity_crosswalks_status_check,
  ADD CONSTRAINT kentity_crosswalks_status_check
    CHECK (status IN ('review','confirmed','conflict','rejected','withdrawn')),
  ADD CONSTRAINT kentity_crosswalks_confirmed_identity
    CHECK (status <> 'confirmed' OR target_identity_revision > 0);
CREATE INDEX kentity_crosswalks_entity ON kentity_crosswalks (entity_id);      -- D-11
CREATE INDEX kentity_crosswalks_review_scan
  ON kentity_crosswalks (status, source_system, source_table, updated_at, id);

-- 6.2 external_ids / id_reservations (§10.1 + S04)
ALTER TABLE kentity_external_ids
  ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  ADD COLUMN observed_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN operator_locked boolean NOT NULL DEFAULT false,
  ADD COLUMN policy_version text;
UPDATE kentity_external_ids SET policy_version = 'legacy-import-v0' WHERE policy_version IS NULL;
ALTER TABLE kentity_external_ids ALTER COLUMN policy_version SET NOT NULL;
CREATE INDEX kentity_external_ids_evidence ON kentity_external_ids (evidence_id, entity_id)
  WHERE evidence_id IS NOT NULL;

ALTER TABLE kentity_id_reservations
  ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now();
ALTER TABLE kentity_id_reservations RENAME COLUMN native_owner TO entity_id;
CREATE INDEX kentity_id_reservations_entity ON kentity_id_reservations (entity_id);   -- D-13

-- 6.3 pair 판정 (§10.2)
CREATE TABLE kentity_identity_decisions (
  left_id  uuid NOT NULL REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  right_id uuid NOT NULL REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  decision text NOT NULL DEFAULT 'possible_same'
    CHECK (decision IN ('possible_same','confirmed_same','distinct','withdrawn')),
  left_identity_revision  bigint NOT NULL CHECK (left_identity_revision  > 0),
  right_identity_revision bigint NOT NULL CHECK (right_identity_revision > 0),
  left_evidence_id  uuid,
  right_evidence_id uuid,
  actor          text NOT NULL CHECK (btrim(actor) <> ''),
  reason         text NOT NULL CHECK (btrim(reason) <> ''),
  policy_version text NOT NULL CHECK (btrim(policy_version) <> ''),
  revision   bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (left_id, right_id),
  CONSTRAINT kentity_identity_decisions_order CHECK (left_id < right_id),
  FOREIGN KEY (left_evidence_id,  left_id)  REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  FOREIGN KEY (right_evidence_id, right_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  -- confirmed_same/distinct 는 양쪽 근거가 있어야 한다. possible_same 은 보류 상태다.
  CONSTRAINT kentity_identity_decisions_strong_needs_evidence CHECK (
    decision NOT IN ('confirmed_same','distinct')
    OR (left_evidence_id IS NOT NULL AND right_evidence_id IS NOT NULL))
);
CREATE INDEX kentity_identity_decisions_right ON kentity_identity_decisions (right_id, left_id);
CREATE INDEX kentity_identity_decisions_queue ON kentity_identity_decisions (decision, updated_at);

-- 6.4 병합/분리/유형 정정 (§10.3 + S05)
CREATE TABLE kentity_identity_operations (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_key    text NOT NULL CHECK (btrim(owner_key) <> ''),
  request_key  text NOT NULL CHECK (btrim(request_key) <> ''),
  payload_hash text NOT NULL CHECK (payload_hash ~ '^[a-f0-9]{64}$'),
  operation    text NOT NULL CHECK (operation IN ('merge','split','retype')),
  source_id    uuid NOT NULL REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  target_id    uuid NOT NULL REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  status       text NOT NULL DEFAULT 'planned' CHECK (status IN ('planned','applied','rejected')),
  plan         jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(plan) = 'object'),
  plan_hash    text NOT NULL CHECK (plan_hash ~ '^[a-f0-9]{64}$'),
  actor        text NOT NULL CHECK (btrim(actor) <> ''),
  reason       text NOT NULL CHECK (btrim(reason) <> ''),
  reversal_of  uuid REFERENCES kentity_identity_operations(id) ON DELETE RESTRICT,
  created_at   timestamptz NOT NULL DEFAULT now(),
  applied_at   timestamptz,
  revision     bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  UNIQUE (owner_key, request_key),
  CONSTRAINT kentity_identity_operations_retype_self CHECK (
    (operation = 'retype') = (source_id = target_id)),
  CONSTRAINT kentity_identity_operations_applied_at CHECK (
    (status = 'applied') = (applied_at IS NOT NULL)),
  CONSTRAINT kentity_identity_operations_no_self_reversal CHECK (
    reversal_of IS NULL OR reversal_of <> id),
  CONSTRAINT kentity_identity_operations_reversal_only_split CHECK (
    (reversal_of IS NOT NULL) = (operation = 'split')),
  CONSTRAINT kentity_identity_operations_plan_size CHECK (pg_column_size(plan) <= 1048576)
);
CREATE UNIQUE INDEX kentity_identity_operations_one_applied_reversal
  ON kentity_identity_operations (reversal_of)
  WHERE status = 'applied' AND reversal_of IS NOT NULL;
CREATE INDEX kentity_identity_operations_source ON kentity_identity_operations (source_id, created_at);
CREATE INDEX kentity_identity_operations_target ON kentity_identity_operations (target_id, created_at);

-- 6.5 redirect (§10.4 + S05)
CREATE TABLE kentity_redirects (
  from_id      uuid PRIMARY KEY REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  to_id        uuid NOT NULL REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  operation_id uuid NOT NULL REFERENCES kentity_identity_operations(id) ON DELETE RESTRICT,
  revision     bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at   timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT kentity_redirects_not_self CHECK (from_id <> to_id)
);
CREATE INDEX kentity_redirects_to ON kentity_redirects (to_id);

-- 6.6 audit 보강 (§10.5 + §15)
ALTER TABLE kentity_audit_events
  ADD COLUMN operation_id uuid REFERENCES kentity_identity_operations(id) ON DELETE RESTRICT,
  ADD COLUMN object_table text NOT NULL DEFAULT '',
  ADD COLUMN object_key   jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(object_key) = 'object'),
  ADD COLUMN before_hash  text,
  ADD COLUMN after_hash   text,
  ADD COLUMN migration_record_id uuid,
  ADD CONSTRAINT kentity_audit_events_operation_needs_object CHECK (
    operation_id IS NULL
    OR (btrim(object_table) <> '' AND object_key <> '{}'::jsonb
        AND before_hash IS NOT NULL AND after_hash IS NOT NULL));
CREATE INDEX kentity_audit_events_operation ON kentity_audit_events (operation_id, id)
  WHERE operation_id IS NOT NULL;

-- ============================================================ 7. 관계·프로필 (§10.6, §11)

ALTER TABLE kentity_relations
  ADD COLUMN subject_type text,
  ADD COLUMN object_type  text,
  ADD COLUMN position_code text REFERENCES kentity_position_types(code) ON DELETE RESTRICT,
  ADD COLUMN operator_locked boolean NOT NULL DEFAULT false,
  ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD COLUMN valid_from_precision  text NOT NULL DEFAULT 'unknown'
    CHECK (valid_from_precision IN ('unknown','open','year','month','day')),
  ADD COLUMN valid_until_precision text NOT NULL DEFAULT 'unknown'
    CHECK (valid_until_precision IN ('unknown','open','year','month','day')),
  ADD COLUMN valid_from_year smallint, ADD COLUMN valid_until_year smallint,
  ADD COLUMN valid_from_month smallint, ADD COLUMN valid_until_month smallint;
-- 0행이므로 backfill 없이 NOT NULL 로 올린다.
ALTER TABLE kentity_relations
  ALTER COLUMN subject_type SET NOT NULL,
  ALTER COLUMN object_type  SET NOT NULL,
  ADD CONSTRAINT kentity_relations_predicate_fk
    FOREIGN KEY (predicate) REFERENCES kentity_relation_types(code) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_relations_subject_typed_fk
    FOREIGN KEY (subject_id, subject_type) REFERENCES kentity_entities(id, entity_type) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_relations_object_typed_fk
    FOREIGN KEY (object_id, object_type) REFERENCES kentity_entities(id, entity_type) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_relations_pair_fk
    FOREIGN KEY (predicate, subject_type, object_type)
    REFERENCES kentity_relation_type_pairs(predicate, subject_type, object_type) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_relations_position_required CHECK (
    predicate <> 'holds_position' OR status <> 'verified' OR position_code IS NOT NULL);

CREATE TABLE kentity_person_profiles (
  entity_id   uuid PRIMARY KEY,
  entity_type text NOT NULL DEFAULT 'person' CHECK (entity_type = 'person'),
  birth_year  smallint, birth_month smallint, birth_day smallint,
  birth_evidence_id uuid,
  role_state  text NOT NULL DEFAULT 'pending'
    CHECK (role_state IN ('pending','verified','not_applicable','conflict')),
  role_reason text NOT NULL DEFAULT 'awaiting_role_evidence' CHECK (btrim(role_reason) <> ''),
  role_evidence_id uuid,
  birth_locked boolean NOT NULL DEFAULT false,
  role_locked  boolean NOT NULL DEFAULT false,
  revision   bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (entity_id, entity_type) REFERENCES kentity_entities(id, entity_type) ON DELETE RESTRICT,
  FOREIGN KEY (birth_evidence_id, entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  FOREIGN KEY (role_evidence_id,  entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  -- 월은 연도가, 일은 월이 있을 때만. 없는 월/일을 1로 채우지 않는다.
  CONSTRAINT kentity_person_profiles_birth_shape CHECK (
    (birth_year IS NULL OR birth_year BETWEEN 1 AND 9999)
    AND (birth_month IS NULL OR (birth_year IS NOT NULL AND birth_month BETWEEN 1 AND 12))
    AND (birth_day IS NULL OR (birth_month IS NOT NULL
         AND birth_day BETWEEN 1 AND 31
         AND make_date(birth_year::int, birth_month::int, birth_day::int) IS NOT NULL))),
  CONSTRAINT kentity_person_profiles_not_applicable_needs_evidence CHECK (
    role_state <> 'not_applicable' OR role_evidence_id IS NOT NULL)
);

CREATE TABLE kentity_location_profiles (
  entity_id   uuid PRIMARY KEY,
  entity_type text NOT NULL DEFAULT 'location' CHECK (entity_type = 'location'),
  latitude double precision, longitude double precision,
  coordinate_precision_m double precision,
  geo_evidence_id uuid,
  address_ko text, address_en text, address_evidence_id uuid,
  admin_namespace text, sido_code text, sigungu_code text, ldong_code text,
  admin_evidence_id uuid,
  geo_locked boolean NOT NULL DEFAULT false,
  address_locked boolean NOT NULL DEFAULT false,
  admin_locked boolean NOT NULL DEFAULT false,
  revision   bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  updated_at timestamptz NOT NULL DEFAULT now(),
  FOREIGN KEY (entity_id, entity_type) REFERENCES kentity_entities(id, entity_type) ON DELETE RESTRICT,
  FOREIGN KEY (geo_evidence_id,     entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  FOREIGN KEY (address_evidence_id, entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  FOREIGN KEY (admin_evidence_id,   entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  CONSTRAINT kentity_location_profiles_coords CHECK (
    (latitude IS NULL) = (longitude IS NULL)
    AND (latitude IS NULL OR (latitude BETWEEN -90 AND 90 AND longitude BETWEEN -180 AND 180))),
  CONSTRAINT kentity_location_profiles_precision CHECK (
    coordinate_precision_m IS NULL OR coordinate_precision_m >= 0),
  -- 행정 코드가 있으면 namespace 필수. 한국 법정동과 타국 코드를 섞지 않는다.
  CONSTRAINT kentity_location_profiles_admin_ns CHECK (
    (sido_code IS NULL AND sigungu_code IS NULL AND ldong_code IS NULL)
    OR admin_namespace IS NOT NULL)
);
CREATE INDEX kentity_location_profiles_region
  ON kentity_location_profiles (admin_namespace, sido_code, sigungu_code, entity_id);

-- ============================================================ 8. 근거·이름 (§12 + S02/S08)

ALTER TABLE kentity_evidence
  ADD COLUMN revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  ADD COLUMN source_policy_id uuid REFERENCES kentity_source_policies(id) ON DELETE RESTRICT,
  ADD COLUMN claim_fingerprint text NOT NULL DEFAULT '',
  ADD COLUMN source_observation_hash text NOT NULL DEFAULT '',
  ADD COLUMN claim_payload jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(claim_payload) = 'object'),
  ADD COLUMN independent_origin text NOT NULL DEFAULT '';
ALTER TABLE kentity_evidence
  DROP CONSTRAINT kentity_evidence_claim_type_check,
  ADD CONSTRAINT kentity_evidence_claim_type_check CHECK (
    claim_type IN ('identity','name','relation','occupation','classification','profile','no_form')),
  ADD CONSTRAINT kentity_evidence_payload_size CHECK (pg_column_size(claim_payload) <= 16384),
  -- D-01: 설계는 "전환"이라 썼지만 현행에 해당 UNIQUE 가 없었다(중복 0 실측). 신규 추가한다.
  ADD CONSTRAINT kentity_evidence_claim_key UNIQUE
    (entity_id, provider, source_record_id, source_url, claim_type, claim_fingerprint, source_observation_hash);
CREATE INDEX kentity_evidence_policy ON kentity_evidence (source_policy_id, entity_id)
  WHERE source_policy_id IS NOT NULL;
CREATE INDEX kentity_evidence_claim  ON kentity_evidence (entity_id, claim_type, status);

-- §12.2: 필수 부모를 여럿 허용
ALTER TABLE kentity_evidence_dependencies
  DROP CONSTRAINT kentity_evidence_dependencies_pkey,
  ADD PRIMARY KEY (evidence_id, depends_on_id);

ALTER TABLE kentity_names
  ADD COLUMN normalized_value text NOT NULL DEFAULT '',
  ADD COLUMN normalization_version text NOT NULL DEFAULT 'nfc-v1',
  ADD COLUMN operator_locked boolean NOT NULL DEFAULT false,
  ADD COLUMN verification_method text,
  ADD COLUMN policy_version text NOT NULL DEFAULT 'legacy-import-v0',
  ADD COLUMN valid_from_precision  text NOT NULL DEFAULT 'unknown'
    CHECK (valid_from_precision IN ('unknown','open','year','month','day')),
  ADD COLUMN valid_until_precision text NOT NULL DEFAULT 'unknown'
    CHECK (valid_until_precision IN ('unknown','open','year','month','day')),
  ADD COLUMN valid_from_year smallint, ADD COLUMN valid_until_year smallint,
  ADD COLUMN valid_from_month smallint, ADD COLUMN valid_until_month smallint;
-- 기존 exact date 는 day 로, NULL 은 unknown 으로만 보수적 backfill 한다.
UPDATE kentity_names SET valid_from_precision  = 'day' WHERE valid_from  IS NOT NULL;
UPDATE kentity_names SET valid_until_precision = 'day' WHERE valid_until IS NOT NULL;
UPDATE kentity_names SET normalized_value = btrim(value) WHERE normalized_value = '';
ALTER TABLE kentity_names
  ADD CONSTRAINT kentity_names_id_entity_id_key UNIQUE (id, entity_id),
  ADD CONSTRAINT kentity_names_locale_fk FOREIGN KEY (locale) REFERENCES kentity_locales(code) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_names_normalized_nonempty CHECK (btrim(normalized_value) <> '');
CREATE INDEX kentity_names_normalized ON kentity_names (locale, normalized_value, entity_id);
CREATE INDEX kentity_names_entity_locale ON kentity_names (entity_id, locale, status);

-- §12.3 + S08
CREATE TABLE kentity_name_evidence (
  name_id     uuid NOT NULL,
  entity_id   uuid NOT NULL,
  evidence_id uuid NOT NULL,
  stance      text NOT NULL DEFAULT 'supports' CHECK (stance IN ('supports','contradicts')),
  resolution  text NOT NULL DEFAULT 'unresolved'
    CHECK (resolution IN ('unresolved','upheld','dismissed','withdrawn')),
  revision    bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  decided_by  text, decided_at timestamptz, decision_reason text,
  created_at  timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (name_id, evidence_id),
  FOREIGN KEY (name_id, entity_id)     REFERENCES kentity_names(id, entity_id) ON DELETE RESTRICT,
  FOREIGN KEY (evidence_id, entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  CONSTRAINT kentity_name_evidence_resolution_shape CHECK (
    (resolution = 'unresolved') = (decided_by IS NULL AND decided_at IS NULL AND decision_reason IS NULL))
);
CREATE INDEX kentity_name_evidence_by_evidence ON kentity_name_evidence (evidence_id, name_id);

-- ============================================================ 9. 준비·보충 (§13 + S01/S03/S06)

ALTER TABLE kentity_preparations
  ADD COLUMN as_of timestamptz,
  ADD COLUMN source_hash text NOT NULL DEFAULT '',
  ADD COLUMN request_form_policy text NOT NULL DEFAULT 'strict-recorded',
  ADD COLUMN article_scope text NOT NULL DEFAULT '';

ALTER TABLE kentity_preparation_items
  ADD COLUMN bound_identity_revision bigint,
  ADD COLUMN bound_entity_revision   bigint,
  ADD COLUMN span_start integer, ADD COLUMN span_end integer,
  -- D-03: 고아 UUID 0 실측 → FK 를 그대로 건다.
  ADD CONSTRAINT kentity_preparation_items_supplied_fk
    FOREIGN KEY (supplied_entity_id) REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_preparation_items_resolved_fk
    FOREIGN KEY (resolved_entity_id) REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_preparation_items_bound_fk
    FOREIGN KEY (bound_entity_id) REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_preparation_items_span CHECK (
    (span_start IS NULL) = (span_end IS NULL)
    AND (span_start IS NULL OR (span_start >= 0 AND span_end > span_start))),
  ADD CONSTRAINT kentity_preparation_items_bound_revisions CHECK (
    bound_entity_id IS NULL
    OR (bound_identity_revision > 0 AND bound_entity_revision > 0)),
  -- S01: readiness 가 참조할 복합 키
  ADD CONSTRAINT kentity_preparation_items_bound_key UNIQUE
    (preparation_id, ordinal, bound_entity_id, bound_identity_revision, bound_entity_revision);

ALTER TABLE kentity_locale_readiness
  ADD COLUMN name_id uuid,
  ADD COLUMN entity_id uuid,
  ADD COLUMN identity_revision bigint,
  ADD COLUMN entity_revision bigint,
  ADD COLUMN name_revision bigint,
  ADD COLUMN dependency_epoch bigint,
  ADD COLUMN fallback_locale text REFERENCES kentity_locales(code) ON DELETE RESTRICT,
  ADD COLUMN usable_value text NOT NULL DEFAULT '',
  ADD COLUMN usable_form text,
  ADD COLUMN proof_policy_version text NOT NULL DEFAULT '',
  ADD COLUMN scope_key text NOT NULL DEFAULT 'global',
  ADD COLUMN no_form_evidence_id uuid,
  ADD COLUMN policy_proof jsonb NOT NULL DEFAULT '[]'::jsonb
    CHECK (jsonb_typeof(policy_proof) = 'array');
-- state 목록을 먼저 넓힌 뒤에 값을 바꾼다(순서를 뒤집으면 옛 CHECK 가 stale 을 거부한다).
ALTER TABLE kentity_locale_readiness
  DROP CONSTRAINT kentity_locale_readiness_state_check,
  ADD CONSTRAINT kentity_locale_readiness_state_check CHECK (state IN (
    'pending','ready','ambiguous','no_evidence','policy_blocked','failed',
    'unverified','cancelled','no_form','stale'));

-- ★ D-18: 기존 ready 행에는 S01 의 bound UUID·두 revision 도, S03 의 policy_proof 도 없다.
-- 없는 증명을 만들어 넣는 것은 금지돼 있다(§14.3 "임의 JSON self-assertion 을 승인으로 쓰지 않는다").
-- 새 계약에서 이 행들은 재계산 대상이므로 stale 로 내린다. 최초 ready 시각은 first_ready_at 에 남는다.
-- (기존 CHECK 가 (state='ready') = (ready_at IS NOT NULL) 을 강제하므로 ready_at 도 함께 비운다.)
UPDATE kentity_locale_readiness
   SET state = 'stale', ready_at = NULL,
       reason = CASE WHEN btrim(reason) = '' THEN 'legacy_ready_without_proof_p1'
                     ELSE reason || '; legacy_ready_without_proof_p1' END
 WHERE state IN ('ready', 'no_form');

ALTER TABLE kentity_locale_readiness
  ADD CONSTRAINT kentity_locale_readiness_locale_fk
    FOREIGN KEY (locale) REFERENCES kentity_locales(code) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_locale_readiness_name_fk
    FOREIGN KEY (name_id, entity_id) REFERENCES kentity_names(id, entity_id) ON DELETE RESTRICT,
  ADD CONSTRAINT kentity_locale_readiness_no_form_fk
    FOREIGN KEY (no_form_evidence_id, entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  -- S01: item 의 bound 대상과 반드시 같은 UUID·버전이어야 한다.
  ADD CONSTRAINT kentity_locale_readiness_bound_fk
    FOREIGN KEY (preparation_id, ordinal, entity_id, identity_revision, entity_revision)
    REFERENCES kentity_preparation_items
      (preparation_id, ordinal, bound_entity_id, bound_identity_revision, bound_entity_revision)
    DEFERRABLE INITIALLY IMMEDIATE,
  ADD CONSTRAINT kentity_locale_readiness_ready_shape CHECK (
    state NOT IN ('ready','no_form')
    OR (entity_id IS NOT NULL AND identity_revision > 0 AND entity_revision > 0)),
  -- S02: no_form 은 전용 근거를 요구하고 name_id 를 갖지 않는다.
  ADD CONSTRAINT kentity_locale_readiness_no_form_shape CHECK (
    (state = 'no_form') = (no_form_evidence_id IS NOT NULL)),
  ADD CONSTRAINT kentity_locale_readiness_no_form_no_name CHECK (
    state <> 'no_form' OR name_id IS NULL),
  -- S03: ready/no_form 은 비어 있지 않은 정책 증명을 요구한다.
  ADD CONSTRAINT kentity_locale_readiness_policy_proof CHECK (
    state NOT IN ('ready','no_form') OR jsonb_array_length(policy_proof) > 0),
  -- S06: waiter 가 참조할 복합 키
  ADD CONSTRAINT kentity_locale_readiness_scope_key UNIQUE
    (preparation_id, ordinal, locale, entity_id, identity_revision, entity_revision, scope_key);

ALTER TABLE kentity_locale_fill_jobs
  ALTER COLUMN qid DROP NOT NULL,                      -- provider 중립화
  ADD COLUMN provider text NOT NULL DEFAULT '',
  ADD COLUMN source_reference text NOT NULL DEFAULT '',
  ADD COLUMN entity_revision bigint,
  ADD COLUMN identity_revision bigint,
  ADD COLUMN scope_key text NOT NULL DEFAULT 'global',
  ADD COLUMN purpose text NOT NULL DEFAULT 'locale_fill',
  ADD COLUMN model_version text, ADD COLUMN prompt_version text,
  ADD COLUMN input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  ADD COLUMN output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0),
  ADD COLUMN changed_names integer NOT NULL DEFAULT 0 CHECK (changed_names >= 0),
  ADD COLUMN newly_ready integer NOT NULL DEFAULT 0 CHECK (newly_ready >= 0),
  ADD COLUMN started_at timestamptz, ADD COLUMN finished_at timestamptz;
ALTER TABLE kentity_locale_fill_jobs
  DROP CONSTRAINT kentity_locale_fill_jobs_state_check,
  ADD CONSTRAINT kentity_locale_fill_jobs_state_check CHECK (state IN (
    'pending','running','complete','no_evidence','policy_blocked','failed','stale','cancelled')),
  ADD CONSTRAINT kentity_locale_fill_jobs_locale_fk
    FOREIGN KEY (locale) REFERENCES kentity_locales(code) ON DELETE RESTRICT,
  -- S06: waiter 가 참조할 복합 키
  ADD CONSTRAINT kentity_locale_fill_jobs_scope_key UNIQUE
    (id, entity_id, locale, identity_revision, entity_revision, scope_key);

ALTER TABLE kentity_resolution_jobs
  ADD COLUMN identity_revision bigint,
  ADD COLUMN scope_key text NOT NULL DEFAULT 'global',
  ADD COLUMN provider_policy_version text NOT NULL DEFAULT '',
  ADD COLUMN model_version text, ADD COLUMN prompt_version text,
  ADD COLUMN input_tokens bigint NOT NULL DEFAULT 0 CHECK (input_tokens >= 0),
  ADD COLUMN output_tokens bigint NOT NULL DEFAULT 0 CHECK (output_tokens >= 0);

-- S06: 한 요청의 취소가 다른 요청의 공통 job 을 취소하지 못하게 하는 대기 원장
CREATE TABLE kentity_fill_waiters (
  preparation_id uuid NOT NULL,
  ordinal        integer NOT NULL,
  locale         text NOT NULL,
  job_id         uuid NOT NULL,
  entity_id      uuid NOT NULL,
  identity_revision bigint NOT NULL CHECK (identity_revision > 0),
  entity_revision   bigint NOT NULL CHECK (entity_revision > 0),
  scope_key      text NOT NULL,
  created_at     timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (preparation_id, ordinal, locale, job_id),
  FOREIGN KEY (preparation_id, ordinal, locale, entity_id, identity_revision, entity_revision, scope_key)
    REFERENCES kentity_locale_readiness
      (preparation_id, ordinal, locale, entity_id, identity_revision, entity_revision, scope_key)
    ON DELETE RESTRICT,
  FOREIGN KEY (job_id, entity_id, locale, identity_revision, entity_revision, scope_key)
    REFERENCES kentity_locale_fill_jobs
      (id, entity_id, locale, identity_revision, entity_revision, scope_key)
    ON DELETE RESTRICT
);
CREATE INDEX kentity_fill_waiters_job ON kentity_fill_waiters (job_id);

-- §13 + S03: Entity 이벤트와 정책 이벤트를 한 원장에서 다루되 정확히 하나만 채운다.
CREATE TABLE kentity_invalidation_outbox (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  entity_id uuid REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  dependency_epoch bigint,
  source_policy_id uuid REFERENCES kentity_source_policies(id) ON DELETE RESTRICT,
  policy_revision bigint,
  reason text NOT NULL CHECK (btrim(reason) <> ''),
  created_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  next_attempt_at timestamptz NOT NULL DEFAULT now(),
  fanout_after_entity_id uuid,
  fanout_started_at timestamptz,
  fanout_completed_at timestamptz,
  CONSTRAINT kentity_invalidation_outbox_exactly_one CHECK (
    ((entity_id IS NOT NULL AND dependency_epoch > 0)::int
     + (source_policy_id IS NOT NULL AND policy_revision > 0)::int) = 1),
  CONSTRAINT kentity_invalidation_outbox_fanout_only_policy CHECK (
    source_policy_id IS NOT NULL
    OR (fanout_after_entity_id IS NULL AND fanout_started_at IS NULL AND fanout_completed_at IS NULL))
);
CREATE UNIQUE INDEX kentity_invalidation_outbox_entity_event
  ON kentity_invalidation_outbox (entity_id, dependency_epoch) WHERE entity_id IS NOT NULL;
CREATE UNIQUE INDEX kentity_invalidation_outbox_policy_event
  ON kentity_invalidation_outbox (source_policy_id, policy_revision) WHERE source_policy_id IS NOT NULL;
CREATE INDEX kentity_invalidation_outbox_due
  ON kentity_invalidation_outbox (next_attempt_at, id) WHERE processed_at IS NULL;

-- D-12
CREATE INDEX kentity_candidate_requests_entity ON kentity_candidate_requests (entity_id);

-- ============================================================ 10. 이관 제어 (§15, MIGRATION_CONTROL §3~§5)

CREATE TABLE kentity_migration_runs (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  owner_key   text NOT NULL CHECK (btrim(owner_key) <> '' AND octet_length(owner_key) <= 200),
  request_key text NOT NULL CHECK (btrim(request_key) <> '' AND octet_length(request_key) <= 200),
  request_hash text NOT NULL CHECK (request_hash ~ '^[a-f0-9]{64}$'),
  mode  text NOT NULL CHECK (mode IN ('dry_run','apply')),
  state text NOT NULL DEFAULT 'planned'
    CHECK (state IN ('planned','running','paused','completed','failed','cancelled')),
  mapper_version text NOT NULL CHECK (btrim(mapper_version) <> ''),
  canonicalization_version text NOT NULL CHECK (btrim(canonicalization_version) <> ''),
  source_basis jsonb NOT NULL CHECK (jsonb_typeof(source_basis) = 'object'
                                     AND pg_column_size(source_basis) <= 32768),
  cohort_hash text NOT NULL CHECK (cohort_hash ~ '^[a-f0-9]{64}$'),
  selection_policy_hash text NOT NULL CHECK (selection_policy_hash ~ '^[a-f0-9]{64}$'),
  expected_records  bigint NOT NULL CHECK (expected_records  >= 0),
  expected_entities bigint NOT NULL CHECK (expected_entities >= 0),
  max_changed_objects bigint NOT NULL CHECK (max_changed_objects >= 0),
  approved_by text, approved_at timestamptz, approval_ref text,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  started_at timestamptz, finished_at timestamptz,
  last_error_code text,
  UNIQUE (owner_key, request_key),
  CONSTRAINT kentity_migration_runs_apply_needs_approval CHECK (
    NOT (mode = 'apply' AND state = 'running')
    OR (approved_by IS NOT NULL AND approved_at IS NOT NULL AND approval_ref IS NOT NULL)),
  CONSTRAINT kentity_migration_runs_terminal_finished CHECK (
    state NOT IN ('completed','failed','cancelled') OR finished_at IS NOT NULL)
);
CREATE INDEX kentity_migration_runs_queue ON kentity_migration_runs (state, created_at, id);

CREATE TABLE kentity_migration_records (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  run_id uuid NOT NULL REFERENCES kentity_migration_runs(id) ON DELETE RESTRICT,
  source_system text NOT NULL CHECK (octet_length(source_system) <= 64),
  source_table  text NOT NULL CHECK (octet_length(source_table)  <= 64),
  source_pk jsonb NOT NULL CHECK (jsonb_typeof(source_pk) = 'object'
                                  AND pg_column_size(source_pk) <= 512),
  source_fingerprint text NOT NULL CHECK (source_fingerprint ~ '^[a-f0-9]{64}$'),
  source_state text NOT NULL DEFAULT 'unknown'
    CHECK (source_state IN ('present','deleted','merged','unknown')),
  source_observed_at timestamptz,
  disposition text NOT NULL
    CHECK (disposition IN ('include','conditional','archive_only','exclude_operational')),
  state text NOT NULL DEFAULT 'planned'
    CHECK (state IN ('planned','held','excluded','validated','applied','failed','reverted')),
  target_mode text NOT NULL DEFAULT 'none' CHECK (target_mode IN ('none','existing','create')),
  planned_target_id uuid,
  target_entity_id  uuid REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  expected_entity_revision   bigint,
  expected_identity_revision bigint,
  expected_owner text,
  policy_proof jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(policy_proof) = 'array'),
  plan_hash text NOT NULL CHECK (plan_hash ~ '^[a-f0-9]{64}$'),
  expected_object_count integer NOT NULL DEFAULT 0 CHECK (expected_object_count >= 0),
  actual_object_count   integer NOT NULL DEFAULT 0 CHECK (actual_object_count   >= 0),
  reason_code text NOT NULL CHECK (btrim(reason_code) <> ''),
  recovery_ref text,
  applied_result_hash text,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  applied_at timestamptz,
  UNIQUE (run_id, source_system, source_table, source_pk),
  -- 제어 설계 §4 의 target_mode truth table
  CONSTRAINT kentity_migration_records_target_shape CHECK (
    CASE target_mode
      WHEN 'none' THEN planned_target_id IS NULL AND target_entity_id IS NULL
                       AND expected_entity_revision IS NULL AND expected_identity_revision IS NULL
                       AND expected_owner IS NULL
      WHEN 'existing' THEN planned_target_id IS NOT NULL AND target_entity_id = planned_target_id
                       AND expected_entity_revision > 0 AND expected_identity_revision > 0
                       AND expected_owner IS NOT NULL
      WHEN 'create' THEN planned_target_id IS NOT NULL
                       AND expected_entity_revision = 0 AND expected_identity_revision = 0
                       AND expected_owner IS NOT NULL
                       AND (state IN ('applied','reverted')) = (target_entity_id IS NOT NULL)
                       AND (target_entity_id IS NULL OR target_entity_id = planned_target_id)
    END),
  CONSTRAINT kentity_migration_records_applied_shape CHECK (
    state <> 'applied'
    OR (applied_at IS NOT NULL AND applied_result_hash IS NOT NULL
        AND actual_object_count = expected_object_count))
);
CREATE INDEX kentity_migration_records_run    ON kentity_migration_records (run_id, state, id);
CREATE INDEX kentity_migration_records_target ON kentity_migration_records (target_entity_id);
CREATE INDEX kentity_migration_records_source ON kentity_migration_records (source_system, source_table, source_pk);
ALTER TABLE kentity_audit_events
  ADD CONSTRAINT kentity_audit_events_migration_record_fk
    FOREIGN KEY (migration_record_id) REFERENCES kentity_migration_records(id) ON DELETE RESTRICT;
CREATE INDEX kentity_audit_events_migration ON kentity_audit_events (migration_record_id, id)
  WHERE migration_record_id IS NOT NULL;

CREATE TABLE kentity_source_guards (
  id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  origin_system text NOT NULL, origin_table text NOT NULL,
  origin_pk jsonb NOT NULL CHECK (jsonb_typeof(origin_pk) = 'object' AND pg_column_size(origin_pk) <= 512),
  scope_key  text NOT NULL CHECK (octet_length(scope_key) <= 768),
  scope_kind text NOT NULL CHECK (scope_kind IN ('source_binding','entity','name_slot')),
  subject_system text, subject_table text, subject_pk jsonb,
  entity_id uuid REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  identity_revision bigint,
  rejected_entity_id uuid REFERENCES kentity_entities(id) ON DELETE RESTRICT,
  locale text REFERENCES kentity_locales(code) ON DELETE RESTRICT,
  claim_fingerprint text,
  guard_kind text NOT NULL
    CHECK (guard_kind IN ('rejected_binding','withdrawn_name','operator_correction','hold','empty_slot')),
  state text NOT NULL DEFAULT 'proposed'
    CHECK (state IN ('proposed','active','released','superseded')),
  basis_record_id uuid REFERENCES kentity_migration_records(id) ON DELETE RESTRICT,
  evidence_id uuid,
  reason_code text NOT NULL CHECK (btrim(reason_code) <> ''),
  source_fingerprint text NOT NULL CHECK (source_fingerprint ~ '^[a-f0-9]{64}$'),
  decided_by text, decided_at timestamptz, decision_ref text,
  revision bigint NOT NULL DEFAULT 1 CHECK (revision > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (origin_system, origin_table, origin_pk, scope_key),
  FOREIGN KEY (evidence_id, entity_id) REFERENCES kentity_evidence(id, entity_id) ON DELETE RESTRICT,
  CONSTRAINT kentity_source_guards_scope_shape CHECK (
    CASE scope_kind
      WHEN 'source_binding' THEN subject_system IS NOT NULL AND subject_table IS NOT NULL
                                 AND subject_pk IS NOT NULL AND entity_id IS NULL AND locale IS NULL
      WHEN 'entity'    THEN entity_id IS NOT NULL AND identity_revision > 0 AND locale IS NULL
      WHEN 'name_slot' THEN entity_id IS NOT NULL AND identity_revision > 0 AND locale IS NOT NULL
    END),
  -- guard_kind 별 허용 scope (제어 설계 §5)
  CONSTRAINT kentity_source_guards_kind_scope CHECK (
    CASE guard_kind
      WHEN 'rejected_binding'    THEN scope_kind = 'source_binding' AND rejected_entity_id IS NOT NULL
      WHEN 'withdrawn_name'      THEN scope_kind = 'name_slot' AND claim_fingerprint IS NOT NULL
      WHEN 'operator_correction' THEN scope_kind = 'name_slot' AND claim_fingerprint IS NOT NULL
      WHEN 'empty_slot'          THEN scope_kind = 'name_slot' AND claim_fingerprint IS NULL
      WHEN 'hold'                THEN true
    END),
  CONSTRAINT kentity_source_guards_decided CHECK (
    state = 'proposed' OR (decided_by IS NOT NULL AND decided_at IS NOT NULL AND decision_ref IS NOT NULL))
);
CREATE INDEX kentity_source_guards_entity  ON kentity_source_guards (entity_id, locale, state)
  WHERE entity_id IS NOT NULL;
CREATE INDEX kentity_source_guards_subject ON kentity_source_guards (subject_system, subject_table, state)
  WHERE subject_pk IS NOT NULL;
CREATE INDEX kentity_source_guards_queue   ON kentity_source_guards (state, updated_at, id);
CREATE INDEX kentity_source_guards_basis   ON kentity_source_guards (basis_record_id)
  WHERE basis_record_id IS NOT NULL;

-- ============================================================ 11. P1.03~P1.06 시험이 드러낸 보강

-- ★ D-19: 부분 날짜 CHECK 가 NULL 전파로 뚫렸다.
-- `year BETWEEN 1 AND 9999` 는 year 가 NULL 이면 NULL 을 돌려주고, CHECK 는 NULL 을 통과로 본다.
-- 그 결과 precision='month' + year NULL 이 저장돼 possible_validity 가 (,) 무한대가 됐다.
-- person_roles 는 위에서 COALESCE(...,false) + IS NOT NULL 로 고쳤고, 같은 6컬럼을 받은
-- relations/names 에는 애초에 CHECK 가 없었으므로 여기서 같은 규칙을 건다.
ALTER TABLE kentity_relations
  ADD CONSTRAINT kentity_relations_from_precision CHECK (COALESCE(
    CASE valid_from_precision
      WHEN 'unknown' THEN valid_from IS NULL AND valid_from_year IS NULL AND valid_from_month IS NULL
      WHEN 'open'    THEN valid_from IS NULL AND valid_from_year IS NULL AND valid_from_month IS NULL
      WHEN 'year'    THEN valid_from IS NULL AND valid_from_year IS NOT NULL AND valid_from_year BETWEEN 1 AND 9999 AND valid_from_month IS NULL
      WHEN 'month'   THEN valid_from IS NULL AND valid_from_year IS NOT NULL AND valid_from_year BETWEEN 1 AND 9999 AND valid_from_month IS NOT NULL AND valid_from_month BETWEEN 1 AND 12
      WHEN 'day'     THEN valid_from IS NOT NULL
                          AND (valid_from_year  IS NULL OR valid_from_year  = EXTRACT(YEAR  FROM valid_from))
                          AND (valid_from_month IS NULL OR valid_from_month = EXTRACT(MONTH FROM valid_from))
    END, false)),
  ADD CONSTRAINT kentity_relations_until_precision CHECK (COALESCE(
    CASE valid_until_precision
      WHEN 'unknown' THEN valid_until IS NULL AND valid_until_year IS NULL AND valid_until_month IS NULL
      WHEN 'open'    THEN valid_until IS NULL AND valid_until_year IS NULL AND valid_until_month IS NULL
      WHEN 'year'    THEN valid_until IS NULL AND valid_until_year IS NOT NULL AND valid_until_year BETWEEN 1 AND 9999 AND valid_until_month IS NULL
      WHEN 'month'   THEN valid_until IS NULL AND valid_until_year IS NOT NULL AND valid_until_year BETWEEN 1 AND 9999 AND valid_until_month IS NOT NULL AND valid_until_month BETWEEN 1 AND 12
      WHEN 'day'     THEN valid_until IS NOT NULL
                          AND (valid_until_year  IS NULL OR valid_until_year  = EXTRACT(YEAR  FROM valid_until))
                          AND (valid_until_month IS NULL OR valid_until_month = EXTRACT(MONTH FROM valid_until))
    END, false));
ALTER TABLE kentity_names
  ADD CONSTRAINT kentity_names_from_precision CHECK (COALESCE(
    CASE valid_from_precision
      WHEN 'unknown' THEN valid_from IS NULL AND valid_from_year IS NULL AND valid_from_month IS NULL
      WHEN 'open'    THEN valid_from IS NULL AND valid_from_year IS NULL AND valid_from_month IS NULL
      WHEN 'year'    THEN valid_from IS NULL AND valid_from_year IS NOT NULL AND valid_from_year BETWEEN 1 AND 9999 AND valid_from_month IS NULL
      WHEN 'month'   THEN valid_from IS NULL AND valid_from_year IS NOT NULL AND valid_from_year BETWEEN 1 AND 9999 AND valid_from_month IS NOT NULL AND valid_from_month BETWEEN 1 AND 12
      WHEN 'day'     THEN valid_from IS NOT NULL
                          AND (valid_from_year  IS NULL OR valid_from_year  = EXTRACT(YEAR  FROM valid_from))
                          AND (valid_from_month IS NULL OR valid_from_month = EXTRACT(MONTH FROM valid_from))
    END, false)),
  ADD CONSTRAINT kentity_names_until_precision CHECK (COALESCE(
    CASE valid_until_precision
      WHEN 'unknown' THEN valid_until IS NULL AND valid_until_year IS NULL AND valid_until_month IS NULL
      WHEN 'open'    THEN valid_until IS NULL AND valid_until_year IS NULL AND valid_until_month IS NULL
      WHEN 'year'    THEN valid_until IS NULL AND valid_until_year IS NOT NULL AND valid_until_year BETWEEN 1 AND 9999 AND valid_until_month IS NULL
      WHEN 'month'   THEN valid_until IS NULL AND valid_until_year IS NOT NULL AND valid_until_year BETWEEN 1 AND 9999 AND valid_until_month IS NOT NULL AND valid_until_month BETWEEN 1 AND 12
      WHEN 'day'     THEN valid_until IS NOT NULL
                          AND (valid_until_year  IS NULL OR valid_until_year  = EXTRACT(YEAR  FROM valid_until))
                          AND (valid_until_month IS NULL OR valid_until_month = EXTRACT(MONTH FROM valid_until))
    END, false));

-- ★ D-20: redirect 가 planned/rejected operation 에도 붙었다.
-- S05 는 "같은 endpoints 의 status=applied / operation=merge 만 참조하도록 지연 제약 트리거로 검사한다"
-- 고 정했는데 FK 만으로는 표현할 수 없다. 같은 transaction 안의 merge→redirect 순서를 허용하려고
-- DEFERRABLE INITIALLY DEFERRED 로 둔다.
CREATE OR REPLACE FUNCTION kentity_guard_redirect_operation() RETURNS trigger LANGUAGE plpgsql AS $rg$
DECLARE op record;
BEGIN
  SELECT operation, status, source_id, target_id INTO op
    FROM kentity_identity_operations WHERE id = NEW.operation_id;
  IF NOT FOUND THEN RAISE EXCEPTION 'redirect references an unknown operation'; END IF;
  IF op.operation <> 'merge' OR op.status <> 'applied' THEN
    RAISE EXCEPTION 'redirect requires an applied merge operation (got %/%)', op.operation, op.status;
  END IF;
  IF op.source_id <> NEW.from_id OR op.target_id <> NEW.to_id THEN
    RAISE EXCEPTION 'redirect endpoints must match the merge operation';
  END IF;
  RETURN NEW;
END $rg$;
CREATE CONSTRAINT TRIGGER kentity_redirect_operation_guard
  AFTER INSERT OR UPDATE ON kentity_redirects
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
  EXECUTE FUNCTION kentity_guard_redirect_operation();

-- ★ D-21: Entity UUID 불변(I01/I02)을 DB 가 강제하지 않았다.
-- 참조가 없는 행은 PK 를 그냥 바꿀 수 있었다. 이름 기반 재생성 금지의 최후 방어선이므로 트리거로 막는다.
CREATE OR REPLACE FUNCTION kentity_guard_entity_id_immutable() RETURNS trigger LANGUAGE plpgsql AS $ii$
BEGIN
  IF NEW.id <> OLD.id THEN
    RAISE EXCEPTION 'Entity UUID is immutable (I01/I02): % -> %', OLD.id, NEW.id;
  END IF;
  RETURN NEW;
END $ii$;
CREATE TRIGGER kentity_entity_id_immutable BEFORE UPDATE ON kentity_entities
  FOR EACH ROW EXECUTE FUNCTION kentity_guard_entity_id_immutable();

-- ★ D-22: kentity_names.normalized_value 를 모든 writer 가 채우게 하면 11곳이 제각기 정규화한다.
-- 정규화는 value 의 순수 함수이므로 트리거로 한 곳에서 찍는다(표기 계약 §2 "표시값은 원문을 보존하며
-- 정규화 버전 변경은 후보 검색 색인만 재생성한다"와 일치). DEFAULT '' 는 nonempty CHECK 와 충돌하므로 없앤다.
ALTER TABLE kentity_names ALTER COLUMN normalized_value DROP DEFAULT;
CREATE OR REPLACE FUNCTION kentity_stamp_name_normalization() RETURNS trigger LANGUAGE plpgsql AS $nn$
BEGIN
  IF TG_OP = 'INSERT' OR NEW.value IS DISTINCT FROM OLD.value
     OR btrim(COALESCE(NEW.normalized_value,'')) = '' THEN
    NEW.normalized_value := btrim(regexp_replace(NEW.value, '\s+', ' ', 'g'));
  END IF;
  RETURN NEW;
END $nn$;
CREATE TRIGGER kentity_name_normalization BEFORE INSERT OR UPDATE ON kentity_names
  FOR EACH ROW EXECUTE FUNCTION kentity_stamp_name_normalization();

-- ★ D-23: kentity_adopt_rejected_legacy() 가 subtype='' 를 쓴다(사전 FK 위반) 그리고
-- entity_domains 에 policy_version 을 주지 않는다. P1.01 에서 이 트리거를 "재사용"으로 분류했는데
-- 실제로는 보강 대상이었다. 나머지 동작(잠금·지문·기대 revision 검사)은 0118 원본 그대로 둔다.
CREATE OR REPLACE FUNCTION kentity_adopt_rejected_legacy() RETURNS trigger LANGUAGE plpgsql AS $ar$
DECLARE old_source kwave_entities%ROWTYPE; old_core kentity_entities%ROWTYPE; d text; old_domains jsonb;
BEGIN
 SELECT * INTO STRICT old_source FROM kwave_entities WHERE id=NEW.entity_id FOR UPDATE;
 SELECT * INTO STRICT old_core FROM kentity_entities WHERE id=NEW.entity_id FOR UPDATE;
 IF old_source.status<>'rejected' OR old_core.status<>'rejected' OR old_source.operator_locked OR old_core.operator_locked OR old_core.write_owner<>'kdb'
 OR old_core.revision<>NEW.expected_revision OR md5(to_jsonb(old_source)::text)<>NEW.source_fingerprint
 THEN RAISE EXCEPTION 'legacy transition input or lock changed'; END IF;
 IF cardinality(NEW.domains)<1 OR cardinality(NEW.domains)>8 THEN RAISE EXCEPTION 'transition requires reviewed domains'; END IF;
 SELECT COALESCE(jsonb_agg(domain ORDER BY domain),'[]'::jsonb) INTO old_domains FROM kentity_entity_domains WHERE entity_id=NEW.entity_id;
 -- subtype 은 목표 사전 코드만 들어갈 수 있다. 미확인은 NULL 이며 선매핑은 P2.02 다(D-16/D-23).
 UPDATE kentity_entities SET write_owner='native',status='candidate',entity_type=NEW.selected_type,subtype=NULL,revision=revision+1,updated_at=now() WHERE id=NEW.entity_id;
 DELETE FROM kentity_entity_domains WHERE entity_id=NEW.entity_id;
 FOREACH d IN ARRAY NEW.domains LOOP
  INSERT INTO kentity_entity_domains(entity_id,domain,assigned_by,reason,policy_version) VALUES(NEW.entity_id,d,NEW.actor,NEW.reason,'legacy-scope-review-v1')
  ON CONFLICT(entity_id,domain) DO UPDATE SET assigned_by=EXCLUDED.assigned_by,reason=EXCLUDED.reason;
 END LOOP;
 INSERT INTO kentity_names(entity_id,locale,value,kind,form,source_code) VALUES(NEW.entity_id,'ko',old_core.canonical_ko,'canonical','unknown','legacy-scope-review');
 INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value)
 VALUES(NEW.entity_id,NEW.actor,'legacy_scope_adopted',NEW.reason,
  jsonb_build_object('write_owner','kdb','status',old_core.status,'revision',old_core.revision,'entity_type',old_core.entity_type,'subtype',old_core.subtype,'domains',old_domains),
  jsonb_build_object('write_owner','native','status','candidate','revision',old_core.revision+1,'origin','kdb','legacy_status_unchanged',true,'source_url',NEW.source_url,'domains',to_jsonb(NEW.domains)));
 RETURN NEW;
END $ar$;

-- ★ D-24: 컬럼을 바꾸면 그 컬럼을 읽는 트리거도 같이 바꿔야 한다.
-- kentity_reserve_legacy_external_id() 가 kentity_id_reservations.native_owner 를 RETURNING 으로 읽는데
-- 그 컬럼은 entity_id 로 바뀌었다(§10.1). Go 코드에는 없어서 grep 으로는 안 잡혔고
-- kwave_entity_external_refs INSERT 를 하는 순간 런타임에서만 터졌다.
CREATE OR REPLACE FUNCTION kentity_reserve_legacy_external_id() RETURNS trigger LANGUAGE plpgsql AS $rl$
DECLARE owner_id uuid;
BEGIN
 IF NEW.provider<>'wikidata' OR NEW.external_id IS NULL OR NEW.external_id='' THEN RETURN NEW; END IF;
 -- ★ D-35: 예약에 **주인을 적어야** 한다. 종전에는 entity_id 를 채우지 않고 넣어서 새 예약의
 -- 주인이 항상 NULL 이었고, 그러면 아래 RAISE 는 영원히 도달하지 않는다. 실제로 서로 다른 두
 -- legacy 대상이 같은 wikidata QID 를 동시에 가져갔다(격리본에서 재현). 식별 계약 §4
 -- "검증된 한 외부 키의 현재 소유자는 하나다"가 이 경로에서만 비어 있었다.
 -- DO UPDATE 는 기존 행을 잠그므로 동시 요청도 여기서 직렬화된다. 먼저 온 주인은 유지하고
 -- (COALESCE), 다른 대상이 오면 아래에서 거부된다.
 INSERT INTO kentity_id_reservations(provider,external_id,entity_id)
 VALUES(NEW.provider,NEW.external_id,NEW.entity_id)
 ON CONFLICT(provider,external_id) DO UPDATE
   SET entity_id = COALESCE(kentity_id_reservations.entity_id, EXCLUDED.entity_id)
 RETURNING entity_id INTO owner_id;
 IF owner_id IS NOT NULL AND owner_id<>NEW.entity_id THEN RAISE EXCEPTION 'external ID reserved by another common Entity'; END IF;
 IF EXISTS(SELECT 1 FROM kentity_entities c JOIN kentity_external_ids x ON x.entity_id=c.id
  WHERE c.id=NEW.entity_id AND c.write_owner='native' AND x.provider=NEW.provider AND x.status='verified' AND x.external_id<>NEW.external_id)
 THEN RAISE EXCEPTION 'legacy writer cannot replace common verified identity anchor'; END IF;
 RETURN NEW;
END $rl$;

-- ★ D-25: crosswalk PK 가 (source_system,source_table,source_id) 로 넓어졌으므로
-- source_table 없이 source_id 만으로 갱신하면 다른 원천 표의 같은 ID 를 건드릴 수 있다.
-- 현재는 tdb_places 하나뿐이라 사고가 나지 않았을 뿐이다. 갱신 조건에 source_table 을 넣는다.
CREATE OR REPLACE FUNCTION kentity_invalidate_tdb_binding() RETURNS trigger LANGUAGE plpgsql AS $tb$
DECLARE link kentity_crosswalks%ROWTYPE;
BEGIN
 IF NEW.source_fingerprint=OLD.source_fingerprint AND NEW.source_locked=OLD.source_locked AND NEW.generation=OLD.generation THEN RETURN NEW; END IF;
 FOR link IN SELECT * FROM kentity_crosswalks WHERE source_system='tdb' AND source_binding_id=NEW.id AND status<>'rejected' FOR UPDATE LOOP
  IF link.evidence_id IS NOT NULL THEN UPDATE kentity_evidence SET status='withdrawn' WHERE id=link.evidence_id AND status='verified'; END IF;
  UPDATE kentity_crosswalks SET status='conflict',reason='TDB source binding changed; mapping requires fresh review',revision=revision+1,updated_at=now()
   WHERE source_system='tdb' AND source_table=link.source_table AND source_id=link.source_id;
  INSERT INTO kentity_audit_events(entity_id,actor,action,reason,before_value,after_value)
   VALUES(link.entity_id,'tdb-source-policy','tdb_mapping_invalidated','Source fingerprint or protection changed',
    jsonb_build_object('status',link.status,'revision',link.revision,'source_id',link.source_id),
    jsonb_build_object('status','conflict','revision',link.revision+1,'shadow_id',NEW.id));
  INSERT INTO kentity_tdb_shadow_events(shadow_id,actor,action,after_value)
   VALUES(NEW.id,'tdb-source-policy','mapping_invalidated',jsonb_build_object('reason','원본 변경으로 연결 재검토 필요','revision',link.revision+1));
 END LOOP;
 RETURN NEW;
END $tb$;

-- ============================================================ 12. 원천 정책 승인 — 운영자 결정

-- ★ D-26 해소의 첫 단계. P0.09 §5.5 는 모든 provider 를 unreviewed(차단)로 두고
-- "approved 는 검토자·시각·terms_url·조건을 갖춘 새 version INSERT 만" 이라고 정했다.
-- 운영자 결정(2026-09-13)으로 wikidata 를 첫 승인 원천으로 올린다.
--
-- 근거: Wikidata 의 구조화 데이터(레이블·별칭·설명·statement)는 CC0 1.0 으로 배포된다.
-- 범위 한정: 레이블/별칭 같은 구조화 표기만 해당한다. Wikidata 에서 링크되는
-- Wikipedia 본문은 CC BY-SA 이므로 excerpt_export_allowed 는 false 로 둔다.
-- 이 행은 격리본의 결정 기록이다. 운영 반영은 P3.02 승인 범위에서 별도 판단한다.
INSERT INTO kentity_source_policies
 (provider, version, license_code, status, reviewed_by, reviewed_at, terms_url,
  storage_allowed, verification_allowed, name_export_allowed, excerpt_export_allowed, conditions)
VALUES
 ('wikidata','2026-09-13','CC0-1.0','approved','operator', now(),
  'https://www.wikidata.org/wiki/Wikidata:Licensing',
  true, true, true, false,
  '구조화 표기(레이블/별칭)만 해당. 링크된 Wikipedia 본문(CC BY-SA)은 제외. 범위 확대는 새 version 검토.');

-- ============================================================ 13. 추가 정책 승인 — 운영자 내부 근거

-- 운영자 내부 근거라 외부 이용권 문제가 없는 세 갈래를 승인한다(2026-09-13 운영자 결정).
-- 외부 카탈로그 9종은 아래에서 함께 승인한다(운영자가 이용 가능함을 확인).
INSERT INTO kentity_source_policies
 (provider, version, license_code, status, reviewed_by, reviewed_at, terms_url,
  storage_allowed, verification_allowed, name_export_allowed, excerpt_export_allowed, conditions)
VALUES
 ('operator','2026-09-13','internal-operator-review','approved','operator', now(), NULL,
  true, true, true, false,
  '운영자가 직접 검수·확정한 표기. 외부 원천이 아니므로 재배포 제약이 없다. 발췌 공급은 별도.'),
 ('correction','2026-09-13','internal-operator-review','approved','operator', now(), NULL,
  true, true, true, false,
  '소비자 정정 신고를 운영자가 근거로 확인해 확정한 표기(correction-verified).'),
 ('media-consensus','2026-09-13','internal-operator-review','approved','operator', now(), NULL,
  true, true, true, false,
  '독립 근거 2건 이상의 매체 합의로 확정한 표기. 같은 회사 도메인 둘은 독립 2건이 아니다(X07).');

-- 외부 카탈로그 원천. 2026-09-13 운영자가 이용 가능함을 확인했고 일부는 API key 로 접근 중이다.
-- 여기서 여는 것은 **표기(레이블) 공급**뿐이다. 본문 발췌(excerpt_export_allowed)는 요청 범위가 아니므로
-- 전부 false 로 둔다 — 필요해지면 provider 별로 새 version 을 검토한다.
-- license_code 는 운영자 확인을 근거로 표기하며, provider 별 표기(attribution) 의무는 conditions 에 남긴다.
INSERT INTO kentity_source_policies
 (provider, version, license_code, status, reviewed_by, reviewed_at, terms_url,
  storage_allowed, verification_allowed, name_export_allowed, excerpt_export_allowed, conditions)
VALUES
 ('musicbrainz','2026-09-13','CC0-1.0','approved','operator', now(),'https://musicbrainz.org/doc/About/Data_License',
  true,true,true,false,'핵심 데이터는 CC0. 운영자 확인 2026-09-13.'),
 ('tmdb','2026-09-13','operator-confirmed-api-terms','approved','operator', now(),'https://www.themoviedb.org/api-terms-of-use',
  true,true,true,false,'API key 로 접근. 운영자 확인 2026-09-13. 각 provider 의 표기 의무를 소비자 안내에 반영한다.'),
 ('itunes','2026-09-13','operator-confirmed-api-terms','approved','operator', now(),'https://performance-partners.apple.com/terms-of-service',
  true,true,true,false,'iTunes Search API. 운영자 확인 2026-09-13.'),
 ('kofic','2026-09-13','operator-confirmed-api-terms','approved','operator', now(),'https://www.kobis.or.kr/kobisopenapi',
  true,true,true,false,'영화진흥위원회 공개 API, API key 보유. 운영자 확인 2026-09-13.'),
 ('kmdb','2026-09-13','operator-confirmed-api-terms','approved','operator', now(),'https://www.kmdb.or.kr/info/api/apiDetail',
  true,true,true,false,'한국영상자료원 KMDb API, API key 보유. 운영자 확인 2026-09-13.'),
 ('discogs','2026-09-13','operator-confirmed-api-terms','approved','operator', now(),'https://www.discogs.com/developers',
  true,true,true,false,'Discogs API. 운영자 확인 2026-09-13.'),
 ('netflix','2026-09-13','operator-confirmed-official-page','approved','operator', now(), NULL,
  true,true,true,false,'공식 카탈로그 페이지의 작품 표기. 운영자 확인 2026-09-13.'),
 ('disney','2026-09-13','operator-confirmed-official-page','approved','operator', now(), NULL,
  true,true,true,false,'공식 카탈로그 페이지의 작품 표기. 운영자 확인 2026-09-13.'),
 ('naver-people','2026-09-13','operator-confirmed-api-terms','approved','operator', now(), NULL,
  true,true,true,false,'네이버 인물 정보. 운영자 확인 2026-09-13.');

-- ============================================================ 14. 공통 name writer 보호선 (P1.07)

-- 표기 주장 지문. source_guards 의 claim_fingerprint 와 같은 규칙으로 계산한다.
-- 철회·수동정정 guard 는 "그 슬롯 전체"가 아니라 **그 주장 하나**를 막는다(제어 설계 §5).
CREATE OR REPLACE FUNCTION kentity_name_claim_fingerprint(p_locale text, p_value text, p_kind text, p_form text)
RETURNS text LANGUAGE sql IMMUTABLE AS $cf$
  SELECT md5(concat_ws('|', p_locale, btrim(p_value), p_kind, p_form))
$cf$;

-- ★ P1.07: 잠금·출처 우선순위·현재 효력 guard 를 공통 name writer 의 **DB 보호선**으로 둔다.
-- Go 경로는 우회될 수 있으므로(WRITER_AUTHORITY A01/A02) 규칙 자체는 DB 에 있어야 한다.
-- 우선순위 판정은 legacy 가 쓰던 kdb_source_priority() 를 그대로 재사용한다 — 두 벌을 만들지 않는다.
CREATE OR REPLACE FUNCTION kentity_guard_name_write() RETURNS trigger LANGUAGE plpgsql AS $gn$
DECLARE blocked int;
BEGIN
  -- ⓪ 근거가 죽은 주장의 내림은 막지 않는다 (D-30).
  --
  -- 근거 철회 전파(kentity_invalidate_withdrawn_evidence)는 이 표의 status 만 'blocked' 로
  -- 내린다. 값·종류·형식·출처는 그대로다. 그런데 아래 ①은 "status 가 바뀌었고 출처가
  -- 자동 출처"라는 이유로 이를 덮어쓰기로 오인해 예외를 던졌고, 그 결과 **운영자가 잠근
  -- 이름이 하나라도 있으면 그 근거를 철회하는 트랜잭션 전체가 중단**됐다. 안전장치가
  -- 안전장치를 막은 것이다(TRG01 로 실증).
  --
  -- 구분 기준: **주장을 바꾸는 것**과 **근거가 사라져 내리는 것**은 다르다. 값·종류·형식이
  -- 그대로이고 뒤를 받치던 근거가 더는 쓸 수 없는 상태라면, 이것은 교체가 아니라 회수다.
  -- 회수를 막으면 근거 없는 이름을 계속 서빙하게 되는데 그쪽이 더 나쁘다("빈칸 > 틀린값").
  -- 근거가 여전히 멀쩡하면 이 면제는 걸리지 않으므로 잠금 보호는 그대로 남는다.
  IF TG_OP = 'UPDATE'
     AND (NEW.value, NEW.kind, NEW.form, NEW.locale) IS NOT DISTINCT FROM (OLD.value, OLD.kind, OLD.form, OLD.locale)
     AND NEW.status IN ('blocked','withdrawn') AND OLD.status <> NEW.status
     AND NOT EXISTS (SELECT 1 FROM kentity_evidence v
                      WHERE v.id = NEW.evidence_id AND v.entity_id = NEW.entity_id
                        AND v.status = 'verified' AND v.export_allowed)
  THEN
    RETURN NEW;
  END IF;

  -- ① 운영자 잠금 (X01). 잠긴 표기는 자동 출처가 바꿀 수 없다. 해제는 별도 운영자 절차다.
  IF TG_OP = 'UPDATE' AND OLD.operator_locked
     AND NEW.source_code NOT IN ('operator','operator-locked','correction-verified')
     AND (NEW.value, NEW.status, NEW.kind, NEW.form) IS DISTINCT FROM (OLD.value, OLD.status, OLD.kind, OLD.form)
  THEN
    RAISE EXCEPTION 'operator-locked name cannot be replaced by automatic source %', NEW.source_code;
  END IF;

  -- ② 출처 우선순위 (X03). 검증된 대표명을 더 낮은 등급 출처로 교체하지 않는다.
  -- kdb_source_priority 는 숫자가 작을수록 높은 등급이다.
  IF TG_OP = 'UPDATE' AND OLD.kind = 'canonical' AND OLD.status = 'verified'
     AND NEW.value IS DISTINCT FROM OLD.value
     AND kdb_source_priority(NEW.source_code) > kdb_source_priority(OLD.source_code)
  THEN
    RAISE EXCEPTION 'lower-priority source % cannot replace verified canonical from %', NEW.source_code, OLD.source_code;
  END IF;

  -- ③ 현재 효력 guard (X11). 철회·수동정정된 주장은 원천·repair·옛 worker 가 재설치할 수 없고,
  -- 운영자가 의도적으로 비운 대표명 슬롯은 별칭 자동 승격으로 채울 수 없다.
  SELECT count(*) INTO blocked FROM kentity_source_guards g
   WHERE g.state = 'active' AND g.scope_kind = 'name_slot'
     AND g.entity_id = NEW.entity_id AND g.locale = NEW.locale
     AND ( (g.guard_kind IN ('withdrawn_name','operator_correction')
            AND g.claim_fingerprint = kentity_name_claim_fingerprint(NEW.locale, NEW.value, NEW.kind, NEW.form))
        OR (g.guard_kind = 'empty_slot' AND NEW.kind = 'canonical') );
  IF blocked > 0 THEN
    RAISE EXCEPTION 'an active source guard blocks this name claim for locale %', NEW.locale;
  END IF;

  RETURN NEW;
END $gn$;
CREATE TRIGGER kentity_guard_name_write BEFORE INSERT OR UPDATE ON kentity_names
  FOR EACH ROW EXECUTE FUNCTION kentity_guard_name_write();

-- D-34: "현재 승인된 원천 정책"은 provider 당 하나여야 하는데 아무것도 강제하지 않았다.
-- UNIQUE (provider, version) 만 있어서 같은 provider 의 승인 정책이 둘 이상 공존할 수 있고,
-- 읽기 쪽 loadApprovedPolicies 는 DISTINCT ON (provider) 로 그중 하나를 고른다 — 어느 권한이
-- 적용될지를 결정이 아니라 ORDER BY 가 정하게 된다. 갱신은 겹치기가 아니라 인계여야 한다:
-- 새 버전을 approved 로 올릴 때 옛 버전을 같은 transaction 에서 expired 로 내린다.
CREATE UNIQUE INDEX kentity_source_policies_one_approved
  ON kentity_source_policies (provider) WHERE status = 'approved';

-- ============================================================ 15. 트리거 캐스케이드 보정 (D-30~D-32)
-- 전수 감사로 드러난 것: 제약은 촘촘한데 **트리거 본문이 P1 이전 스키마를 가정한 채** 남아
-- 있었다. 과거 런타임에서만 터진 4건과 같은 사각지대다.

-- D-31: 근거 철회 전파가 이름·외부ID 에서 멈춘다.
-- P1 이 "verified 는 근거 필수"를 직업·분야·분류·이름근거까지 넓혔는데 전파 대상은
-- 0117 시절 두 표 그대로였다. 근거를 철회해도 그 근거를 가리키는 verified 행이 남아,
-- 철회된 주장이 계속 검증된 것처럼 서빙된다.
CREATE OR REPLACE FUNCTION kentity_invalidate_withdrawn_evidence() RETURNS trigger LANGUAGE plpgsql AS $iw$
BEGIN
 IF OLD.status='verified' AND OLD.export_allowed AND (NEW.status<>'verified' OR NOT NEW.export_allowed) THEN
  UPDATE kentity_names SET status='blocked',revision=revision+1,updated_at=now()
   WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_external_ids SET status='withdrawn' WHERE evidence_id=NEW.id AND status='verified';
  -- P1 이 넓힌 면들. 값은 지우지 않는다 — 검증 상태만 내리고 이력은 남긴다.
  UPDATE kentity_person_roles SET status='blocked',revision=revision+1,updated_at=now()
   WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_entity_domains SET status='blocked',revision=revision+1,updated_at=now()
   WHERE evidence_id=NEW.id AND status='verified';
  UPDATE kentity_entities SET classification_status='pending',revision=revision+1,updated_at=now()
   WHERE classification_evidence_id=NEW.id AND classification_status='verified';
  UPDATE kentity_name_evidence SET resolution='withdrawn',revision=revision+1,
         decided_by='source-policy',decided_at=now(),
         decision_reason='backing evidence withdrawn or reuse permission removed'
   WHERE evidence_id=NEW.id AND resolution='unresolved';
  UPDATE kentity_entities e SET status='candidate',revision=revision+1,updated_at=now()
   WHERE e.id=NEW.entity_id AND e.write_owner<>'kdb' AND e.status='active' AND NOT EXISTS (
    SELECT 1 FROM kentity_evidence v WHERE v.entity_id=e.id AND v.claim_type='identity' AND v.status='verified' AND v.export_allowed);
  INSERT INTO kentity_audit_events(entity_id,actor,action,reason,after_value)
   VALUES(NEW.entity_id,'source-policy','evidence_invalidated','verified evidence withdrawn or reuse permission removed',jsonb_build_object('evidence_id',NEW.id));
 END IF;
 RETURN NEW;
END $iw$;

-- D-32: 의존 guard 가 자식 근거를 claim_type='name' 으로 하드코딩한다.
-- P1 이 claim_type 을 7종으로 넓히고 PK 를 (evidence_id, depends_on_id) 로 넓혀 다중 부모를
-- 허용해 놓고, 정작 직업·분류·프로필 근거의 의존은 등록조차 안 됐다. 규칙의 본질은
-- "자식이 무엇이든 부모는 같은 Entity 의 재사용 가능한 verified identity 여야 한다"이다.
CREATE OR REPLACE FUNCTION kentity_guard_evidence_dependency() RETURNS trigger LANGUAGE plpgsql AS $ed$
BEGIN
 IF TG_OP='UPDATE' THEN RAISE EXCEPTION 'evidence dependency is immutable'; END IF;
 IF NOT EXISTS(SELECT 1 FROM kentity_evidence p JOIN kentity_evidence c ON c.entity_id=p.entity_id
 WHERE p.id=NEW.depends_on_id AND c.id=NEW.evidence_id AND p.entity_id=NEW.entity_id
 AND p.claim_type='identity' AND p.status='verified' AND p.export_allowed
 AND c.status='verified' AND c.export_allowed AND c.id <> p.id)
 THEN RAISE EXCEPTION 'evidence dependency requires a reusable verified identity of the same Entity'; END IF;
 RETURN NEW;
END $ed$;

-- D-33: "승인 근거는 불변"의 비교 튜플이 10컬럼에서 멈춰 있다. P1 이 추가한
-- claim_fingerprint/source_observation_hash 는 새 UNIQUE(kentity_evidence_claim_key)의
-- 구성 컬럼이다 — 주장의 정체성을 정하는 값이 감시 밖에서 조용히 바뀔 수 있었다.
CREATE OR REPLACE FUNCTION kentity_guard_evidence_observation() RETURNS trigger LANGUAGE plpgsql AS $eo$
BEGIN
 IF OLD.status='verified' AND NEW.status='verified' AND
 (OLD.entity_id,OLD.provider,OLD.source_record_id,OLD.source_url,OLD.claim_type,OLD.license_code,
  OLD.observed_at,OLD.verified_by,OLD.verified_at,OLD.summary,
  OLD.claim_fingerprint,OLD.source_observation_hash,OLD.claim_payload,OLD.independent_origin,OLD.source_policy_id)
 IS DISTINCT FROM
 (NEW.entity_id,NEW.provider,NEW.source_record_id,NEW.source_url,NEW.claim_type,NEW.license_code,
  NEW.observed_at,NEW.verified_by,NEW.verified_at,NEW.summary,
  NEW.claim_fingerprint,NEW.source_observation_hash,NEW.claim_payload,NEW.independent_origin,NEW.source_policy_id)
 THEN RAISE EXCEPTION 'approved evidence is immutable; withdraw and record a new observation'; END IF;
 RETURN NEW;
END $eo$;

COMMIT;
