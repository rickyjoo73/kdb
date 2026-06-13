# KDB handoff — 2026-06-14 (12차, 대규모 세션: admin 전면개선 + 신뢰축 + 협업API)

이 세션 핵심: **① 운영자 액션 전면 복구(CSRF/라우트 마비), ② 신뢰등급(신뢰축) 도입,
③ 사이드바 유형별 DB(명확성 축), ④ 협업 API 강화(진입 게이트 + 교정신고 전수접수),
⑤ 오염 DB 정리(일반어/비K콘텐츠).** HEAD `1aa4325`, 7커밋 전부 origin/main push.

## 0. 30초 점검
```sh
cd /data/home2/kdb.aiinplanet.com
DSN='postgres://kdb:147f6037443ea3df5dd9df240ce51ff2bbbab1098911e2ee@127.0.0.1:5432/kdb'
docker ps --filter name=kdb-app --format '{{.Status}}'
docker exec kdb-app sh -c 'echo provider=$KDB_LLM_PROVIDER codex=$KDB_CODEX_CONCURRENCY gemma=$KDB_GEMMA_CONCURRENCY'
docker exec kdb-db psql "$DSN" -t -A -F'|' -c "select
 (select count(*) from kwave_entities where status='active') active,
 (select count(*) from kwave_entities where needs_disambig=true and status='active') 충돌,
 (select count(*) from kwave_kdb_corrections where status in ('pending','proposed','verifying')) corr_open,
 (select count(*) from kwave_entities where status='active' and (canonical_ja~'[가-힣]' or canonical_zh~'[가-힣]')) charset오염;"
```
세션 종료 시점: active **3216** · 충돌 8(자율처리) · corr_open 0 · charset오염 1(자율dataqa 대상).
신뢰분포: 확정 222 · 검증 2309 · 매체확인 611 · 미검증 74 (= 신뢰 79%).

## 1. ★운영 핵심 (반드시 숙지 — 변경 없음, 11차 계승)
- **빌드 없는 배포**: 코드수정 → `docker restart kdb-app`(~12s). `docker compose build` 금지.
  env 변경만 `up -d kdb-app`. 소스는 `.:/app` bind-mount, compose command 가 `go build→exec`.
- **LLM 하이브리드**: provider=gemma(전역), codex 동시성3·gemma12. codex=고난도(Disambig·
  dataqa·FillLocale·corrections), gemma=대량(extract·classify·gatekeeper). `KDB_LLM_<ROLE>` 재정의.
- **push**: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`
- **owner 세션 curl 검증은 auto-mode 에서 차단**됨(secret 위조 분류). admin 검증은 렌더 테스트
  (`go test ./internal/kdbadmin/`)로 — 이번에 entity_detail/라우트 등록 테스트 추가. 프로덕션 DB
  쓰기도 광범위하면 차단되니 **구체 ID/조건으로 좁혀** 실행.

## 2. 이번 세션 처리 완료 (커밋순)
- **`27f58cc` admin 운영자 액션 복구(최대 임팩트)**: ① `entity_detail.html` 이 핸들러가 가져온
  9locale·9alias·인물상세·관계·외부ref·resolution·research·관측 중 일부만 렌더하던 것 전수
  렌더 + 동명이인 형제 조회 추가(사장님 지시). ② **운영자 액션 전면 마비 복구** — `lock·
  wikidata-adopt·whitelist add/rss/toggle/delete` 6라우트가 핸들러만 있고 router 미배선(404),
  settings 외 13개 POST폼에 `_csrf` 0개(전부 403)였음 → 라우트 등록 + 전 폼 csrf 추가.
  ③ Hermes 깨진 CSS(미정의 class)→Tailwind. ④ 대시보드 착시: 품질검토 conf<0.7 742→
  bumpable(Wikidata ref)129, 누락locale 123→exhaustion반영 0, "codex calls"→"LLM calls".
- **`138a3a4` LLM감사/Settings 하이브리드 가시성**: codex-runs "Codex Audit"→"LLM 추출 감사",
  실제 EXTRACT provider 배지(=gemma) + 역할별은 Hermes 링크 안내(provider 컬럼 부재 오표기 해소,
  `extractProviderLabel`). Settings 에 gemma 키 추가 + `/v1/models` 연결테스트(실측 통과) +
  apiSettingsTest 가 consumers 누락해 소비자키 섹션 사라지던 버그 수정.
- **`2e77bfc`+`77a0b6f` 신뢰등급(신뢰축) + 검증 커버리지 대시보드**: `kdb.TrustOf`. ★사장님
  방침으로 재정의: **확정 = 현지매체 ≥3 distinct domain 또는 operator_locked**(수동 아님, 자동),
  **검증 = 권위출처(Wikidata/TMDb/MusicBrainz/KOFIC/정정)**, **매체확인 = 매체1~2**, **미검증 =
  LLM·근거없음**. 매체≥3 이 권위보다 위(이 DB 목적=현지 실사용 표기). `/admin/entities/trust`
  대시보드(유형별 분포·신뢰%·🔵검증필요). 배지: 목록·상세 전항목. SQL CASE = TrustOf 동일.
  (덤: 고유명사DB 에 인물DB 같은 locale 채움바.)
- **`8c39e28` 사이드바 유형별 DB(명확성 축)**: entitiesList `?group=` 도입 — 작품(drama+movie+
  show+song_album)·방송기관(channel_outlet+agency)·브랜드장소(brand_place)·그룹. 사이드바
  하위항목 + title/하이라이트.
- **`1aa4325` 협업 API 강화**(prepare/match/lookup/corrections 실호출 점검 후): ① prepare/lookup
  게이트를 자율과 동일한 `gatekeeper.PreGate` 로 일원화(공백없는 명령형 "점심메뉴추천해줘" 가
  candidate 오염 만들던 것 차단, josaTails 에 명령형 어미 추가). ② **corrections 전수접수**:
  charset 가드 실패 'rejected'→'queued'(운영자검토), 미보유 고유명사 신고 400→발굴 큐 접수.
  클라 오류지적 존중, 자동반영만 막고 신고 안 버림. ③ docs: 입력규칙·전수접수 정책.

## 2.5 DB 오염 정리 (사장님 지시, 적용 완료 — 전부 reversible status=rejected)
- charset 혼합문자셋 1건(준한 zh=俊한) 비움 / **일반어 term 24건 reject**(김치·소주·한복·컴백·
  한류·떡볶이…고유명사 아님) / **미디어파인 reject**(비 K-콘텐츠 자사 플랫폼) / 천천히 강렬하게(지명).
- ★**중요 교훈**: DB 는 대체로 깨끗. 크루드 휴리스틱(길이>20·한글無·conf<0.4·1글자·문구패턴·
  무출처)은 **정당한 K-콘텐츠를 대량 오탐**(천천히 강렬하게=실제 드라마 Tantara, 강남스타일·디토·
  지금우리학교는, 기안84, 뷔·비, CNBLUE). 제거는 **출처(refs)·웹검색 검증** 기준으로만. 무차별
  삭제 금지. 외국인물/외국언론/정당(메릴스트립·The Guardian·국민의힘 등 Tier1)은 **유지 결정**.
- prepare 게이트 강화로 신규 노이즈 유입 차단됨. 일반어 재발 방지는 autopilot classify(term→reject)
  가 백스톱(단 conf>0.4 매체합의 term 은 우회 가능 — 잔존 시 수동 reject).

## 3. ★다음 세션 작업 후보
- **song_album 검증율 14%**(242 중 권위출처 17·매체확인 182) — 최신곡이라 Wikidata 미반영.
  `drain-enrich` 가동으로 🟡/🔵→🟢 끌어올리면 신뢰% 상승(`/admin/entities/trust` 로 추적).
- **API 소비자 신뢰필터**: match/lookup 에 `min_trust=확정|검증` 노출(신뢰축 데이터 토대 완성됨).
- admin 잔여(8차 B1~B4): sticky header 일부, 충돌 페이지 인라인 머지 액션(현재 상세에서 처리).
- 핸드오프가 SSOT — memory 에 진행상태 적지 말 것.

## 4. 운영 명령
- 배포: 코드 → `docker restart kdb-app`. env → `docker compose -f docker-compose.kdb.yml up -d kdb-app`.
- 처리 가동: `docker exec -d kdb-app sh -c '/tmp/kdb-app drain-enrich 6 >/tmp/drain.log 2>&1'`,
  `kdb-app dataqa --apply`, `kdb-app corrections list|approve|reject`, `kdb-app zh-normalize`.
- API 테스트: `KEY=$(grep '^KDB_API_KEYS=' .env|cut -d= -f2-|cut -d, -f1|tr -d '"')` →
  `curl -H "X-KDB-Key: $KEY" http://127.0.0.1:9100/v1/...` (prepare/match/lookup/corrections).
- 신뢰등급/유형뷰: `/admin/entities/trust`, `/admin/entities?group=works|orgs|places`, `?type=group`.

## 5. 미해결/관망
- 충돌 8(유재석 Antenna/FNC·서인영 등 실제 동명이인) — Disambiguator/운영자 영역, UI 액션 작동.
- charset오염 1 재출현 — 자율 dataqa charset 탐지 대상(수동 정리 시 구체 id 로).
- corrections id 4·7·8(rejected, 06-13 13~14시 타 신고) 보존 — 정책상 향후엔 전수접수되나 과거건.
- 코드 미커밋 없음(working tree clean). 자율운영은 컨테이너 독립 가동.

HEAD = `1aa4325`. 연관: [[project-kdb-cutover]], [[reference-kdb-handoff]], [[reference-kdb-api-and-ops]].
