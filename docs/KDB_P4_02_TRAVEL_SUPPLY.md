# P4.02 — 여행 공급 경로와 남은 기존 공급 의존성

2026-09-14 · 운영 aiin23 · 전부 실측

계획서의 P4.02 는 이렇게 적혀 있다:

> 실제 필요한 여행 출처/표기 규칙만 공통 보충에 연결하고 남은 기존 공급 의존성을
> 목록화한다. TDB 레인/에이전트 시스템은 복제하지 않는다.

재어 보니 **연결할 여행 출처가 없었다.** 표기는 이미 다 들어와 있고, 막고 있던 것은
출처가 아니라 대상 상태였다. 아래가 그 실측과, 실측이 시킨 일이다.

---

## 1. 여행 표기는 이미 원장에 있다

범위 안 흡수분(`write_owner='native' AND status<>'rejected'`) 기준, 공통 계약의
표기 조건(canonical · recorded · verified · 승인 정책 · https 근거)을 통과하는 표기를
**정확히 하나** 가진 대상 수다(둘 이상이면 `ambiguous` 인데 §2 대로 0건이다):

| 로케일 | 공급 가능한 대상 |
|---|---|
| ko | 412,549 |
| en | 341,796 |
| ja | 303,379 |
| zh-Hans | 300,024 |
| zh-Hant | 278,080 |
| es / fr / de | 13,576 / 10,505 / 8,614 |
| id / ru / vi | 2,866 / 2,655 / 1,447 |

출처는 `aihub_tour` 1,605,881 · `ngii_gazetteer` 348,745 · `tourapi_*` 149,421 ·
`heritage_khs` 70,962 · `seoul_dict` 41,255 이고, **전부 승인된 원천 정책**
(`status='approved'`, `name_export_allowed=true`)을 갖고 있다. P4.00 이 올려 둔 것이다.

## 2. 표기 규칙(출처 우선순위)은 필요 없다

한 대상·한 로케일에 canonical recorded verified 표기가 **둘 이상인 경우: 0건.**
운영 DB 에서 두 가지 방법으로 따로 쟀다(근거 조건 포함 / 근거 조건 없이). 둘 다 0 이다.

즉 `aihub_tour` 와 `tourapi_*` 와 `ngii_gazetteer` 가 같은 대상의 같은 로케일을
서로 다르게 말하는 자리가 없다. 우선순위 규칙을 만들 이유가 없다 —
**없는 충돌을 위해 규칙을 만들면 그 규칙이 나중에 틀린 답을 만든다.**

## 3. 그런데 공통 경로로 공급되는 대상은 0건이었다

막은 것은 대상 상태 두 가지이고, 둘은 DB 가 의존관계로 강제한다(0116/0118 트리거):

```
흡수분 537,841  → status='candidate'            → policy_blocked
                   정체성 근거 export_allowed=false
기존 원장 12,688 → write_owner='kdb'             → unverified
```

트리거: `activation requires verified reusable identity evidence`.
정체성 근거를 재사용 가능으로 올리지 않으면 `active` 로 올릴 수 없다.

### ★그 근거를 올리는 코드 경로가 wikidata 만 받는다

`resolution_approve.go` 의 `ApproveResearch` 가 유일한 활성화 경로인데,
QID·`wikidata-label` 표기를 전제한다. 흡수분의 정체성 근거는 `provider='tdb'`
(자체 ID 관측)라 그 문으로 들어가지 못한다.

이는 이 계획 자신의 원칙과 어긋난다:

- 식별 계약 I03 — "wikidata QID 는 보조다. **자체 ID 가 주 앵커다.**"
- `p3_expand.sql` 주석 — "외부 식별자가 아니라 **우리 원본의 관측**이 정체성 근거다."

**여행 출처를 아무리 연결해도 이 문이 닫혀 있으면 한 건도 공급되지 않는다.**
그래서 P4.02 가 실제로 필요로 한 것은 출처 연결이 아니라 이 문이었다.

→ [`docs/p4/activate_absorbed_supply.sql`](p4/activate_absorbed_supply.sql)

## 4. 왜 일괄로 열지 않았는가 — 실수요가 금했다

3개월치 소비자 요청 실측(고유 표제어 6,530개, 요청 99,360건):

여행 유형과 겹치는 요청은 `tourist_spot` 2 · `heritage_site` 6 ·
`cultural_facility` 6 뿐이고, 그 겹침의 대부분이 **동명 함정**이었다.

| 요청 표제어 | 소비자가 보낸 type | 흡수분이 들고 있는 것 |
|---|---|---|
| 아일랜드 | `drama` | 충남 태안군 관광지, zh-Hans = **爱尔兰**(아일랜드 나라) |
| 돈 | `movie` | heritage_site |
| JYP엔터테인먼트 | — | `location.cultural_facility` (건물) |
| 신동 | — | `location.legal_dong` 6건 |
| 대치·태산·미도·구천·이현 | — | `location.natural_feature` (하천·산) |
| 신데렐라 / 쉼 | — | 펜션 / 펜션(en=**Swim**) |
| 금강 | `term` | 장수군 지명, zh-Hans = **琴江** (금강이 아니다) |

일괄로 열면 뉴스 표제어가 식당·펜션 이름으로 답해질 자리를 만든다.

**규칙만으로는 대상을 가릴 수 없다는 것도 실측으로 확인했다.** "요청됨 + 유형 확정 +
외국어 표기 있음 + 활성 동명 없음"이라는 규칙은 302건을 고르는데, 그 안에
`아일랜드`·`돈`·`문재`·`상원`(법정동 9건)이 그대로 들어 있다. 규칙은 선별을
**돕는** 것이지 대신하지 못한다. 그래서 스크립트는 ID 목록을 인자로 받고,
고르는 일을 자기 안에 두지 않는다.

### 동명 함정 방어는 두 겹이다

1. 스크립트: 같은 ko 이름의 **활성 기존 원장 대상**이 있으면 올리지 않는다.
2. 공통 계약 자체: `commonCandidates` 는 발견 전용이고, 소비자가 `entity_id` 를
   명시해 묶지 않으면 `identity='ambiguous'` 다. **이름만으로는 답이 나가지 않는다.**

## 5. 실제로 연 것 — 1건

실수요·유형힌트·표기를 하나씩 확인해 **소비자 요청과 대상이 실제로 일치하는 것**은
하나였다.

```
서울패션위크  d93faa2a-63cf-48a0-8d8d-f60304829786
  소비자 type 힌트 : event_tour
  원장 유형        : event.festival_series
  candidate → active (revision 2), 정체성 근거 export_allowed false → true
```

공통 경로 실제 응답(운영, `/v1/preparations`):

```
ja       ready        ソウルファッションウィーク   src=seoul_dict
zh-Hans  ready        首尔时装周                  src=seoul_dict
en       unverified   -   (있는 en 은 rule 이 만든 Seoulpaesyeonwikeu — 생성값은 공급하지 않는다)
vi       no_evidence  -
```

`policy_proof` 는 정체성 정책(tdb)과 표기 정책(seoul_dict)을 revision 과 함께 싣는다.
`state='ready'` 인데 policy_proof 가 빈 행: **0건**(S03).

**공통 원장(`kentity_entities`)이 실제 값을 공급한 첫 사례다.** 그전까지 0건이었다.

정확히 해 둔다: `kentity_locale_readiness` 에 `ready` 675건이 이미 있지만 전부
`native-evidence-v1` 정책이고, **그 정책은 `kwave_entities`(기존 원장)를 읽는다**
(`store.go` 의 후보 조회·잠금·읽기 세 곳 전부). 공통 원장을 읽는
`common-reviewed-names-v1` 의 `ready` 는 **2건 — 위의 것뿐이다.**

## 6. 공통 보충(자동 채움)에 여행 출처를 연결하지 않은 이유

`vi` 가 `no_evidence` 로 남았는데 **보충 작업이 큐에 들어가지 않았다.** 맞는 동작이다.

`commonFillEligible` 은 wikidata 앵커가 **정확히 하나**일 것을 요구한다. 흡수분에는
앵커가 없다(전체 원장에서 verified wikidata 외부 ID 보유 대상 243건). 그래서
`common-anchored-fill-v1` 작업은 흡수분에 대해 만들어지지 않는다.

여기에 여행 출처를 붙이는 방법은 둘뿐인데 둘 다 하지 않는다:

| 방법 | 왜 안 하는가 |
|---|---|
| TDB DB 재조회 | **가져올 것이 없다.** TDB 전량 536,322 흡수, 누락 0(G3 인수). 이미 다 들어와 있다. |
| tourapi 실시간 조회 | P4.02 가 명시적으로 금지한 **"TDB 레인/에이전트 시스템 복제"** 다. |

즉 공통 보충에 연결할 여행 출처는 **없다**. 공급은 이미 흡수된 표기로 하고,
문을 여는 것은 §3 의 활성화 경로다.

## 7. 실측이 드러낸 것 — 흡수분의 값어치는 장소가 아니라 사람이다

요청 표제어와 겹치는 흡수분 1,814건의 유형:

```
person.real              1,516   ← 83.6%
location.restaurant        156
location.district          131
location.accommodation      34
… (여행 유형 tourist_spot 2 · heritage_site 6 · cultural_facility 6)
```

흡수분에는 `person.real` 이 **31,377건** 있고, 표기가 wikidata recorded 다:

```
곽튜브   en KwakTube      zh-Hans 郭俊彬
황재균   en Hwang Jae-gyun ja 黄載鈞    zh-Hans 黄载均
도겸     en Lee Seok-min   ja ドギョム   zh-Hans 李硕珉
전소연   en Jeon Soyeon    ja ソヨン     zh-Hans 田小娟
```

**KDB 가 존재하는 이유가 바로 이것들이다.** 다만 인물 원천은 P4.03~P4.06 의 몫이고,
개별 검수 없이 31,377건을 여는 것은 §4 가 금한 일과 같은 종류다. 여기 기록만 남긴다.

주의: 표본에서 `하나`(person) 한 건이 **신보라의 표기**(en Shin Bo-ra · ja シン・ボラ ·
zh 辛衍抒)를 ko `하나` 아래 달고 있었다. 동일성 판정(P5)이 볼 자리다.

---

## 8. 남은 기존 공급 의존성 (P4.02 두 번째 요구)

### 8.1 소비자 트래픽은 아직 99.97% 가 기존 원장이다

| 계층 | 요청 | 비중 |
|---|---|---|
| 기존 `kwave_*` | 99,329 | **99.97%** |
| 공통 `kentity_*` | 31 | 0.03% |

경로별(최근 30일): `/v1/lookup/bulk` 10,636 · `/v1/prepare` 6,409 ·
`/v1/entities/match` 7,119 · `/v1/corrections` 1,669 · `/v1/lookup` 305 ·
`/v1/kentity/entities` 11 · `/v1/preparations` 8.

소비자 둘(`bdf54270…`, `19f59fa9…`)이 트래픽의 거의 전부다. **둘 다 공통 경로를
쓰지 않는다.** 공통 원장으로 옮기려면 소비자 쪽 변경이 필요하다 — 아직 안 했다.

### 8.2 기존 보충 레인이 살아 있고, 그 산출물은 공통 계약이 안 받는다

`kwave_entities` 활성 12,688건의 en 출처:

```
gtranslate 4,144 · wikidata-label 3,280 · tmdb 1,650 · correction-verified 701 ·
codex-fallback 621 · musicbrainz 550 · local-usage 527 · romanization 414 · kofic 110
```

`gtranslate`·`codex-fallback`·`romanization` 은 **생성값**이다. 공통 계약은
`form='recorded'` 만 공급하므로 이 값들은 공통 경로로 나갈 수 없다.
그래서 기존 원장 12,688건은 공통 경로에서 전부 `unverified` 다 —
`write_owner='kdb'` 라 아예 그 앞에서 막히기도 한다.

kdb-app 은 지금도 이 레인을 돌린다: `KDB_LOCALFILL_ENABLED=1` ·
`KDB_LLM_FILL=gemma` · `KDB_LOCALFILL_ESCALATE=1` · `KDB_LOCALFILL_FANOUT=3`.

### 8.3 TDB 레인이 아직 돌고 있다

`tdb-worker` 가 자기 DB 에 대해 `watch` · `fresh_fill` · 불변식 검사를 계속 한다.
2026-09-14 05:41 로그:

```
불변식 위반: block 0 · warn 5 · 되돌림 0
gaps: ja 128,966(99.8%) en 128,941(99.8%) ru 128,926(99.7%)
      zh-Hans 72,794(56.3%) zh-Hant 65,460(50.6%) …
```

`tdb-api` 도 살아 있고 `tdb.aiinplanet.com` 으로 노출된다.
청사진 10.3 은 "안정화 이후 실사용 의존성 해소 후 구 writer 종료"라고 적었다.
**아직 해소되지 않았다.** 끄는 것은 별도 결정이다(원본 폐기는 운영자 승인 사항).

### 8.4 공통 계약이 못 받는 로케일에 표기가 있다

`commonLocales` 는 ko·en·ja·zh(3종)·vi·id·es·pt(2종) 만 받는다. 그런데 흡수분에
공급 조건을 통과하는 표기가 있는 로케일:

```
fr 10,505 · de 8,614 · ru 2,655
```

요청하면 `unsupported locale "fr"` 로 400 이 난다(실측). 표기는 있는데 계약에 칸이
없다. 실수요가 생기면 `commonLocales` 를 늘리는 일이 남아 있다 — 지금은 수요가 없다.

### 8.5 기존 준비 레인(`native-evidence-v1`)

`kentity_preparations` 243건이 이 정책이고, 이것도 wikidata 앵커 전용이다.
그리고 **이 정책은 이름이 `kentity_*` 표에 살 뿐 실제로는 `kwave_entities` 를 읽는다.**
소비자가 오늘 받는 `ready` 675건은 전부 기존 원장 값이다 — 표 이름만 보고 공통
원장으로 옮겨졌다고 읽으면 안 된다.
보충 작업 큐 `kentity_locale_fill_jobs` 에는 이 정책의 `no_evidence` 22건만 있다
(`anchored source has no admissible requested-locale label`).
`common-anchored-fill-v1` 작업은 **0건** — §6 의 이유로 만들어지지 않는다.

---

## 9. 다음에 할 수 있는 일 (이 문서가 정하지 않는 것)

- 흡수분 `person.real` 31,377건의 개별 검수·활성화 → **P4.03~P4.06**
- `하나` 같은 표기 혼입의 동일성 판정 → **P5**
- 소비자 둘을 공통 경로로 옮기기 → 소비자 쪽 작업이 필요하다
- `tdb-worker` 종료 → 8.3 의 의존성 해소가 먼저다
- `commonLocales` 에 fr/de/ru 추가 → 실수요가 먼저다
