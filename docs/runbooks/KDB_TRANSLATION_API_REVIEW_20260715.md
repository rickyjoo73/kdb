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

> **★오너 결정(07-15): gemma 우선안 기각.** LLM(gemma) 추측 문제를 피하려고 번역 API를 검토하는 것이므로 엔진은 **Google 번역(공식 Cloud Translation v3)**으로 확정. 전송 방식(단어 vs 문장)은 §6 테스트로 검증 → **유형 템플릿 문장(B) 방식** 채택.

## 5. 부수 발견 (별도 이슈)

- **"Stay This Way", "LIKE YOU BETTER" 등 실제 K-pop 곡이 `rejected(term)`로 오거부돼 있음.** 음차 매칭을 열어도 이 오거부에 막힘. 기존 게이트키퍼 오거부 리스크(06-17 23건 복구)와 동일 계열 — 음차 파일럿의 rejected 히트 리포트로 목록화해 별도 점검 필요.

## 6. 실측 테스트: 단어만 vs 문장(문맥) 단위 (2026-07-15, 오너 지시)

> 질문: 단어만 던질지, 문장으로 보낼지 — 문맥에 맞게 번역해 주는지 테스트 필요.
> 방법: 실제 miss 단어 33개(문맥보유 우선 25 + 음차류 8), 구글 번역(평가용 무키 gtx 엔드포인트, 1회성 소량. 정식 도입 시 공식 v3), ko→en, 3개 모드 비교.

### 모드별 결과
| 모드 | 방식 | 결과 |
|---|---|---|
| A. 단어만 | `거짓말` | ❌ **일반명사 오역**: 거짓말→"lie", 갈매기→"seagull", 곰탕→"oxtail soup", 광장→"square" |
| B. 유형 템플릿 문장 | `신곡 '거짓말'를 발표했다.` | ✅ **공식 제목으로 인식**: 거짓말→**Lies**(티아라 공식 영문명), 가시나→**Gashina**(선미 공식), 광화문연가→**Gwanghwamun Sonata**(뮤지컬 공식 영문명), 곰탕→Gomtang(로마자), 갈매기→The Seagull |
| C. 실제 기사 문장 | context_hint 원문 180자 | ⚠️ 번역 자체는 문맥 반영 잘 됨. 그러나 번역문에서 해당 단어 대응부를 재추출하는 **정렬 문제**(T-ara's 류 아포스트로피가 따옴표 추출 오염). v3 glossary/후처리 필요 |

- **문맥에 맞게 번역해 주는가 = 예.** 같은 단어도 문장 안에 넣으면 구글이 고유명사(작품 제목)로 인식하고, 유명 작품은 **학습된 공식 영문명**을 반환.
- **음차 복원은 8/8 완벽**: 스테이 디스 웨이→Stay This Way, 라이크 유 베터→Like You Better, 필 굿→Feel Good, 원 모어 타임→One More Time, 레프트 앤 라이트→Left and Right, 포레버 줄라이→Forever July, 마빈 게이→Marvin Gaye.
- **재매칭 실효성**: 후보 16개 역매칭 → **7개 DB 해결(44%)** = active 4 + candidate 1 + **rejected(오거부 자동 발견) 2**('Stay This Way', 'LIKE YOU BETTER' — §5의 오거부를 번역 재매칭이 자동으로 끄집어냄).

### 결론: 실전 방식 = B (유형 템플릿 문장)
- 단어만(A)은 금지 — 일반명사 오역. 기사 원문(C)은 추출 정렬 비용이 커서 차선.
- B는 문맥 효과를 얻으면서 따옴표 위치가 고정돼 추출이 깨끗하고, 문자수도 ~20자/단어로 절약(기사문 ~180자 대비 9배 절감).
- 문맥 보유 현황: miss의 52%가 has_context=true, 리서치큐 `context_hint`에 기사 문장 저장됨 → 템플릿에 유형+아티스트 힌트를 보강하는 하이브리드 가능(예: "티아라의 신곡 '거짓말'").

### 볼륨 (2026-07-15 실측)
- 백로그: miss 유니크 1,668개(한글 1,394), 핵심 타깃 제목류 ~500-650개.
- 신규 유입: ~240단어/일(제목류 ~70-90/일). 평균 6자/단어.
- B 모드 환산 시 월 ~15만 자 이내 → Google NMT 무료한도(50만 자)의 30% 미만.

## 7. ★구축 완료 (2026-07-15/16, 배포 `gtranslate-20260716-2`)

오너가 GCP Cloud Translation API 활성화 + 키 발급(`KDB_GTRANSLATE_KEY`, .env) → 정식 v2 로 전면 구축.

### 구현 (읽기 경로 전용 — 번역값 canonical/aliases 기록 없음)
- `internal/kdb/gtranslate.go` — v2 클라이언트. B방식 템플릿, 최외곽 따옴표 추출(소유격 아포스트로피 버그 실측 수정), 캐시(`kwave_kdb_translate_cache`, mig 0093, 실패도 기록=재과금 방지), **월 40만자 자체 상한**(무료 50만자의 80%).
- **오매칭 가드 `GTranslateSafeHit`** — 매칭 엔티티가 요청어와 다른 '한글 고유제목'이면 기각(실측: 광장→The Square 가 '더 스퀘어'(다른 영화)에 오매칭). 공백·구두점 변형은 동일 제목으로 통과(더쇼=더 쇼, 놀면 뭐하니=놀면 뭐하니?). 단위테스트 `gtranslate_test.go`.
- `/v1/prepare`·`/v1/lookup` miss 분기(api.go `translateRematch`): 한글 제목류만, active 정확일치 시 즉시 서빙. rejected 일치 시 `translate_hit_rejected` 플래그(오거부 점검용). prepare 는 요청당 fresh 호출 6회 예산(10s 타임아웃 보호, 캐시는 무제한).
- intake 증거수집(`gatherEvidence`) 3차: 번역 원형으로 Naver news 재검색(reason `auto_evidence_news_translated`).
- 백로그 배치: `kdb-app translate-backlog [N]`.

### 실측 결과 (백로그 619개 일괄, 07-16)
| 결과 | 건수 | 비고 |
|---|---|---|
| **active 히트(해결)** | **102 (16.5%)** | 라이드 오어 다이→Ride or Die, 뷰티풀 카오스→BEAUTIFUL CHAOS, 문을 열어→Open the Door, 놀면 뭐하니→Hangout with Yoo(공식영문명) 등 |
| 오거부 후보(rejected 일치) | 14 | 라이크 유 베터, 글로우 미 등 — `translate_hit_rejected` 플래그, 운영자 점검 대상 |
| candidate 일치 | 11 | 승급 drain 이 처리 |
| 동명 기각(가드 작동) | 4 | 광장→더 스퀘어 등 오염 차단 |
| 미해결 | 477 | 번역 무관 갭(발굴 대상) |
| 당월 문자 사용 | 11,403자 | 무료한도의 2.3% |

핫패스 실검증: prepare "라이드 오어 다이" → entity_id 반환(서빙), "광장" → 동명 기각 로그. 롤백: `.env` `KDB_APP_IMAGE=kdb-app:intake-autoverify-20260713-5` 복원 후 up -d (키만 빼려면 `KDB_GTRANSLATE_KEY=` 비우기 — 전체 비활성, 스키마 영향 없음).

### 남은 것
1. **오거부 후보 14건 운영자 점검** — `precheck_flags @> '{translate_hit_rejected}'` 조회.
2. **GCP 키 보안(오너)**: 키가 채팅에 노출됐음 — API 제한(Translation만)+서버 IP 제한 확인, 가능하면 회전 후 .env 교체.
3. 번역형의 aliases_ko 영구 추가는 여전히 범위 외(그라운딩 근거 필요 — §4 원칙).
