# KDB 핸드오프 40차 (2026-07-29) — 다국어 대량채움 + 오염 경로 차단 + 중복 병합

직전: 39차(`KDB.handoff-20260725.md`). 이번 아크 = 오너 질의 연쇄로 진행됐다:
"고유명사 쪽이 해결 안 된다, 왜 DB를 빠르게 못 채우나" → "3,000 이상 안 채워지는 상태,
심각하다" → "빠르게 해결할 솔루션 없나" → "진짜 고유명사 판단을 못해 이상한 게 섞였나"
→ "codex 버전/모델은?" → "요청이 막힘없이 처리되나".

**결론 한 줄: 채움을 막던 것은 소스 천장이 아니라 배선 누락이었고(17,000셀 즉시 해소),
오염은 단일 승급 경로(Wikidata 인명요소 앵커)에 집중돼 있었다.**

## §0. 새 세션 첫 명령 (상태 40초 파악)

```bash
cd /data/home2/kdb.aiinplanet.com
curl -s -m 5 http://127.0.0.1:9100/v1/health   # version=typehint-20260729-6

# ★최우선 이월: 타입 재할당으로 candidate 에 갇힌 건 (서빙 불가)
docker exec kdb-db psql -U kdb -d kdb -tAc "
select count(*) from kwave_entity_research_queue q
 where 'autoverify_type_reassigned'=any(q.precheck_flags)
   and exists(select 1 from kwave_entities e where e.canonical_ko=q.entity_ko and e.status='candidate')"

# 상시 레인 4종 정상 가동 확인
tail -3 logs/locale-fill.log; tail -3 logs/adjudicate.log; tail -3 logs/recheck-active.log
```

## §1. 배포 6건 (전부 커밋·푸시 완료)

| 태그 | 커밋 | 내용 |
|---|---|---|
| latinfill-20260729-1 | 864176d | 라틴 로케일 전파 전 타입 확장 (14,231셀 + en 345셀) |
| cjklatin-20260729-2 | bf02498 | 라틴 원제 → zh/zh_hant/ja 승계 (1,500셀) |
| namegate-20260729-3 | fa67c38 | Wikidata 인명요소 앵커 차단 + active 재심 레인 신설 |
| gapqueue-20260729-4 | 7290e89 | admin 빈칸 큐 요청순 정렬 + zh 누락 수정 |
| codex146-20260729-5 | 01897e3 | codex 0.146.0 + 모델 gpt-5.6-sol 통일 |
| typehint-20260729-6 | a97013f | 소비자 타입 힌트 존중(뉴스 1건 덮어쓰기 차단) |

부수 커밋: d40193c(codex exec 실행 규약).

## §2. 다국어 채움 — 배선 누락이 원인이었다

**진단**: 소비자 preparing 609건 중 **365건이 이미 active**(발굴 문제 아님, 요청 로케일만 빈칸).

원인 3층:
1. `romanize.go` 드레인이 `entity_type IN ('person','group')` 제한 → 작품·브랜드류는
   canonical_en 보유해도 라틴 4종 영구 빈칸(**12,463셀**, 빈칸의 97%가 작품류).
2. en 빈칸 445 중 **345건이 canonical_ko 자체가 라틴 원제**("Thank U"·"IDOL RADIO UNIVERSE")
   인데 translate-fill 이 `ko ~ [가-힣]` 요구로 영구 제외 → en 이 비면 라틴 4종도 막힘(1건=5셀).
3. translate-fill 이 실패건 쿨다운 없이 매시간 같은 83건 재시도(filled=0 무한).

**결과**: en 96→99% · es/vi/id/pt_br 65→98.6% · ja 86→90.5% · zh 79→85.3%.
zh/ja 는 **라틴 원제 승계**로 추가 확보(근거: Wikidata 실측 — 핑클 zh="FIN.K.L", KBS Joy,
DAILY:DIRECTION. 중국어권도 이 계층은 라틴 원문을 쓴다).

★상세 = 메모리 `reference-kdb-locale-fill-drain`.

## §3. 오염 — 단일 경로에 집중돼 있었다

**원인**: Wikidata 는 한국어 **인명 요소**를 대량 등재한다("만원"=Korean male given name
Q69511178, "인형"=Q69510371, "경남"=Q69508295). `sweep.tryRescue` 의 veto 구제가 이를
실존 근거로 인정해 일반명사를 active 로 승급시켰다. 의심군 372건 QID 전수조회 → **87 QID /
88 엔티티** 확인.

**차단**: `wikidata.Entity.InstanceOf`(P31) + `IsNameElement()` → **SearchAndFetch 단일
지점에서 배제**(승급·다국어채움·정정검증이 모두 이 함수를 탄다).

**active 재심 레인 신설**: `scripts/kdb-recheck-active.sh`, cron `30 1,7,13,19 * * *`.
over-drop 금칙 — drop 은 두 모델 합의만 + candidate 강등(삭제 아님). 감사
`kwave_kdb_recheck_log`(mig 0097).

**결과**: 총 1,837건 판정 → keep 1,761 · drop 67 · retype 9. **오염률 3.7%**이고, 무효앵커
88건 구간이 39%·나머지 1,725건은 1.9% — **DB 전반이 아니라 그 경로에 집중**됐음이 확인됐다.

★상세 = 메모리 `project-kdb-name-anchor-contamination`.

## §4. 중복 엔티티 병합 (37그룹 → 5)

패널 합의 32건 병합. **corrections 400(`ambiguous: 2 entities share ko`) 원인 해소** —
24시간 4xx/5xx 0건 확인. 부수 효과로 zh 확보(슬기 康瑟琪·영케이 姜永晛·유재석 劉在錫).
동명이인은 정확히 분리 유지(채영=트와이스/프로미스나인, 영웅=인물 vs 곡).
스냅샷 `kwave_kdb_merge_snapshot_20260729`(64행).

★상세 = 메모리 `project-kdb-duplicate-merge-20260729`.

## §5. ★실측으로 반증된 것 (다시 시도하지 말 것)

오늘 "빠르게 채울 방법"을 6개 시도해 전부 실패를 확인했다. 근거 없이 재시도하면 시간 낭비다.

| 시도 | 실측 결과 |
|---|---|
| MT(구글번역) 재판정 | 쿨다운 해제 후 30건 재판정 → 회수 2건(6.7%). 기각 사유 전부 정당("별이 될게"→我将成为一颗明星=서술문, "스테이 클로즈"→保持联系=완전 오역) |
| iTunes 지역 스토어(JP/TW) | 무용 — 봄날 JP→惜春鳥(일본 영화), TW→엉뚱한 결과 |
| Netflix/Disney 지역 카탈로그 | **이미 소진** — ottcascade 719건 시도·425건 쿨다운 중(로그 0인 건 처리 0일 때 안 찍혀서) |
| MyDramaList | 30건 전부 앵커 불일치 → filled 0. 작품 자체가 MDL 에 없다 |
| TMDb 현지제목 | 미등록 시 **한국어 원제를 그대로 반환** → 채우면 zh 칸에 한글이 들어가는 오염 |
| KMDb 한자명 | `actorEnNm`(영문명)만 있고 **한자명 필드가 없다** |
| ja 가타카나 규칙 확장 | **폐기** — 작품 제목은 순가타카나 음차 33%뿐, 67%가 뜻번역(날 보러와요→消された女, 나쁜 남자→赤と黒). 규칙 생성 불가. 인명·그룹은 이미 kana-fill 커버 |
| claude+codex 지식 패널 | 블라인드 정확도 ja 80%·zh 90%(환각 없이 기권)이나 **실제 빈칸엔 97% 기권** → 회수 ~50건 |

**단 SNS/공식사이트 배선이 들어가면 이 결론은 재검증해야 한다**(§7).

## §6. codex 운영 규약 (실측 — 안 지키면 무응답 타임아웃)

버전 0.146.0(호스트·컨테이너 통일), 모델 `gpt-5.6-sol`, effort `xhigh`.
compose 가 `${CODEX_MODEL:-gpt-5.5}` 로 .env 를 읽으므로 **Dockerfile ENV 만 고치면 안 되고
.env 에 명시** 필요.

1. **`< /dev/null` 로 stdin 을 닫아라** — 열린 채 비어 있으면(백그라운드) 무응답. 재현율 100%
   (10행 청크 10개 전량 400s 타임아웃 → 포그라운드 39초).
2. **stderr 를 `2>/dev/null` 로 버리지 마라** — 같은 증상. `2>&1` 로 파이프에 보낸다.
3. 그 대가로 프롬프트가 stdout 에 에코되므로 **grep 을 줄 시작에 고정**(`^{"id"`).
4. 타임아웃난 codex 가 좀비로 남아 뒤 실행을 막는다 → `pkill -9 -f codex` 후 재시도.
5. 행 수에 비선형으로 느려진다 — 출력이 무거운 판정은 **10행 청크**.

판정 스크립트 2개(adjudicate·recheck)에 1~3 반영 완료(d40193c).

## §7. 다음 작업

1. **★최우선: candidate 정체 174건 해소** — typehint 버그로 타입이 잘못 재할당돼 갇힌 건들
   (강화도→character, 서울구치소→channel_outlet, 김병걸→character). 신규 유입은 차단됐으나
   기존 건은 자동으로 안 풀린다. 패널 타입 재판정 → 교정 → 재처리.
2. **1차 출처 배선** — en 감사에서 기업·브랜드 오류율 **17%**(470건 중 ~80건) 확인, 그리고
   **패널 지식만으로는 확정 불가**가 실증됐다(데프콘TV·대림창고에서 두 모델 갈림).
   순서: ①Wikidata P856(QID 5,192건 보유, 추가 수집 0) ②자체 SearXNG 그라운딩(비용 0)
   ③SNS 공식 계정(웨이보→zh 최대 갭. 오너 07-29 논의 — 범위는 "공식·인증 계정의 공개 프로필
   메타데이터"로 한정, 게시물 내용·팔로워 수는 수집 안 함).
3. (관찰) 재심 레인 cron — 대상 0 이므로 신규 유입만 잡는다. 유입량 추이 확인.
4. (낮은 우선) 39차 이월: match 핫패스 p95 3.2s · 인물 DB 4,619 vs entities person 4,051
   이중 저장소 정합성.

## §8. 오너 결정 대기

- SNS 수집 범위·플랫폼 우선순위(웨이보 API 접근성 검토 필요).
- gtranslate en 전체 감사 확대 여부(위험군 937건, 합의 기준 오류율 4%·기업 구간 17%).
- 39차 이월: 비K 작품 정책 · KOPIS 키 재발급 · C등급 3사 계약.
