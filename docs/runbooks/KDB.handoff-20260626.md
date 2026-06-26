# KDB handoff — 19차 (2026-06-26): OTT(넷플릭스) 현지제목 그라운딩 신규 구축 + 신뢰성/provenance 분석

새 세션 첫 명령 = 이 문서 §0. 18차([[KDB.handoff-20260623.md]]) 모니터링 방침 계속 유효.

## §0 — 30초 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
# 자율 flag + 이번세션 신규: LOCALFILL_BATCH 10→30, CORRECTION_TRUSTED(기본 off)
docker exec kdb-app sh -c 'echo HERMES=$KDB_HERMES_ENABLED GROUND=$KDB_ENRICH_GROUND STRICT=$KDB_ENRICH_GROUND_STRICT REGROUND=$KDB_LOCALFILL_REGROUND LOCALFILL_BATCH=$KDB_LOCALFILL_BATCH CORRECTION_TRUSTED=${KDB_CORRECTION_TRUSTED_SOURCE:-unset} OTT_INTERVAL=${KDB_OTT_MIN_INTERVAL_MS:-10000}'
# 소스 추이(codex-fallback 순감 정상) + 신규 netflix source
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('codex-fallback','wikidata-label','tmdb','netflix','local-usage') GROUP BY s ORDER BY count(*) DESC;"
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'hangul_leak='||count(*) FROM kwave_entities WHERE status='active' AND (canonical_es~'[가-힣]'OR canonical_vi~'[가-힣]'OR canonical_en~'[가-힣]');"
```
**배포**: 코드만 `docker restart kdb-app`(go build→exec) / env(.env) 변경 시 `docker compose -f docker-compose.kdb.yml up -d kdb-app`. 빌드: `docker exec -w /app kdb-app sh -c 'GOFLAGS=-buildvcs=false go build ./internal/kdb/... ./cmd/...'`. **★HEAD `09b6f99`(main, 미push) — 이번 세션 코드 커밋됨, push는 오너 요청 시.**

## §1 — 이번 세션 요약 (2026-06-26)
모니터링 시작 → 신뢰성/현지제목 품질 이슈 추적 → **OTT 그라운딩 신규 구축**으로 귀결.
- 시작: 요청 처리(24h 492건 전 200)·수정요청 루프 정상 확인. corrections 8건 처리(로마자 검증·vi 현지제목 복원).
- candidate 정리: 비-K 15건 reject(외국 인물·중복본).
- 데이터 현황: active~4,311, codex-fallback **12,726→12,532**(reground·tmdb-refresh·ott로 순감), local-usage 272→417, hangul_leak 0.

## §2 — ★OTT(넷플릭스) 현지제목 그라운딩 (신규, `internal/kdb/ott.go`)
**목적**: Wikidata·TMDb에 없는(또는 codex-fallback인) 작품(drama/show/movie) locale을 넷플릭스 지역페이지에서 권위 현지제목으로 확보.

**파이프라인**(`DrainNetflixWorks`, CLI `kdb-app ott-fill [n|작품명]`):
1. `site:netflix.com {ko}` (SearXNG **직접호출** `searxngDefault`, engines 제한 없이) → title ID. ★주의: 키워드 site-search는 현지페이지에 한국어 없어 **다른 작품 오매칭**(실측 vn→Nguyet lan). 반드시 ID 앵커.
2. `site:netflix.com/{loc}/title/{id}` → 그 작품 페이지 결과만(지역 미가용=0건→빈칸유지, fabrication 안 함).
3. **gemma 추출**(`localFillVote` 재사용) → 현지제목.
4. **★non-ASCII 가드**(`isPureASCII`): 순수 ASCII=영어leak→거부. ja/zh_hant/vi/es만(pt_br/id는 ASCII언어라 영어 구분 불가 → `netflixLocalePath`에서 제외).
5. source=`netflix`(prio 4, migration 0080 + source_priority.go + api.go provenance verified-tier). can_replace로 codex만 교체, wikidata/tmdb 보존.

**★IP 차단 방지(오너 절대방침 "벌크 금지")**: 매 조회 사이 **10초 pacing**(`KDB_OTT_MIN_INTERVAL_MS` 기본 10000). 절대 일괄 버스트 금지. `websearch.Chain`은 engines=wikipedia,bing 제한이라 netflix site:를 못 찾음 → OTT는 `searxngDefault`(직접·engines무제한) 사용, pacing이 throttle 대신.

**검증됨**: 굿파트너2 ja="グッド・パートナー〜離婚のお悩み解決します〜", 모범형사2 vi="Thanh tra mẫu mực"·es="El buen policía", 월간남친 vi="Bạn trai theo yêu cầu", 사랑의가족 vi 등. netflix source ~8+.

**★교훈(디버깅서 확인)**: ①넷플릭스 스니펫 구분자 `|`/`-` 혼재 ②es/id/pt는 넷플릭스가 영어로 서빙(영어leak) → non-ASCII 가드로 봉인 ③gemma는 watch동사(Ve/Tonton)·영어 못 거름 → 가드 필수 ④넷플릭스 유통작은 TMDb도 보유(무소스∩넷플릭스는 좁음) → OTT 대상은 "codex locale 보유 작품 전체"로 스코핑(무소스 한정 X).

**제한/수율**: 작품당 부분 채움(아시아 locale 위주, 지역 라이선스 차이). 시즌 엔티티(시즌/시리즈) 제외(넷플릭스 시리즈명이 codex "...시즌2"보다 덜 구체적). 대상 ~411 작품(codex locale·비시즌).

## §3 — 신뢰성/provenance 분석 (오너 우려 "신뢰 붕괴")
- **provenance 이미 구현·노출**: 값별 `canonical_<loc>_source`(wikidata-label/tmdb/codex-fallback/rss-observation:사이트명/operator/netflix), API `locale_source`·`provenance`·`source_urls`. codex-fallback→`llm-only`로 매핑, **`verified_only=true`가 llm-only 제외**(소비자 게이팅 = 영어leak 차단의 즉효). issuetalk/미디어파인이 이 플래그만 켜면 즉시 회복.
- **정정 출처 = 전부 미디어파인**(mediafine.co.kr, consumer 19f59fa9). issuetalk는 KDB 미등록(미디어파인 뒤 또는 별개). corrections 482건 전부 미디어파인.
- **source-ceiling 확증(6방식)**: codex-fallback 12,532 꼬리는 Wikidata·TMDb·KMDb·Langlinks·tmdb-refresh·Netflix **어디에도 없는** 데이터. 인위적 드레인 무의미, 사람 출처(corrections)가 유일한 길.

## §4 — 권위소스 점검 (전부 이미 구현됨)
- Wikidata(labels+sitelinks/**langlinks** 13,702)·TMDb(translations+alt_titles 2,710)·KOFIC(9)·MusicBrainz 다 가동. **KMDb는 KR/EN 제목만**(외국 locale 무용 — 연동해도 못 채움). **tmdb-refresh**(`kdb-app tmdb-refresh [n]`) 실행 → 작품 codex 18건 권위 교체.
- 신규 권위소스 연동 여지 없음(다 있음). 남은 건 source-ceiling 꼬리 + OTT(부분).

## §5 — 운영 결정 / 다음 작업
- **OTT autopilot 편입**: 현재 수동 CLI(`ott-fill`). 상시 throttle 드레인 원하면 autopilot step 편입(코드). **단 10초 pacing 유지 필수**.
- **대규모 OTT 드레인**: 411작품 throttle(10초)이라 수시간. 백그라운드 `ott-fill <N>` 점진 실행. 영어leak는 non-ASCII 가드로 봉인됨.
- **corrections 신뢰출처 플래그**(`KDB_CORRECTION_TRUSTED_SOURCE=1`): 빈칸+신뢰도메인 출처→즉시반영. 기본 off로 배포됨. ON시 미디어파인 출처기반 빈칸채움 활성(보안: 외부데이터 운영DB 반영이라 신중).
- **불량 fill 청소 패턴**(참고): netflix source 값 중 watch동사 접두(`^(Ve|Ver|Tonton|Nonton|Watch|Xem|Assistir) `) 또는 순수ASCII(영어leak)는 블랭크.

## §6 — 모니터링 (18차 §9 계속)
오너 방침: 모니터링 지속, 이상시만 개입. codex-fallback 순감 정상·급증시 누수회귀 추적(§7/§7.1 of 18차). hangul_leak=0 유지. cycle <1800s. 자세한 baseline·경보표 = 18차 §9.

연관: [[reference-kdb-handoff]] [[reference-kdb-websearch]] [[project-kdb-cutover]].
