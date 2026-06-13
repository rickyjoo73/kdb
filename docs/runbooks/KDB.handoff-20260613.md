# KDB handoff — 2026-06-13 (10차)

이 세션 핵심: **다국어 표기 "품질" 단계 진입 — 채움은 끝났고 누수 수정 + codex 토큰
차등 최적화 + 클라이언트 정정 피드백 루프 신설.**

## 0. 30초 상태 점검
```sh
cd /data/home2/kdb.aiinplanet.com
DSN='postgres://kdb:147f6037443ea3df5dd9df240ce51ff2bbbab1098911e2ee@127.0.0.1:5432/kdb'
docker ps --filter name=kdb-app --format '{{.Status}}'
docker exec kdb-app sh -c 'echo extract=$CODEX_EFFORT_EXTRACT classify=$CODEX_EFFORT_CLASSIFY global=$CODEX_REASONING_EFFORT'
docker exec kdb-db psql "$DSN" -t -A -F'|' -c "select
 (select count(*) from kwave_kdb_hermes_runs where resolved=false and status<>'ok') open_incidents,
 (select count(*) from kwave_kdb_corrections where status='pending') pending_corrections,
 (select count(*) from kwave_entities where status='active' and entity_type='person' and coalesce(canonical_zh,'')='') empty_zh_person;"
```

## 1. 다국어 품질 누수 수정 (커밋 `97a8f1b`, 배포·실측 완료)
이전 세션까지 "채움"은 끝났음(active 3,210 / ja·vi 100%, zh·es·id·pt_br 98.6%↑).
병목은 **품질**이었고 다음을 수정:

- **③ zh/zh-hant 라틴 오염 근본수정**: FillLocale 프롬프트(`codexcli/prompts.go`)에
  스크립트 규칙 명시 — zh=간체·zh-hant=번체 **한자 필수**, 인명 라틴 로마자 금지·
  모르면 skip. (이전엔 규칙 줄이 zh-hant 를 라틴 목록에 섞고 zh 언급 누락 → codex 가
  "Hong Sung-yoon" 류를 한자 칸에 채움.)
- **① 레거시 정리**(migration `0073`, 적용 완료): person 의 codex-fallback 라틴 zh
  **224** + zh_hant **245** 건을 비움 → `kwave_kdb_dataqa_log`(verdict='wrong-script')에
  스냅샷되어 **Revert 가능**. enrich_attempts 삭제로 재채움 backlog 진입(고친 프롬프트
  + 쓰기 가드로 한자 확보 or 빈칸 유지: 빈칸>틀린값). group/song/brand 의 라틴
  통용명(BTS 등)은 정당하므로 person 만 대상.
- **② dataqa CJK 확장**: dataqa 가 en/vi/es/id/pt_br 만 검수했음 → ja/zh/zh_hant 추가
  (`dataqa.go` SuspectSQL·프롬프트·schema·localeColumn). 박보검류 ja 오염도 이제 동명이인
  검수 범위. 추가 codex 비용 0.
- **④ Match API provenance 정확화**: `provenance`/`verified_only` 가 엔티티 전역
  source_urls 기준이라 en=wikidata·ja=codex-fallback 인 흔한 경우 ja 를 검증된 것으로
  오노출했음 → **반환된 locale 값 자체의 source 컬럼 기준**으로 정확화 + raw
  `locale_source` 필드 노출(`api.go` localeProvenanceExpr/localeVerifiedExpr/
  effectiveSourceExpr). 실측: 한정훈 ja(codex)=llm-only·verified 제외, en(wikidata)=통과.
- **⑤ transport 실패 ≠ 시도**: codex 타임아웃/브레이커 실패가 attempts 를 소진해
  채울 수 있는 필드가 조기 exhausted 되던 문제 — `cascade.go`/`layers.go`/`person.go`
  에서 호출 성공 시에만 tried 기록, 실패는 failed 분리해 다음 cycle 재시도.

## 2. codex 토큰 차등 최적화 (커밋 `97a8f1b`, 배포 완료)
**품질 핵심 경로(지식 회상)는 high 유지, 단순 작업만 낮춤.**
- `codexcli.Runner.WithEffort()`/`RoleEffort()` 추가. role 별 reasoning effort:
  - **EXTRACT→low**(RSS 표기 추출, 최대 볼륨 ~일 수백), **CLASSIFY/GATEKEEPER/
    RECONCILE→medium**(분류·이진 판정). `CODEX_EFFORT_<ROLE>` env 로 재정의.
  - **FillLocale·Disambiguator·dataqa 는 전역 high 유지** — 현지 표기 지식 회상이
    품질의 핵심이라 절대 낮추지 않음.
- 프롬프트 입력 캡: extract title 300/desc 600 rune, FillLocale sitelink 를 서비스
  locale 위키로 한정. compose 에 `CODEX_EFFORT_*` 명시(가시성).
- **모델 교체(gemma) 판단**: 지금 하지 말 것. 이 시스템 LLM 가치는 추론이 아니라
  "현지 표기를 아는가"라는 frontier 지식 → fill/disambig/dataqa 는 소형모델 시 환각·
  skip 증가. 단 **extract(창조 금지·텍스트 추출)는 로컬 적합** → 추후 교체 시 extract
  부터, `kwave_kdb_codex_runs`(raw 입출력)를 골든셋 삼아 오프라인 A/B 후 단계 교체.

## 3. ★클라이언트 표기 정정 피드백 루프 (커밋 `16957c1`, 배포·실측 완료)
사장님 요청: "KDB 가 잘못 보냈을 때 클라이언트가 수정요청 + 검증 + 지속 업데이트".
**신뢰 모델 = 근거기반 자동 + 미달은 큐**(사장님 선택).

- **`POST /v1/corrections`** {entity_id|ko, locale, returned, suggested, evidence_url}:
  - `auto_applied`(200): ① locale 문자셋 가드 ② 현재 값 교체가능(can_replace_canonical
    — codex/wikipedia/빈칸만, operator/consensus/external/rss 보호) ③ **Wikidata
    label/sitelink 정규화 일치** → 즉시 반영(source=wikidata-label, 원값 스냅샷 revert).
  - `queued`(202): 근거 미달/보호된 값 → `kwave_kdb_corrections`(migration `0074`).
  - `rejected`(422): 문자셋 가드 실패(ja 칸 한글 등).
  - **단일 클라이언트 주장만으론 절대 자동반영 안 됨**(권위 외부소스 교차검증 필수,
    오남용 저항). read 키 소비자도 신고 가능. reporter=키 해시 prefix(평문 미저장).
- **운영자 심사 CLI**: `kdb-app corrections list | approve <id> | reject <id> [사유]`
  (approve=source=operator 적용, 원값 스냅샷). **admin UI 페이지는 미구현(다음 세션)**.
- 소비자 워크플로우(KDB-API.md): `verified_only`+`locale_source` 로컬 게이팅 →
  `llm-only` 만나면 정정 신고 → `updated_since` 델타 동기화.

## 4. 빌드/배포/푸시
- 코드 변경: `docker compose -f docker-compose.kdb.yml build kdb-app && up -d kdb-app`
  (이미지 baked 바이너리 실행 — 반드시 build. env 변경도 `up -d`). 빌드 ~9분.
- migration: 수동 `docker exec -i kdb-db psql "$DSN" < migrations/00NN.sql` (추적테이블 없음).
- **push 미실행** — 두 커밋 `97a8f1b`,`16957c1` 로컬에만 있음. 푸시:
  `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`
- 백엔드는 세션과 무관히 컨테이너 독립 실행.

## 5. 남은 작업 (다음 세션)
1. **zh/zh_hant 재채움 수렴 확인**: §0 의 empty_zh_person 감소 추적(현재 ~228).
   재채움이 한자를 못 얻으면 빈칸→exhausted(정상, 빈칸>틀린값).
2. **corrections admin UI**: 현재 CLI 만. `/admin` 에 pending 큐 리스트+승인/거부 페이지
   (`kdbadmin` 패턴, `corrections.ListPending`/`Approve`/`Reject` 재사용).
3. **신규 effort 효과 관측**: 다음 cycle 들에서 codex 평균 latency/실패율 하락 확인
   (이전 3일 http_error 15%). 필요시 EXTRACT→minimal 까지.
4. 이월: B1~B4 admin UX, TMDb/KOFIC 필모 backfill, 미디어 워커 하이브리드 통합.

## 6. 협업 플랫폼 + 작품 번역 + 운영 전환 (같은 날 2부)
사장님 비전: 단방향 제공 → **클라이언트와 교류하며 품질 올리는 협업 시스템**.

- **작품 "번역" 품질**(`4d0a498`): 인물=음역이지만 드라마/영화/예능=공식 현지 제목(번역).
  근본원인=주력 Enricher 에 TMDb 미배선 → codex 가 비영어 locale 에 영어 복사
  (es=en 289·pt_br=en 307·vi=en 208). 수정: cascade L1 TMDb 배선 + FillLocale 영어복사
  금지 규칙 + TMDb country 변종 영어복사 제거(es-MX 번역 우선) + migration `0075` 로
  기존 706건 정리(dataqa_log revert). 실측: 오징어게임 es="El juego del calamar".
- **POST /v1/prepare**(`4d0a498`): 받기+빠른준비. 기사 작성 시 한글 고유명사(문자열 또는
  `{ko,type}`)를 미리 던지면 조회 전 백그라운드 enrich. K-콘텐츠만 준비, 비-K=out_of_scope.
  스코프=K-엔터 13 type(사장님 확정). bgEnrich.Trigger 재사용.
- **공개 /docs**(`4d0a498`): 무인증 HTML(`kdbapi/docs.go`) — 제공 DB 범위(13 type)·표기 vs
  번역·협업 워크플로우·엔드포인트. KDB-API.md 도 동일 보강(외부 공유용).
- **양방향 corrections**(`bbbe293`): 오역 신고 → Wikidata 즉시확인 또는 **codex 검증(비동기)**
  → 제안 정확=반영/제3안=proposed 회신/현재 정확=rejected/불확실=운영자큐. 적용 source=
  `correction-verified`(prio 4, migration `0076`). confirm_id/accept 핸드셰이크 + GET 폴링.
  ★**비동기 전환 결정적**: 동기 codex 가 API 10s 타임아웃에 잘려 항상 queued 였음 →
  goroutine+폴링, effort medium(120s→32s). 
- ★**빌드 없는 배포로 전환**(`bbbe293`): 호스트 소스가 `/app` 볼륨 마운트인데 baked 바이너리
  실행이라 매번 9분 이미지 빌드를 했음(불필요·이미지 누적). compose `command` 를
  `go build -o /tmp → exec` 로 변경 → **코드 수정 후 `docker restart kdb-app` 만으로 반영**
  (~12s, 이미지 안 늘어남). dangling 이미지 정리. **앞으로 `docker compose build` 쓰지 말 것.**
- **codex 속도**(`621fd67` 등): role 별 effort 차등(extract low·classify/gatekeeper/reconcile/
  correction/FILL medium·disambig 만 high). 실측 extract 98→48s, verify 120s→32s. TMDb 배선으로
  작품 codex 호출 자체 감소. 한계: gpt-5.5+ChatGPT OAuth 의 본질적 지연은 못 없앰.

## 7. 미해결/방향 (다음)
- **펜딩 실태**: corrections 는 비동기 codex 로 자동수렴(pending~1). **진짜 backlog =
  needs_disambig 116**(동명이인, 39=데이터있음 자동구분가능 / 77=데이터부족). 처리방향=
  사람 큐에 두지 말고 **enrich 로 데이터 채워 자동 disambig**. catch-up `drain-enrich 4`
  백그라운드 가동 중(작품 제목·zh·disambig 데이터). 잔여 진짜-모호는 자동 distinct 규칙 검토.
- **gemma**: 호스트·11434·컨테이너 어디에도 미설치. 쓰려면 ollama+gemma 셋업 필요. 이 작업의
  LLM 가치는 frontier 지식이라 fill/disambig 는 gpt-5.5 우위. gemma 는 검색증강 hard-case·
  extract 오프라인 평가용으로만 고려. (사장님 제안 = 검토 후 보류 권장.)
- **운영**: 재배포는 `docker restart kdb-app`(빌드 X). drain 은 restart 시 죽으니 재기동 필요.

HEAD = `621fd67`. 미push(로컬 6커밋). 연관: [[project-kdb-cutover]], [[reference-kdb-handoff]],
[[reference-kdb-api-and-ops]].
