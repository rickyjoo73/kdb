# 인수 시험 fixture 설계 — M01~M14 합성 사례 + 19 원본 유형 경계 표본 (P0.08)

기록일: 2026-09-13 KST. 기계 판독 정본은 [checks/kdb-acceptance-fixtures.json](checks/kdb-acceptance-fixtures.json),
정적 검사는 `node docs/checks/validate-kdb-acceptance-fixtures.cjs`다. 실행 상태 정본은 [KDB_INTEGRATION_TODO.md](KDB_INTEGRATION_TODO.md) §6 이며 M ID 는 거기서 온다.
어느 fixture 도 실행하지 않았고 DB 를 읽거나 쓰지 않았다. 실데이터 수치는 건수만 참조했다.

## 1. 원칙 — "입력 출처를 자기 정답으로 쓰지 않는다"를 구조로 만든다

| 사례 종류 | 정답의 출처 | 판정자 |
|---|---|---|
| M01~M14 (합성) | **구성으로 확정.** fixture 가 UUID·이름·근거·기대 상태를 정의하므로 판정이 필요 없다. 단언은 DB/API 상태와 fixture 를 비교한다 | `constructed_truth` (+ 규칙 판정기 대조) |
| T01~T19 (실데이터 경계) | **독립 판정 2경로의 합의 + 근거 provider 분리.** 행의 place_type·이름은 정답이 아니다 | `gemma_classify` ∧ `claude_judge` ∧ `evidence_provider_disjoint`, 불일치 → pending |

판정자는 시스템 안에 이미 있는 자율 판정기다: `internal/kdb/homonym`(결정적 신호: agency·birth_year·role·works, 비어 있으면 절대 병합 안 함),
`internal/kdb/aijudge.Classify`(gemma4:26b), `internal/kdb/claudejudge`(claude CLI sonnet), `internal/kdb/dataqa`(gpt-5.5, ok|contaminated|duplicate|uncertain),
`claude_adjudicate.go`(REJECT/RESCUE 최종). **운영자는 두 판정기가 불일치하거나 근거가 상충할 때만** 개입한다 — 기본 승인자가 아니라 escalation 이다.
같은 회사 도메인 두 개는 독립 근거 2개가 아니다(X07).

## 2. M01~M14 — 무엇을 고정했나

| ID | 합성 fixture 핵심 | 반드시 거부돼야 하는 것 | 불변조건 |
|---|---|---|---|
| M01 | 인물-A 두 명(singer / actor) | B 의 직업 행에 A 의 근거 FK | I01 I05 |
| M02 | 이름·직업·생년 동일 두 명 | 자동 병합; 판정은 possible_same 에 머묾 | I01 I06 |
| M03 | 한 사람 singer+actor | 직업 발견으로 두 번째 인물 생성 | I01 I09 |
| M04 | 명칭-D 가 person/company/location/product | 근거 없는 verified 관계, 허용표 밖 pair | I01 I04 |
| M05 | 역·역 앞 상점(30m)·학교·캠퍼스 | 상점에 역 QID verified; 이름·근접만으로 병합 | I01 I08 |
| M06 | 문맥 없는 이름만 match | 첫 행/유명인 자동 선택 → ambiguous+후보 | I06 I07 |
| M07 | 한 기사에 동명 가수·배우 span 2개 | ordinal 0 readiness 에 B 의 이름 (S01 FK) | I05 I12 |
| M08 | 같은 QID 가 person 과 work 에 | 두 번째 verified; 자동 병합 → conflict | I06 I08 |
| M09 | 개명·직업 추가·이적 | 연도만 아는 사실에 1월1일 채움 (S07) | I09 I12 |
| M10 | 같은 fingerprint 재수입 run-2 | 이름으로 다른 UUID 해소; binding 2개 | I02 I03 I10 |
| M11 | unknown 유형·직업 없는 인물 | other 승인, 근거 없는 not_applicable | I07 |
| M12 | 근거 철회·구 worker 재제안·병합 후 분리 | 철회 표기 재설치; split 뒤 redirect 잔존 | I10 I11 |
| M13 | 늦은 job·동시 writer·tenant scope | stale revision 쓰기; 다른 UUID 에 결과 부착; tenant 값 global 노출 | I07 |
| M14 | zh-Hans recorded / zh-Hant translated / ja generated | generated strict-ready; zh-Hant→Hans 무단 정규화 (R05) | I12 |

이름은 전부 `인물-A`·`작품-N` 류 자리표시자다. 검사기가 실명 패턴을 거부한다.
실규모 참고(건수만): KDB active 동명 그룹 22(45행), 동명 인물 3그룹(disambig 4/6), 복수 직업 2,974명, QID 공유 248키, needs_disambig 23. TDB 동명 그룹 22,304(99,068행, 최대 336), 유형 교차 동명 4,388.

## 3. T01~T19 — 19 원본 유형 층화 표본 명세

`tdb_places` 19유형(행 있는 것만; road/transport/travel_course 는 0행) × 층 5(정상 / 상충·유형교차동명 / other·subtype 불명 / QID 없음 / 삭제·병합).
층당 최소 10, 10 미만이면 전수(인수 기준 §2). aihub_tour 링크 1.4M 하나가 유형을 대표하지 못하게 한다.

| ID | 유형 | 총행 | QID | 한정어 | 목표 후보 | 경계 |
|---|---|---|---|---|---|---|
| T01 | restaurant | 145,433 | 47 | 145,420 | location.restaurant | 역 인근 식당 ≠ 역; 브랜드/운영사 별개 |
| T02 | other | 106,602 | 681 | 106,572 | unknown → 재조사 | 일괄 장소화 금지; QID 681 건은 상충 우선 (표본 50) |
| T03 | district | 83,897 | 1 | 82,924 | location.district | 행정구역과 상권 경계 |
| T04 | tourist_spot | 36,981 | 0 | 36,979 | tourist_spot / natural_feature / heritage_site | no_qid 전형; 근거는 tourapi/ngii provider |
| T05 | person | 31,378 | 14,504 | 0 | person.real / character.fictional | KDB 인물과 동명 교차; QID 공유 충돌 |
| T06 | accommodation | 26,569 | 25 | 26,567 | location.accommodation | merged 1·non_active 1 전수 |
| T07 | shopping | 25,509 | 70 | 25,509 | shopping / brand / company | 역 QID 오연결 2건(P2.05) |
| T08 | leisure_sports | 20,796 | 58 | 20,793 | sports_facility / stadium / event | 시설 vs 행사 |
| T09 | nature | 19,664 | 283 | 19,662 | natural_feature | 같은 산 이름 다른 지역 |
| T10 | heritage | 13,888 | 1,675 | 13,888 | heritage_site / artifact / artwork / concept | 고정·이동·무형 구분 |
| T11 | cultural_facility | 5,943 | 233 | 5,940 | cultural_facility | 시설 vs 운영 기관 |
| T12 | legal_dong | 4,912 | 0 | 4,912 | legal_dong | 행정동 동일시 금지; tmp_ld 비정답 |
| T13 | work | 3,822 | 73 | 3,822 | film/song/album/book/artwork/artifact | KDB work 와 동명 교차 |
| T14 | festival_event | 3,322 | 16 | 3,307 | festival_series/edition, performance | 행사장 ≠ 행사 → held_at |
| T15 | food | 2,774 | 0 | 0 | concept.food_name / product.food_product | 음식명 vs 브랜드 제품 |
| T16 | organization | 2,557 | 0 | 0 | organization / company / team / league | 상세형 확정 금지 |
| T17 | education | 1,122 | 0 | 0 | school / campus / company | 조직·캠퍼스·사업체 |
| T18 | transit | 911 | 0 | 0 | station / route / facility / company | 시설·노선·회사 |
| T19 | admin_region | 249 | 249 | 232 | admin_region | 전수 |

합계 536,329 = 9/12 관측 TDB 전체. 한정어(disambiguator) 보유율이 restaurant·district 등에서 ~100% 인 것은 D-04 `qualifier_ko` 가 흡수하되 **정체성 키로 쓰지 않는다**는 결정의 근거다.
person 유형만 한정어 0·QID 14,504 로 성격이 완전히 다르다 — KDB 인물과 QID 로만 교차하고 이름으로 교차하지 않는다.

## 4. 실행 위치와 남은 것

- M 사례 DB/API 실행: P1.04(M01~M08), P1.05(M09~M14). 하네스는 `internal/testdb`(KDB_TEST_DATABASE_URL 격리, 운영 DB 이름 거부, `Restored()` 는 명시된 일회용 복원본만).
- T 표본 추출·판정: P2.02 선매핑, P2.07 층화 검수. 판정 결과는 근거 provider·두 판정기 출력과 함께 기록한다.
- 이 문서는 fixture 정의다. 통과 주장이 아니다. 실제 인물 분류 품질의 정답으로 합성 fixture 를 쓰지 않는다(인수 기준 §2).
