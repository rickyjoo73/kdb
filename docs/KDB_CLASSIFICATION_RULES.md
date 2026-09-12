# KDB 통합 분류 코드표 — P0.01 설계 산출물

작성: 2026-09-12 KST · 버전: `classification-design-v1`.
기준: [선택 흡수 기획안](KDB_TDB_INTEGRATION_BLUEPRINT.md), [실행 TODO](KDB_INTEGRATION_TODO.md).
상태: **분류 설계안 작성. 운영 코드/DB에 아직 적용하지 않음. G0 인수 전.**

## 1. 실제 구조 조사와 이번 결정

2026-09-12 20:21 KST, KDB/TDB PostgreSQL 16.14에서 READ ONLY로 enum·코드표·컬럼·제약을 확인했다.
KDB legacy 유형 13종/인물 primary_role 16종, 공통 유형 11종/분야 8종, TDB 지원 유형 22종을 대조했다.
TDB 실제 데이터 유형은 앞선 19:28 조사에서 19종이었다. **지원 코드 수와 등록 데이터 수는 다르다.**
Go의 `internal/kentity/store.go`, `catalog.go`, `tdb_types.go`에도 허용값이 있으므로 DB 사전만 바꾸어서는 적용되지 않는다.

| 결정 | 이유 | 구현 시 영향 |
|---|---|---|
| 공통 기존 11코드 보존 + brand/character 2코드 추가 제안 | 회사·브랜드·상품, 실존 인물·허구 인물을 억지로 같은 유형에 넣지 않음 | DB CHECK→FK, Go 검증/조회/승인, mapper, API enum, UI 회귀 검증 후 활성화 |
| 인물 기본 유형은 person, 직업은 N:M | 가수이자 배우인 동일인은 UUID 하나 | person 전용 복수 직업 연결과 직군 하위 포함 조회 |
| 분야 8개 유지, 새 분야는 같은 사전에 추가 | 정치/경제용 별도 DB·메뉴·worker 불필요 | 새 분야 코드만으로 실제 수집/언어 준비 완료를 주장하지 않음 |
| subtype은 유형별 통제 코드, 미확인은 NULL | 자유 문자열/other로 분류 완료를 가장하지 않음 | 잘못된 유형+subtype 조합은 DB에서 거부 |
| 원천 유형은 후보 매핑일 뿐 승인 근거가 아님 | legacy의 잘못된 work 투영과 TDB 장소→역 오연결 승계 방지 | 원천 ID·원래 코드·판정 근거와 변환 버전을 보존 |

새 2코드는 **목표 설계 결정**이다. 기존 소비자가 모르는 코드를 받게 하는 전환은 P0.06/P1/P5에서 따로 검증한다.
유형 추가를 숨기기 위해 brand를 company, character를 person으로 거짓 응답하지 않는다.

## 2. 기본 유형 사전

기본 유형은 하나다. 표의 ‘집합 조회’는 화면 필터이며 별도 부모 Entity/복제 행이 아니다.

| code | 표시명 | 경계 / 집합 조회 |
|---|---|---|
| person | 실존 인물 | 직업은 별도. 실존 여부 불명은 후보 보류 |
| organization | 기관·단체 | 공공기관/정당/학교/협회/비법인 공연단체. 조직 전체 조회에 포함 |
| company | 기업·사업체 | 상업적 운영 주체. 개인사업의 사업체와 사업주 인물은 별개. 조직 전체 조회에 포함 |
| team | 스포츠팀 | 팀과 팀 운영 법인은 별개. 조직 전체 조회에 포함 |
| league | 리그 | 지속되는 경기 조직/리그 정체성. 특정 시즌 대회는 event. 조직 전체 조회에 포함 |
| location | 장소 | 지역/자연물/시설/지점/실제 노선·코스. 운영 기관과 별개 |
| work | 작품 | 영화/음악/방송/문학/예술작품. 창작자와 별개 |
| event | 행사·대회 | 행사 시리즈 또는 특정 회차. 개최 장소/주최자와 별개 |
| product | 상품 | 명명된 상품 모델/소프트웨어/제품. 제조사·브랜드와 별개 |
| concept | 명명된 개념·용어 | 번역에 필요한 고정 명칭만. 일반 명사 사전/기사 키워드 전체를 넣지 않음 |
| brand | 브랜드 | 상표·브랜드 정체성. 동명의 회사/상품/점포를 자동 병합하지 않음 |
| character | 캐릭터 | 허구의 인물/캐릭터. 연기한 배우와 별개 |
| unknown | 유형 검수 대기 | 운영 검수용. 분류 완료/공통 신규 서비스 활성화 불가 |

## 3. 세부유형 사전

키는 `(entity_type, code)`. 다음은 1차 허용 목록이다. `parent_code`는 전부 NULL로 시작한다.
첫 버전은 유형→세부유형 2단계만 사용한다. 실제 수요 없는 세부 계층을 만들지 않는다.
추가가 필요하면 사례·근거·mapper/API/UI/시험 영향을 검토한다. 임의 문자열은 제안으로만 받는다.

| entity_type | code | 표시명 |
|---|---|---|
| person | real | 실존 인물 |
| organization | public_body | 공공기관·행정기관 |
| organization | political_party | 정당 |
| organization | school | 학교 조직 |
| organization | media_outlet | 매체·채널 운영 단체 |
| organization | association | 협회·단체 |
| organization | performance_group | 음악·공연 그룹 |
| organization | research_institute | 연구기관 |
| organization | religious_body | 종교 조직 |
| company | corporation | 법인 기업 |
| company | business | 비법인 사업체 |
| company | agency | 상업적 매니지먼트·대행 사업체 |
| team | sports_team | 스포츠팀 |
| league | sports_league | 스포츠 리그 |
| location | tourist_spot | 관광 명소 |
| location | accommodation | 숙박 시설·지점 |
| location | restaurant | 음식점 지점 |
| location | shopping | 판매 시설·점포 |
| location | cultural_facility | 문화시설 |
| location | sports_facility | 체육·레포츠 시설 |
| location | stadium | 경기장 |
| location | heritage_site | 장소형 문화유산 |
| location | natural_feature | 자연 지명 |
| location | admin_region | 행정구역 |
| location | legal_dong | 법정동 |
| location | district | 상권·지구 |
| location | road | 도로 |
| location | station | 역·정류장 |
| location | airport | 공항 |
| location | port | 항구·터미널 |
| location | transport_facility | 그 밖의 확인된 교통시설 |
| location | transit_route | 명명된 교통 노선 |
| location | travel_course | 명명된 여행 코스 |
| location | campus | 학교 캠퍼스 |
| work | film | 영화 |
| work | drama | 드라마 |
| work | broadcast_program | 방송 프로그램 |
| work | episode | 개별 회차 작품 |
| work | song | 곡 |
| work | album | 음반 |
| work | book | 도서·문학 작품 |
| work | artwork | 미술·창작 작품 |
| work | artifact | 이동 가능한 유물 |
| event | festival_series | 축제 시리즈 |
| event | festival_edition | 축제 회차 |
| event | performance | 공연 행사 |
| event | tour | 공연 투어 |
| event | competition_series | 대회 시리즈 |
| event | competition_edition | 대회 회차·시즌 |
| event | election | 특정 선거 |
| event | conference | 회의·박람회 행사 |
| product | named_product | 고유명으로 구별되는 상품 |
| product | vehicle_model | 차량 모델 |
| product | software | 소프트웨어 제품 |
| product | food_product | 명명된 식음료 상품 |
| concept | named_term | 번역 관리 대상의 고정 용어 |
| concept | food_name | 전통 음식·메뉴의 고정 명칭 |
| concept | award | 상의 명칭 |
| concept | program | 정책·사업의 명칭 |
| brand | commercial_brand | 상업 브랜드 |
| character | fictional | 허구 캐릭터 |

선택 우선순위: 근거 있는 구체 코드가 포괄 코드보다 우선한다. 예: 경기장은 sports_facility보다 stadium,
소속사는 법인이어도 용도가 확인되면 agency. subtype 자체가 법인격·소유·직업을 증명하지는 않는다.
분류가 바뀌어도 같은 현실 대상이면 UUID를 유지한다. 유형 간 오분류 수정은 P0.03의 참조 검증 대상이다.

## 4. 인물 직군·직업 사전

키는 `code`, 부모는 같은 사전의 FK. 상위도 근거가 있을 때 직접 부여 가능하다.
하위 입력 시 상위 행을 중복 삽입하지 않고 검색에서 조상 포함으로 처리한다.

| code | parent_code | 표시명 |
|---|---|---|
| businessperson | — | 기업인 |
| entrepreneur | businessperson | 창업가 |
| executive | businessperson | 기업 경영자 |
| politician | — | 정치인 |
| public_official | — | 공직자 |
| entertainer | — | 연예인 |
| singer | entertainer | 가수 |
| rapper | singer | 래퍼 |
| actor | entertainer | 배우 |
| idol | entertainer | 아이돌 |
| broadcaster | entertainer | 방송인 |
| comedian | entertainer | 코미디언 |
| model | entertainer | 모델 |
| sports_person | — | 스포츠인 |
| athlete | sports_person | 선수 |
| coach | sports_person | 지도자 |
| referee | sports_person | 심판 |
| journalist | — | 언론인 |
| researcher | — | 연구자 |
| educator | — | 교육자 |
| writer | — | 작가 |
| director | — | 감독 |
| producer | — | 제작자·프로듀서 |
| creator | — | 창작자 |
| artist | — | 예술가 |
| professional | — | 전문직 종사자 |

`idol`만으로 singer/actor를 자동 부여하지 않는다. 둘 다 확인되면 둘 다 부여한다.
감독/프로듀서/창작자를 모두 연예인으로 묶지 않는다. 영화 감독/스포츠 감독이 혼재하는 원문은 문맥 검수한다.
국회의원·시장·대표이사·감독으로의 재임은 **직책/소속 관계와 기간**이다. 일반 직업 코드와 섞지 않는다.
종목은 선수의 동일인 단독 증거가 아니며 P0.02 관계/최소 속성 설계에서 담는다.
`other`, `fictional`, `famous_person`은 직업 코드로 만들지 않는다.
직업을 모르면 pending, 서로 다르다고 주장하는 출처가 충돌하면 conflict다. 유명하다고 임의 직업을 부여하지 않는다.
직업을 갖지 않은 공개 인물 등은 근거 있는 `not_applicable` 검수 판정이 가능하되 자동 기본값으로 사용하지 않는다.

## 5. 분야 사전과 완성도

| code | 표시명 | 부여 기준 |
|---|---|---|
| politics | 정치 | 정치 활동·정당·선거 등 해당 분야 관련성이 확인됨 |
| government | 행정 | 공공행정·정책·기관 업무 관련성. 주소 존재만으로 부여 금지 |
| economy | 경제 | 경제 활동·기업·사업·금융 관련성. 음식점 전부에 기계 부여 금지 |
| society | 사회 | 사회 활동·사건·교육 등 관련성. 미분류의 대체 통 금지 |
| entertainment | 연예 | 연예 활동·공연·방송 관련성. 모든 인물/작품의 기본값 금지 |
| sports | 스포츠 | 스포츠 활동·팀·대회·시설의 관련성 |
| travel | 여행 | 관광 활용과 관련성이 확인된 장소·행사·대상 |
| culture | 문화 | 문화·예술·유산 등 관련성 |

분야는 복수다. 어떤 기사 카테고리에서도 모든 유형/분야를 검색할 수 있다. 분야는 접근 권한 경계가 아니다.
행정구역/여행 명소를 경제 기사에서 쓸 때 경제 Entity를 복제하지 않는다.
과학기술 등 추가 분야는 다음 버전에서 코드/표시/부여 규칙을 추가하며 새 DB를 만들지 않는다.

분류 상태와 이름 상태는 별개다:

- `pending`: 판단 근거 부족/필요 코드 미승인. 검수 사유와 다음 조치가 필수다.
- `conflict`: 원천/검수 간 유형·직업 등 모순. 자동 우선순위로 감추지 않는다.
- `verified`: 알려진 유형·맞는 세부유형·근거 있는 분야 1개 이상을 승인한 상태.
- 인물은 직업 승인 또는 근거 있는 해당 없음 판정까지 있어야 **분류 완성**으로 집계한다.
- 분류 완성은 동일인/locale 이름/권리 승인과 다르다. strict-ready는 P0.04에서 별도로 계산한다.
- 공통 신규 활성 서비스 대상 미분류 0이 목표다. 기존 legacy 활성 기록을 이번 설계만으로 대량 비활성화하지 않는다.
  전환 대상별 새 기준 판정/보류와 기존 서비스 호환 범위를 P2/P5에서 정한다.

## 6. KDB 기존 유형 → 목표 후보 매핑 (13/13)

아래 화살표는 정답 자동 승인이 아니다. 원본 ID별 판정과 P0.05 필드 매핑이 추가로 필요하다.

| source_code | 목표 후보 | 보류/분기 기준 |
|---|---|---|
| person | person.real | 실존 근거 필요. fictional 역할이면 충돌 검수 |
| group | organization.performance_group / team.sports_team / company | 음악 그룹·스포츠팀·법인 구분 |
| show | work.broadcast_program / work.episode / event.performance | 프로그램과 실연 행사 구분 |
| drama | work.drama | 실재 작품과 캐릭터/행사를 분리 |
| movie | work.film | 작품 정체성 확인 |
| song_album | work.song / work.album | 곡과 음반을 제목만으로 합치지 않음 |
| agency | company.agency / organization | 상업적 사업체 여부. 불명 subtype은 pending |
| channel_outlet | organization.media_outlet / company / brand / work.broadcast_program | 매체·운영 법인·브랜드·프로그램 구분 |
| brand_place | brand.commercial_brand / location / company / product | 브랜드·지점·법인·상품을 각각 판정 |
| event_tour | event.tour / event.performance / event.festival_series / event.festival_edition / event.competition_series / event.competition_edition | 시리즈와 회차 구분 |
| character | character.fictional | 실존 인물의 별칭을 캐릭터로 자동 분리하지 않음 |
| term | concept.named_term / concept.food_name / concept.award / concept.program | 일반명사·시험 자료는 사유 있는 제외/보관 |
| unknown | unknown | 원천 추가 조사. work로 기본 투영 금지 |

## 7. KDB 기존 인물 역할 → 목표 후보 매핑 (16/16)

primary/secondary 모두 개별 판정한다. primary만 복사하고 secondary를 버리지 않는다.

| source_code | 목표 role 또는 처리 |
|---|---|
| idol | idol |
| singer | singer |
| rapper | rapper |
| actor | actor |
| broadcaster | broadcaster |
| comedian | comedian |
| director | director; 스포츠 지도자 문맥이면 coach 검수 |
| producer | producer |
| model | model |
| creator | creator |
| athlete | athlete |
| politician | politician |
| businessperson | businessperson |
| journalist | journalist |
| fictional | 직업으로 이관 금지. character/person 유형 충돌 검수 |
| other | 직업 미확인 pending. 임의 entertainer 부여 금지 |

## 8. TDB 지원 유형 → 목표 후보 매핑 (22/22)

`is_geographic`는 원천 힌트다. true라도 인물/이동 유물/행사가 장소가 되는 것은 아니다.
QID가 없어도 조사·분류·별도 후보 UUID 수용이 가능하다. QID 존재만으로 동일인/표기 승인은 불가하다.

| source_code | 목표 후보 | 보류/분기 기준 |
|---|---|---|
| accommodation | location.accommodation | 브랜드/운영 회사와 지점 분리 |
| admin_region | location.admin_region | 행정 코드 namespace/개편 시점 확인 |
| cultural_facility | location.cultural_facility | 시설과 운영 기관 구분 |
| district | location.district | 행정구역/상권 경계 구분 |
| education | organization.school / location.campus / company | 학교 조직·캠퍼스·교육 사업체 구분 |
| festival_event | event.festival_series / event.festival_edition / event.performance / event.competition_edition | 행사장 주소를 행사 UUID의 장소 정체성으로 사용 금지 |
| food | concept.food_name / product.food_product | 전통 음식명과 특정 브랜드 제품 구분 |
| heritage | location.heritage_site / work.artifact / work.artwork / concept | 고정 장소·이동 유물·무형 유산을 구분. 코드 부족 시 보류 |
| legal_dong | location.legal_dong | 법정동/행정동 코드 동일시 금지 |
| leisure_sports | location.sports_facility / location.stadium / event | 시설·실제 행사 구분 |
| nature | location.natural_feature | 같은 산/하천 이름의 다른 지역 분리 |
| organization | organization / company / team / league | 이름/출처 종류만으로 상세형 확정 금지 |
| other | unknown | 재조사 후 판정. 건수 감소를 위한 일괄 장소화 금지 |
| person | person.real / character.fictional | 직업은 별도 근거. 전원 연예인/스포츠인 금지 |
| restaurant | location.restaurant | 역 인근 식당 ≠ 역. 브랜드/운영사와도 구분 |
| road | location.road | 같은 이름의 지역별 도로 분리 |
| shopping | location.shopping / brand.commercial_brand / company | 점포·브랜드·회사 구분. 역 QID 오연결 차단 |
| tourist_spot | location.tourist_spot / location.natural_feature / location.heritage_site | 확인된 구체 세부유형 우선 |
| transit | location.station / location.transit_route / location.transport_facility / company | 시설·노선·운영 회사 구분 |
| transport | location.station / location.airport / location.port / location.transit_route / location.transport_facility / company / product | 교통시설·운송사·상품 분리 |
| travel_course | location.travel_course | 명명된 코스와 일시적인 일반 추천 문장을 구분 |
| work | work.artifact / work.artwork / work.book / work.film / work.song / work.album | 제목·창작자·제작 시점 등 확인 |

## 9. ID·UI·변경 운영에 미치는 규칙

가상 동명 사례: 가수 김민수 A와 배우 김민수 B는 각각 별도 UUID이며 같은 한국어 이름 행이 공존한다.
A가 연기 활동도 한다는 독립 근거가 생기면 A에 actor 역할을 추가한다. **그것이 B와의 병합 근거는 아니다.**
동일 직업/생년까지 같아도 별개일 수 있다. 정당·소속사·팀·작품·공식 인물 ID와 문맥으로 확인한다.

등록 UI는 유형→허용 subtype→인물 복수 직업→복수 분야→근거 순으로 같은 폼을 사용한다.
동명 비교에는 UUID·직업·소속/지역·출처·판정 상태를 같이 보여주고 ‘새 대상 등록’도 허용한다.
필터는 유형/세부유형/직군 하위 포함/분야/미확인/신규 등록이다. 분야별 관리 포털은 만들지 않는다.
등록 건수, 분류 완성 수, 검증된 이름 수, 요청 locale 준비 수를 각각 구분한다.

코드 사전 변경은 개발 검토+승인된 migration으로 수행한다. 표시명 수정은 코드/UUID 재발급 사유가 아니다.
참조 중인 코드는 삭제하지 않고 enabled=false로 신규 부여를 막되 기존 값 조회는 유지한다.
부모 변경은 순환·깊이(최대 3레벨)·하위 검색 결과와 기존 인수 fixture 영향을 검증한다.

## 10. 검증 및 남은 의존성

문서 정적 검증 대상: 유형/세부유형/직업/분야 코드 중복 0, subtype 부모 유형 존재,
직업 부모 존재/순환 0, KDB 13유형·16역할/TDB 22유형 매핑 누락 0, 출력 코드의 사전 존재.
재현: `node docs/checks/validate-kdb-classification.cjs` ([검사 스크립트](checks/validate-kdb-classification.cjs)).
이 검사는 2026-09-12의 11코드 Go 기준과의 호환 차이도 확인한다. 향후 실제 코드가 확장되면 검사를 삭제하지 말고 승인된 새 계약으로 갱신한다.
분류 실데이터 정확도 검증, 실제 DB FK/동시성 시험, Go/API/UI 반영은 **아직 하지 않았다**.
P0.05는 원본 필드 단위 매핑, P0.08은 실제 정답 사례, P0.10은 G0 인수를 책임진다.
P0.01 문서 작성 완료를 전량 분류 완료나 G0 승인으로 해석하지 않는다.
