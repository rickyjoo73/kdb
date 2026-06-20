# KDB 핸드오프 2026-06-20 (15차) — 전면 audit·품질/자율/비용·K범위 강제

Opus xhigh 다중에이전트 전면 audit(9 워크플로우 + 영역별) → 11단계 수정 → 단계별 코드리뷰(워크플로 적대검증) → 배포·라이브검증. **전부 라이브.** Hermes 모드 활성(KDB_HERMES_ENABLED=1, 13 steps).

## 배포된 수정 (코드 + 데이터)

1. **소비자 API 정확성(WF7)** — `/v1/entities/match`·bulk `normalized()` status 미지정→`active` 강제(rejected merge-tombstone 유출 차단; 카리나 conf0.97 tombstone 라이브 검증). `MatchedEntity` 에 `disambig`·`locale_ambiguous`(EXISTS 서브쿼리) 추가. 문서 갱신.
2. **canonical_en 교차인물(WF2/WF7)** — 근원=서로 다른 인물이 같은 QID/tmdb 공유 + FILL 라벨 복사. `enrich/orchestrator.go xrefClaimedByOther`(QID/tmdb mislink 가드, fail-closed: ErrNoRows만 통과), runWikidata 는 qidConfirmed 시 skip, runVideoAPIs tmdbMislink(movie/tv 네임스페이스 분리). 데이터: 김하늘→Kim Ha-neul(QID Q292687), 유정→Yujeong, 그림자아이→Shadow Child.
3. **게이트키퍼 오거부(WF1/WF2)** — tryRescue 끝 typed 힌트면 `quarantineTyped`(candidate 보류, 센티넬 `[kdb:q:typed]` — 사람이 못 쓰는 고유토큰), candidate-처리 step SELECT 9곳 + drain 제외. on-demand drain reject도 tryRescue 게이트. 오거부 99건 candidate 복구.
4. **WF-2 dedup** — 파괴적 자동병합 폐기(canonical_ko 일치는 동명이인 시그니처라 위험, 외부ref도 mislink 가능) → 비파괴 가시화(요약로그 + 대시보드[8]).
5. **canonical_ko 손상** — `IsValidSpellingForLocale("ko")` 가나/순수한자 거부(enrich + candidate INSERT 양쪽). 데이터: 허준/구암 허준/백일의 낭군님 교정, 범위밖 일본(常田大希 등) reject.
6. **저신뢰 2차검증(WF6)** — drainquality tier2: 권위 external_ref(tmdb/musicbrainz/kofic/kmdb)만 0.70 승급(RSS-only/wikidata 제외 — 소비자 min_confidence 게이트 오통과 방지).
7. **오염 핑퐁(WF6, 근본)** — `snapshot.isSuppressed` 가 같은 locale 변종 2+ clear 시 값무관 차단(문자변종 우회 종료). L4 codex-fallback 은 오염이력 locale 재합성 금지(김하늘 zh=姜河呢 류 교차인물 재유입 차단).
8. **Hermes parity(WF5 등)** — clearResolvedDisambig·stepDrainOnDemandCandidates·stepFillPersonDetails·WF-2 가 **Hermes 모드에서 미실행이던 회귀** → `Sweeper.RunTail`로 묶어 SuperviseCycle 후 호출(cmd/kdb + cmd/kdb-worker 양쪽).
9. **K-범위 강제(잡탕 정리)** — classify 프롬프트 K-SCOPE FILTER(비-K 인물 범위밖 → term/conf≤0.30/reason '비-K', K-pop 외국멤버·한국활동 외국인 예외). 데이터: 젠슨황·안젤리나졸리·브래들리쿠퍼·숀펜·BadBunny·스코티셰플러·장사의신·고은언니한고은·사라킴이라는여자·오드리누나 reject + kwave_persons 정리.
10. **persons UI(WF9)** — `/admin/persons` 9개 언어 전부 표시 + 이름클릭 `/admin/persons/{id}` detail(누락 locale '— 누락' 표시). 역할필터 vocab=person_role enum 16개 정합(host/writer 제거→HTTP500 수정).

## 자율 운영(완전자율) — "다른 오염 누가/언제 자동으로?"

- **stepScopeReview**(Gemma, runTail 매 cycle): active person K-범위 재판정 → 비-K 의심 자동발굴·`[scope:review]` 표면화(대시보드[11]). 자동 reject X(미상 K 보호, 운영자 확인). transport실패/unknown 은 마킹 안 함(Gemma 장애 cycle 에 정상인물 [scope:ok] 영구각인 방지). `[scope:ok]` 마킹으로 점진 전수 1회 후 신규만.
- **stepSweepContamination**(runTail 매 cycle): canonical_ko 비한글(가나/한자, 한글無) 손상 자율 reject.
- **Gemma 헬스 모니터**(`internal/kdb/gemma_health.go`, fast tick 30초): KDB_GEMMA_BASE_URL reachability probe, 3연속 fail→incident(codex_runs) + `codexcli.GemmaDown` 훅으로 CLASSIFY/FILL **Codex 자동 폴백**(자가복구, 복구 시 환원). cmd 가 `codexcli.GemmaDown=func(){return !kdb.GemmaHealthy()}` 와이어.
- 기존: DataQA(gpt-5.5, 20분) 라틴/charset 오염, 유입 가드(PreGate 팬호칭결합 `고은언니 한고은` 등).

## 비용 (gpt-5.5 절감)
- `.env CODEX_EFFORT_EXTRACT=low`(최대볼륨 role). 핑퐁 차단으로 dataqa Codex 재검수(최다 478회/1엔티티) 정지. Gemma=주력(classify/fill), Codex=DATAQA/DISAMBIG/EXTRACT+감독.

## 대시보드 (`./scripts/kdb-dashboard.sh`)
[7]quarantine(typed) [8]canonical_en 충돌 [9]저신뢰 분해(extref bumpable/media-only/unverifiable) [10]호칭오염 의심 [11]비-K 의심(scope:review).

## 빌드/배포
컨테이너 내 빌드 검증: `docker exec -w /app kdb-app go build -o /tmp/kdb-app-test ./cmd/kdb`. 배포=`docker restart kdb-app`(마운트 소스 재빌드+exec, ~6초). api=9100(/v1/health), admin=9101.

## 정직하게 보류(deferred) — 다음 세션
- **Gemma fill `source` 라벨 정정**: codec-fallback 로 오라벨(7곳 회귀위험으로 보류). source_priority/trust/provenance 동시 수정 필요.
- **WF8 correction 값 손실 1건**(앨리스류 — disambig/resolve-unknowns 가 검증된 교정값 전소), **WF4 동일 QID active-active 잔존 mislink**(가드 도입 전 누적분 ~18쌍).
- **codexcli env-의존 테스트 2건** 선재 깨짐(컨테이너 KDB_LLM_PROVIDER=gemma — Run 테스트가 codex 기본 가정). 환경독립화 필요.
- **scope:review/scope:ok** 는 배포 직후 0 — 다음 autopilot cycle부터 점진 마킹. 운영자가 [scope:review] 확인 후 reject(또는 향후 고신뢰 자동 reject 검토).
- 미커밋: 이번 세션 코드 전부 로컬(미push). push는 §아래 gh CLI 방식.

## push 방법
`git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main` (deploy key는 read-only라 막힘, gh `aiinplanet` 계정 사용).
