# KDB handoff - 30차 (2026-07-10): 미해결 병목 전수감사와 다음 실행 순서

29차 `KDB.handoff-20260710.md` 이후 수행한 읽기 전용 감사 기록이다. 기존 문서, 현재 workspace 코드, live container, 운영 DB, 최근 Hermes/log 지표를 대조해 아직 처리되지 않은 병목과 대안을 확정했다.

중요:

- 이번 감사에서는 **코드 동작을 변경하지 않았다**.
- DB row update/delete, 환경변수 변경, 서비스 restart를 하지 않았다.
- 새로 추가한 파일은 이 handoff와 `KDB_UNRESOLVED_BOTTLENECK_ALTERNATIVES_20260710.md`다.
- 기존 dirty workspace 변경은 다른 세션 작업이다. 관련 없는 변경을 되돌리거나 한꺼번에 배포하지 말 것.

## §0 - 다음 세션 시작 순서

아래 순서로 현재 상태를 먼저 확인한다. 모두 read-only다.

```sh
cd /data/home2/kdb.aiinplanet.com

# 1. 이 문서와 상세 대안서부터 읽기
sed -n '1,360p' docs/runbooks/KDB.handoff-20260710-2.md
sed -n '1,480p' docs/runbooks/KDB_UNRESOLVED_BOTTLENECK_ALTERNATIVES_20260710.md

# 2. 기존 dirty worktree 확인. 절대 일괄 reset/checkout 하지 말 것.
git status --short
git log -1 --format='%H %ci %s'

# 3. live 상태 확인
docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' | rg 'NAMES|kdb-'

# 4. MusicBrainz type mismatch 재확인
docker exec kdb-db psql -U kdb -d kdb -P pager=off -c "
SELECT e.status, e.entity_type::text, e.canonical_ko,
       e.verification_tier, r.external_id, r.url
  FROM kwave_entity_external_refs r
  JOIN kwave_entities e ON e.id=r.entity_id
 WHERE r.provider='musicbrainz'
   AND e.entity_type='song_album'
 ORDER BY e.canonical_ko;"

# 5. discovery/RSS 결합 상태 확인
docker exec kdb-db psql -U kdb -d kdb -P pager=off -c "
SELECT locale, count(*) total,
       count(*) FILTER (WHERE enabled) enabled
  FROM kwave_news_whitelist
 GROUP BY locale ORDER BY locale;"

# 6. 현재 주요 runtime flag만 확인. API key/secret은 출력하지 말 것.
docker exec kdb-app sh -c 'env | sort | grep -E "^KDB_(DISABLE_RSS_POLLING|LOCALFILL_ENABLED|LOCALFILL_BATCH|LOCALFILL_REGROUND|RESEARCH_INTERVAL_SECONDS|HERMES_ENABLED)="'
```

## §1 - 이번 감사에서 확정한 결론

현재 주 병목은 서버 자원이 아니다.

- API 24시간 오류 0, 전체 p95 약 694ms.
- 최근 3시간 연구 queue p90 8.43초, process p90 1.89초, e2e p90 9.44초.
- PostgreSQL cache hit 99.984%, lock waiter 0, 장기 query 0.
- CPU throttling, 메모리, 디스크 여유 문제도 관측되지 않았다.

현재 병목은 다음 네 가지다.

1. **공식 source 공급 경로 부족 및 discovery 차단**
2. **수렴한 대상을 fixed ticker가 계속 재처리하는 무수익 반복**
3. **candidate/typed quarantine/disambiguation의 폐루프**
4. **workspace와 live release 불일치**

상세 근거와 대안 비교표는 `KDB_UNRESOLVED_BOTTLENECK_ALTERNATIVES_20260710.md`에 있다.

## §2 - P0 신규 발견: MusicBrainz type mismatch

### 문제

`internal/kdb/musicbrainz_drain.go`는 `group`과 `song_album` candidate를 함께 고른다. 그러나 `internal/kdb/musicbrainz/client.go`의 `Search`는 MusicBrainz `/artist`만 검색한다.

그 결과 live DB에서 다음 `song_album` 3건이 `/artist/` external ref로 연결되어 `active`와 `authoritative`로 승급됐다.

- `MONSTER`
- `Now`
- `Under`

이는 단순 수율 문제가 아니라 공식 source가 잘못된 정체성 앵커가 된 정확도 문제다.

### 다음 세션 첫 구현 권고

1. `DrainMusicBrainzCandidates`의 자동 승급 대상에서 `song_album`을 제외하고 `group`만 남긴다.
2. group/person=`artist`, song=`recording`, album=`release-group` resource type 계약을 코드로 분리한다.
3. `song_album`을 계속 지원하려면 exact title 외에 artist/ISRC/release anchor를 필수로 요구한다.
4. KDB entity type과 provider resource type 불일치를 거부하는 unit test를 추가한다.
5. 기존 3건은 코드 차단 후 별도 DB 검토한다. 자동 삭제/강등하지 말고 원래 상태와 근거를 snapshot한 뒤 처리한다.

### 완료 기준

- `song_album`에 MusicBrainz `/artist/` ref가 새로 생성되지 않는다.
- 기존 3건의 처리 결정과 revert 근거가 audit log에 남는다.
- provider resource type mismatch가 `type_mismatch` outcome으로 기록된다.

## §3 - P0: RSS 중단과 discovery 상태 분리

### 현재 상태

2026-07-04 전략 변경에서 RSS keyword 수집은 의도적으로 폐기했다.

- `KDB_DISABLE_RSS_POLLING=1`
- `kwave_news_whitelist` 99개 모두 `enabled=false`
- 최근 24시간 RSS raw/observation 신규 유입 0

하지만 다음 두 코드가 같은 `enabled` 컬럼을 사용한다.

- Poller: `internal/kdb/rss_poller.go:78-85`
- 온디맨드 SiteSearch: `internal/kdb/site_search.go:234-249`

따라서 RSS를 끄면서 빈 locale용 현지매체 discovery도 함께 꺼졌다. 최근 1시간 `no enabled whitelist domains` 로그가 346회 발생했다.

### 권장 구현

1. migration으로 `rss_poll_enabled`와 `discovery_enabled`를 분리한다.
2. Poller와 SiteSearch가 각자 전용 컬럼을 사용한다.
3. admin toggle과 health metric도 분리한다.
4. discovery source가 0개면 locale 6개 search를 시작하지 않고 `blocked_no_source`를 기록한다.
5. Naver exact-name discovery를 1차로, SearXNG를 fallback으로 둔다.

RSS 99개 전량 재활성화는 07-04 전략과 충돌하므로 하지 않는다.

## §4 - P0: 수렴 후 무수익 반복

### 24시간 Hermes 실측

| 역할 | wall time | 입력 | 출력 |
|---|---:|---:|---:|
| LocalFill | 10,447.8초 | 계측 없음 | 13 |
| ReviewCandidates | 8,635.6초 | 2,400 | 7 |
| EnrichEmpty | 2,114.0초 | 3,000 | 0 |
| PromoteConsensus | 908.7초 | 214 | 0 |
| PersonExtractor | 489.0초 | 1,000 | 0 |
| QualityReview | 391.7초 | 2,940 | 0 |

추가 상태:

- autopilot 48회/24시간, 평균 528초, 최대 812.5초.
- `KDB_RESEARCH_INTERVAL_SECONDS=8`.
- 최근 1시간 `DrainQuality bumped=0` 450회.
- `localfill:rg` 3,023 entity, 5,433 attempts, exhaustion 0.
- 운영 LocalFill은 batch 30, reground ON이다.

### 단기 완화

1. `KDB_LOCALFILL_REGROUND=0`, batch 10으로 24시간 canary한다.
2. `DrainQuality`를 8초 주기에서 external-ref 변경 이벤트 또는 일 1회로 바꾼다.
3. RSS/observation OFF 상태에서는 `PromoteConsensus`를 skip한다.
4. 새 role agent와 겹치는 legacy `EnrichEmpty`/Review step을 audit-only로 바꾼다.
5. `ok`, `noop`, `no_match`, `transient`, `permanent`, `type_mismatch`를 분리한다.

### 구조적 대안

`(entity_id, job_type, provider, locale, input_fingerprint)` unique durable job과 lease worker로 전환한다. fixed ticker는 due job을 깨우는 역할만 해야 한다.

## §5 - P1: candidate와 실제 요청 커버리지

감사 시점 snapshot:

- active 6,710 / candidate 951 / rejected 3,036.
- candidate 951건 중 `source_domains=0` 924건.
- typed quarantine 600건.
- 가장 오래된 candidate는 2026-05-24 생성.
- active `needs_disambig` 18건, candidate `needs_disambig` 362건.
- research queue 8,157건은 모두 `done`.
- 그러나 요청 고유 keyword 기준 candidate 13.1%, entity 없음 2.3%로 실질 미답변은 15.4%.

`done`은 worker 종료일 뿐 답변 가능을 뜻하지 않는다.

권장:

1. request status와 resolution status를 분리한다.
2. typed quarantine 전용 상태와 7~14일 공식 source 재검증 job을 둔다.
3. 오래된 candidate를 일괄 reject하지 않는다.
4. 소비자 요청 빈도와 최근성을 priority에 반영한다.
5. `parked_reason`, `evidence_required`, `next_retry_at`, `last_outcome`을 notes 문자열 밖의 컬럼으로 관리한다.

## §6 - P1: source connector 실행 순서

검색/LLM은 discovery와 판독 도구일 뿐 자동 승급 source가 아니다.

권장 순서:

1. **person**: Wikidata QID -> Naver People/KOFIC/KMDb -> agency/broadcaster official profile.
2. **group**: MusicBrainz artist -> agency official -> official music platform artist.
3. **movie**: TMDb/KOFIC movie ID/KMDb -> OTT official page.
4. **drama/show**: TMDb -> broadcaster official program -> OTT official page.
5. **song**: MusicBrainz recording/iTunes track/DSP track + artist anchor.
6. **album**: MusicBrainz release-group/iTunes collection/DSP album + artist anchor.

`internal/kdb/source_policy.go`는 현재 정책/UI registry이며 실행 scheduler가 아니다. `live` 상태도 client 존재와 pipeline 배선을 구분하지 못한다. 다음 구현에서는 `client_ready`, `pipeline_wired`, `live_healthy`를 분리한다.

## §7 - workspace와 배포 주의

현재 branch와 HEAD:

- branch: `admin/requests-keyword-locale-dashboard`
- HEAD: `95da5fefd8c088acb3aa03eea3aa5ca778e8cc6b`
- HEAD 날짜: 2026-07-05

29차 변경과 이후 source pipeline 변경이 미커밋 상태로 섞여 있다. `docker restart kdb-app`은 bind-mounted workspace를 다시 build하므로 dirty 변경 전체가 live에 반영될 수 있다.

다음 세션은 다음 순서를 지킨다.

1. P0 containment 변경 파일만 식별한다.
2. 기존 변경의 소유권과 의도를 diff/handoff로 확인한다.
3. 테스트를 외부 provider와 운영 env에서 격리한다.
4. review 가능한 commit/image artifact를 만든다.
5. commit SHA, feature flag, rollback artifact를 확인한 뒤 canary 배포한다.

단순 dirty restart는 하지 않는다.

## §8 - 테스트 상태

29차에서 통과한 범위:

```sh
go test ./internal/kdb ./internal/kdb/autopilot ./internal/kdb/agents ./internal/kdbadmin
go build -o /tmp/kdb-test ./cmd/kdb
```

이번 감사에서 넓게 실행한 `go test ./internal/kdb/...`는 대부분 통과했지만 다음 1건이 실패했다.

- `internal/kdb/codexcli.TestRun_GateReleasedAfterRun`

테스트가 `Runner.Provider`를 고정하지 않고 production `KDB_LLM_PROVIDER=gemma`를 읽어 외부 LLM 경로를 탈 수 있다. 다음 세션은 provider 주입/fake 또는 `t.Setenv`로 unit test를 hermetic하게 만든다. 기본 unit test에서 network를 사용하지 않도록 한다.

추가 우선 테스트:

- `song_album` -> MusicBrainz artist 승급 금지
- research transient row 즉시 재선점 금지
- 동일 entity/locale grounding 중복 enqueue 금지
- context cancel이 provider cooldown을 열지 않음
- Hermes selected IDs와 실제 processed IDs 일치

## §9 - 운영에 손대지 않은 항목

이번 감사에서 다음은 발견만 했고 수정하지 않았다.

- MusicBrainz type mismatch 3건
- LocalFill/reground runtime flag
- RSS/discovery flag와 whitelist row
- `DrainQuality` 8초 반복
- candidate/typed/disambiguation backlog
- raw RSS retention
- live service binary

따라서 다음 세션 시작 시 상태가 달라졌는지 반드시 §0 query로 재확인한다.

## §10 - 다음 세션 권장 작업 단위

가장 안전한 첫 작업은 아래 하나의 containment change다.

> **MusicBrainz drain에서 `song_album` 자동 승급을 막고 resource type regression test를 추가한다. DB 보정과 서비스 재시작은 분리한다.**

그 다음 순서:

1. hermetic test와 review 가능한 release artifact
2. RSS/discovery 상태 분리
3. no-op/backoff 공통화
4. type-safe official connector
5. typed quarantine 폐루프

완료 후 이 문서를 새 handoff로 대체하고 `.codex/memories/kdb-handoff-pointer.md`를 다시 갱신한다.

## §11 - 관련 파일

- 상세 감사/대안: `docs/runbooks/KDB_UNRESOLVED_BOTTLENECK_ALTERNATIVES_20260710.md`
- 직전 코드 작업: `docs/runbooks/KDB.handoff-20260710.md`
- 온디맨드 전략 기준: `docs/runbooks/KDB.handoff-20260704.md`
- 마지막 커밋 흐름: `docs/runbooks/KDB.handoff-20260705.md`
- 포인터 메모: `.codex/memories/kdb-handoff-pointer.md`
