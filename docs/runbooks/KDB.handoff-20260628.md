# KDB handoff — 21차 (2026-06-26~28): 코드레드 전면개선 + 다국어 확보 + gemma 풀가동

새 세션 첫 명령 = 이 문서 §0. 20차([[KDB.handoff-20260627.md]]) 이어짐.

## §0 — 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
docker exec kdb-app sh -c 'echo "LLM: DISAMBIG=$KDB_LLM_DISAMBIG DATAQA=$KDB_LLM_DATAQA FILL=$KDB_LLM_FILL | GEMMA URLS=$KDB_GEMMA_BASE_URL CONC=$KDB_GEMMA_CONCURRENCY | GROUND=$KDB_ENRICH_GROUND STRICT=$KDB_ENRICH_GROUND_STRICT OTT=$KDB_OTT_ENABLED DISNEY=$KDB_DISNEY_ENABLED CORRECTION_TRUSTED=$KDB_CORRECTION_TRUSTED_SOURCE"'
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('codex-fallback','wikidata-label','tmdb','romanization','opencc','netflix','disney','local-usage') GROUP BY s ORDER BY count(*) DESC;"
```

## §0.5 — ★빌드/배포 (변경됨, 필독)
- **GOMODCACHE 우회**: 기본 `/go/pkg/mod`는 read-only(신규 deps 추가불가)라 **`/app/.gomodcache`(복사본+opencc)**로 우회. compose env(`GOMODCACHE/GOSUMDB=off/GOFLAGS`)에 박힘. 빌드 시 항상 `GOMODCACHE=/app/.gomodcache GOSUMDB=off`. .gomodcache는 gitignore(호스트 /app 에 영속).
- 코드만: `docker restart kdb-app` / env(.env) 변경: `docker compose -f docker-compose.kdb.yml up -d kdb-app`(recreate).
- 수동 빌드: `docker exec -w /app kdb-app sh -c 'GOMODCACHE=/app/.gomodcache GOFLAGS=-buildvcs=false GOSUMDB=off go build ./internal/... ./cmd/...'`
- ★stale PATH 바이너리(/usr/local/bin/kdb-app 06-16) — 수동 CLI는 **`docker exec kdb-app /tmp/kdb-app <cmd>`**.
- **HEAD `f8cee3a`(main, 미push). push는 오너 요청 시.** .env(런타임 config)는 gitignore — 아래 §4 값으로 복원.

## §1 — 이번 세션 대형 작업 (오너 주도, 코드레드)
1. **OTT 캐스케이드**(Disney→Netflix, ott.go provider 일반화 `drainOTT`/`DrainOTTCascade`) + Disney+ 신규(`disney.go`) + **오매칭 오염 봉인**(`resolveOTTID` ko-제목 앵커 `ottTitleMatchesKo`, ott_test.go). 넷플릭스 오염 6건 복구. 실증: OTT 수율 낮음(source-ceiling).
2. **코드레드 8도메인 멀티에이전트 감사**(Opus xhigh) → 마스터플랜 `docs/runbooks/KDB_CODERED_PLAN_20260628.md`. **5 CR + 다수 QW 구현·배포**(§3).
3. **다국어 확보 8도메인 리서치** → 로드맵 `docs/runbooks/KDB_ACQUIRE_ROADMAP_20260628.md`. **핵심통찰: 갭은 빈칸 아닌 LLM 미검증표기, Wikidata 재조회 신규수율~0(꼬리는 niche라 외부소스 부재). 최선=내부 결정적 재속성.**
4. **확보 실행**: 로마자 재속성 863셀 + OpenCC 614셀 + Wikidata QID-pin refill. codex 12,529→11,263.
5. **gemma 풀가동**(§4): ai1+ai2 멀티엔드포인트, 전 LLM역할 gemma(dataqa 제외).
6. **admin 데이터소스 인벤토리**(§5).

## §2 — ★다국어 확보 신규 메커니즘 (CLI·source)
- **로마자 재속성**(`internal/kdb/romanize.go`, CLI `romanize-persons`): person/group 빈/codex vi·es·id·pt_br ← canonical_en(검증 로마자). 외부호출0. source `romanization`(prio7).
- **OpenCC zh↔zh_hant**(`internal/kdb/opencc_convert.go`, CLI `opencc-convert`): 검증 zh↔zh_hant 결정적 변환(s2t/t2s, github.com/longbridgeapp/opencc). source `opencc`(prio7).
- **권위 refill**(`enrich/orchestrator.go RefillFromWikidata/DrainAnchoredRefill`, CLI `refill-anchored [n]`): QID/기본정보 보유 엔티티의 빈/codex를 Wikidata QID-pin 직접조회로 업그레이드. ★QID-pin = 이름검색 동명이인 라벨복사 차단(`runWikidata` stored QID 우선 Fetch).
- **rejudge-rejects [n] [--dry]**(`rejudge.go`): rejected 백로그 Wikidata 재심→candidate 복원. ★term/일반어는 복원금지(오타 오염방지). 실복원 49명.
- migrations 0081(mydramalist) 0082(romanization) 0083(opencc) — source_priority 7. 전부 적용됨.
- ★음역 허용선: Latin person 로마자·zh↔zh_hant=허용. **Hangul→한자(zh) 직합성 금지**(오염최대). 작품 Latin은 로마자 아님(번역이라 제외).

## §3 — 코드레드 5 CR + QW (배포완료)
- **CR-1 오거부봉인**: gatekeeper(`agents/gatekeeper/agent.go`) Wikidata veto(고확신 reject 직전 존재검증→quarantine) + `resolveUnknownOne` tryRescue + rejudge CLI.
- **CR-2/D-9 매칭 어절경계**(`kdbapi/api.go`): 1자제외·2~3자 순수한글 경계정규식(합성어 오탐차단·조사부착보존)·4자+strpos. alias 대소문자무시.
- **CR-3 간체 zh**: entityLocaleColumns 'zh'→canonical_zh(간체), zh-hant/tw/hk/mo=번체.
- **CR-4 QID-pin + mixed-script 정리**(3건) + codex charset 가드(runCodexFallback).
- **CR-5 피드복구**: kill된 42피드 재활성화(enabled 15→57) + QW-13 피드헬스경보(<40).
- **QW-7** match locale_fallback 플래그. **D-15** research context_hint 200자 절단.

## §4 — ★gemma 라우팅/멀티엔드포인트 (.env 복원값)
오너 방침: **gemma를 워크플로우 전 LLM 지점에 최대 투입**(직번역 아님 — grounded). codex는 폴백+dataqa만.
```
KDB_GEMMA_BASE_URL=https://ai1.aiinplanet.com,https://ai2.aiinplanet.com
KDB_GEMMA_MODEL=gemma4-moe:latest,gemma-4-26b-a4b   # ai1=MoE고품질, ai2=4bit고속
KDB_GEMMA_CONCURRENCY=24
KDB_LLM_PROVIDER=gemma · CLASSIFY=gemma · DISAMBIG=gemma · FILL=gemma
KDB_LLM_DATAQA=codex   # ★gemma는 dataqa 대형배치 JSON 못 만듦("JSON 추출 실패") → codex 유지
KDB_ENRICH_GROUND=1 · STRICT=1   # grounded(직번역 차단). STRICT=0 절대금지(=직번역 firehose)
KDB_LOCALFILL_BATCH=30 · OTT_ENABLED=1 · DISNEY_ENABLED=1 · CORRECTION_TRUSTED_SOURCE=1
```
- gemma.go: `gemmaEndpoints()`/`pickGemmaEndpoint()` URL×MODEL CSV 라운드로빈. **추가 gemma = CSV에 ai3… 한 줄 append**(오너가 capacity 제공 시).
- ai1(MoE)은 reasoning이라 느림(~10-30s)·고품질, ai2 고속(0.8s). 라운드로빈 분산.
- 자동폴백 유지(gemma다운→codex, codex다운→gemma).

## §5 — admin 데이터소스 인벤토리
`/admin/settings` 하단 "데이터 소스"(`kdbadmin/sources.go` `sourceInventory()`): 전 소스를 tier·상태(연동/신규/실험/계획)·제공·벌크안전으로 표시. 새 소스 추가 시 여기 등록. codex-fallback=실제 gemma+그라운딩(레거시 라벨) 명시.

## §6 — ★정직한 제약 (반복 확인됨)
- **꼬리(codex 11k)는 gemma 용량 문제 아님 — 검색/소스 한계.** niche 엔티티는 외부 권위소스(Wikidata/TMDb)에 없고, 그라운딩(SearXNG) 검색결과도 없음. gemma는 idle(병목 아님).
- 더 빠르게 = ①검색 쓰루풋↑(IP밴 위험·오너 금지) ②직번역(오너 금지). → **현 상태가 안전 최대**. 꼬리는 grounded 점진 + verified_only 게이팅으로 관리.
- 벌크차단 주의: Wikidata/MB/VIAF 덤프=safe / iTunes·MediaWiki·OTT=throttle / 구글·네이버 SERP 스크래핑=즉시밴.

## §7 — 다음 작업 후보 (로드맵)
- **iTunes Search**(song_album ja/zh, 316 최난구간 — 넷플릭스 throttle 패턴, 무키 20/min) [계획]
- **Wikidata hanja(P1814=kana→ja)·MediaWiki langlinks 직접**(무QID 꼬리) [계획]
- **gemma-dataqa**(배치 20→5 줄이면 gemma 가능) — 원하면 구현
- 코드레드 잔여(M-effort): D-2 typed데드레터·D-3 dedup·D-5 song_album MusicBrainz·D-4 관측성. dataqa 작품검수·CJK ASCII청소=오너검토(오탐위험).

## §8 — 모니터링 (계속)
큐 캐치업 상태(candidate~82·unknown 0·empty~89). DataQA codex 복구됨. cycle 5~10분(<30분). 밀리는 곳 발견 시 gemma는 이미 max라 검색/소스 한계 확인. codex-fallback 순감 정상.

연관: [[reference-kdb-handoff]] [[KDB.handoff-20260627.md]] [[reference-kdb-gemma-discovery]] [[reference-kdb-websearch]].
