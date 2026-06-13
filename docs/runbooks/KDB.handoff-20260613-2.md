# KDB handoff — 2026-06-13 (11차, 대규모 세션 2부)

이 세션 핵심: **gemma 게이트웨이 도입·codex 부활→하이브리드 라우팅, 협업 API(prepare/
corrections/docs), 멀티에이전트 점검 후 누수·오염·정체 일괄 처리, admin 백로그 카드.**
HEAD `5c832d6`, 전부 origin/main push 완료. 다음 작업 = **admin 페이지 전수 개선**.

## 0. 30초 점검
```sh
cd /data/home2/kdb.aiinplanet.com
DSN='postgres://kdb:147f6037443ea3df5dd9df240ce51ff2bbbab1098911e2ee@127.0.0.1:5432/kdb'
docker ps --filter name=kdb-app --format '{{.Status}}'
docker exec kdb-app sh -c 'echo provider=$KDB_LLM_PROVIDER codex_conc=$KDB_CODEX_CONCURRENCY gemma_conc=$KDB_GEMMA_CONCURRENCY'
docker exec kdb-db psql "$DSN" -t -A -F'|' -c "select
 (select count(*) from kwave_entities where needs_disambig=true and status='active') 충돌,
 (select count(*) from kwave_kdb_corrections where status in ('pending','proposed','verifying')) corrections,
 (select count(*) from kwave_entities where status='active' and (canonical_ja~'[가-힣]' or canonical_zh~'[가-힣]')) charset오염;"
```

## 1. ★운영 핵심 변경 (반드시 숙지)
- **빌드 없는 배포**: compose `command` 가 `go build -o /tmp/kdb-app → exec`. 코드 수정 후
  **`docker restart kdb-app`** (~12s)만으로 반영. **`docker compose build` 절대 쓰지 말 것**
  (이미지 누적). env 변경만 `up -d`. drain 은 restart 시 죽으니 재기동 필요.
- **LLM 하이브리드 라우팅** (사장님 방침: 고난도=codex, 대량=gemma):
  - **codex(gpt-5.5)**: Disambiguator(동명이인 표기변종 판단)·dataqa(오염검수)·FillLocale
    (작품 공식 현지 제목/번역)·corrections 검증. `KDB_LLM_<ROLE>` env 로 재정의.
  - **gemma(ai2 게이트웨이 gemma-4-26b)**: extract·classify·gatekeeper·reconcile·인물필드fill.
  - codex 동시성 3(OAuth 1회용 refresh 경쟁 회피), gemma 12. `KDB_LLM_PROVIDER=gemma`(전역기본).
  - gemma 는 **추론 OFF**(chat_template_kwargs.enable_thinking=false — 4.5s→0.77s) + temp0+top_p0.1.
  - gemma 단독은 환각(검색 컨텍스트 없으면) → FillLocale 은 Wikidata 컨텍스트로 검색증강.
    dataqa 배치 프롬프트는 gemma 가 http 500 → codex 필수.
  - 되돌리기: `.env` KDB_LLM_PROVIDER=codex 또는 KDB_LLM_<ROLE> 조정.
- gemma 설정: `.env` KDB_GEMMA_BASE_URL=https://ai2.aiinplanet.com, KDB_GEMMA_MODEL, KDB_GEMMA_API_KEY(시크릿).

## 2. 이번 세션 처리 완료 (커밋순 요약)
- **다국어 품질**: zh/zh-hant 라틴오염 정리(0073/0075), FillLocale 프롬프트 작품 영어복사 금지,
  dataqa ja/zh/zh_hant 확장, Match `provenance`/`verified_only` 를 locale별 source 정확화 +
  `locale_source` 노출, transport 실패가 attempts 소진 막음.
- **작품 번역**: 주력 Enricher 에 **TMDb 배선**(작품 공식 현지 제목, codex보다 먼저) + country
  변종 영어복사 제거. **zh 간체/번체 정규화**(internal/kdb/zhvariant — zhwiki LanguageConverter,
  `kdb-app zh-normalize`). 朴宝剑/张娜拉 정확.
- **협업 API**: `POST /v1/prepare`(받기+빠른준비, K-콘텐츠만, out_of_scope), **양방향 corrections**
  (Wikidata→codex 비동기 검증→auto/proposed/queued/rejected, confirm 핸드셰이크, GET 폴링),
  공개 **/docs**(무인증, DB 스코프 13 type·표기vs번역·워크플로). KDB-API.md 동기화.
- **operator_locked enrich 버그**: 잠긴 유명인물(변우석/정국 등)의 빈 locale 이 영구 빈칸이던 것
  수정(enrich-empty SELECT 의 operator_locked=false 제거, 쓰기는 empty-only 라 안전).
- **admin 백로그 카드**: 신규/충돌/누락/품질 카드에 "자동 해결방안+운영자 액션" + 미연결 라우트
  복구(inbox/unclassified/locale-gaps + POST 액션).
- **ultrareview 4건**: Match provenance 권위source 누락, Approve 가 proposed 무시, zhvariant
  fetch실패 캐시오염, Confirm resolved_at 불변식.
- **멀티에이전트 점검(5영역) 후 일괄 처리**: charset 가드(observations zh 한글거부 + candidates
  Observe 가드), research/corrections **reaper**(stuck 복구), corrections 큐 proposed 노출,
  SweepPromote needs_disambig 초기화, markHomonyms 재플래그 가드, dataqa charset 탐지.
  DB정리: 가짜 충돌 109 해제(118→0), confidence backfill 11, research stale 4.
- **충돌 안 줄던 근본원인**: Disambiguator 가 해소(distinct/merge)해도 needs_disambig=false 를
  안 내려 영구 stuck. `clearResolvedDisambig`(매 cycle) + 즉시 0.
- **노이즈 게이트**: PreGate/looksLikeEntityName 에 키보드 마구입력(qwertyzxcv12345) 탐지
  (gatekeeper.LooksLikeMash). 현 DB 깨끗(노이즈 0).

## 3. ★다음 세션 작업: admin 페이지 전수 개선 (사장님 지시, 미착수)
사장님: "**인물DB/고유명사DB 클릭 시 관련 항목을 모두 보여줘야 하는데 안 보여준다**. 각 페이지를
운영자 관점에서 제대로 보고/관리/작동하게 개선. **Opus 4.8 xhigh 멀티에이전트로 페이지별 점검·개선**.
KDB는 다국어 사이트 핵심 — 무너지면 안 됨."

- **★최우선 버그**: `entity_detail.html` 이 핸들러가 가져온 데이터의 일부(관련필드 ~3개)만 렌더.
  핸들러(handlers_entities.go:298 entityDetail)는 9 locale canonical+source, 9 alias 다 가져오는데
  템플릿이 다 안 보여줌. + external_refs/relations/observations/person_details/동명이인(같은 ko)
  관련 항목 누락. **클릭 시 그 entity 의 모든 관련 정보(다국어 표기+source 마크, alias, 외부ref,
  관계, 인물상세, 동명이인 형제, 매체 관측)를 한 화면에** 보이게.
- **대상 12 페이지**(라우트는 router.go): 운영개요(/admin), 고유명사DB(/admin/entities),
  인물DB(/admin/persons), Entity후보(/admin/kdb/candidates), 검토큐(/admin/entities/review),
  충돌(/admin/entities/conflicts), 매체관측(/admin/kdb/observations), 다국어소스(/admin/entities/
  sources), RSS Whitelist(/admin/entities/whitelist), Codex Audit(/admin/kdb/codex-runs),
  Hermes(/admin/hermes), Settings(/admin/settings), + 상세(/admin/entities/{id}).
- **각 페이지 점검 관점**: ①우리 용도/상태에 맞는 내용인가(gemma/codex 하이브리드 반영? codex-runs
  는 extractor만 — gemma 호출 안 보임), ②운영자가 보고/관리/작동 가능한가, ③빠진 관련항목/액션,
  ④디자인(sticky header·대형테이블·source 마크 범례 — 8차 핸드오프 B1~B4 잔여).
- **방법**: Workflow 로 페이지별 Opus 에이전트 점검(읽기+owner 세션 curl 렌더확인)→구체 개선안
  (파일:라인). admin 은 단일 패키지라 **에이전트 직접 동시수정은 충돌** → 점검+개선안 수집 후
  메인이 구현(검증하며). owner 세션 발급: `.env` KDB_ADMIN_SESSION_SECRET 으로 HMAC
  (session.go encodeSession 포맷: base64url(user:exp).base64url(hmac)).

## 4. 운영 명령
- 배포: 코드수정 → `docker restart kdb-app`. env변경 → `docker compose -f docker-compose.kdb.yml up -d kdb-app`.
- push: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`.
- 처리 가동: `docker exec -d kdb-app sh -c '/tmp/kdb-app drain-enrich 6 > /tmp/drain.log 2>&1'`,
  `kdb-app dataqa --apply`(codex·느림), `kdb-app corrections list|approve|reject`, `kdb-app zh-normalize`.

## 5. 미해결/관망
- 누락 ~180: 검색에도 없는 hard-case(무명 인물 한자 등) = **빈칸이 정답**(억지로 채우면 환각). 작품은
  exhausted 리셋 후 codex/TMDb 재채움(느림). 충돌 0(근본수정됨). corrections 0.
- 품질 "미검수 1474"·"conf<0.7 740" 은 **측정 착시** — 진짜 dataqa suspect 104, conf<0.7 의 728은
  Wikidata 없는 무명의 정당한 저신뢰(floor). 대시보드 metric 을 suspect/bumpable 기준으로 바꾸면 착시 해소(다음).
- codex 느림(동시성3·OAuth)이 throughput 상한. 고난도만 codex 라 OK 지만, 작품 FillLocale 대량이면
  체감 느림 — KDB_LLM_FILL=gemma 로 임시 가속 가능(품질 trade-off).

HEAD = `5c832d6`. 연관: [[project-kdb-cutover]], [[reference-kdb-handoff]], [[reference-kdb-api-and-ops]].
