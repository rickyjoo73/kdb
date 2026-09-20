# 카테고리별 원천 조사 — 2026-09-20

계기: 번역 쪽에서 다섯 건이 한꺼번에 틀렸다. 전부 같은 결함이다.

```
전어     zh 鲭鱼(고등어)       실제 窩斑鰶
광어     zh 平鱼(병어)         실제 扁口鱼   (표준 한국명 넙치)
우럭     zh 石首鱼(조기)       실제 許氏平鮋 (표준 한국명 조피볼락)
도라지   es bellota(도토리)    실제 Platycodon grandiflorus
거위벌레 ja ゴキブリ(바퀴벌레) 실제 オトシブミ科
```

**기계번역은 뜻을 옮긴다.** 종명을 뜻으로 옮기면 다른 종이 나온다. 소리로 옮겨도 그
언어의 표기가 아니다. 지금 소비자에게 주는 지시는 `transliterate`(소리)와
`translate_title`(뜻) 둘뿐이라, 이 셋째 부류를 담을 칸이 없다.

아래는 그 칸을 메울 원천을 **실제로 찔러 본 결과**다. 문서에서 읽은 것이 아니라
2026-09-20 에 서버23 에서 호출해 응답을 받아 적었다.

---

## 1. 지금 쓸 수 있는 것 (키 없음 · 실증 완료)

### ① Wikidata P225(학명) + 각 언어판 위키 표제 — **채택, 구현함**

| 항목 | 실측 |
|---|---|
| 키 | 불필요 |
| 지연 | 낱말당 약 450ms (ko.wikipedia 1회 + wikidata 2회) |
| 게이트 | `P225`(학명) 유무. 없으면 분류군이 아니므로 손대지 않는다 |
| 통칭 해소 | ko.wikipedia **리다이렉트**가 이미 안다 — 광어→넙치, 우럭→조피볼락 |

끝단 결과(실행 출력 그대로):

```
우럭   → 표준명 조피볼락 | Q1985193 | Sebastes schlegelii
         ja クロソイ · zh_hant 許氏平鮋 · en/es/vi/pt_br Sebastes schlegelii
광어   → 표준명 넙치     | Q1322753 | Paralichthys olivaceus
         ja ヒラメ · zh_hant 扁口鱼 · en Olive flounder · vi Cá bơn vỉ
거위벌레 → Q542516 | Attelabidae
         ja オトシブミ科 · zh_hant 卷叶象鼻虫科 · vi Họ Bọ cuốn lá
이재명 · 오징어 게임 → 막힘(P225 없음) ← 게이트가 의도대로 작동
```

구현: `internal/kdb/taxon.go`. 값 고르는 규칙은 `TaxonLocaleNames` 하나에 모았다.

- 언어판 **문서 제목**이 위키데이터 라벨을 이긴다(9/19 zhwiki 레인과 같은 판단).
- ja/zh/zh_hant 칸의 **라틴 학명은 버린다** — 그건 표기가 아니라 결손이다.
- es/pt_br/id 의 학명은 **남긴다** — 그 언어판 표제가 실제로 학명이다(도라지 eswiki).
- 한글이 든 값, 요청어와 같은 값은 버린다.

★함정 하나를 실물로 확인했다. `wikidata.BatchClaims` 는 `wikibase-entityid` 만 뽑는다.
학명은 `string` 이라 **한 건도 안 나온다.** 그 문으로 붙였다가 끝단을 돌렸더니 다섯 건
전부 "분류군이 아니다"로 막혔다 — 값이 있는데 없다고 답하는 조용한 0건이다.
`BatchStringClaims` 를 따로 두고 시험으로 박았다.

### ② GBIF — 교차확인용 2차 출처

```
GET https://api.gbif.org/v1/species/match?name=Sebastes schlegelii   → usageKey 2335435
GET https://api.gbif.org/v1/species/2335435/vernacularNames
    jpn = クロソイ · jpn = Kurosoi · eng = Korean rockfish · eng = Schlegel's rockfish
```

키 불필요. 위키와 **독립된** 출처라 합의 판정에 쓸 수 있다 — 위키 표제와 GBIF 통칭이
같으면 `media-consensus` 급 확신을 근거 있게 말할 수 있다. 지금은 조사만 했고 붙이지 않았다.

주의: `/species/search` 응답의 `vernacularNames` 는 비어 있다. **backbone usageKey 로
다시 불러야** 나온다. 첫 시도에서 0건을 받고 "없다"고 적을 뻔했다.

---

## 2. 무료지만 **사장님 손이 필요한 것**

| 원천 | 상태 | 무엇이 필요한가 |
|---|---|---|
| **국가법령정보 OPEN API**(law.go.kr DRF) | 호출은 되지만 `"사용자 정보 검증에 실패하였습니다"` | OC 계정 등록 + **서버 IP/도메인 등록**. 「정부조직 영어명칭에 관한 규칙」 전문을 받아 부·처·청 영문명을 고시 근거로 실을 수 있다 |
| **공공데이터포털**(data.go.kr) | `apis.data.go.kr` 400(키 없음) | 인증키. 학교 공식 영문 교명·공공기관 목록 |
| **Spotify** | 미등록 | Client ID/Secret. song_album 미해소 326낱말/주 468요청의 직접 해법 |

셋 다 **무료 등록**이고, 없으면 해당 카테고리는 계속 기계번역이 메운다.

---

## 3. 더 조사해야 하는 것

| 원천 | 실측 | 다음 |
|---|---|---|
| 한식진흥원(hansik.or.kr) | 루트 200 · `/kr/board/foodDic` **404** | 음식명 외국어 표기(en/ja/zh)의 실제 경로를 다시 찾는다. 「한식메뉴 외국어 표기법」은 국립국어원 감수본이라 권위 등급이 높다 |
| 국립생물자원관(species.nibr.go.kr) | 검색 페이지 200 | 공개 API 유무 확인. 있으면 **한국명↔학명**을 국가 목록으로 받아 위키 의존을 줄인다 |
| 국립국어원(korean.go.kr) | 200 | 「공공 용어의 외국어 번역 및 표기 지침」 — 카테고리 규칙의 상위 근거 |
| 국토지리정보원 | 미조사 | 지명 로마자 표기 |

---

## 4. 카테고리 → 원천 배치 (목표)

| 카테고리 | 1차 원천 | 2차(교차확인) | `fill_hint` |
|---|---|---|---|
| 생물 종·분류군 | Wikidata P225 + 언어판 표제 ✅ | GBIF vernacularNames | `use_standard_name` |
| 음식·식재료 | 한식진흥원/국립국어원 (조사 중) | 언어판 위키 | `use_standard_name` |
| 정부조직·공공기관 | 국가법령정보(고시) | 기관 공식 영문 사이트 | `use_official_name` |
| 학교 | data.go.kr + 대학 공식 영문 교명 | — | `use_official_name` |
| 지명 | 국토지리정보원 | 관광공사 다국어 | `transliterate` |
| 인물·그룹·캐릭터 | 기존 경로 | — | `transliterate` |
| 작품·행사 | 기존 경로(TMDb·iTunes·OTT…) | — | `translate_title` |

`use_standard_name` 은 **"소리도 뜻도 아니다 — 그 언어의 표준 통용명을 써라. 없으면
학명을 그대로 둬라"** 는 지시다. 지금 없는 칸이고, 이것이 없으면 소비자는 빈칸을 받고
기계번역으로 메운다. 그게 이 문서의 계기가 된 다섯 건이다.

---

## 5. 남은 일

- [ ] `fill_hint` 에 `use_standard_name` 추가 (`internal/kdbapi/absent_reason.go`, 소비자 문서)
- [ ] 종명·음식명 칸에 **기계번역 금지** — 권위 원천만 허용, 없으면 빈칸
- [ ] 통칭→표준명 별칭을 원장에 싣는 경로(요청어가 별칭이 되어야 다음 요청이 만난다)
- [ ] GBIF 합의 판정 붙이기
- [ ] 범위 기록: 종명·음식명은 기획안 `concept` 정의(*"일반 명사 사전을 넣지 않는다"*)를
      건드린다. **기사에 실제로 등장했고 권위 원천이 있는 것만** 이라는 좁은 형태로
      `KDB_INTEGRATION_TODO.md` §변경 통제에 남긴다.
