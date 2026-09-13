-- P2.02 선매핑 코드표 — 원천 유형/직군/기각 사유 → 목표 코드
--
-- 격리 복원본에서만 실행한다. 운영 DB 에 적용하지 않는다.
-- 원장 요구: "KDB의 기존 유형·primary/secondary_role·기각 사유와 TDB 원천 유형을 코드표에
-- 선매핑한다. 추측·상충·비고유명사·권리 문제는 각각 보류/제외로 설명한다."
--
-- 이 표의 성격: **후보 매핑이지 승인이 아니다**(분류 규칙 §1). 원천 유형은 판정 재료일 뿐,
-- 실제 분류 승인은 근거를 검사한 뒤 이뤄진다. 여기서 정하는 것은 "무엇을 그 재료로 삼고,
-- 무엇은 재료로도 삼지 않을지"다.

\set ON_ERROR_STOP on
BEGIN;

DROP TABLE IF EXISTS kentity_source_type_map;
CREATE TABLE kentity_source_type_map (
  source_system text NOT NULL CHECK (source_system IN ('tdb','kdb','kdb_role')),
  source_code   text NOT NULL CHECK (btrim(source_code) <> ''),
  disposition   text NOT NULL
    CHECK (disposition IN ('map','hold','exclude')),
  -- map 일 때만 목표가 있다. hold/exclude 는 목표를 비워 두고 이유를 남긴다.
  target_entity_type text,
  target_subtype     text,
  -- 하나의 원천 코드가 여러 목표로 갈릴 수 있다. 그 경우 자동 확정하지 않고 후보만 적는다.
  alt_targets  text NOT NULL DEFAULT '',
  -- 구분값(disambig)을 무엇으로 만들지. 흡수 시 반드시 명시값을 준다(식별 계약 §1.2).
  disambig_rule text NOT NULL CHECK (btrim(disambig_rule) <> ''),
  reason        text NOT NULL CHECK (btrim(reason) <> ''),
  policy_version text NOT NULL DEFAULT 'tdb-premap-v1',
  created_at    timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (source_system, source_code),
  FOREIGN KEY (target_entity_type) REFERENCES kentity_types(code) ON DELETE RESTRICT,
  FOREIGN KEY (target_entity_type, target_subtype)
    REFERENCES kentity_subtypes(entity_type, code) ON DELETE RESTRICT,
  CONSTRAINT kentity_source_type_map_shape CHECK (
    CASE disposition
      WHEN 'map'  THEN target_entity_type IS NOT NULL
      ELSE target_entity_type IS NULL AND target_subtype IS NULL
    END)
);

-- ============================================================ 1. TDB 19유형
-- 실측 분모는 KDB_P2_SOURCE_SURVEY.md §3. 문서 §8 의 후보 매핑을 실행값으로 확정한다.
-- 구분값 규칙: 장소는 행정구역이 가장 강한 구분 신호다(같은 이름의 다른 지역이 흔하다).
INSERT INTO kentity_source_type_map
 (source_system, source_code, disposition, target_entity_type, target_subtype, alt_targets, disambig_rule, reason) VALUES
 ('tdb','restaurant','map','location','restaurant','','원천 disambiguator → 없으면 시군구(addr_ko)','145,433건. 역 인근 식당을 역으로 승계하지 않는다(P2.05 위험)'),
 ('tdb','district','map','location','district','','원천 disambiguator → 없으면 상위 행정구역','83,897건. 행정구역과 상권 경계를 같은 것으로 보지 않는다'),
 ('tdb','tourist_spot','map','location','tourist_spot','location.natural_feature, location.heritage_site','원천 disambiguator → 없으면 시군구','36,981건. 확인된 구체 세부유형이 있으면 그쪽을 우선한다'),
 ('tdb','accommodation','map','location','accommodation','','원천 disambiguator → 없으면 시군구','26,569건. 브랜드/운영 회사와 지점을 분리한다'),
 ('tdb','shopping','map','location','shopping','brand.commercial_brand, company.business','원천 disambiguator → 없으면 시군구','25,509건. 점포·브랜드·회사를 구분한다. 역 QID 오연결 차단(P2.05)'),
 ('tdb','leisure_sports','map','location','sports_facility','location.stadium, event','원천 disambiguator → 없으면 시군구','20,796건. 시설과 실제 행사를 구분한다'),
 ('tdb','nature','map','location','natural_feature','','원천 disambiguator → 없으면 시군구','19,664건. 같은 산/하천 이름의 다른 지역을 분리한다'),
 ('tdb','heritage','map','location','heritage_site','work.artifact, work.artwork, concept.named_term','원천 disambiguator → 없으면 지정번호(heritage_no)','13,888건. 고정 장소·이동 유물·무형 유산이 섞여 있다. 이동 유물은 장소가 아니다'),
 ('tdb','cultural_facility','map','location','cultural_facility','','원천 disambiguator → 없으면 시군구','5,943건. 시설과 운영 기관을 구분한다'),
 ('tdb','legal_dong','map','location','legal_dong','','법정동 코드(ldong_code)','4,912건. 법정동과 행정동을 같은 것으로 보지 않는다'),
 ('tdb','festival_event','map','event','festival_series','event.festival_edition, event.performance, event.competition_edition','원천 disambiguator → 없으면 개최지 시군구','3,322건. 행사장 주소를 행사 UUID 의 장소 정체성으로 쓰지 않는다'),
 ('tdb','admin_region','map','location','admin_region','','행정 코드','249건. 행정 코드 namespace 와 개편 시점을 확인한다'),
 -- 갈리는 것: 자동 확정하지 않는다.
 ('tdb','transit','hold',NULL,NULL,'location.station, location.transit_route, location.transport_facility, company.business','노선/시설 구분이 정해진 뒤에 결정','911건 전부 구분자 없음. 시설·노선·운영 회사가 한 코드에 섞여 있어 이름만으로 갈 수 없다'),
 ('tdb','education','hold',NULL,NULL,'organization.school, location.campus, company.business','학교 조직인지 캠퍼스인지 사업체인지 확인 후','1,122건 전부 구분자 없음. 학교 조직·캠퍼스·교육 사업체가 한 코드에 있다'),
 ('tdb','organization','hold',NULL,NULL,'organization.*, company.*, team.sports_team, league.sports_league','상세형을 이름/출처 종류만으로 확정하지 않는다','2,557건 전부 구분자 없음. 상충 위험이 가장 큰 코드다'),
 ('tdb','work','hold',NULL,NULL,'work.artifact, work.artwork, work.book, work.film, work.song, work.album','제목·창작자·제작 시점 확인 후','3,822건. 작품 세부형은 근거 없이 고를 수 없다'),
 ('tdb','food','hold',NULL,NULL,'concept.food_name, product.food_product','전통 음식명인지 특정 브랜드 제품인지','2,774건 전부 구분자 없음. **비고유명사 위험** — 일반 음식명은 고유명사가 아니다'),
 ('tdb','person','hold',NULL,NULL,'person.real, character.fictional','동일인 판정 후 부여(§아래)','31,378건 전부 구분자 없음. KDB 와 이름이 겹치는 3,604건은 동일인 판정이 선행돼야 한다'),
 ('tdb','other','hold',NULL,NULL,'unknown','재조사 후 판정','106,602건. **추측 금지 구간.** 건수를 줄이려고 일괄 장소화하지 않는다(분류 규칙 §8)');

-- 문서 §8 에는 있으나 실데이터에 행이 없는 3코드. 없다고 지우지 않고 0건으로 기록한다.
INSERT INTO kentity_source_type_map
 (source_system, source_code, disposition, target_entity_type, target_subtype, alt_targets, disambig_rule, reason) VALUES
 ('tdb','road','map','location','road','','원천 disambiguator → 없으면 시군구','실데이터 0건. 같은 이름의 지역별 도로를 분리한다'),
 ('tdb','transport','hold',NULL,NULL,'location.station, location.airport, location.port, location.transit_route, company.business, product.vehicle_model','교통시설·운송사·상품 분리 후','실데이터 0건'),
 ('tdb','travel_course','map','location','travel_course','','원천 disambiguator → 없으면 시군구','실데이터 0건. 명명된 코스와 일시적 추천 문장을 구분한다');

-- ============================================================ 2. KDB 기존 유형 (P1.02 가 보류시킨 18,042건)
-- P1.02 에서 legacy subtype 을 NULL 로 내리고 원문을 classification_reason 에 보존했다.
-- 여기서 그 원문을 목표 코드로 푼다.
INSERT INTO kentity_source_type_map
 (source_system, source_code, disposition, target_entity_type, target_subtype, alt_targets, disambig_rule, reason) VALUES
 ('kdb','person','map','person','real','','기존 disambig → 없으면 소속사·생년·대표작','6,689건. 실존 인물. 직업은 person_roles 로 따로 간다'),
 ('kdb','song_album','hold',NULL,NULL,'work.song, work.album','곡인지 앨범인지 근거 확인 후','2,336건. 한 코드에 곡과 앨범이 섞여 있어 자동으로 못 가른다'),
 ('kdb','term','hold',NULL,NULL,'concept.named_term','**비고유명사 위험**. 번역에 필요한 고정 명칭만 남긴다','1,981건. 일반 명사 사전이 섞여 들어왔을 수 있다(분류 규칙 §2 concept 경계)'),
 ('kdb','show','map','work','broadcast_program','','기존 disambig → 없으면 채널·방영 시작연도','1,607건. 방송 프로그램'),
 ('kdb','group','hold',NULL,NULL,'organization.performance_group, team.sports_team','공연 단체인지 스포츠팀인지','1,289건. 그룹의 성격이 코드에 없다'),
 ('kdb','movie','map','work','film','','기존 disambig → 없으면 개봉연도·감독','945건'),
 ('kdb','event_tour','hold',NULL,NULL,'event.tour, event.performance, event.festival_series','투어인지 개별 공연인지','833건'),
 ('kdb','character','map','character','fictional','','기존 disambig → 없으면 출연 작품','654건. 허구 인물. 연기한 배우와 별개다'),
 ('kdb','agency','map','company','agency','','기존 disambig → 없으면 소재 시군구','559건. 연예 기획사'),
 ('kdb','brand_place','hold',NULL,NULL,'brand.commercial_brand, location.shopping, company.business','브랜드인지 점포인지 회사인지','512건. **상충 코드** — 세 가지가 한 이름에 묶여 있다'),
 ('kdb','channel_outlet','hold',NULL,NULL,'organization.media_outlet, work.broadcast_program','매체 조직인지 프로그램인지','450건'),
 ('kdb','unknown','hold',NULL,NULL,'unknown','재조사 후 판정','186건. 유형 검수 대기'),
 ('kdb','tdb:person','exclude',NULL,NULL,'','원천 접두어가 값에 섞인 오염 1건. 정상 코드로 정정 후 재평가','1건. P1.01 에서 발견된 legacy 오염값');

-- ============================================================ 3. 인물 직군 (primary_role 16종)
-- 직업은 정체성이 아니다(식별 계약 §1.1). 한 사람이 여러 직업을 가지는 것이 정상이므로
-- 이 매핑은 person_roles 행을 만드는 재료이며 UUID 를 나누는 근거가 아니다.
INSERT INTO kentity_source_type_map
 (source_system, source_code, disposition, target_entity_type, target_subtype, alt_targets, disambig_rule, reason) VALUES
 ('kdb_role','actor','map','person','real','role:actor','직군은 구분값이 아니다','1,335건'),
 ('kdb_role','idol','map','person','real','role:idol','직군은 구분값이 아니다','768건'),
 ('kdb_role','singer','map','person','real','role:singer','직군은 구분값이 아니다','753건'),
 ('kdb_role','broadcaster','map','person','real','role:broadcaster','직군은 구분값이 아니다','280건'),
 ('kdb_role','comedian','map','person','real','role:comedian','직군은 구분값이 아니다','124건'),
 ('kdb_role','rapper','map','person','real','role:rapper','직군은 구분값이 아니다','80건'),
 ('kdb_role','creator','map','person','real','role:creator','직군은 구분값이 아니다','80건'),
 ('kdb_role','director','map','person','real','role:director','직군은 구분값이 아니다','75건'),
 ('kdb_role','athlete','map','person','real','role:athlete','직군은 구분값이 아니다','62건'),
 ('kdb_role','producer','map','person','real','role:producer','직군은 구분값이 아니다','59건'),
 ('kdb_role','model','map','person','real','role:model','직군은 구분값이 아니다','41건'),
 ('kdb_role','businessperson','map','person','real','role:businessperson','직군은 구분값이 아니다','21건'),
 ('kdb_role','politician','map','person','real','role:politician','직군은 구분값이 아니다','14건'),
 ('kdb_role','journalist','map','person','real','role:journalist','직군은 구분값이 아니다','6건'),
 -- 목표 사전에 대응이 없는 둘.
 ('kdb_role','fictional','exclude',NULL,NULL,'','직업이 아니라 유형이다 → character.fictional 로 간다','37건. 직군 사전에 넣지 않는다'),
 ('kdb_role','other','hold',NULL,NULL,'','근거 없이 직군을 만들지 않는다','2,017건(최다). "정보가 없어서 other" 를 직업으로 승인하지 않는다(구조 §11)');

COMMIT;
