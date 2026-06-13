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

HEAD = `16957c1`. 연관: [[project-kdb-cutover]], [[reference-kdb-handoff]],
[[reference-kdb-api-and-ops]].
