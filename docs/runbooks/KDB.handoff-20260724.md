# KDB 핸드오프 38차 (2026-07-22~24) — 고유명사 승급 전면전(Phase 0~4) + 뉴스축 기아 수정

직전: 36~37차(07-21 즉시서빙+오염정정, 메모리 `project-kdb-serve-contamination-20260721`),
35차(`KDB.handoff-20260719.md`). 이번 아크 = 오너 질의("인물은 채워지는데 고유명사는 왜 정체?")
→ 심층진단 → 오너 승인("승인한다 개선안 실행해 Phase 0부터") → **4단계 전면 실행 + 마지막 날
뉴스축 기아 구조버그 발견·수정**. 설계 SSOT = `docs/KDB_PROPERNOUN_RESOLUTION_PLAN.md`.
최종 배포 `kdb-app:candfix-20260723-1`. 전 커밋 push 완료(HEAD `322b036`).

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s -m 5 http://127.0.0.1:9100/v1/health   # version=candfix-20260723-1 (외부: https://kdb.aiinplanet.com/v1/health)

# ★뉴스축(cand-evidence) 살아있나 — "대상 candidate=0" 공회전이면 기아 재발(§1F)
docker logs kdb-app --since 6h 2>&1 | grep -E 'cand-evidence: (start|done)' | tail -6

# 비인물 candidate 풀(07-24 아침 660) + 검토플래그 재누적(07-24 아침 572 — P3 배치 필요 신호)
docker exec -i kdb-db psql -U kdb -d kdb -c "
SELECT (SELECT count(*) FROM kwave_entities WHERE status='candidate' AND entity_type<>'person') nonperson_cand,
       (SELECT count(*) FROM kwave_entities WHERE status='candidate'
         AND (notes LIKE '%[cand-evidence:review]%' OR notes LIKE '%[cand-evidence:unclear]%')) flags,
       (SELECT count(*) FROM kwave_entities WHERE status='active') active_total;"

# 카탈로그 드레인 밤사이 실적(KOPIS·MB곡·iTunes·OTT후보)
docker logs kdb-app --since 20h 2>&1 | grep -iE 'kopis|MBSong|mb-song|itunes-cand|OTTCand' | tail -12

# P4 재인테이크(autoverify) 소화 — 일 600콜 예산, 잔여 review 큐
docker logs kdb-app --since 20h 2>&1 | grep 'intake-autoverify: checked' | tail -4
docker exec -i kdb-db psql -U kdb -d kdb -c "
SELECT count(*) FROM kwave_entity_research_queue WHERE precheck_status='review'
  AND precheck_flags::text NOT LIKE '%autoverify_exhausted%';"
```

## §1. 이번 아크 산출물 (전부 배포·커밋·push 완료)

**A. 진단(07-22)** — 고유명사 정체 1,764건의 근본원인 = 소스천장 아닌 **배선 4중 결함**:
①승급 드레인 타입 편중(song/event/character/agency 드레인 부재) ②cand-evidence 선별 사각
(review행 요건이 풀 75% 제외) ③판정 종결자 부재 ④type-retrace 범위갭. 상세·오너결정 6건 =
`docs/KDB_PROPERNOUN_RESOLUTION_PLAN.md` §1·§4.

**B. Phase 0 — 전수 판정(07-23)**: 1,764건 claude 7병렬 판독 + 본세션 전건 재검토 → keep 1,017·
retype 359·reject 341·merge 35(파친코 2→파친코 alias 등). 스냅샷 `kwave_kdb_p0_snapshot_20260723`
+ dataqa_log(p0-*) 전건 revert 가능. 배포 p0-propernoun-20260723-1.

**C. Phase 1 — 카탈로그 축 신설(07-23)**: ①MB recording/release-group 곡 드레인
(`musicbrainz_drain.go` — en우선 검색·score≥95, 한글제목 vs 라틴저장 불일치 실측수정)
②iTunes KR candidate 승급(`itunes_drain.go` — 아티스트 스코프 게이트 필수, title-only는 오염)
③KOPIS event_tour 드레인(`kopis_drain.go`+`kopis/kopis.go` — **FP 2회 실측→게이트 2단 강화**:
6자미만=세그먼트 정확일치, 4자미만=아티스트 corroborate 필수. '그리스'·'달빛' 오앵커 revert 완료)
④TMDb alt-title 관용화(`tmdb.go`) ⑤Netflix ID 앵커(`ott.go` recordOTTAnchor + DrainOTTCandidate-
Promotions 4/cycle — 죽은 신뢰토큰 정상화) ⑥kmdb officialPromotionProviders 등재.
CLI 원샷: `kdb-app mb-songs|itunes-cands|kopis-events`. 스팟체크: MB 15/15·iTunes 20/20 정상.

**D. Phase 2 — 뉴스축 확대(07-23)**: `verify/candidate_evidence.go` 재작성 — review행 요건 제거
(전 candidate 대상), 소비자 기사 context_hint 문맥증강(co-mention 아티스트 스코프 쿼리+힌트=증거,
단 검색 실증거 1+ 필수), 2회 소진→`[cand-evidence:exhausted]`(무한쿨다운 차단).

**E. Phase 3·4 — 종결자+재인테이크(07-23)**: P3=플래그 575건 claude 판독+전건 재검토 →
keep 258·promote 17·reject 300(`[adjudicated:claude]` 감사). P4=autoverify_exhausted 재개방
1,837건 → autoverify(일 600콜)가 소화 중(~07-27 완료 예상). 무경로 버킷 282행 처리
(type_shape 40 판정: 비K 31 기각·9 재심 / 엔티티전무 79 재개방).

**★F. 뉴스축 기아 수정(07-23 밤 — 이번 아크 최대 구조버그)**: cand-evidence 쿨다운이
`last_enriched_at`을 on-demand 공식앵커축·lookup enrich와 **공유** → 타 축이 일 150~325건
스탬프를 찍어 풀 1,055 전체가 상시 7일 쿨다운 → 레인이 매 사이클 **"대상=0" 공회전**(로그 실측).
카탈로그 no_match 잔여의 유일한 탈출구가 죽어 있었음. 선별 쿨다운을 enrich_attempts
field='cand-evidence' 전용 키로 분리(0→1,044 가용). 커밋 `322b036`, 배포 candfix-20260723-1.
**효과 실측: 재가동 후 ~24h에 813건 뉴스근거 승급**(40건/20분 풀가동).

**G. 부수**: KOPIS 인증키 DB 저장(`kwave_kdb_api_settings` name='KDB_KOPIS_API_KEY' — 챗 노출,
재발급 권장). YouTube API 키 **불필요 판정**(yt-dlp 무키 채널조회 라이브 실증). `.env`
KDB_KOPIS_DRAIN_ENABLED=1.

## §2. 다음 작업 (우선순위)

1. **★뉴스축 대량승급 품질 스팟체크 — 최우선**. 813건/24h는 전례없는 속도. 무작위 30~50건
   눈검사(특히 song_album 영문제목·character). 07-24 아침 20건 샘플은 전건 그럴듯 +
   사후 공식앵커/strong-source 획득 확인됐으나 표본 부족.
   ```sql
   SELECT entity_type, canonical_ko, left(verification_evidence,80) FROM kwave_entities
    WHERE status='active' AND notes LIKE '%[cand-evidence] 뉴스근거 승급%'
      AND updated_at > now()-interval '48 hours' ORDER BY random() LIMIT 50;
   ```
   오염 발견 시 dataqa_log 'candidate-evidence-promote' 행으로 revert.
2. **P3 배치 재실행** — 플래그 572건 재누적(뉴스축 풀가동의 부산물, 예상보다 빠름). 방식 =
   35차 §5e/이번 P3와 동일(스크래치 p3_rubric.md·p3_apply.py 참고, 병렬 판독+전건 재검토+
   호모님 가드). P3 상시화 여부는 오너 결정待(§3).
3. **P4 소화 관찰**(~07-27) — §0 명령으로 잔여 review 큐 추적. 완료 후 잔존 no_entity 처분.
4. **드레인 일일 스팟체크 지속** — KOPIS 2차 강화 게이트 이후 신규 승급, OTTCandDrain 첫 실적,
   MEMORIA/이리온(iTunes) 관찰항목.
5. song_album no_context ~150건 — 아티스트 문맥 부재로 카탈로그 검색 불가. 뉴스축이 소화하는지
   확인, 안 되면 context 보강 방안.

## §3. 오너 액션/결정 대기

- **P3 상시화**: 플래그 재누적 속도면 주 2~3회 배치 필요. 선택지 = ①세션 배치 반복(현행)
  ②호스트 cron + claude CLI(비용/인증 오너 결정).
- **비K 작품 정책**(플랜 §4): 귀멸의칼날류 수용 vs 기각 — 현재 K-스코프 기각 일관 적용 중.
- **KOPIS 키 재발급**(챗 노출분) · 유튜브 채널 수용범위(14채널 중) — 플랜 §4 참조.
- 35차 이월: C등급 3사 요청계약 전달, 네이버 쿼터 확장, KMDb 활용사례(기한 08-30).

## §4. 지표 (아크 전후)

| 지표 | 07-22 시작 | 07-24 아침 |
|---|---|---|
| 비인물 candidate | 1,764 | **660** |
| person candidate | 1,278 | 535 |
| active 총계 | 7,306 | **8,373** |
| song_album cand | 499 | 195 |
| event_tour cand | 174 | 79 |
| 판정대기 플래그 | 575→0 (P3) | 572 (재누적 — §2-2) |
| 뉴스축 처리량 | 0/일 (기아) | ~800 승급/일 |
