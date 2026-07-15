# 번역 API 도입 검토 (2026-07-15)

> 질문(오너): "정보가 없어서 못 찾는 것"에 구글 번역 LLM API 또는 다른 번역 API를 쓰는 것은 어떤가 — 조사 후 의견.
>
> **결론 요약: 쓰기 경로(현지표기 채움)에는 반대, 읽기 경로(질의 변환→재매칭)에는 찬성. 엔진은 유료 API 이전에 gemma 먼저. 비용은 쟁점이 아님(무료티어로 충분한 볼륨) — 쟁점은 "어느 경로에 쓰느냐".**

---

## 1. "못 찾는 것"의 실체 (2026-07-15 DB 실측)

### A. 요청 키워드 미해결 — 읽기 경로
- 커버리지(요청 유니크 키워드): active 4,117 / rejected 2,442 / candidate 1,724 / **unresolved 2,306**.
- unresolved 문자종 분포: **한글 1,987 / 영문(ASCII) 305 / 기타 14** → 일문·중문 키워드는 사실상 0. "외국어 키워드가 와서 못 찾는" 문제가 아님.
- miss(request_terms `preparing`+`new` 2,812건) 유형 1위 = **song_album 633건**. 샘플 확인 결과 상당수가 **영문 곡명의 한글 음차**:
  - "스테이 디스 웨이"(=Stay This Way), "라이크 유 베터"(=LIKE YOU BETTER), "필 굿", "원 모어 타임", "레프트 앤 라이트", "포레버 줄라이", "뷰티풀 카오스", "비 허" …
- 현재 매칭은 저장 문자열 exact/ILIKE + aliases 배열뿐(`internal/kdb/api.go` ListEntities). **질의를 변환(음차→원형, 외국어→한국어)해 재시도하는 단계가 아예 없음** — 구조적 구멍.

### B. 현지표기 채움 갭 — 쓰기 경로
- active 7,074 중 canonical 빈칸: en 1,259 / **ja 1,846 / zh 1,858** / vi 1,731.
- 병목감사(07-10) 기준: 8로케일×active = 53,680셀 중 **37%가 빈칸 또는 LLM합성(codex-fallback, 서빙 숨김)**.

## 2. 현행 코드/정책 현황

- 번역 API는 코드 어디에도 미사용 (Papago/DeepL/Google 전무. `internal/worker/translate`는 계획 문서에만 존재).
- LocalFill(`internal/kdb/localfill.go`)은 **검색 그라운딩 추출** 방식 — 프롬프트에 "지어내기·**번역·음역 금지**" 명문화. 원칙 = **"빈칸 > 틀린값"**.
- 음역 허용선(`KDB_ACQUIRE_ROADMAP_20260628.md`): 라틴계(vi/es/id/pt_br) 인명 로마자 복사 허용, zh↔zh_hant OpenCC 허용, **한글→zh 한자 합성 금지**(1:다 대응, 오염 최대 리스크), 곡명 음역 금지.
- 결정적 변환기 보유: `romanize.go`(라틴 로케일 복사), `opencc_convert.go`·`zhvariant/`(간번체), `aliasmatch/`(한글 약자·오타, 한국어 전용).

## 3. 번역 API 비용/조건 조사 (2026-07)

| 옵션 | 무료한도 | 단가 | 비고 |
|---|---|---|---|
| Google NMT (v2/v3) | 월 50만자 영구 | $20/M자 | GCP 결제계정(카드) 필요 |
| Google 번역 LLM | **없음** | $10/M in + $10/M out | LLM/Adaptive는 프리티어 제외 |
| Papago (NCP) | 없음 | 100만자 단위 **올림** 과금 | 소량이어도 월 최소단위 결제 |
| DeepL API Free | 월 50만자 | — | 한·일·중 품질 최약 평가(비교 리뷰 다수) |
| **gemma (자체 3대)** | 무제한·무료 | 0 | codex-free 정책 부합, 이미 LocalFill 두뇌 |

- 볼륨 실측: miss ~600건/일 × ~20자 ≈ **월 36만자** → 어떤 무료티어로도 충분. **비용이 쟁점이 아님.**
- 출처: [Google 공식 요금](https://cloud.google.com/translate/pricing) · [2026 정리](https://apispine.com/google-cloud-translation/pricing) · [Papago NCP](https://ncloud.com/product/aiService/papagoTranslation) · [Papago 과금기준](https://api.ncloud-docs.com/docs/ai-naver-papagonmt) · [DeepL plans](https://support.deepl.com/hc/en-us/articles/360021200939-DeepL-API-plans) · [무료티어 비교](https://www.bluente.com/blog/best-translation-apis-free-tiers)

## 4. 결론

1. **쓰기 경로(canonical_ja/zh 채움)에 번역 API: 반대.**
   - "추측=빈칸" 오너 게이트·"빈칸>틀린값" 원칙 위반.
   - 중국어는 본명 한자(이지은→李知恩)를 번역기가 원리적으로 알 수 없음 → 음역 한자 = 오염.
   - 이미 codex 합성값 19,841셀을 숨기고 드레인하는 중 — 새 합성 소스 추가는 역행.
2. **읽기 경로(질의 변환→재매칭): 찬성 — 진짜 레버.**
   - 음차 한글→원형 복원 후 재매칭. **틀려도 그냥 미스일 뿐 DB 오염 없음** → 안전.
   - miss 최대 유형(song_album 633)의 상당분 + 향후 ja/zh 키워드 유입 대비.
3. **엔진은 gemma 먼저, 외부 API는 폴백 옵션.**
   - 무료·무키·codex-free 정책 부합. 음차↔원형 복원은 26B에 쉬운 작업.
   - gemma 실측 품질 미달 시에만 Google NMT 프리티어(카드 필요 — 오너 결정) 검토. Papago는 무료티어 부재로 비추천, DeepL은 한·일·중 품질로 비추천.

## 4.5 최종 추천 — "가장 정확하고 신뢰할 수 있는 것"

1. **우리 용도(읽기 경로 질의 변환) 1순위 = 자체 gemma.** 비용 0·키 불요·기존 정책 부합. 음차 복원("스테이 디스 웨이"→Stay This Way)은 짧은 고정 패턴 작업이라 26B로 충분할 것으로 판단 — 단 정확도는 파일럿으로 실측 후 확정.
2. **외부 번역 API를 굳이 하나 고른다면 = Google Cloud Translation v3 (NMT).**
   - 한국어·K-엔티티 고유명사(인명·곡명) 커버리지가 상용 API 중 최상 — 학습 데이터 규모와 비교 리뷰 종합에서 한국어 기준 일관되게 1위권.
   - GA 서비스로 SLA 보장, 월 50만자 영구 무료(우리 볼륨 월 ~36만자 커버). 단 GCP 결제계정(카드) 등록 필요.
   - 같은 구글이라도 "번역 LLM"($10/M in+out, 무료티어 없음)은 긴 문맥 번역용이라 짧은 고유명사 변환에는 이점 없이 비용만 발생 → **NMT v3 권장**.
3. **Papago**: ko↔ja 자연문에는 강하지만 무료티어가 없고(100만자 올림 과금) 고유명사에서 Google 대비 이점 없음 → 비추천. **DeepL**: 한·일·중 품질 최약 평가 → 비추천.

즉, **"가장 정확·신뢰" = Google NMT v3, "우리에게 가장 맞는 것" = gemma 파일럿 → 미달 시 Google NMT 폴백.**

## 5. 부수 발견 (별도 이슈)

- **"Stay This Way", "LIKE YOU BETTER" 등 실제 K-pop 곡이 `rejected(term)`로 오거부돼 있음.** 음차 매칭을 열어도 이 오거부에 막힘. 기존 게이트키퍼 오거부 리스크(06-17 23건 복구)와 동일 계열 — 음차 파일럿의 rejected 히트 리포트로 목록화해 별도 점검 필요.

## 6. 제안: 파일럿 (미실행, 승인 대기)

1. **실측 스크립트 1개** (DB 쓰기 없음, 리포트만):
   - miss 한글 키워드(song_album/drama/show ~200개, `kwave_kdb_request_terms` preparing/new)를 gemma로 "음차→원형 후보" 변환(기존 `KDB_GEMMA_BASE_URL` 재사용).
   - 변환형으로 `kwave_entities` 재매칭 → 변환 성공률 / active·candidate·rejected 히트 수 리포트.
2. 히트율 유의(>10%) 시 정식 편입: `/v1/prepare`·`/v1/lookup` 미스 시 gemma 변환형 1회 재매칭 + intake 증거수집(`intake_autoverify.go` gatherEvidence) 검색 쿼리에 변환형 추가. 변환형의 aliases_ko 영구 추가는 검색 그라운딩 근거 있을 때만(기존 LocalFill 패턴 재사용).
3. rejected 오거부 목록 별도 리포트.
