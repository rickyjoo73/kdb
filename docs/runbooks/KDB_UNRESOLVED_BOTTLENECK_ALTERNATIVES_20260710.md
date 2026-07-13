# KDB 미해결 병목 대안서 (2026-07-10)

## 0. 문서 목적

이 문서는 기존 handoff, 설계 문서, 현재 코드, 운영 DB와 최근 로그를 대조해 다음을 구분한다.

- 이미 해결된 병목
- 전략 변경으로 폐기된 과거 작업
- 코드만 작성되고 운영에는 반영되지 않은 작업
- 현재 운영에서 실제로 남아 있는 병목
- 각 병목의 단기 완화안과 구조적 대안

조사 기준 시각은 **2026-07-10 13:16~13:22 KST**다. 이번 조사에서는 DB row 변경, 서비스 재시작, 환경변수 변경을 하지 않았다.

## 1. 결론

현재 KDB의 주 병목은 CPU, 메모리, PostgreSQL, Gemma 용량이 아니다.

1. API와 온디맨드 연구 큐의 속도 병목은 대체로 해소됐다.
2. 현재 병목은 **공식 소스 공급 경로 부족**, **성과가 없는 작업의 반복 실행**, **후보/검토 상태가 닫히지 않는 구조**다.
3. RSS 수집은 2026-07-04 전략 변경으로 의도적으로 중단됐다. 문제는 RSS용 `enabled` 값이 온디맨드 현지매체 discovery에도 재사용되어 discovery까지 함께 중단된 것이다.
4. 최신 LocalFill 오염 차단과 qualified-member 정규화는 workspace에만 있고 live 프로세스에는 반영되지 않았다.
5. MusicBrainz candidate drain은 `song_album`도 artist 검색으로 처리한다. 그 결과 `MONSTER`, `Now`, `Under`가 `/artist/` 참조로 `authoritative` 승급됐다. 커넥터 확대 전에 유형 계약을 바로잡아야 한다.
6. GPU/LLM 증설이나 RSS 전량 재활성화는 현재 병목의 해법이 아니다.

권장 방향은 다음과 같다.

> **변경분을 검토 가능한 릴리스로 고정하고, RSS와 discovery 상태를 분리한 뒤, 작업을 durable job으로 전환하고, 엔티티 유형별 공식 커넥터를 연결한다. 검색과 LLM은 공식 URL을 찾고 판독하는 도구로만 사용한다.**

## 2. 기준 자료와 상태 판정

### 2.1 기준 문서

| 자료 | 현재 해석 |
|---|---|
| `docs/runbooks/KDB.handoff-20260710.md` | 최신 작업 기록. LocalFill/후보 정규화 변경은 테스트만 했고 live 재시작과 DB write는 하지 않음 |
| `docs/runbooks/KDB.handoff-20260705.md` | 현재 HEAD와 마지막 커밋 기준. 온디맨드 속도, 백업, API 연동 완료 상태 |
| `docs/runbooks/KDB.handoff-20260704.md` | RSS/상시 LLM 감사 폐기, 온디맨드+권위 채움 유지라는 현행 전략의 기준 |
| `docs/runbooks/KDB_CODERED_PLAN_20260628.md` | 과거 진단 자료. 후속 handoff에서 완료 선언된 항목은 현행 병목으로 다시 세지 않음 |
| `docs/runbooks/KDB_ACQUIRE_ROADMAP_20260628.md` | 로마자/OpenCC 등 일부 완료. P1814 해석 등 후속 실측으로 폐기된 항목은 제외 |
| `docs/KDB_*_DESIGN.md` 일부 | 구현 전 설계이거나 07-04 전략 변경 전 문서. 현행 상태의 단독 근거로 사용하지 않음 |

### 2.2 이미 해결됐거나 폐기된 항목

| 항목 | 판정 | 근거 |
|---|---|---|
| 온디맨드 연구 큐 지연 | 해결 | 최근 3시간 queue p90 8.43초, process p90 1.89초, e2e p90 9.44초 |
| API 처리 오류/과부하 | 현재 병목 아님 | 최근 24시간 1,171건, 오류 0, 전체 p95 693.5ms |
| DB lock/용량/메모리 | 현재 병목 아님 | cache hit 99.984%, lock waiter 0, DB 약 223MB, 디스크 사용 8% |
| 자동 백업 부재 | 해결 | 03:00/12:00 백업 정상, 최신 백업 약 46.5MB |
| CR-1~4 오거부/매칭/zh/QID-pin | 완료 선언 | `KDB.handoff-20260628.md` §3 |
| 상시 RSS 키워드 수집 | 의도적 폐기 | `KDB.handoff-20260704.md` §4, `KDB_DISABLE_RSS_POLLING=1` |
| 상시 LLM dataqa/audit | 의도적 폐기 | `KDB.handoff-20260704.md` §4 |

### 2.3 운영 스냅샷

| 지표 | 관측값 | 해석 |
|---|---:|---|
| active / candidate / rejected | 6,710 / 951 / 3,036 | 후보 적체는 여전히 큼 |
| candidate 중 `source_domains=0` | 924, 97.2% | 매체 관측 근거 공급이 거의 없음 |
| candidate 중 typed quarantine | 600, 63.1% | 일반 후보 루프로는 닫히지 않는 별도 백로그 |
| 가장 오래된 candidate | 2026-05-24 | TTL/재심/운영자 판정 루프 부족 |
| 8개 locale 전체 셀 | 53,680 | active 6,710 x 8 |
| 빈 셀 | 13,078 | 공식 표기 미확보 |
| 값은 있으나 `codex-fallback` | 6,763 | 서빙 시 숨겨지는 LLM-only 값 |
| 빈칸 또는 LLM-only | 19,841, 37.0% | 실질적인 공식 표기 미해결량 |
| verification tier | authoritative 4,378 / evidenced 1,906 / unverified 426 | 정체성 검증과 locale 완성도는 별도 문제 |
| 연구 큐 | 8,157건 전부 `done` | 작업 종료이지 답변 가능을 뜻하지 않음 |
| 요청 고유 키워드 결과 | active 53.3%, candidate 13.1%, 미생성 2.3% | 실질 미답변은 15.4% |

## 3. 우선순위별 미해결 병목과 대안

### P0-1. Workspace와 live 릴리스 불일치

#### 현상

- 현재 HEAD는 2026-07-05 커밋이다.
- 2026-07-10 LocalFill exact-name 필터, active-only 대상 제한, qualified-member reject는 미커밋 workspace 변경이다.
- 최신 handoff는 서비스 재시작을 하지 않았다고 명시한다.
- live 로그에는 한국어 문자열이 그대로 `zh` 표기로 grounded 처리되는 사례가 남아 있다.
- 작업트리에 다수의 기존 미커밋 변경이 섞여 있어 단순 `docker restart kdb-app`은 변경 전체를 한 번에 live 빌드한다.
- `go test ./internal/kdb/...`는 `internal/kdb/codexcli` 테스트 1건이 운영 provider 환경을 읽고 외부 LLM 경로를 타면서 실패했다. 테스트도 hermetic하지 않다.

#### 대안

| 대안 | 장점 | 위험/한계 | 판정 |
|---|---|---|---|
| A. 현재 dirty workspace에서 즉시 restart | 가장 빠름 | unrelated 변경까지 동시 배포, 재현/rollback 어려움 | 비권장 |
| B. 변경 파일을 검토해 release commit/image로 고정 후 배포 | 변경 범위, 테스트, rollback이 명확 | 배포 전 정리 시간이 필요 | **권장** |
| C. B에 LocalFill/qualified-member feature flag와 canary를 추가 | 데이터 변경을 단계적으로 관찰 가능 | 플래그와 계측 추가 필요 | B와 병행 |

#### 완료 기준

- 테스트가 운영 환경변수와 외부 네트워크에 의존하지 않는다.
- 배포 artifact의 commit SHA를 admin/health에서 확인할 수 있다.
- LocalFill dry-run 회귀 샘플과 qualified-member 샘플이 통과한다.
- rollback artifact가 존재한다.

### P0-2. RSS 중단 상태가 온디맨드 discovery까지 차단

#### 현상

- RSS 중단은 현행 전략상 의도적이다. whitelist 99개가 모두 `enabled=false`이고 최근 24시간 RSS/observation 유입은 0이다.
- `internal/kdb/rss_poller.go`와 `internal/kdb/site_search.go`가 같은 `kwave_news_whitelist.enabled`를 사용한다.
- 연구 워커는 빈 locale 6개에 대해 site search를 실행하지만, 최근 1시간에 `no enabled whitelist domains`가 346회 발생했다.
- 연구 row는 site search를 background goroutine으로 넘긴 직후 `done` 처리된다. 따라서 큐 SLA는 정상인데 locale discovery는 실패하는 false-success 상태다.

#### 대안

| 대안 | 장점 | 위험/한계 | 판정 |
|---|---|---|---|
| A. `KDB_DISABLE_RSS_POLLING=1`을 유지하고 신뢰 domain의 기존 `enabled`만 복원 | 코드 변경 없이 discovery를 빠르게 복구 | 컬럼 의미가 계속 혼재, 운영자가 RSS 상태로 오해 | 임시 canary |
| B. `rss_poll_enabled`와 `discovery_enabled`를 분리 | 전략 의도가 명확하고 독립 운영 가능 | migration/admin 수정 필요 | **권장** |
| C. whitelist site search를 제거하고 공식 connector router로 전환 | 공식 근거 수율/정확도 향상 | 범용 롱테일 fallback은 별도 필요 | B 이후 목표 |
| D. RSS 99개 전량 재활성화 | 과거 관측 파이프라인 즉시 복원 | 07-04 전략과 충돌, 실패/노이즈 재발 | 비권장 |

#### 권장 설계

- Poller는 `rss_poll_enabled=true`만 조회한다.
- SiteSearch는 `discovery_enabled=true`만 조회한다.
- admin에서 두 상태와 최근 성공/실패/수율을 따로 보여준다.
- 활성 discovery source가 0이면 6 locale 호출을 시작하지 않고 명시적 `blocked:no_discovery_source` 결과를 기록한다.
- Naver는 한국어 고유명사에서 공식 URL을 찾는 1차 discovery로 사용하고, SearXNG는 fallback으로 제한한다.

### P0-3. MusicBrainz 엔티티 유형 불일치로 잘못된 authoritative 승급

#### 현상

`DrainMusicBrainzCandidates`는 `group`과 `song_album`을 함께 선택하지만, 실제 client는 `/ws/2/artist`만 검색하고 `/artist/{mbid}`를 저장한다.

운영 DB에서 다음 `song_album` 3건이 artist resource로 연결되어 active/authoritative가 됐다.

| KDB entity type | canonical_ko | 저장된 resource type |
|---|---|---|
| song_album | `MONSTER` | MusicBrainz `/artist/` |
| song_album | `Now` | MusicBrainz `/artist/` |
| song_album | `Under` | MusicBrainz `/artist/` |

이 상태에서는 공식 source가 오히려 잘못된 승급 앵커가 된다.

#### 대안

| 대안 | 장점 | 위험/한계 | 판정 |
|---|---|---|---|
| A. MusicBrainz drain에서 `song_album`을 즉시 제외하고 group만 유지 | 오승급 추가 발생 차단 | 음악 작품 채움은 중단 | **즉시 권장** |
| B. 유형별 client 구현: group/person=`artist`, song=`recording`, album=`release-group` | 올바른 공식 ID와 alias 확보 | 현 `song_album` 통합 타입의 song/album 판별 필요 | **구조적 권장** |
| C. iTunes/Discogs 제목 일치만으로 계속 confirm | 보수적이고 기존 코드 활용 | 신규 값 discovery 수율은 낮음 | 안전 fallback |

#### 필수 가드

- KDB entity type과 외부 resource type의 허용 행렬을 코드와 DB constraint 수준에서 검사한다.
- song/album은 제목 exact match만으로 승급하지 않고 artist/ISRC/발매 anchor 중 하나를 추가로 요구한다.
- transient 오류, no-match, type-mismatch를 서로 다른 outcome으로 저장한다.
- 위 3건은 참조와 승급 사유를 별도 검토해야 한다. 이 문서 작성 중에는 DB를 변경하지 않았다.

### P0-4. 수렴 후 무수익 작업 반복

#### 최근 24시간 실측

| 역할 | 실행시간 | 입력 | 출력 | 해석 |
|---|---:|---:|---:|---|
| LocalFill | 10,447.8초 | 계측 없음 | 13 | 출력 1건당 약 803.7초 |
| ReviewCandidates | 8,635.6초 | 2,400 | 7 | 수율 0.29%, 출력 1건당 약 1,233.7초 |
| EnrichEmpty | 2,114.0초 | 3,000 | 0 | superseded 경로 반복 |
| PromoteConsensus | 908.7초 | 214 | 0 | RSS/observation 중단 상태에서 성공 불가 |
| PersonExtractor | 489.0초 | 1,000 | 0 | 소진 대상 반복 |
| QualityReview | 391.7초 | 2,940 | 0 | 소진 대상 반복 |

Autopilot은 최근 24시간 48회, 평균 약 528초, 최대 약 812.5초였다. 최근 1시간에는 `DrainQuality bumped=0`가 450회 발생했다. 운영 설정은 `KDB_RESEARCH_INTERVAL_SECONDS=8`, `KDB_LOCALFILL_BATCH=30`, `KDB_LOCALFILL_REGROUND=1`이다.

#### 근본 원인

- fixed ticker가 입력 변화 여부와 관계없이 같은 SELECT/UPDATE를 반복한다.
- LocalFill `localfill:rg`는 3,023 entity에 5,433 attempts가 있지만 `exhausted`를 설정하지 않는다.
- Hermes의 legacy step과 새 role agent가 함께 등록되어 같은 영역을 중복 처리한다.
- 실행 성공(`status=ok`)과 데이터 개선(`items_out>0`)을 구분하지 않는다.
- 외부 drain은 오류도 30일 no-match와 비슷하게 기록하며 lane claim/single-flight가 없다.

#### 대안

| 대안 | 장점 | 위험/한계 | 판정 |
|---|---|---|---|
| A. LocalFill reground off, batch 30→10, zero-yield role 주기 축소 | 즉시 부하/로그 감소, rollback 쉬움 | 근본 스케줄 구조는 유지 | 24시간 canary 권장 |
| B. `next_eligible_at`+outcome+exponential backoff | 같은 실패의 반복 차단 | 각 selector 수정 필요 | **권장** |
| C. durable source job + lease/worker pool | 중복, 재시작 유실, 우선순위 문제를 함께 해결 | migration/worker 구현 필요 | **목표 구조** |
| D. LLM/GPU 증설 | 호출 처리량 증가 | 공식 source 부재와 no-op 반복은 해결 못함 | 비권장 |

#### 즉시 완화 기준

- `DrainQuality`는 8초 주기가 아니라 external-ref 생성 이벤트 또는 일 1회로 실행한다.
- RSS/observation이 꺼져 있으면 `PromoteConsensus`를 skip한다.
- `EnrichEmpty`, `ReviewCandidates` 등 새 agent로 대체된 legacy step은 audit-only 또는 제거한다.
- 3회 연속 no-op이고 입력 fingerprint/source set이 같으면 backoff한다.
- `ok/noop/no_match/transient/permanent/type_mismatch`를 분리한다.

### P1-1. 무제한 background goroutine과 source 전역 제어 부재

#### 현상

- async grounding은 semaphore를 잡은 뒤 goroutine을 만드는 구조가 아니라, goroutine을 먼저 만든 뒤 그 안에서 semaphore를 기다린다.
- research, API background, Hermes Enricher, legacy EnrichEmpty가 같은 entity/locale을 중복 enqueue할 수 있다.
- websearch는 실제 호출을 전역 직렬 throttle하므로 producer가 consumer보다 빨라질 수 있다.
- context cancel/timeout도 provider 실패로 취급되어 전역 cooldown을 열 수 있다.
- MusicBrainz/Wikidata limiter는 client instance별이라 여러 lane의 합산 호출률을 제한하지 못한다.
- research transient 실패는 attempt를 되돌리고 즉시 `pending`으로 바꾼다. 오래된 같은 row가 한 batch에서 다시 선점되면 뒤의 요청을 굶길 수 있다.
- API/admin/background가 DB pool 10개를 공유한다. 현재 DB wait 병목은 없지만 background burst 때 API 우선순위가 없다.

#### 대안

1. `(entity_id, locale, job_type, provider)` UNIQUE인 durable job을 둔다.
2. `FOR UPDATE SKIP LOCKED`, `leased_until`, heartbeat로 고정 worker가 claim한다.
3. foreground/research 요청을 maintenance보다 높은 priority로 처리한다.
4. source별 process-wide broker가 rate, concurrency, quota, breaker를 관리한다.
5. context cancel은 breaker 실패로 세지 않고 403/429/5xx/network만 정책별로 센다.
6. pool acquire p95를 계측한 뒤 필요할 때 API pool과 batch pool을 분리한다.

### P1-2. Candidate/typed quarantine의 폐루프

#### 현상

- candidate 951건 중 600건이 typed quarantine이고 924건에 source domain이 없다.
- 연구 큐는 모두 `done`이지만 candidate 또는 entity 없음으로 실질 미답변인 요청 키워드는 15.4%다.
- active `needs_disambig` 18건, candidate `needs_disambig` 362건이다.
- 일반 후보 review, on-demand retry, disambiguation, typed quarantine의 상태와 재시도 조건이 notes 문자열에 섞여 있다.

#### 대안

| 대안 | 장점 | 위험/한계 | 판정 |
|---|---|---|---|
| A. 오래된 candidate 자동 reject | backlog가 빠르게 감소 | 실존 niche entity 오거부 | 비권장 |
| B. typed 전용 dead-letter/triage + 7~14일 공식 source 재검증 | 이유/상태가 명확하고 보수적 | admin/상태 컬럼 필요 | **권장** |
| C. 소비자 요청 빈도 기반 우선순위 | 실사용 커버리지부터 개선 | 요청 body/결과 계측 보완 필요 | B와 병행 |
| D. generic LLM review 반복 | 구현 변경이 작음 | 공식 근거를 만들지 못함 | 비권장 |

권장 상태 필드는 `parked_reason`, `evidence_required`, `next_retry_at`, `last_outcome`, `review_owner`다. notes 문자열 파싱은 표시용으로만 남긴다.

### P1-3. 공식 source connector 부족

#### 현재 상태

- Wikidata/TMDb는 계속 유효하지만 롱테일 신규 수율의 천장이 확인됐다.
- iTunes/Discogs는 confirm-only라 잘못된 값을 막는 데 유효하지만 신규 locale 발견에는 약하다.
- Naver Search client는 registry상 `live`지만 LocalFill 앞단에는 연결되지 않았다.
- 방송사/OTT/국내 DSP/중화권 DSP의 다수 source는 registry상 `plan`이다.
- source registry는 UI/정책 목록이며 실행 scheduler가 아니다. SourceExpand는 하드코딩된 순서로 실행된다.

#### 권장 source-first cascade

| 엔티티 유형 | 1차 공식 source | 2차 source | discovery fallback |
|---|---|---|---|
| person | Wikidata QID, Naver People, KOFIC/KMDb | agency/broadcaster 공식 profile | Naver exact-name |
| group | MusicBrainz artist, agency 공식 profile | official music platform artist page | Naver exact-name |
| movie | TMDb, KOFIC movie ID, KMDb | Netflix/Disney/Watcha/Wavve/Viki 공식 page | Naver web/news |
| drama/show | TMDb, 방송사 공식 program page | 국내/해외 OTT 공식 page | Naver web/news |
| song | MusicBrainz recording, iTunes track ID, DSP track page | Spotify, YouTube official evidence | exact title+artist search |
| album | MusicBrainz release-group, iTunes collection ID, DSP album page | label 공식 discography | exact title+artist search |
| event/place | organizer/venue 공식 page | broadcaster award page | exact-name search |

공통 규칙은 다음과 같다.

- discovery 결과나 LLM 생성값은 승급 앵커가 아니다.
- 공식 URL에서 entity type, provider ID, exact name, 필요 시 parent/artist 관계를 검증한 뒤 채택한다.
- provider별 `discover/verify/fill`, quota, retry, health, supported types/locales 계약을 registry에 둔다.
- connector 수율은 calls, match, promoted, filled cells, no-match, transient, p95로 측정한다.

### P1-4. `done`과 실제 결과가 다른 관측성

#### 현상

- research row는 background site search 완료 전에 `done`이 된다.
- `done=8,157`이지만 요청 키워드의 15.4%는 candidate 또는 entity 없음이다.
- Hermes `status=ok`는 실행 오류가 없다는 뜻이며 데이터 개선 성공을 뜻하지 않는다.
- LocalFill은 `items_in=0`으로 기록돼 시도 대비 수율을 계산하기 어렵다.
- Enrich 계측은 일부 경로에서 실제 filled cell과 무관하게 증가할 수 있다.
- source health는 Naver/SearXNG 중심이고 TMDb, iTunes, MusicBrainz, KOFIC 등의 공통 수율/지연 계측이 없다.

#### 대안

작업 상태와 결과 상태를 분리한다.

```text
request_status: queued -> processing -> finished | failed
resolution_status: active | candidate | rejected | not_created
locale_status: complete | partial | blocked_no_source | retrying
job_outcome: applied | noop | no_match | transient | permanent | type_mismatch
```

대시보드는 queue SLA와 answer coverage를 별도 카드로 보여준다. `finished`를 성공률로 사용하지 않는다.

### P2-1. 기사 맥락, 동명이인, 필드 오염의 미폐쇄

#### 남은 항목

- source URL은 저장되지만 발굴 단계의 정체성 판정에서 직접 읽지 않는다.
- active `scope:review` 276건, `contam:review` 363건이 남아 있다.
- Disambiguator는 최근 24시간 50회 중 12회 incident였다. 주요 사유는 정보 부족 quarantine, cluster 밖 `same_as`, LLM/transport 오류다.
- 엔티티 자체는 실존하지만 대표작/소속사/locale 한 필드만 오염된 경우를 entity reject와 분리해 정정하는 경로가 부족하다.

#### 대안

1. source URL을 fetch할 때 SSRF 방지, size/time limit, content hash/cache를 적용한다.
2. 기사 맥락에서 entity mention과 relation을 추출한 뒤 candidate identity evidence로 저장한다.
3. disambiguation 전에 agency, birth year, works, group relation을 공식 source로 보강한다.
4. field-level correction ledger에 old/new/source/reason/revert를 저장한다.
5. LLM은 evidence가 충분할 때만 merge/split을 제안하고, cluster 밖 target은 자동 merge하지 않는다.

### P2-2. 저장소와 운영 문서 부채

- `kwave_rss_items_raw` 76,713건 중 61,345건이 14일 이상이고 약 94MB로 DB의 약 42%다.
- README는 분리형 `kdb-api`/`kdb-worker` 실행을 안내하지만 현재 compose는 통합 `kdb-app` 구조다.
- compose 주석의 2 services와 실제 DB/app/SearXNG 3개 서비스가 다르다.
- `.env.example`은 운영 플래그를 충분히 설명하지 않는다.
- source registry의 `live`가 client 존재인지 end-to-end pipeline 가동인지 구분하지 않는다.

대안은 상태 조건을 둔 14일 batch prune, 현행 배포 문서 갱신, env 계약표, source 상태를 `client_ready/pipeline_wired/live_healthy`로 세분화하는 것이다.

## 4. 권장 목표 구조

```text
Consumer request
  -> normalized request + durable resolution job
  -> mention/canonical normalization
  -> entity-type router
  -> official connector discovery
  -> provider ID/type/name/relation verification
  -> candidate promotion decision
  -> locale fill jobs
  -> source-priority write guard + audit/revert
  -> serving cache/tier update

Fallback order
  official structured API
  -> official page connector
  -> curated discovery search
  -> community/search evidence for review
  -> empty value
```

### 최소 job schema

```text
source_jobs
  id, entity_id, request_id, job_type, provider, locale, priority
  status, outcome, attempts, next_attempt_at, leased_until
  input_fingerprint, evidence_url, provider_external_id
  error_class, error_text, created_at, updated_at

UNIQUE(entity_id, job_type, provider, locale, input_fingerprint)
```

이 구조는 중복 goroutine, 재시작 유실, 30일 오류 억제, fixed ticker no-op, foreground 우선순위 문제를 한 경계에서 해결한다.

## 5. 실행 순서

### Phase 0. 오염 확산과 무수익 반복 차단, 1일

1. 미커밋 변경을 release 단위로 분리하고 hermetic test를 만든다.
2. MusicBrainz `song_album` 자동 승급을 중지하고 기존 3건을 검토한다.
3. `KDB_LOCALFILL_REGROUND=0`, batch 10으로 24시간 canary한다.
4. `DrainQuality`를 8초 주기에서 제거하고 입력 변경/event 기반으로 바꾼다.
5. discovery source가 0일 때 locale site-search를 조기 skip하고 상태를 기록한다.

### Phase 1. 상태 분리와 scheduler 수렴, 2~4일

1. `rss_poll_enabled`/`discovery_enabled`를 분리한다.
2. outcome, `next_attempt_at`, backoff, exhaustion을 공통화한다.
3. superseded Hermes legacy step을 제거하거나 audit-only로 바꾼다.
4. source별 process-wide limiter/breaker를 둔다.
5. queue SLA와 answer coverage 대시보드를 분리한다.

### Phase 2. 공식 connector, 1~2주

1. Naver exact-name discovery를 LocalFill/source router 앞단에 연결한다.
2. 작품은 TMDb ID, KOFIC movie ID, 방송사/OTT 공식 page connector를 구축한다.
3. 음악은 artist/recording/release-group을 분리하고 iTunes/DSP artist anchor를 함께 검증한다.
4. typed quarantine을 유형별 connector job으로 재투입한다.

### Phase 3. 품질 폐루프, 이후

1. source URL 기사 맥락 판정
2. field-level correction/revert
3. disambiguation evidence 선보강
4. raw retention과 문서/설정 정리

## 6. 검증 지표와 중단 조건

| 지표 | 목표 |
|---|---:|
| API 전체 p95 | 1초 미만 유지 |
| 연구 queue e2e p90 | 20초 미만 유지 |
| autopilot cycle wall time | 300초 미만 |
| zero-yield role 입력량 | 현재 대비 80% 이상 감소 |
| LocalFill | 시도/검색/공식증거/적용을 모두 계측, 0-output 연속 시 자동 backoff |
| 7일 이상 candidate | 2주 내 50% 감소 |
| typed quarantine | reason/next action 100% 보유 |
| MusicBrainz type mismatch | 0건 |
| `/artist/`가 연결된 song_album | 0건 |
| discovery source 0 상태의 무효 호출 | 0건 |
| connector | provider별 match/promote/fill/error/p95 노출 |
| background DB pool wait p95 | API SLA를 침해하지 않는 범위 |

다음 조건이면 canary를 중단한다.

- authoritative 오승급 1건 이상
- API p95 1초 초과가 지속
- provider 429/403 급증
- 기존 값 overwrite drift 증가
- candidate 자동 reject의 복원 요청 발생

## 7. 최종 의사결정

### 채택 권고

- dirty restart가 아닌 검토 가능한 release artifact
- RSS polling과 discovery 상태 분리
- LocalFill reground 축소와 no-op backoff
- durable source job/lease/outcome
- entity type별 공식 connector 계약
- Naver는 discovery, 공식 provider/page는 승급 anchor
- candidate/typed quarantine 전용 폐루프
- queue 처리속도와 실제 답변 커버리지의 분리 계측

### 채택하지 않을 것

- RSS 전량 일괄 재활성화
- 검색 결과 또는 LLM 생성값의 자동 authoritative 승급
- GPU/LLM 증설로 source 부재를 해결하려는 접근
- 오래된 candidate의 일괄 자동 reject
- 성공 결과가 없는 fixed ticker 반복
- `song_album`을 MusicBrainz artist로 검증하는 현재 경로

## 8. 주요 코드 근거

- live/workspace 차이: `docs/runbooks/KDB.handoff-20260710.md:5`, `:165-176`
- 현행 온디맨드 전략: `docs/runbooks/KDB.handoff-20260704.md:66-70`
- RSS와 discovery의 `enabled` 결합: `internal/kdb/rss_poller.go:78-85`, `internal/kdb/site_search.go:234-249`
- background site search와 조기 queue 종료: `internal/kdb/research/worker.go:277-298`, `:331-365`
- LocalFill 대상/쿨다운: `internal/kdb/localfill.go:196-238`
- MusicBrainz artist-only client: `internal/kdb/musicbrainz/client.go:51-71`
- 잘못된 group/song_album 공용 drain: `internal/kdb/musicbrainz_drain.go:19-36`, `:65-84`
- 8초 research ticker의 반복 작업: `cmd/kdb/main.go:851-869`
- async grounding enqueue: `internal/kdb/enrich/orchestrator.go:285-306`
- 공식 source 정책/계획: `internal/kdb/source_policy.go:112-146`
- 기존 코드레드의 미처리 권고: `docs/runbooks/KDB_CODERED_PLAN_20260628.md:67-77`
