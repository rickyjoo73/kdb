# KDB handoff — 28차 (2026-07-05): kstory 연동 완비 + DB 백업 + match 기사맥락판별 + 병목 재해소

27차 [[KDB.handoff-20260704.md]] 이어짐(같은 흐름, 세션이 커져 분리). 오너 방침 유지: **온디맨드 — kstory 등 소비자가 한국어 키워드/기사를 던지면 KDB 가 공식소스로 빠르게 채워 다국어 서빙. "속도+정확도가 기본". 요청은 다 받고(거부X) 판단은 KDB 가.**

## §0 — 상태 체크 (새 세션 첫 명령)
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health   # 200
# 검증 tier 분포(서빙 신뢰도). 실측 authoritative~3200/evidenced~890/unverified~580
docker exec kdb-db psql -U kdb -d kdb -c "SELECT verification_tier,count(*) FROM kwave_entities WHERE status='active' GROUP BY 1;"
# ★kstory 유입(연동 확인 — source_url 누적)
docker exec kdb-db psql -U kdb -d kdb -c "SELECT count(*) FROM kwave_entity_research_queue WHERE source_url LIKE '%kstory%';"
# ★병목(발굴 e2e). 큐대기·처리 둘 다 <10s 기대(ground 비동기화 후)
docker exec kdb-db psql -U kdb -d kdb -c "SELECT round(avg(EXTRACT(EPOCH FROM(picked_at-created_at)))::numeric,1) 큐대기, round(percentile_cont(0.9) WITHIN GROUP(ORDER BY EXTRACT(EPOCH FROM(finished_at-picked_at)))::numeric,1) 처리p90 FROM kwave_entity_research_queue WHERE finished_at IS NOT NULL AND created_at>now()-interval '3 hours';"
# DB 백업(P0 해소 — 마지막 백업 시각). admin: /admin/ops/health
docker exec kdb-db psql -U kdb -d kdb -c "SELECT max(created_at) FROM kwave_kdb_backup_log WHERE status='ok';"
# 관리자 라우트(302=정상)
for p in /admin /admin/ops/health /admin/quality/verification; do curl -s -o /dev/null -w "$p %{http_code}\n" http://127.0.0.1:9101$p; done
```

## §1 — 이번 세션 한 일 (TL;DR, 25 커밋 PR#1)
1. **증분2 검증 tier**: `internal/kdb/verify` + mig0084(verification_tier/evidence/verified_tier_at) + 서빙노출(/v1/lookup·match) + 10분 주기스윕 + admin `/admin/quality/verification`. 결정론(권위앵커) + EvidencePass(네이버news+gemma).
2. **채움 속도 158s→15s**: 발굴 site-search 백그라운드화 + 신규키워드 즉시 kick(researchKick 채널).
3. **"추측=빈칸" 서빙게이트**: codex 추측 다국어값 제외(`stripLLMOnlyLocales`, lookup/prepare/match). `KDB_SERVE_HIDE_LLM_ONLY`(기본 on). 경계=codex 추측만(위키/음역/local-usage 유지).
4. **처리=즉시/점검=주기**: admin `/admin/ops/health`(처리현황·SLA·품질·소스헬스·백업) + 워커 이상감지 + 소스헬스 계측(`internal/kdb/sourcehealth`, 네이버쿼터·429) + 넷플릭스 OTT 발굴 백그라운드 보강.
5. **admin 전체메뉴 사용자시각 개선**: 대시보드 4구획 재설계 + 페이지별 intro 평이화(전문용어·테이블명·죽은내용 제거).
6. **★기사 맥락 판별 엔진**(오너 핵심통찰: 미해결·동명이인 근본원인=기사 안읽고 키워드만 검색): §2·§3.
7. **★kstory 연동 완비**: §4.
8. **★DB 자동백업**(P0 해소): §5.
9. **★병목 재해소**(ground 비동기화): §6.

## §2 — 기사 맥락 판별: 역추적 배치 (`cdec700`·`cb17de3`)
- **gemma 실측**: "미소" 4맥락(그룹멤버/가수/노래/일반어) → 4/4 정확. `enable_thinking:false` 필수. model=`gemma-4-12b-qat-q4`(ai2·ai4 round-robin, CONCURRENCY 12).
- **역추적**: URL 없어도 이름+단서로 네이버news 재검색→gemma 가 기사 읽고 판별. `EvidencePass` verdict 3분기:
  - **real** → evidenced 승급+정체 기록(세은→STAYC멤버).
  - **contaminated** → **자동 기각**(status='rejected' tombstone, ★완전삭제 아님). `kwave_kdb_dataqa_log`(verdict='retrace-contam-reject') revert 스냅샷. 복원: `kdb-app verify-entities revert-contam [n]`.
  - **unclear** → 유지.
  - ★교훈: 기각기준="애초에 K-엔티티가 아닐 때만"(해외인물·일반어·스포츠·의약품·종류근본오류). 실존은 세부부실해도 real(주호=SF9 잃을 뻔→revert 후 기준정정).
- CLI `kdb-app verify-entities evidence [n]`. 실측 100건→real 67·기각 15. 미해결 649→583(진행 중). **남은 583 은 저부하로 천천히**(오너 서버부하 우려 — api 서버 대량 gemma 자제).

## §3 — match 기사맥락 판별 (`30c5e38`, `internal/kdbapi/match_disambig.go`)
- `/v1/entities/match` 에 `disambiguate:true`(opt-in) → gemma 1회로 기사본문+매칭후보 읽어 **직접 지칭된 것만** 남김(일반어·부분매칭·동명이의 제거). 핫패스 보호: 기본off(mediafine 현행), 8s timeout, 실패시 원본유지.
- 실측: "차은우가 주연을 맡았다"→차은우만(이주연 제거), "진심·진지"→진 제거, "뉴진스 하니가 무대에"→하니·뉴진스 유지.
- ★동명이인은 실제 DB엔 19그룹뿐(대부분 중복). 주가치=오매칭·일반어 배제.

## §4 — kstory 연동 (완비, `36463ea`·`df4170c`·`0245802` + nginx)
- **오너 방침 "요청은 다 받아, 판단은 우리가"**: prepare 입구 게이트키퍼 거부 제거 → basicNameSanity(2자+·글자)만. type 오류(place)도 비워서 접수. ★버그수정: "슬기로운 의사생활 2"(실존)가 게이트키퍼에 오거부되던 것.
- **source_url 수용**: PrepareRequest/PrepareTerm.SourceURL + mig0085(research_queue.source_url). 추적·역추적 기반.
- **/api/* → /v1/* alias**: kstory 가 /api/health 로 폴링 → /v1 로 리라이트(`apiPrefixAlias`). nginx 무수정.
- **★진단(중요)**: prepare·키·source_url 다 완비인데 kstory 404 → 원인은 **kstory 가 http:// 로 요청(nginx 301 redirect)** + **health 를 kdb.aiinplanet.com 이 아닌 다른 host 로**. KDB 측 완비(https 200 실증). **kstory 측 수정 필요**: kdb-notify.sh 를 `https://kdb.aiinplanet.com` 로 통일. ★nginx conf 로 http /v1 프록시하는 건 auto-mode 차단(공유 인프라) — 오너가 직접 적용 or kstory https 전환 권장.
- **실측: kstory 실제 유입 확인**(source_url 219건). 연동 동작 중.

## §5 — DB 자동백업 (P0 해소, `971b03e`)
- `scripts/kdb-db-backup.sh`: `docker exec kdb-db pg_dump → gzip → 무결성검증(gzip -t·최소크기) → retention 14일`. **cron 하루 2회(03:00·12:00)** 등록됨(`crontab -l` 확인). 199MB DB→41MB gzip 5.8초.
- **복원 검증**: 임시DB 복원→6472건 일치 실측. 복원: `gunzip -c backups/kdb-<날짜>.sql.gz | docker exec -i kdb-db psql -U kdb -d kdb`.
- mig0086 `kwave_kdb_backup_log` + 스크립트가 성공시 DB기록 → admin `/admin/ops/health` 백업카드+경고(없음/26h+=crit).

## §6 — 병목 재해소 (ground 비동기화, `70e2fb0`)
- ★오너 통찰("gemma 여력충분한데 밀림=다른원인") 정확. 실측: 발굴 처리 p50 3s인데 **p99 150s·22%가 30s+** — `GroundEntity`(SearXNG 다회검색+gemma투표) 동기실행이 워커 점유 → 큐대기 313s. codex-fallback 은 0건(무관).
- **해결**: enrich L3.5 ground 를 백그라운드 goroutine(`groundSem`=4)으로. 발굴은 권위표기(Wikidata/TMDb) 완료 즉시종결, 현지표기는 백그라운드. groundHandled=true 로 L4 codex 스킵(추측=빈칸·롱테일방지). `KDB_ENRICH_GROUND_ASYNC=0` 복원.
- **실측**: 처리 1~5초(150s 롱테일 소멸), 큐대기 1.7초. **e2e ~7초**(대량유입에도 안 밀림).
- **.env 스케일**: RESEARCH_BATCH 16→32·WORKERS 4→6·INTERVAL 15→8s(백업 `.env.bak.20260704-research-scale`). 발굴 즉시경로는 gemma 거의 안 씀(CPU 0.06%)이라 감당.

## §7 — 남은 일 (다음 세션)
1. **역추적 배치 잔여 583건** — 저부하로 천천히(`verify-entities evidence`). api 서버 대량 gemma 자제(오너 우려). 야간 저빈도 or 소량 수동.
2. **발굴 시 source_url 기사 읽기** — kstory 가 준 기사 URL 을 gemma 가 읽어 동명이인(박시우 등) 정확 특정(현재 source_url 저장만). match disambiguate 와 유사 패턴.
3. **requests 페이지** — 기사본문+KDB 판별엔티티+모호 표시(오너 지목).
4. **필드 오염 정리** — 옥희류(실존하나 대표작만 오염). 엔티티 기각과 구분, 오염 필드만 정리(dataqa 계열).
5. **kstory http→https** — kstory 측 수정 대기(§4). 되면 유입 폭증 예상 → 병목 재관찰.
6. **PR #1 main 머지** — 25 커밋 브랜치 `admin/requests-keyword-locale-dashboard`. 오너 판단.
7. 기술부채: dead code(homonyms/unclassified) 삭제, Hermes DISAMBIG codex→gemma, 검토큐 bumpable 축소.

## §8 — 배포/운영 참조
- **배포**: 코드=`docker restart kdb-app`(go build→exec). **.env 변경=`docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app`**(재생성).
- **push**: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:admin/requests-keyword-locale-dashboard`.
- **컴파일**: `docker exec -e GOFLAGS=-mod=mod -e GOMODCACHE=/app/.gomodcache kdb-app sh -c 'cd /app && go build ./internal/... ./cmd/kdb/'`.
- **gemma 테스트**: ai2·ai4, `enable_thinking:false` 필수(reasoning 길면 타임아웃). 키=env KDB_GEMMA_API_KEY.
- **네트워크**: kdb-app=127.0.0.1:9100(api)·9101(admin) 호스트loopback. nginx(kdb.aiinplanet.com)→kdb-app:9100/9101. 다른 컨테이너에서 127.0.0.1:9100 은 자기자신(도달불가) — 도메인 or 컨테이너명.

연관: [[KDB.handoff-20260704.md]] [[reference-kdb-handoff]] [[feedback-authoritative-fill-sources]] [[project-kdb-ondemand-fill]] [[feedback-honest-visibility]].
