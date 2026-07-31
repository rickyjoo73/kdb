# KDB 핸드오프 41차 (2026-07-31) — 병목 전수진단 + 커버리지 배선

한 세션에서 **같은 결함을 네 번** 만났다. 개별 수리 6건을 하고, 마지막에 그 결함을
자동으로 드러내는 계측을 깔았다. 이번 문서의 핵심은 개별 수리가 아니라 §6 이다.

배포 8회(spinfix-1 → coverage-8), 커밋 7건 전부 푸시. 현재 `coverage-20260731-8`.

---

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s -m 5 http://127.0.0.1:9100/v1/health   # version=coverage-20260731-8

# ★최우선 — 이번에 깐 커버리지 계측부터 본다. 어디가 막혔는지 여기 다 나온다.
# 기동 20초 후 1회 + 이후 6h 주기로 찍힌다.
docker logs kdb-app 2>&1 | grep backlog-watch | tail -6
#  ⚠ 가 붙은 줄 = 그 조건을 담당하는 레인이 자기 백로그를 못 덮고 있다는 뜻.
#  oldest_days 가 세션마다 커지면 그 레인은 사실상 죽어 있다.

# 로그가 비었으면(방금 재기동 등) 즉석 조회 — 위와 같은 값을 DB 에서 직접 뽑는다.
docker exec kdb-db psql -U kdb -d kdb -c "
SELECT 'candidate-stuck' c, count(*) n, COALESCE(EXTRACT(day FROM now()-min(COALESCE(last_enriched_at,created_at)))::int,0) oldest_d
  FROM kwave_entities e WHERE status='candidate' AND operator_locked=false
UNION ALL SELECT 'active-cjk-gap', count(*), COALESCE(EXTRACT(day FROM now()-min(updated_at))::int,0)
  FROM kwave_entities e WHERE status='active' AND (COALESCE(canonical_ja,'')='' OR COALESCE(canonical_zh,'')='' OR COALESCE(canonical_zh_hant,'')='')
UNION ALL SELECT 'active-no-anchor', count(*), COALESCE(EXTRACT(day FROM now()-min(updated_at))::int,0)
  FROM kwave_entities e WHERE status='active' AND NOT EXISTS (SELECT 1 FROM kwave_entity_external_refs r WHERE r.entity_id=e.id) AND COALESCE(array_length(source_urls,1),0)=0
UNION ALL SELECT 'never-examined', count(*), COALESCE(EXTRACT(day FROM now()-min(created_at))::int,0)
  FROM kwave_entities e WHERE status IN ('active','candidate') AND NOT EXISTS (SELECT 1 FROM kwave_kdb_enrich_attempts a WHERE a.entity_id=e.id)"

# 상시 레인 5종 가동 확인 (retype-stuck 이 41차에 cron 신규 등록됨)
tail -2 logs/locale-fill.log; tail -2 logs/adjudicate.log
tail -2 logs/recheck-active.log; tail -2 logs/retype-stuck.log

# 신설 드레인 3종 동작 확인
docker logs kdb-app 2>&1 | grep -E "revert-term|tmdb-locale|type-audit" | tail -6

# 오늘 시작 시점 기준값(비교용)
#   candidate-stuck 621 / active-cjk-gap 2,027 / active-no-anchor 4,344
#   never-examined 372 / active-latin-gap 163
```

---

## §1. 배포·커밋 (전부 푸시 완료)

| 커밋 | 내용 |
|---|---|
| `f20f3d0` | fix(ondemand) 승급 실패 무쿨다운 → 영구 공회전 |
| `86ce486` | test(source-policy) iTunes 승급축 반영 — main 8일 red 해소 |
| `070e896` | feat(revert-term) 강등 잔존 candidate 종결 레인 |
| `e24bd3f` | perf(match) TTL 캐시 + single-flight |
| `db9d43d` | fix(corrections) 400 사유 DB 영속화 |
| `7cd9336` | fix(tombstone) revert-term 기각을 tombstone 근거에서 제외 |
| `7791853` | feat(tmdb/ott) active 작품 ja/zh 공식 현지제목 회수 |
| `37bf23c` | feat(coverage) 백로그 계측 + 결정적 타입감사 |

cron 신규 1건: `50 3,9,15,21 * * * scripts/kdb-retype-stuck.sh 120 10` (KDB cron 3종 → 4종)

---

## §2. 해소한 병목 6건

1. **ondemand 영구 공회전** — '아리랑'(term) 1건이 8초 주기로 **22일간 약 23만 회** 재처리.
   셀렉트는 term 통과 / `promoteCandidateWithOfficialAnchor` 는 term 무조건 거부 /
   **승급 실패 시 쿨다운 없이 continue**. 셋째가 본질(다른 사유로 거부돼도 재발).
   ★부수효과가 컸다 — 레인 로그가 100% 노이즈여서 opencc·wikidata 오염가드·candidate
   hold 가 전부 가려져 있었다. 해소 후 실제 작업이 로그에 보인다.

2. **main 8일 red 테스트** — `TestSourcePolicyPromotionAnchors`. 회귀가 아니라 07-23
   Phase 1 이 iTunes 를 승급축으로 신설하고 07-13 테스트를 안 고친 것. **오염 가드가
   red 면 진짜 벡터도 묻힌다.**

3. **retype-stuck cron 미등록** — 코드만 머지되고 한 번도 실행 안 됨(로그 파일 자체 없음).

4. **audit-revert 갇힘 191건** — §3 참조(진단이 뒤집힌 건).

5. **match p50 1.7초 → 80ms** — 중복률 실측 **75.2%**(1,454콜/고유 361), 재호출의 **60%가
   10초 이내**(동시 요청, 53ms 간격 실측). TTL 캐시만으론 동시분이 둘 다 미스라
   single-flight 합류를 같이 넣었다(합류는 stale 위험 0). 조회+disambiguate 전체를 감싸야
   8초대 꼬리가 잡힌다. TTL 기본 300s(`KDB_MATCH_CACHE_TTL_SECONDS`, 0=off).
   라이브: 동일 본문 3회 → 805ms / 86ms / 79ms.

6. **corrections 400 — 진행 중이 아니었다.** 07-25 5시간 버스트(하츄핑 2종 108건, 각 54회
   재시도)였고 마지막 400 은 07-29. 사유는 로그가 재기동에 소실돼 특정 실패 →
   `rejected_400:<사유>` 를 logRequestTerms 로 DB 영속화. 재발하면 남는다.

---

## §3. ★진단이 뒤집힌 것 두 건 (같은 세션 내 자기정정)

**(a) audit-revert 191건 — "억울하게 갇힘"이 아니라 "종결이 안 됨"이었다.**
이름만 보고 실존 인물로 판단했다가, wikidata 라이브 조회로 뒤집었다. **K-엔터 0건**:
```
가람 Q69509682 → P31=Q3409032 "Korean unisex given name"  (인명요소 항목)
황윤서 → basketball player   이병욱 → politician   송지윤 → football player
이혜원 → entrepreneur        김은정 → curler       이창석 → Go player
```
07-21 감사도 두 가드도 전부 옳았다. 진짜 문제는 **승급 가드(person+wikidata 차단)와
wdperson 드레인(wikidata ref 보유분 제외) 사이 사각지대**. `DrainTerminateRevertedCandidates`
신설(15분, 기각은 P31 이름요소 / desc 가 K-엔터 allowlist 밖일 때만, desc 없으면 보류).
첫 실행 20/20 기각, 근거 전건 명시(인명요소 5 · 비-엔터 직업 15).

**★그 직후 부작용을 하나 만들었고 즉시 막았다.** 기각 20건이 **전부 tombstone 대상**이었다.
`Tombstoned()` 는 **이름 기준**이라 김은정(컬링) 기각이 "김은정"이란 이름을 종결시켜
lookup/prepare/corrections 에서 "재조회 불필요"로 막는다. 종결 대상이 하필 김은정·김양·
서진·가람·상원·준현·호정 같은 흔한 이름이었다. → `[revert-term:reject]` 표식 행을
tombstone 근거에서 제외. **명제 구분이 핵심: "이 레코드의 QID가 비-K" ≠ "이 이름의
K-엔티티가 없다".**

**(b) CJK 빈칸 — "소스 천장"이라 했다가 오너 지적으로 뒤집혔고, 다시 절반만 맞았다.**
오너: "더 글로리는 넷플릭스에 있을 건데 활용 못 하냐" → 맞았다. 더 글로리 ja
`ザ・グローリー ～輝かしき復讐～` / zh `黑暗荣耀` 는 **tmdb** 가 채운 값이고 drama/movie ja
1위 소스가 tmdb(589건)다. 그리고 배선 사각지대가 실재했다 — `tmdb_drain.go` 가 candidate
전용·ref 있으면 제외·en 만 채움이라 "active+ref 보유+로케일 빈칸" 193건을 아무도 안 봤다.
→ `EnrichByID` 분리 + `DrainTMDbLocaleFill` 신설 + `drainOTT` 대상 확장.
**단 내가 "192건 직행"이라 한 건 과장이었다** — §5 참조.

---

## §4. ★커버리지 배선 (이번 세션의 본체)

오너 지시: "땜방이 아니라 전반적으로 해결될 배선으로 구축해놔".

한 세션에서 **동일 결함 4회**:
| | 레인 | 대상 선정이 묶여 있던 곳 |
|---|---|---|
| ① | ondemand | 셀렉트/승급 계약 불일치(term) |
| ② | retype-stuck | `autoverify_type_reassigned` 플래그 |
| ③ | revert-term | 두 가드 사이 |
| ④ | tmdb-locale | `status='candidate'` |

공통 원인 하나: **레인의 대상 선정이 "고치려는 조건"이 아니라 부수 속성(유입경로·notes
표식·ref 보유·status)에 묶여 있다.** 조건을 만족하는데 아무도 안 집는 집합이 조용히 쌓이고,
넷 다 사람이 손으로 찾았다.

**`internal/kdb/backlog_watch.go`** — 조건별 백로그 크기·최고령·임계초과를 6h 계측.
- **레인이 아니라 상태로 정의**했다. 레인 구현이 바뀌어도 감시가 같이 무너지지 않게.
- 판정·변경 0. 계측과 경보만 — 이 워치 자체가 또 손볼 대상이 되지 않도록.
- 위 넷은 전부 `oldest_days` 단조증가로 사전에 드러났을 것이다.

**`internal/kdb/type_audit.go`** — 근거↔entity_type 결정적 불일치를 **표식만**(LLM 무사용,
타입 변경 없음). 기존 `verify/type_retrace.go` 는 active 를 **유입경로**(`auto_evidence`)로만
골라 다른 경로로 들어온 오분류를 영구히 안 본다(실측: 하츄핑이 애니 캐릭터인데 person).
→ `scripts/kdb-recheck-active.sh` 가 `[typeaudit:mismatch]` 를 받도록 연결·우선순위 부여.
표시만 하고 받는 레인이 없으면 그게 다시 ④번 결함이다.

---

## §5. ★실측으로 반증된 것 (다시 시도하지 말 것)

1. **ja 빈칸은 TMDb 로 못 채운다.** 백로그 표본 60건 중 **ja 보유 0건**(zh 는 29건).
   ja-gap 193건 중 zh 도 빈 건 49 뿐(나머지 144 는 zh 이미 채워짐). "192건 직행"은 내 과장.
2. **ja 빈칸은 ko위키 langlinks 로도 못 채운다.** 표본 40건: 문서 자체 없음 24(60%) ·
   ja 링크 없음 11 · 보유 5(12%). 그런데 **그 5 중 3이 일반어 오매칭**(땅거미→薄暮=황혼,
   탈진→疲労=피로, 초혼→御招霊=일본 민속의례). 실사용 ≈5%, 07-29 에 없앤 일반어 오염을
   되살리는 경로다. **드레인 만들지 않기로 결정.**
3. **⇒ ja 에 구글번역 확대는 금지.** 권위소스가 아예 없다는 건 나중에 덮어쓸 것도 없다는
   뜻이라 MT 날조가 영구화된다. gtranslate 290셀이 선례이고 그건 TMDb zh 덕에 *일부만*
   교체되는 중이다. **빈칸 유지가 정답.**
4. **wikidata description 어휘 부분매칭 금지.** `(film|song|band|...)` 로 썼다가 **27건
   오탐** — 아이유(songwriter 의 song) · RM(boy band 의 band) · 연상호(film director 의 film).
   전부 정상 person. 표시분 전건 회수했다. 올바른 판별은 어휘가 아니라 **서술 대상**:
   인물 설명은 직업을, 작품 설명은 사물을 말한다(연도 시작 / "by <아티스트>" /
   fictional character / 작품종류 명사구, `\m\M` 단어경계). 재실측 오탐 0 · 진짜 1.
5. **Wikimedia API 를 UA 없이 개별 연타하면 rate limit 에 걸려 전건 실패가 "수율 0%"로
   위장된다.** 실제로 한 번 속았다. `titles=A|B|C` 배치(최대 50) + UA 필수.
6. **★잘못된 데이터를 지우기 전에 그 데이터를 만드는 코드를 먼저 멈춰라.** 위 4번 오탐
   27건을 DB 에서 지웠는데, 당시 검증용으로 워치 주기를 120초로 낮춰둔 **구버전 컨테이너가
   계속 돌고 있어서 몇 분 만에 27건이 그대로 되살아났다**(아이유·RM·박찬욱·정호석 재표시).
   수정본 배포 후 다시 지워 해결. 순서는 **①코드 수정 배포 → ②데이터 정리 → ③재발 확인**.
   임시로 주기를 낮췄으면 정리 전에 반드시 원복하거나 컨테이너를 세울 것.

---

## §6. 다음 작업

**첫 명령은 §0 의 backlog-watch 로그다.** 어디가 막혔는지 지표가 말해준다.

1. **`active-no-anchor` 4,344건** — 외부 ref 도 source_urls 도 없이 **서빙 중**. 임계(90d)
   안이라 경보는 안 떴지만 규모가 가장 크다. 지금 보이는 값 중 1순위. 오염 재심 대상인지,
   근거 수집 대상인지부터 나눠야 한다.
2. **`never-examined` 372건** — 어떤 드레인도 한 번도 시도 안 한 엔티티. 커버리지 구멍의
   직접 지표라 여기가 줄지 않으면 새 레인이 필요하다는 뜻.
3. **하츄핑류 active 오분류** — 하츄핑은 wikidata 무등재라 결정적 신호가 없어 미해결로
   남았다(`person` 인데 애니 캐릭터). 전수 패널은 active 10,254 × LLM 이라 비현실적.
   회전 스윕(하루 N건씩 전수 순회)이 유일한 정공법인데 비용 판단이 필요하다.
4. **`인사이더`** — type_audit 이 잡은 진짜 1건(person 인데 "2022 South Korean TV series").
   recheck-active 패널이 다음 실행(`30 1,7,13,19`)에 받는다. 결과 스팟체크할 것.
5. **gtranslate 290셀 감사** — drama/movie ja 2위 소스가 MT 다. `DrainTMDbLocaleFill` 이
   상위소스 우선으로 자동 교체하지만 zh 가 있는 건만 된다. 잔여가 얼마인지 재볼 것.

---

## §7. 오너 결정 대기

- **하츄핑류 전수 스윕 비용** — active 10,254 를 회전 패널로 돌릴지(§6-3). 안 하면 조용한
  오분류는 계속 남는다.
- **트래블 프로젝트 분리** — 07-31 오너 결정으로 KDB 와 분리. 조사 결과는
  `project-traveli-multilingual` 메모리에 있다(TourAPI 6개 언어·국가유산청 무인증 한자
  API·문체부 훈령 427호). KDB 쪽엔 소스만 차용 가능하나 K-콘텐츠와 겹치지 않는다.
