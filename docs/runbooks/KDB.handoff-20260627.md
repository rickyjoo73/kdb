# KDB handoff — 20차 (2026-06-27): 운영결정 3건 실행 + 신규소스 탐색(나무위키/MDL) 실증 + source-ceiling 재확인

새 세션 첫 명령 = 이 문서 §0. 19차([[KDB.handoff-20260626.md]]) OTT·18차 모니터링 방침 계속 유효.

## §0 — 30초 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health
curl -s -o /dev/null -w 'admin=%{http_code}\n' http://127.0.0.1:9101/healthz
# 자율 flag(이번세션 신규: OTT_ENABLED·CORRECTION_TRUSTED)
docker exec kdb-app sh -c 'echo HERMES=$KDB_HERMES_ENABLED OTT_ENABLED=$KDB_OTT_ENABLED OTT_BATCH=$KDB_OTT_BATCH CORRECTION_TRUSTED=$KDB_CORRECTION_TRUSTED_SOURCE'
# 소스 추이 + 신규 netflix/mydramalist
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT s,count(*) FROM (SELECT unnest(ARRAY[canonical_en_source,canonical_ja_source,canonical_vi_source,canonical_id_source,canonical_es_source,canonical_pt_br_source,canonical_zh_source,canonical_zh_hant_source]) s FROM kwave_entities WHERE status='active') t WHERE s IN ('codex-fallback','wikidata-label','tmdb','netflix','mydramalist','local-usage') GROUP BY s ORDER BY count(*) DESC;"
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'hangul_leak='||count(*) FROM kwave_entities WHERE status='active' AND (canonical_es~'[가-힣]'OR canonical_vi~'[가-힣]'OR canonical_en~'[가-힣]');"
```
**배포**: 코드만 `docker restart kdb-app`(go build→exec) / env(.env) 변경 시 `docker compose -f docker-compose.kdb.yml up -d kdb-app`. ★주의: 컨테이너 PATH `kdb-app`(/usr/local/bin)은 **06-16 stale 바이너리** — 신규 서브커맨드(ott-fill·mdl-fill) 누락. 수동 CLI는 **`docker exec kdb-app /tmp/kdb-app <cmd>`**(현 소스 빌드본) 사용. **★HEAD `9bcba9c`(main, 미push) — 이번 세션 코드 커밋, push는 오너 요청 시.**

## §1 — 이번 세션 요약 (2026-06-27, 오너 위임 자율작업)
오너 지시: "밀린 것 처리 + 넷플릭스처럼 신규소스 발굴 + 막힌 것 뻥 뚫기"(취침 중 자율).
1. **운영결정 3건 전부 실행**(19차 §5):
   - **corrections 신뢰출처 flag ON**(`KDB_CORRECTION_TRUSTED_SOURCE=1`): allowlist 도메인(방송사·wiki·TMDb)+**빈칸만** 즉시채움, 기존값 보존·charset가드·provenance기록. 안전 검증 후 ON.
   - **OTT autopilot 편입**(`runAutonomousOTT`, cmd/kdb/main.go): cycle tail에서 `KDB_OTT_BATCH`(3)/cycle·10초 pacing 드레인. **발화 확인**(hermes_runs Role=OTT, processed=3). flag `KDB_OTT_ENABLED=1`.
   - **OTT 점진 드레인**: 백그라운드 `ott-fill 200`(단일프로세스 10초 pacing) 가동.
2. **corrections 6건 권위검증 처리**: 전부 reject(빈칸>틀린값/영어leak/활동명규칙) + **야왕 wikidata 오링크 교정**(Q626622 야구선수→Q28685658 드라마). ※corrections CLI = `kdb-app corrections list|approve|reject`.
3. **샘 킴/샘 김 = 중복 아닌 동명이인 확정**(18차 "dup" 오판 정정): 샘 킴=셰프 김희태(Wikidata Q18566804·나무위키 일치), 샘 김=가수. disambig 이미 `(셰프)`/`(가수)`로 정상 구분됨(병합 금지). **손댈 것 없음**.

## §2 — ★신규소스 탐색 실증 (나무위키/MDL/AsianWiki) — 정직한 결과
오너 "넷플릭스처럼 다른 사이트" 지시로 외국제목 소스 후보를 **실측 테스트**:
- **나무위키**(namu.wiki): 컨테이너 직접 fetch **가능**(Cloudflare 무차단, 서버사이드 렌더). 그러나 **작품 외국어 제목을 거의 안 실음**(오징어게임·킹덤아신전 = kana 0·hanja 0). 한국어 위키라 locale 필러 **부적합**. 강점은 **본명/직업/출생(동명이인 구분)·일부 한자명** — 별개 파이프라인(미구축).
- **MyDramaList**(mydramalist.com): 직접 search(200)·fetch **가능**. "Also Known As"에 다국어 제목 보유하나 **유럽어·로마자 위주, ja/zh는 불균일**(Queen of Tears는 涙の女王 있음, CLOY는 없음).
- **AsianWiki**: 403 차단.
- **결론**: codex-fallback **외국제목 꼬리는 진짜 source-ceiling**(19차 §3 재확인, 이번엔 나무위키/MDL 직접 실측 추가). 넷플릭스 저과일 소진(이번 세션 netflix +2=8→10, 야간드레인 69작품 처리 **신규fill 0**). 다수 codex ja값은 실제 일본 공식제목 형태(스틸러="スティーラー～七つの朝鮮通宝～")=오류 아님.

## §3 — ★MDL 그라운딩 소스 구축 (완료·테스트·셸브)
넷플릭스 OTT 패턴 그대로 **완전 구현**(오너 "넷플릭스처럼 시도" 이행):
- **`internal/kdb/mdl.go`**: `DrainMDLWorks` — 작품 ja를 MDL "Also Known As"에서. **한국어 원제 앵커**(오매칭 차단=넷플릭스 ID앵커 등가)+**가나 결정적탐지**(LLM불요)+non-ASCII/charset 가드. source=`mydramalist`(prio 7, **codex만 교체**·권위소스 불가침). 4초 pacing. 7d 쿨다운(field='mdlfill').
- 배선: `source_priority.go`(SourceMyDramaList=7·Mark "m") + **migration 0081**(kdb_source_priority 'mydramalist'→7, 적용·검증완료) + `api.go`(provenance 'community-db', **verified tier 제외**=verified_only 비노출) + CLI `mdl-fill [n|작품명]` + source_priority_test(0079→**0081** 갱신, netflix/disney/mydramalist 정합검증 추가).
- **실증(12+ codex-ja 작품)**: **수율 0**. 원인 = ①앵커가 오매칭 정확차단(사랑의가족→일본드라마"愛のお荷物"·스틸러→"Karma" 거부) ②앵커 OK여도 MDL에 JP제목 없음(NO_JA). **빌드·테스트 통과, 코드 정확·안전**.
- **운영 판단**: niche 수율 0이라 **autopilot 미편입·flag 없음·수동 CLI만**(`docker exec kdb-app /tmp/kdb-app mdl-fill N`). MDL 커버리지 개선/인기작 스팟용으로 보존. zh/zh_hant는 간/번체 구분 리스크로 v1 제외(미구현).

## §4 — 미해결/주의 (오너 검토 권장, 자율처리 안 함)
- **CJK locale ASCII 오염(codex)**: ja 308·zh 519·zh_hant 586건이 순수 ASCII. **단, 다수가 정당**(ITZY·NiziU·Mrs.GREEN APPLE 등 그룹/인물 라틴 공식명) → **일괄 블랭킹 금지**(대량오탐, 핸드오프 14차 교훈). 작품(drama/show/movie)의 ASCII zh/ja 일부는 진짜 leak(퍼스트닥터 ja="First Doctor"·비긴즈유스 zh="BEGINS≠YOUTH")이나 영어공식제목(비긴즈유스 ja)과 구분 필요 → **dataqa(gpt-5.5) 의미판정**으로 처리 권장, 수동 블랭킹 비권장.
- **stale PATH 바이너리**: /usr/local/bin/kdb-app 06-16. 수동 CLI는 /tmp/kdb-app. 근본해결=이미지 재빌드(빌드없는배포 방침과 트레이드오프) 또는 컨테이너 내 cp.

## §5 — 모니터링 (18차 §9 / 19차 §6 계속)
오너 방침: 모니터링 지속, 이상시만 개입. codex-fallback 순감 정상·hangul_leak=0·cycle<1800s. corrections 큐 주기 확인(`kdb-app corrections list`). OTT autopilot 자율가동(batch3/cycle). 야간 ott-fill 200 백그라운드(PID 변동, ~3h 후 종료, 무해).

## §6 — 다음 작업 후보
- **Disney+ OTT 소스**(source_priority에 disney=4 이미 정의): 넷플릭스 mdl.go/ott.go 패턴으로 구현 가능. 단 K-콘텐츠 적어 수율 넷플릭스보다 작을 것.
- **나무위키 동명이인 보강기**(별개 파이프라인): 본명/직업/출생 추출로 disambiguation·한자명. locale필러 아님.
- corrections flag ON 효과 관찰(미디어파인 등 신뢰출처 빈칸채움 활성).

연관: [[reference-kdb-handoff]] [[KDB.handoff-20260626.md]] [[reference-kdb-websearch]] [[project-kdb-cutover]].
