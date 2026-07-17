# KDB 핸드오프 33차 (2026-07-16 밤 ~ 07-17) — 워크플로우 보드 · 게이트 가속 · 에이전트 시스템 · KMDb

직전: 32차(07-13) 인테이크 자동검증. 이번 세션 = 오너 집중 피드백 세션(주먹구구 지적 →
목적·측정·책임 체계 재정립). 최종 배포 `kdb-app:kmdb-20260717-1`. 전 커밋 push 완료.

## §0. 새 세션 첫 명령 (상태 30초 파악)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s https://kdb.aiinplanet.com/v1/health   # version 확인
./scripts/kdb-dashboard.sh | head -40
docker exec -i kdb-db psql -U kdb -d kdb -c "
SELECT count(DISTINCT entity_ko) AS unresolved FROM kwave_entity_research_queue q
WHERE precheck_status='review'
  AND NOT EXISTS (SELECT 1 FROM kwave_entities e WHERE e.status='active'
    AND (e.canonical_ko=q.entity_ko OR q.entity_ko=ANY(e.aliases_ko)));"  # 미해결 보류(2105에서 감소 확인)
docker logs kdb-app --since 12h 2>&1 | grep -cE "type-retrace\(daily\)|KMDbDrain|triage"  # 자동 루프 생존
```

## §1. 이번 세션 산출물 (전부 배포·커밋됨)

**A. 워크플로우 보드 + admin 재설계**
- `/admin/workflow` 5단계 칸반(유입→심사→발굴→채움→서빙): 카드=신규 실시간, 배지=백로그.
  전체폭, 8언어 칩(카드·헤더 동일 기준), 한 줄 경고행, 단계 범례, 영문코드 전면 한글화(labels.go).
- nav 흐름순 재편 + **에이전트 메뉴**: `/admin/agents` — 판단주체 10종(이름·도메인·역할·스킬·상태env·감독·라이브실적).
- 소비자 성적표: `/admin/ondemand/consumers` 계약 준수율(A/B/C). 실측: kstory A, 나머지 3사 C.

**B. 심사 게이트 — 병목 구조 해소 (핵심 성과)**
- 실측: 유입 927/일 vs 해소 118/일(+377/일 적체, 방치 3,519) → **처리 가능 대기 1건**까지 소진.
- 수단: ①게이트 규칙 v3(commodity_term 광고류 / single_char 비인물 / **latin_passthrough** 로마자
  곡제목·무타입=번역불필요 자동종결, 소급 542) ②SearXNG 웹폴백(네이버 1,000/일 쿼터 병목 해제,
  화이트리스트 매체만 증거) ③병렬 6(`KDB_INTAKE_AUTOVERIFY_CONCURRENCY`)+배치 40+정규화키 dedup
  ④**좀비 보류 결함 수리**(기각엔티티 존재 행이 영구 limbo 500건 → Run 선두 자동 종결)
  ⑤TTL 21일(무근거 보류 자동 기각 — 무인 수렴 보장).
- 잔여 백오프 ~2,300은 24h 내 자동 재검증(설계된 수렴 — 강제 재시도 금지, 같은 검색 낭비).

**C. 에이전트 시스템 (오너 방침: 자동 규칙보다 에이전트 판단)**
- **KeywordTriage**(`keyword_triage.go`): gemma 오염 판별 + **기각 이중판정**(변호인 2차 동의 시만).
  스위프: `triage-exhausted`(보류) / `triage-candidates`(3일+ 정체 후보). 전 기각 복원 가능.
- **골든셋 게이트**: `kdb-app triage-eval` — 20케이스, **오거부 0 절대기준**. 프롬프트 변경 시 필수.
  실증: 이중판정 v1 FAIL(과잉변호)→차단→v2 PASS. ★07-17 오거부 사고("내꺼중에 최고" 등 문장형
  실제 제목 기각)→전량 복원(24+176)→제목보호 프롬프트 v2. 상세=`docs/KDB_GEMMA_DESIGN.md` §6.
- **TypeVerifier**: `verify-entities type-retrace [n]`(무편향 재검색→gemma 타입 재판정, dataqa_log
  스냅샷) + **매일 05:30~07 KST 자동 150건**(main.go 내장, KDB_TYPE_RETRACE_DAILY=0 끔).
  실적 100건: 교정 22(전부 타당)·확인 27. 잔여 ~600.

**D. 공식소스**
- **KMDb 가동**: 키=admin 설정 DB(`KDB_KMDB_API_KEY`, 일 100건 한도) — 오너가 재제공(과거 미저장
  이력 사과). `internal/kdb/kmdb/`+`kmdb_drain.go`(movie 승급+en 채움, 정확제목만, 동명모호 스킵).
  레인 시간당 4건 + `drain-kmdb [n]`. 첫 25건→승급 2(검은꽃→Black Flower).
- **승급 드레인 4종 ON**(.env): rejudge·MusicBrainz·KOFIC·WDperson — 꺼져 있었음(후보 적체 원인).

**E. 문서/계약**
- `docs/KDB_WORKFLOW_DESIGN.md`(파이프라인 SSOT: 단계·SLA·중복규칙·결함), `docs/KDB_GEMMA_DESIGN.md`
  (미션·지표3·판단지도·프롬프트5원칙·품질게이트·사고이력), `docs/KDB-REQUEST-GUIDE.md`+공개 `/docs` §5
  (요청 계약 v1.0: terms[].ko/type/context+source_url 필수, 타입판별표, 금지목록).
- 데이터 교정: K-POP en(K'Pop→K-pop), 김인숙(show→person), 꽃·새 강등(en 음차 오염 제거), 생일광고 기각.

## §2. 다음 작업 (우선순위)

1. **동명이인 충돌 ~202건 자동 라우팅** — 무인화 마지막 5%. 기존 Disambiguator(codex·gemma 메시)로
   conflict-parked review 행을 연결. 오너가 무인 운영 강하게 요구함.
2. **밤사이 수렴 확인**: 미해결 보류(2,105→?), 백오프 재검증 결과, triage/type-retrace/KMDb 일일 실적.
3. **로마자 라벨 오염 스위프**: wikidata-label 이 음차(Kkot류)를 준 active 잔존분 — romanize(ko)==en
   휴리스틱으로 후보 추출→재검증. 2자 일반명사 제목(봄·바다) 포함.
4. **person ja 규칙 변환 설계안**(가타카나 결정론 변환 — MT 아님) — ja 빈칸 1,914 해소 레버,
   **오너 결정 필요**(기계번역 금지 원칙 저촉 여부).
5. resolve-unknowns no-op 원인(139건 0.2초 스킵 — codex-free 무력화 의심) 조사.
6. 골든셋 확장(타입 재판정용 별도 셋) + KMDb person 영문명(actorEnNm) 활용 검토.

## §3. 오너 액션 대기

- **C등급 3사(issuetalk·미디어파인·trendbiz)에 요청 계약 전달** — 보류 적체·오염의 근본 해결.
  전달 문구: "https://kdb.aiinplanet.com/docs §5 대로 세팅" (kstory=레퍼런스). 준수율은 성적표에서 추적.
- **네이버 검색 API 쿼터 확장**(현 1,000/일) / **KMDb 트래픽 증대 신청**(현 100/일) +
  **KMDb 활용사례 제출 기한 2026-08-30**.
- 보안: gtranslate GCP 키 회전, trendbiz ssh 비번 회전(둘 다 채팅 노출).

## §4. 운영 참고 (이 세션에서 굳어진 규칙)

- 배포=베이크드 이미지 태그(.env KDB_APP_IMAGE sed→build→up -d). **docker exec 배치는 재배포 시
  죽음** — 스위프는 태그 기반 멱등이라 재실행하면 이어짐.
- 수동 대량 드레인은 `-e KDB_INTAKE_AUTOVERIFY_DAILY_CALLS=1`로 exec(웹전용 — 상주 워커 네이버 예산 보존).
- **프롬프트 변경 = `kdb-app triage-eval` 통과 필수**(오거부 0). 새 오판 사례는 골든셋에 추가.
- 오거부(실재 K 기각)=최상위 금칙. 모든 자동 기각·교정은 복원 스냅샷 필수(dataqa_log / 큐 승인 버튼).
- 권한 분류기가 cron·세션위조 등을 막음 — 우회 금지, 오너에게 `!` 명령 안내 또는 앱 내장으로 해결.
