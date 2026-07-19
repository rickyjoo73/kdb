# KDB 핸드오프 35차 (2026-07-19) — 채움 근본수리(SearXNG·LocalFill) + 가타카나 규칙채움

직전: 34차(`KDB.handoff-20260717-2.md`, 07-17~18). 이번 세션 = 오너의 반복 지적("아직도 못
채워진다·병목 보이는데 왜 빠른 방법이 없나·근본 솔루션")을 따라 **채움 실패의 뿌리를 추적·수리**.
최종 배포 `kdb-app:kana-20260719-2`. 전 커밋 push 완료(HEAD `1ce83b1`).

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s https://kdb.aiinplanet.com/v1/health          # version=kana-20260719-2
# 미해결 보류(1,938에서 감소했나)
docker exec -i kdb-db psql -U kdb -d kdb -c "
SELECT count(DISTINCT entity_ko) unresolved FROM kwave_entity_research_queue q
WHERE precheck_status='review' AND NOT EXISTS (SELECT 1 FROM kwave_entities e WHERE e.status='active'
  AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)));"
# ★SearXNG 생존 확인(오늘 살린 근본 백엔드 — 죽으면 채움 전체가 다시 0)
docker exec kdb-app sh -c "curl -s 'http://kdb-searxng:8080/search?q=test&format=json' -m 10" | python3 -c "import sys,json;print('searxng results:',len(json.load(sys.stdin)['results']))"
# 밤사이 자율 루프 실적(4종 daily + kana + localfill)
docker logs kdb-app --since 20h 2>&1 | grep -E "triage\(daily\)|type-retrace\(daily\)|cand-evidence\(daily\)|active-audit\(daily\)|kdb.kana:|kdb.localfill: .*native" | tail -20
# 검토 플래그(claude 판독 대기)
docker exec -i kdb-db psql -U kdb -d kdb -c "
SELECT count(*) FROM kwave_entities WHERE status IN ('active','candidate')
  AND (notes LIKE '%[audit:review]%' OR notes LIKE '%[cand-evidence:review]%');"
```

## §1. 이번 세션 산출물 (전부 배포·커밋됨)

**★A. 채움 근본 병목 수리 — SearXNG 전 엔진 차단 (가장 중요)**
- **진단**: LocalFill(타깃언어 매체 검색→현지표기 추출, local-usage 티어)이 켜져 있는데 로그가
  전부 "0건(SearXNG rate-limit?)". 자체 SearXNG의 기본 엔진(brave·ddg·startpage·wikidata·
  qwant·sogou) **전부 DC IP 차단**(CAPTCHA/access-denied)이라 몇 주째 채움 0.
- **수리**: `searxng/settings.yml` `keep_only`로 밴-안전 엔진 풀만(bing·baidu·mojeek·yahoo·
  wikipedia·wikidata·sogou·presearch·qwant·startpage) — 응답 엔진만 취합, 차단은 자동 스킵.
  google·brave·ddg는 오너 IP밴 금지 규칙으로 제외. 재기동 후 0→11~20건. 파일은 977 소유라
  `docker run --rm -v .../searxng:/x alpine`(root)로 기록.
- **★엔진이 또 죽을 수 있음**(고질병): 재차단 시 §0의 searxng 체크가 0이면 `docker exec kdb-app
  sh -c "curl .../search?q=X&engines=ENG&format=json"`로 살아있는 엔진 재조사→settings.yml keep_only 갱신→`docker restart kdb-searxng`.

**B. LocalFill 3중 버그 수리** (`internal/kdb/localfill.go`, `websearch.go`, 배포 fillfix-2)
- ①쿼리가 맨 한국어 이름뿐→동음이의 노이즈(트롤리=전차). 영문명+유형어 결합 쿼리 추가.
- ②필터가 한국어 문자열 포함만 인정→정작 목표인 외국어 페이지 전부 폐기(모순). en 매칭 허용.
- ③`searxngEngines` 로케일 고정제한(ja=bing,wikipedia)이 밴안전 풀을 막고 bing-ja는 한국어
  쿼리에 완전 노이즈 반환→engines 파라미터 생략(settings.yml 풀 사용). throttle 2500→600ms(.env).
- **정직한 한계**: 3중 수리 후에도 니치 인물·작품은 외국어 웹 커버리지 자체가 없어 저수율(트롤리→
  gemma가 車輪 오추출→복원). **남은 빈칸 대부분은 버그 아닌 진짜 소스천장.** 유명 엔티티는 채워짐.

**★C. 가타카나 규칙채움 — 인물 ja 빈칸 90% 해소** (`internal/kdb/kana.go`, 배포 kana-2)
- 니치 K 인물은 ja 표기가 외국어 웹/권위소스에 없음(검색 불가) → 결정적 한글→가타카나 변환으로
  즉시·대량 채움(외부호출 0). 권위 정답 1,850쌍 대조 음운 ~78%/최고정밀 88%.
- **가드(romanization 미러링)**: source='kana-rule' prio8→권위/매체/wiki/검색 자동 업그레이드.
  provenance='rule-transliteration'→verified_only 소비자 제외. 빈칸만·스냅샷 복원가능·비가타카나 스킵.
- 실행 692건→인물 ja 빈칸 781→74. `kdb-app kana-fill [n]`. 자율루프 100/cycle 배선(KDB_KANA_FILL_ENABLED).
- 복원: `UPDATE kwave_entities SET canonical_ja=NULL,canonical_ja_source=NULL WHERE canonical_ja_source='kana-rule'`.

**D. (34차 문서 §5b~5e 연속분) 미해결 보류 구조수리** — 상세=`KDB.handoff-20260717-2.md`
- cand-evidence(요청대기 candidate 뉴스근거 승급)·active 전수감사(이중게이트)·검토플래그 156건
  claude 직접 판독(기각50·확정34·교정10·승급10). 요청계약 v1.1(공개 /docs §5).

## §2. 다음 작업 (우선순위)

1. **밤사이 수렴 확인**(§0): SearXNG 살아난 뒤 LocalFill이 유명작 실제 채우나 / kana daily 신규인물
   채우나 / 4종 daily 실적 / 미해결 1,938 감소분.
2. **검토 플래그 79건 claude 판독**(34차 §5e 방식): `SELECT canonical_ko,entity_type,notes FROM
   kwave_entities WHERE notes LIKE '%[audit:review]%' OR notes LIKE '%[cand-evidence:review]%'`.
   기각/확정/타입교정/승급 분류 → dataqa_log 스냅샷 SQL. 오거부 금칙 유지(확실할 때만 기각).
3. **동명이인 충돌 ~77건 자동 라우팅** — 마지막 무경로 버킷(conflicting_entity_types 등,
   autoVerifyReasons 밖). 기존 Disambiguator 연결. 오너 무인화 강력 요구.
4. **KMDb 드레인 외화 국적 필터** — 포켓몬스터(일본 IP)가 KMDb 한국개봉 외화로 승급된 누수.
   `internal/kdb/kmdb_drain.go`에 K-제작 국적 필터 추가.
5. **있지(ITZY) active 중복 2건 병합** — 34차 감사 발견. `kdb_merge_by_name`류.
6. 로마자 음차 스위프(`docs/runbooks/KDB_ROMANIZATION_AUDIT_20260717.csv` 116건 재판정).

## §3. 오너 액션/결정 대기

- **결정**: ①공연장/경기장 brand_place 수용 여부(전수감사가 잠실야구장류 자동기각 — 콘서트 장소명
  번역 수요 vs K-스코프) ②팬덤명 정책(영웅시대류 — type 체계에 없음) ③비K 명시 종결 규칙(15차
  보류사유=K활동 외국적 멤버 오거부 위험) ④TTL 21→10일 단축.
- **액션**: C등급 3사(issuetalk·trendbiz·미디어파인)에 요청계약 v1.1 전달("kdb.aiinplanet.com/docs
  §5대로"). 네이버 쿼터 확장·KMDb 트래픽 증대(활용사례 제출 기한 08-30). 보안: gtranslate GCP 키·
  trendbiz ssh 비번 회전(채팅 노출).

## §4. 운영 참고 (이번 세션 규칙)

- 배포=베이크드 이미지: `.env` KDB_APP_IMAGE + KDB_BUILD_VERSION 둘 다 sed→build→up -d.
  (version 미갱신 시 /v1/health가 옛 태그 보임.)
- **SearXNG 설정 파일은 977 소유** → 직접 write 불가, `docker run --rm -v .../searxng:/x alpine`(root)로.
- 결정적 채움(romanize·kana·opencc)은 검색·LLM 0이라 즉시·대량·안전 — 소스천장 롱테일의 유일한
  빠른 레버. 단 인물만(작품 제목·zh 인명은 결정 불가). 검색 채움은 유명 엔티티에만 유효.
- push: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`.
- `.env`·`searxng/`는 gitignore — 커밋 시 `-f`. .env 런타임값은 이 문서 §1·§4로 복원.
- **오거부(실재 K 기각)=최상위 금칙.** 모든 자동/수동 기각은 dataqa_log 스냅샷 복원 필수.
```
```
