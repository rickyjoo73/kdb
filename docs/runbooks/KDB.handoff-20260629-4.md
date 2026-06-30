# KDB handoff — 25차 (2026-06-29): 2단계 품질판정 (Gemma 거름망 → Claude Sonnet 최종판단) + 오염 DB

24차 [[KDB.handoff-20260629-3.md]] 이어짐. 오너 지시: ①오염 DB 분리+재유입 자동차단 ②주기적 오염/실제 구분 로직 ③언어별 누락 ④Gemma 판단 품질 파악 ⑤**최종판단을 claude(Sonnet)로**. 자율 완수 세션.

## §0 — 상태 체크
```sh
curl -s -o /dev/null -w 'api=%{http_code}\n' http://127.0.0.1:9100/v1/health   # 200
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT status,count(*) FROM kwave_entities WHERE status IN ('active','rejected','candidate') GROUP BY status;"
# adjudicate 현황
docker exec kdb-db psql -U kdb -d kdb -At -c "SELECT 'reject',count(*) FROM kwave_entities WHERE notes LIKE '%[adjudicated:claude reject]%' UNION ALL SELECT 'rescue',count(*) FROM kwave_entities WHERE notes LIKE '%[adjudicated:claude ok]%';"
```

## §1 — 결론 (TL;DR)
- **2단계 품질판정 파이프라인 구축·배포·가동.** PreGate(규칙) → Gemma 분류 → Gemma 거름망(scope/contam-review 플래그) → **Claude Sonnet 최종판단** → 비-K/정크 reject(오염DB) / 실재K 구제.
- **Gemma 판단 평가(실측)**: 명백한 건 정확, 애매한 건 보수적 과-플래그 + 일부 false-neg(로버트패틴슨 통과). **= 우수한 1차 거름망, 부적합한 최종판사.** → Claude를 최종판사로.
- **Claude(Sonnet) 최종판단 실측**: Gemma 약점 양방향 교정. 백로그 적용 결과 reject ~114+ / rescue ~49+ (강남·준 같은 K-pop 외국적멤버 정확 구제, 메릴스트립·오타니·이재명·앤해서웨이 정확 reject). 호출당 ~5s.
- 오염 재유입 자동차단은 이미 작동(rejected tombstone + Observe + SweepPromote candidate-only + dataqa isSuppressed). [scope:review]/[contam:review]/[adjudicated:*] 마커로 오염 DB 가시화.

## §2 — 빌드 (HEAD `21e22c2` 이후, 전부 push)
- **contam-review** (`autopilot/sweep.go::stepContamReview` + CLI `contam-review` + RunContamReview): 비-person 고유명사를 **오너 휴리스틱(공식 외국어 적을수록 오염후보 1위) 우선순위**로 Gemma 재판정 → 정크/범위밖만 [contam:review] 플래그(자동reject 안 함). ★song_album 제외(Gemma가 영어제목 K-곡 과-플래그 실측: 백현 Winter Ahead·EXID Ah Yeah). authoritativeForeignSources = verified-tier 집합.
- **claudejudge** (`internal/kdb/claudejudge/`): claude CLI(Sonnet) 헤드리스 호출. 컨테이너 마운트 경로의 self-contained ELF 바이너리 실행(node 불요), 구독 인증 HOME=/app(.claude.json+.credentials.json), --model sonnet. resolveBin()이 versions/ 최신 자동탐색. 직렬화(mutex). Available()로 미설치 폴백.
- **claude_adjudicate** (`internal/kdb/claude_adjudicate.go::DrainClaudeAdjudicate` + CLI `claude-adjudicate [n] [--reject]`): Gemma가 [scope:review]/[contam:review] 플래그한 의심군만 claude 최종판정 → k_entity=false → reject(notes '[adjudicated:claude reject] 근거' 감사기록) · k_entity=true → 플래그해제+[adjudicated:claude ok](구제). --reject 없으면 권고만(dry).
- **autopilot 연속**: `runAutonomousAdjudicate`(매 cycle 10건) gated `KDB_CLAUDE_ADJUDICATE_ENABLED=1`+`KDB_CLAUDE_ADJUDICATE_AUTOREJECT=1`(.env line 103-104 추가됨). Hermes run row "ClaudeAdjudicate".

## §3 — 운영/롤아웃 현황 (✅ 완료·가동)
- **백로그 reject 완료**: `claude-adjudicate 300 --reject` → judged=238 **reject=141 rescue=73**. active 4,546→4,416, rejected 1,044→1,191. 남은 미판정 ~33(autopilot 점진 소진).
- **autopilot 연속 ON 활성 확인**: ADJUDICATE_ENABLED=1·AUTOREJECT=1 컨테이너 env 주입됨, 매 30분 cycle runAutonomousAdjudicate(10건/cycle)가 신규 [scope:review]/[contam:review] 의심군 자동 최종판정.
- ★**중요 ops: `.env` 변경은 `docker restart` 로는 안 먹는다**(compose `env_file:[.env]`는 컨테이너 *생성* 시만 로드). 반드시 **`docker compose -f docker-compose.kdb.yml up -d --no-deps kdb-app`** 로 재생성해야 .env 재주입됨. (코드만 바뀌면 docker restart 로 충분 — CMD 가 /app 소스 재빌드.)
- 빌드: `docker exec -w /app kdb-app sh -c 'GOMODCACHE=/app/.gomodcache GOFLAGS=-mod=mod go build -o /tmp/kdb-app ./cmd/kdb'`. 코드배포=`docker restart kdb-app`. **env배포=`docker compose up -d --no-deps kdb-app`**.

## §4 — Claude 판단 품질·비용 메모
- 품질: reject(메릴스트립·찰리푸스·오타니·미요시아야카=외국, 이재명=정치인, 법정=승려 비-K-엔터) 정확. rescue(강남·준=외국적 K-pop멤버, 선데이·유정=실재 가수) 정확. 경계: 애매한 단일 한국이름(옥순·승유)은 "식별불가"로 reject — 보수적이나 일부 실재 가능(복원: rejudge-rejects + notes 감사). 법정/이재명은 한국인이나 K-엔터 아니라 reject = scope 정의상 맞음.
- 비용: 호출당 ~5s, **거름망 통과분(의심군)에만** → 전수 아님(저볼륨). 구독 인증이라 구독 사용량 소모. 모델 sonnet 고정(KDB_CLAUDE_MODEL).
- 약점 보완: claude도 무오류 아님(애매 단일명) → 모든 reject 복원가능+감사기록. 필요시 KDB_CLAUDE_ADJUDICATE_AUTOREJECT=0 으로 권고-only 전환.

## §5 — 이번 세션 전체 누적 (병목→소스확장→품질판정)
1. codex 11,188→~8,000 (#1~6: 로마자 재라벨·게이팅·ko게이트). 2. 소스확장(iTunes·Discogs·langlink-upgrade·autopilot SourceExpand). 3. **품질판정(contam-review + Claude 2단계)**. 커밋 체인: de445da→7e66abe→e5fc02f→d072755→feaef31→83e9f4a→**21e22c2**(전부 origin/main push).

## §6 — 다음 세션
- 백로그 adjudicate 완료 확인 → 재시작 → autopilot 연속 ON 검증(Hermes "ClaudeAdjudicate" run row).
- 남은 [scope:review]/[contam:review] 잔량 모니터(autopilot이 점진 소진).
- 오염 DB 통합 admin 뷰(directive 1 가시화)는 마커로 쿼리 가능하나 전용 페이지는 미구축(선택).
- claude reject 표본 주기 감사(애매 단일명 오삭제 여부) — 의심시 AUTOREJECT=0.

## §7 — 후속 처리 (06-30, 오너 자리비움 자율세션)
- **★핑퐁 버그 적발·종결 (HEAD `b0cadaa` push)**: Gemma `stepScopeReview`/`stepContamReview` 가 Claude 가 이미 구제(rescue)한 항목을 매 cycle 재플래그 → claude_adjudicate 재판정 무한루프. 원인=Claude rescue 는 `[scope/contam:review]` 마커만 제거하고 `[adjudicated:claude ok]` 만 남기는데 sweep SELECT 가드는 `[scope:%`/`[contam:%` 부재만 봐서 ok-판정 항목을 재선택. 수정=양 step 의 SELECT·UPDATE 4곳에 `NOT LIKE '%[adjudicated:claude ok]%'` 가드 추가(Claude 최종판사 판정을 Gemma 가 못 덮어쓰게). 일회성 정리=이미 ok-판정인데 마커 남은 **53건(scope 41/contam 12)** rescue 동일 정규식으로 제거(verdict 보존). **결과: scope/contam:review 잔여 0** — 백로그 전수 소진 + 핑퐁 영구 차단.
- **백로그 adjudicate 수동 마무리**: `claude-adjudicate 40 --reject` ×2 → judged 64, reject 36(원 디렉션·미세스 그린 애플=J-rock·스파이더맨·샤넬·한양대·기아 타이거즈·박종철 등) / rescue 25(TFN·CUTIE STREET·광개토대왕·바이브·중앙그룹 등). 누적 adjudicated reject 246 / ok 118.
- **★오너 지시: 비-K 뉴스사이트 크롤링 중단** ("다른 뉴스사이트 크롤링 고유키워드 수집 불요, 자체 유입 충분"). `kwave_news_whitelist` 의 **gnews-fallback 7개 도메인 비활성화**(20minutos·elmundo·clarin·nacion·milenio·larepublica·los40 = `news.google.com/rss/search?q=site:DOMAIN (BTS OR BLACKPINK OR Kpop OR "drama coreano"...)` 식 스페인/중남미 신문 K-키워드 크롤링=현지표기 수집). enabled=false (가역적). 핵심 K-피드(k-content/kpop=soompi·KBS·daum·koreastardaily 등) **49개 유지**. rss_poller·site_search 둘 다 `WHERE enabled=true` 라 즉시 제외(재시작 불요), 피드헬스 임계(40) 위라 오경보 없음. 재활성화=admin `/admin/entities/whitelist` 또는 `UPDATE kwave_news_whitelist SET enabled=true WHERE category LIKE '%gnews-fallback%'`.
- **승인건 확인**: corrections status='approved' 7건은 `Approve()` 가 트랜잭션에서 엔티티 적용+상태변경 동시 수행 = **이미 반영 완료**(374/375 Yuk Jun-seo·379 With You·382 Hwang In-youp·427 Lee Byung-hun 등 엔티티 값 검증). 별도 적용대기 없음. proposed 4/pending 1 은 DrainWikidataVerified·ReapStale(7일만료) 가 자동 처리.

연관: [[KDB.handoff-20260629-3.md]] [[KDB.handoff-20260629-2.md]] [[reference-kdb-handoff]] [[reference-kdb-gemma-discovery]] [[feedback-honest-visibility]].
