# 소비자 신고 3건에 대한 답 — 2026-09-23

같은 날 세 곳이 각자 실측 신고를 보냈다.

| 보낸 곳 | 문서 | 표본 |
|---|---|---|
| global.nbntv | `aiinplanet/global.nbntv.co.kr` `docs/KDB_API_ISSUES_20260923.md` | 계약 조항별 재현 호출 |
| PressLocale | Claude Docs 「KDB API 확인 결과 — PressLocale」 | 이름 100개 · `/v1/my/changes` 136건 |
| 글로벌 미디어파인 | `rickyjoo73/meidafine-global` `docs/KDB_API_ISSUES_2026-09-23.md` | 인물 200명(성공 181) · 로케일 1,440칸 · 정정신고 4건 |

항목은 열여섯인데 **뿌리는 둘**이다.

**① 조회가 «찾았다»고 거짓말을 했다.** 조회 SQL 은 `ILIKE '%질의%'` 로 부분일치를 함께
걷어 온다(표기 변형·별칭을 놓치지 않으려고 그렇게 짰다). 그런데 `status` 는 «행이 하나라도
있으면 found» 였다. 그래서 «부분일치밖에 없다»와 «정확히 그 대상이다»가 같은 한 글자로
나갔다. 세 곳이 각자 다른 이름으로 같은 것을 신고했다 — 카카오 · 서울시 · 채영.

★안쪽은 이미 «없다»로 처리하고 있었다. `hasNormalizedHit` 이 false 면 발굴 큐에 넣는다.
**서빙만 거짓말을 한 자리다.**

**② 소비자가 보낸 것을 조용히 삼켰다.** 유형 오타(`persson`)와 미상 표시(`unknown`)는
200 miss 로, `ko` 없는 묶음 항목은 결과에서 통째로 빠져서, 정정신고 기각은 422 로 나갔다.
셋 다 «소비자가 무엇을 잘못 보냈는지» 를 말하지 않는다. 그래서 소비자는 보유한 대상을
새로 등록 요청하고(박보검), 답을 한 칸씩 밀려 짝짓고, 같은 신고를 10회 재전송했다.

---

## 고친 것 (브랜치 `fix/consumer-api-contract-20260923`)

| # | 신고 | 고친 내용 |
|---|---|---|
| 1 | 카카오·서울시가 `found` | `matches` 는 **이름이 같은 대상만**. 부분일치는 `related` 로 분리(`internal/kdbapi/lookup_contract.go`) |
| 2 | 단건에 `ambiguous` 없음 | 단건·묶음이 같은 함수로 status 를 정한다(0 miss · 1 found · 2+ ambiguous) |
| 3 | `persson` 오타가 200 miss | 단건 `400 invalid_type`, 묶음은 항목 단위 통지 |
| 4 | `unknown` 이 필터로 작동 | 미상 표시(`unknown`·`term`)는 유형 필터에서 제외 |
| 5 | 묶음이 잘못된 항목을 조용히 탈락 | **보낸 항목 수 = 받는 항목 수.** 그 자리에 `error{code,message}` |
| 6 | 정정신고 기각이 422 | `rejected`·`auto_applied` → **200**. 판정은 성공이다 |
| 7 | `resolution` 중첩 누적 | 사유는 원문 그대로, 재사용은 `reused`·`reported_count`·`first_judged_at` |
| 8 | 문서에 없는 provenance 라벨 24% | 등급표를 **코드에서 생성**(`provenance_catalog.go`) + 드리프트 시험. `verified_only` 통과 여부를 라벨마다 명시 |
| 9 | 한도 잔량을 알 수 없음 | `X-RateLimit-Limit`·`Remaining`·`Reset` |
| 10 | `reask` 에 `type` 이 없음 | 소비자가 보낸 값을 그대로 돌려준다(CTE 에 있는데 안 쓰고 있었다) |
| 11 | 김신록 `zh` = `基姆·申-洛克` | 간체·번체 중 **음차가 아닌 쪽**을 변환 기준으로. 판별은 가운뎃점·붙임표(`zhvariant.PreferNativeHan`) |
| 12 | prepare 와 preparations 의 값이 다름 | 결함이 아니라 다른 것을 말하는 두 필드 — §6-2 에 구분 명시 |

## 답만 하고 코드는 안 고친 것

**제안값이 정식 표기칸에 들어간다(global.nbntv 1).** 코드에 그 경로가 없다.
`/v1/prepare` 의 `suggestions` 는 `kwave_kdb_suggested_names` 에만 적히고
(`prepare_suggestions.go`), 그 표를 읽는 곳은 `SupersedeSuggestions` 하나인데 방향이
반대다 — 우리 쪽에 자격 있는 값이 생기면 **제안을 서빙에서 물린다.** LLM 채움 프롬프트
(`makeFillInput`)에도 제안은 들어가지 않는다.

관측된 `ラムダ256`·`Lambda256` 은 **잠정 채움 레인**(`llm-provisional`, prio 9)이 만든
값으로 보인다. 제안을 보낸 대상에서만 그 라벨이 보인 것은, 그 레인이 **prepare 로 갓
들어온 대상**에서 돌기 때문이다(기존 대상은 이미 다른 레인이 채웠다). 즉 상관이지 인과가
아니다 — 다만 **원장 대조로 확정하지는 못했다**(아래 «막힌 것»).

## 막힌 것 — 원장 접근이 필요하다

| 항목 | 필요한 것 |
|---|---|
| 사당귀(K0003545) · 사장님 귀는 당나귀 귀(K0006914) 중복 병합 | DB 쓰기. 줄임말은 별칭으로 |
| 삼성전자 · 네이버 · 기획재정부 미보유 (문서가 대표 예시로 든 이름) | 발굴 레인 실행 |
| 케이뱅크(K0023012) `brand_place` → `company` 교정, retrace 일관성 | DB 쓰기 + 소비자 type 정정 경로 설계 |
| `wikidata-label` 인데 실제 라벨과 다름 13% | **가설:** per-locale source 가 빈 레거시 행을, 엔티티 `source_urls` 에 wikidata 가 있다는 이유로 `wikidata-label` 로 라벨링하는 폴백(`localeProvenanceLabel` 의 `case "":`). 그 값들은 `verified_only` 도 통과한다. 고치면 검증 통과 칸이 줄어드니 **규모를 먼저 재야 한다** |
| 제안값 ↔ 잠정 채움 인과 확정 | `kwave_kdb_suggested_names` 와 `canonical_*_source`·`updated_at` 대조 |

2026-09-23 시점 `ssh aiin@atikar.com -p 38383` 이 로컬 키 4개 전부 `Permission denied
(publickey)` 다. 읽기 키도 로컬에 없어 운영 API 로 전후 대조를 못 했다.
