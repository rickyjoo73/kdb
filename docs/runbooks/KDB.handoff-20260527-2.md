# KDB 독립 서비스 핸드오프 #2 (2026-05-27 저녁)

이 문서는 동일 날짜 첫 핸드오프(`KDB.handoff-20260527.md`)의 후속이다.
첫 핸드오프 작업이 끝난 직후 `.52` 운영 상태로 진입했고, 그 다음 단계
(codex bridge 자체 구축, admin UI 신설) 진입 중 막혔다. 다음 세션은
이 문서만 읽으면 즉시 이어서 작업 가능하다.

## 0. 30초 상태 확인 — 다음 세션 첫 명령

`.52`(`114.203.210.52`, port 38371, user `kdb`)에 ssh로 들어가서:

```bash
# 0-1) 새 그룹/PATH 적용 — bash 새 셸이면 자동 적용. 그렇지 않으면:
exec bash -l    # ~/.bashrc 의 Node 22 PATH 적용
id              # groups 에 docker 포함 확인 (없으면 sg docker -c '...' 래핑 필요)
node --version  # v22.22.3 이어야 함
codex --version # codex-cli 0.118.0 이어야 함

# 0-2) KDB 컨테이너 3개 healthy
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep -E 'kdb|NAMES'

# 0-3) 공개 도메인 health
curl -sS https://kdb.aiinplanet.com/v1/health
# expected: {"entities":1489,"ok":true,"phase":"1A","service":"kdb-api"}

# 0-4) worker가 codex-bridge에 실패 중인지 (예상: 실패 중)
docker logs --tail 20 kdb-worker 2>&1 | grep -E 'codex-bridge|Sweeper'
```

전부 통과하면 1번/2번은 건너뛰고 3번 (남은 작업) 으로.

---

## 1. 환경 / 인프라 현재 상태

### 1.1 호스트

- **`.52`** `114.203.210.52` — `kdb.aiinplanet.com` 운영 호스트. 본 핸드오프는 여기서 작업.
- **`.59`** `114.203.210.59` — `global.mediafine.co.kr` (mediafine app + 기존 codex-bridge). `.52`의 kdb 계정에서 ssh 접근 ❌ (publickey/password 거부). codex-bridge 코드 가져오려면 별도 권한 필요.

### 1.2 `.52` `kdb` 계정 권한

- ssh: `ssh kdb@114.203.210.52 -p 38371`
- `sudo` ❌
- `docker` 그룹 ✅ (handoff #1 작업 후 root가 `usermod -aG docker kdb` 실행)
  - `getent group docker` 출력에 `kdb` 포함
  - 현 셸이 그룹 캐시 갱신 전이면 `sg docker -c "..."` 또는 `newgrp docker` 로 래핑
  - **새 ssh 세션이면 자동으로 docker 그룹 적용됨**
- `~/.bashrc` 에 Node 22 PATH 영구 추가 (`export PATH="$HOME/.local/opt/node-22/bin:$PATH"`)

### 1.3 디렉토리 / 파일 (핵심만)

```
/data/home2/kdb.aiinplanet.com/        # repo root, working dir
├── docker-compose.kdb.yml              # 미커밋 변경 있음 (1.5 참조)
├── .env                                # mode 600, KDB_POSTGRES_PASSWORD/DATABASE_URL/KDB_API_KEYS
├── kdb-mediafine-dump.sql              # 13MB, SHA256 8bf31b6f...
├── data/postgres/                      # kdb-db bind-mount (uid 70 postgres)
├── docs/runbooks/
│   ├── KDB.handoff-20260527.md         # 1차 핸드오프 (원본)
│   └── KDB.handoff-20260527-2.md       # 이 문서
├── cmd/kdb-api/, cmd/kdb-worker/       # Go binaries
├── internal/kdb/                       # worker 비즈니스 로직
├── internal/kdbapi/api.go              # /v1/* REST handler 만, /admin/* 없음
├── migrations/                         # 0049~0056
└── scripts/codex_schemas/kdb_extract.schema.json
~/.local/opt/node-22 → node-v22.22.3-linux-x64
~/.codex/auth.json                      # mode 600, codex ChatGPT 토큰 (4435 bytes)
~/.ssh/id_github_kdb_aiinplanet         # GitHub deploy key (rickyjoo73/kdb 만)
```

### 1.4 컨테이너

| name | image | ports | networks | status |
|---|---|---|---|---|
| `kdb-db` | `postgres:16-alpine` | `127.0.0.1:5436→5432` | `kdb-platform_kdb_internal` | healthy |
| `kdb-api` | `golang:1.23-bookworm` (`go run`) | `127.0.0.1:9100→9100` | `kdb_internal`, `mediafine_default`, `dockeraiinplanetcom_backend` | healthy |
| `kdb-worker` | `golang:1.23-bookworm` (`go run`) | — | `kdb_internal`, `mediafine_default` | up, but sweeper failing (codex-bridge 미존재) |

compose project name: `kdb-platform`.

### 1.5 `docker-compose.kdb.yml` 미커밋 변경 (운영 적용 중)

핸드오프 #1 이후 외부 작업자가 다음을 수정한 상태로 working tree에 떠있고 git commit 안 됨:

- 각 service에 `container_name` 명시 (`kdb-db`, `kdb-api`, `kdb-worker`)
- DB 볼륨: named volume `kdb-db-data` → bind mount `./data/postgres`
- 외부 네트워크: `dockers_backend` → `dockeraiinplanetcom_backend` (`.52` 실제 이름)

이 변경은 `.52` 환경에 필수. `.59`에 다시 적용하려면 충돌할 수 있음 → commit 시 host별 override 파일 분리 권장 (예: `docker-compose.kdb.52.yml`).

### 1.6 DB 상태

- 테이블 14개 (`kwave_*`) 모두 존재
- 카운트: `entities=1489`, `persons=555`, `media_observations=3305`, `entity_research_queue=343`
- enum: `kwave_entity_type` (13), `person_role` (16) 모두 존재
- migrations 적용 확인됨: **0050, 0051, 0054, 0055, 0056 전부 schema에 반영됨**
  - migration 추적 테이블은 없음 → 다음 migration 추가 시 적용 여부는 schema probe로 확인

### 1.7 공개 도메인

- DNS: `kdb.aiinplanet.com` → `114.203.210.52`
- HTTPS: HTTP/2 200, nginx 1.29.5
- TLS: Let's Encrypt E7, `notAfter=Aug 25 12:20:25 2026 GMT` (90일, renew 필요)
- HTTP → HTTPS 301 redirect 정상
- 보안 헤더: HSTS `max-age=31536000; includeSubDomains`, X-Frame-Options SAMEORIGIN, X-Content-Type-Options nosniff
- `POST /v1/lookup` 인증 헤더: **`X-KDB-Key: <key>`** 또는 **`Authorization: Bearer <key>`** 둘 다 지원
- `GET /v1/lookup`은 405 (POST만)

검증된 호출 예:
```bash
curl -sS -X POST -H 'Content-Type: application/json' -H "X-KDB-Key: $KEY" \
  -d '{"query":"BTS"}' https://kdb.aiinplanet.com/v1/lookup
# → BTS = 방탄소년단 매칭 정상
```

---

## 2. 인증 / 도구 셋업

### 2.1 Node 22 LTS (kdb 사용자 영역)

- 위치: `/data/home2/kdb.aiinplanet.com/.local/opt/node-22` (symlink to `node-v22.22.3-linux-x64`)
- PATH: `~/.bashrc`에 영구 등록 — `export PATH="$HOME/.local/opt/node-22/bin:$PATH"`
- 다음 ssh 세션부터 자동으로 `node` v22, `npm`, `npx` 잡힘
- 시스템 `/usr/bin/node`는 v12 (그대로 둠 — 다른 사용자 영향 없음)

### 2.2 OpenAI Codex CLI

- 패키지: `/usr/local/lib/node_modules/@openai/codex` (시스템 글로벌)
- 바이너리: `/usr/local/bin/codex`
- 버전: `codex-cli 0.118.0`
- 인증: ✅ **ChatGPT 계정 (`rickyjoo@aiinad.com`)으로 device-auth 완료**
- 토큰 위치: `~/.codex/auth.json` (mode 600, 4435 bytes)
- 확인: `codex login status` → `Logged in using ChatGPT`

### 2.3 codex CLI 모델 제약 (해결됨 — 2026-05-27 저녁 update)

`codex-cli 0.134.0`을 user 영역(`~/.local/opt/node-22/bin/codex`)에 설치하고 재검증한 결과 **`gpt-5.5`가 ChatGPT 계정 인증으로 사용 가능**. 다른 모델은 여전히 차단.

| 시도 모델 | 0.118.0 (system) | **0.134.0 (user)** |
|---|---|---|
| `gpt-5.5` | ❌ `requires a newer version of Codex` | ✅ 통과 (pong) |
| `gpt-5.2-codex` (codex 기본) | ❌ | ❌ `not supported when using Codex with a ChatGPT account` |
| `gpt-5-codex` | ❌ | ❌ |
| `gpt-5` | ❌ | ❌ |
| `o3`, `o4-mini`, `gpt-4.1` | ❌ | ❌ |

**적용 사항:**
- 시스템 글로벌 codex 0.118.0(`/usr/local/bin/codex` → `/usr/local/lib/node_modules/@openai/codex`)은 root 권한 없어 그대로 둠. 또한 `/usr/bin/node` v12로 실행되어 SyntaxError 발생.
- user 영역 codex 0.134.0이 PATH에서 우선 잡히도록 `~/.bashrc`의 `~/.local/opt/node-22/bin` 등록 활용. 새 ssh 세션에서 자동 적용.
- codex-bridge는 **`gpt-5.5`**로 호출하면 됨. API key 발급 불필요.

이전 옵션 (a)~(c)는 무효. codex-bridge 컨테이너 내부에도 동일 codex(0.134.0)를 npm install + `~/.codex` mount로 인증 공유하는 방식으로 구현 예정.

---

## 3. 남은 작업 (다음 세션 진행할 순서)

### 3.1 codex-bridge 구축 [DONE — 2026-05-27 저녁]

**완료 상태**:
- `scripts/codex_bridge/server.mjs` (Node 22 HTTP server, `--output-schema`로 JSON 강제, `gpt-5.5` 호출)
- `scripts/codex_bridge/Dockerfile` (node:22-bookworm-slim + `@openai/codex@0.134.0`)
- `docker-compose.kdb.yml`에 `codex-bridge` service 추가
  - uid 1014/gid 1014로 실행 → bind-mounted `~/.codex` 권한 정합
  - bind-mounts: `server.mjs:ro`, `scripts/codex_schemas:ro`, `~/.codex:rw`
  - 포트 `127.0.0.1:9002`, network `kdb_internal`
- `kdb-worker` compose에 `depends_on: codex-bridge healthy` + env var (`KDB_CODEX_BRIDGE_URL`, `CODEX_MODEL=gpt-5.5`) 추가

**검증**:
- 단발 curl `/health` ✅, `/kdb_extract` (BTS sample) → spellings 5건 정확 ✅
- worker 첫 sweep cycle: `kdb.Sweeper: done processed=7 succeeded=7 failed=0` (breaker reset, half-open → close)

**운영 메모**:
- `CODEX_BRIDGE_FORCE_MODEL=1` 환경변수 설정 시 worker가 보낸 model 무시하고 `gpt-5.5` 강제.
- codex-bridge는 fast tick(`bridge_health.go`)에서 `/health` 프로브로 감시됨.
- 비밀스러운 trace: `docker logs codex-bridge` JSON 라인 (`level=info` 호출 / `level=warn` 실패).

worker가 호출하는 endpoint:
- `POST http://codex-bridge:9002/kdb_extract` — body schema는 `internal/kdb/extractor.go:138` 부근
- `GET  http://codex-bridge:9002/health`     — health probe

기대 응답 schema (이미 repo에 있음): `scripts/codex_schemas/kdb_extract.schema.json`

```json
{"spellings":[{"ko_hint":"...","locale":"en","spelling":"...","confidence":0.0-1.0}]}
```

구현 plan (결정 후):

1. **소스 코드 위치**: `cmd/codex-bridge/main.go` (Go로 통일) 또는 `scripts/codex_bridge/server.mjs` (Node — codex CLI 직접 호출 시 편함)
2. **컨테이너화**: `docker-compose.kdb.yml`에 `codex-bridge` 서비스 추가
   - 이미지: `node:22-bookworm` (Node bridge) 또는 `golang:1.23-bookworm` (Go bridge)
   - mount: `~/.codex:/workdir/.codex` (인증 토큰 공유 — 단 codex CLI 호출 방식일 때만)
   - 네트워크: `kdb_internal` 가입 → worker가 `codex-bridge` 호스트명으로 도달
   - 포트: `127.0.0.1:9002:9002` (외부 노출 안 함)
3. **handlers**:
   - `GET /health` → `{"ok":true,"provider":"codex","model_default":"<선택모델>"}`
   - `POST /kdb_extract` → request {locale,title,description,hints[]} → codex 호출 → response {spellings:[...]}
4. **prompt template**: title + description + hints로 외국어 표기 추출 지시. JSON only 응답 강제.
5. **검증**: 
   - 단독 curl 테스트
   - `docker logs kdb-worker` 에서 `sweeper succeeded>0` 확인

### 3.2 KDB admin UI 신설 [BLOCKED — mediafine repo 접근 필요]

이 repo 전수조사 결과: **admin 코드 0개** (handler/template/embed 전혀 없음).

기존 admin은 mediafine에 있고 (분리 계획서 `docs/KDB_PLATFORM_SEPARATION_PLAN.md` 라인 100~120 참조), 옮겨야 할 메뉴:

- `/admin/entities`, `/admin/entities/{id}`
- `/admin/entities/sources`, `/admin/entities/review`, `/admin/entities/conflicts`, `/admin/entities/whitelist`
- `/admin/kdb/candidates`, `/admin/kdb/codex-runs`, `/admin/kdb/observations`
- `/admin/persons`

세 가지 path:

| 방식 | 시간 | 메모 |
|---|---|---|
| mediafine 템플릿/handler를 그대로 발췌 이관 | 짧음 | mediafine repo deploy key 필요 (3.3) |
| `cmd/kdb-admin` 신설하고 처음부터 작성 | 김 | KDB API 위에 thin HTML 서버 |
| `kdb-api`에 HTML 라우트 추가 (`/admin/*`) | 중간 | 단일 binary 유지 가능 |

사용자 의향: "mediafine에서 부족한 부분만 참조" — 즉 (1) 방식 선호.

### 3.3 mediafine GitHub repo 접근 [BLOCKED — deploy key 등록 필요]

- 사용자가 알려준 URL: `https://github.com/rickyjoo73/meidafine-global.git` ("mediafine" 오타로 추정되나 그대로 시도해도 404)
- `rickyjoo73` 의 public repo는 `kdb` 하나뿐 → mediafine repo는 private
- `.52` `kdb`의 ssh 키는 `rickyjoo73/kdb` deploy key로만 등록됨
- **다음 행동**: mediafine repo Settings → Deploy keys → 아래 키 추가 (read-only)
  ```
  ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICGwq+jXNPauTSY8QpW0fUJl7Nxy3gfM9zsO83Uc18M4 kdb@114.203.210.52 aiinplanet/kdb.aiinplanet.com
  ```
  fingerprint: `SHA256:6FDSo5IawyaqeSd11OUheDjbGF1kpR8ULI5JHemgzSs`
- 등록 후 `git clone git@github.com:rickyjoo73/<정확한이름>.git /tmp/mediafine`

### 3.4 mediafine → KDB API URL 전환 (cutover 마무리)

`.52` 공개 API가 검증됐으므로 `.59`의 mediafine `.env`에서:
```env
# 변경 전 (.59 내부 docker DNS)
KDB_API_URL=http://kdb-api:9100
# 변경 후 (공개 도메인)
KDB_API_URL=https://kdb.aiinplanet.com
KDB_API_KEY=<.52와 동일 키>
```
바꾸고 mediafine app 재기동 → app 컨테이너 내부에서 `/v1/lookup` 호출 검증.

이 작업은 `.59` 권한이 있는 분이 진행 (`.52`에서는 불가).

### 3.5 mediafine 내부 KDB 정리 (cutover 후 후처리)

- mediafine worker 중단
- mediafine DB 안의 KDB 테이블 백업 후 제거 여부 검토
- mediafine repo의 임시 `docker-compose.kdb.yml` 의존 제거

이건 3.4 검증이 끝나고 일정 기간 soak 후에 진행.

---

## 4. 보안 미해결 / 경고

### 4.1 노출된 secret (회전 권고)

- **`KDB_API_KEYS`**: 이전 세션 작업 중 화면에 한 번 그대로 출력됨 (`.env` redaction regex가 `KEYS` 복수형을 못 잡음). 회전 권장. 회전 시 `.52`의 `.env`와 mediafine `.52` 호출 측 `.env`를 같이 교체해야 호출 끊김 없음.
- **사용자 비밀번호 `jdch2002!@`**: 평문으로 대화에 노출됨. 회전 권장.

### 4.2 secret 다루는 원칙 (이번 세션 학습)

- `.env` 출력 시 redaction 헬퍼는 반드시 단어 경계 매칭 — `KEYS=` (복수형), `URL=` (DATABASE_URL) 모두 잡혀야 함.
- 비밀번호/토큰을 변수로 캡처해 다음 명령에 전달 — 화면 echo X.
- `docker logs`에 secret 포함 가능 — `sed 's/[[:alnum:]]\{32,\}/<redacted-hex>/g'` 등 필터 통과.

### 4.3 `.env` 정합성

- 1차 핸드오프 시점 `DATABASE_URL`이 `postgres://kdb:change-me@...`로 plaintext 깨져있었음. `.52` 외부 작업자가 `KDB_POSTGRES_PASSWORD`와 동기화함 — 현재 OK.
- compose가 정상 기동 = `.env`가 정합 상태.

---

## 5. 기타 발견

### 5.1 worker 현재 동작

```
kdb.Poller: cycle=100 done feeds=56 items=5218 cheap_pass=1224 candidates=3536 errors=1
kdb.Sweeper: done processed=30 succeeded=0 failed=30
kdb.Breaker: OPEN for 5m0s (10 consecutive fails)
```

- RSS 폴링은 정상 (56 feeds, 5218 items, cheap_pass 1224)
- Codex 호출 단계에서 100% 실패 (codex-bridge 미존재)
- circuit breaker 5분마다 half-open → 또 fail → OPEN 반복

이 상태에서 lookup API는 영향 없음. 새 entity 정규화 파이프라인만 정지.

### 5.2 kdb-api는 외부 도메인에서 정상 호출 가능

```
GET  https://kdb.aiinplanet.com/v1/health        → 200 (no auth)
POST https://kdb.aiinplanet.com/v1/lookup        → 401 no auth / 200 with valid key
POST https://kdb.aiinplanet.com/v1/lookup/bulk   → 동일
```

### 5.3 nginx — host의 PHP nginx와 공존

`.52` host nginx 1.29.5가 `kdb.aiinplanet.com` vhost와 PHP 사이트들을 함께 서빙. KDB vhost가 `/v1/`을 host loopback (127.0.0.1:9100)으로 reverse proxy. 정확한 conf 위치는 `.52` 외부 작업자가 설치했고 아직 확인 안 함.

---

## 6. 메모리 및 외부 참조 위치

이 핸드오프 외 추가 참고:

- `docs/runbooks/KDB.handoff-20260527.md` — 1차 핸드오프 (`.59` → `.52` 초기 이전)
- `docs/KDB_PLATFORM_SEPARATION_PLAN.md` — KDB 분리 마스터 플랜 (admin Phase 4 명세 등)
- `docs/ENTITY_DB_PLATFORM.md` — KDB entity 모델 설계
- `docs/runbooks/KDB.independent-db-cutover.md` — DB cutover runbook
- `docs/runbooks/KDB.phase2-api-adoption.md` — sibling site API 채택 runbook
- `docs/runbooks/KDB.remote-site-cutover.md` — 원격 사이트 cutover runbook
- `docs/runbooks/KDB.site-search.md` — site search 기능 runbook

---

## 7. 다음 세션 권장 순서

1. **§0** 30초 체크 통과 확인
2. **§2.3 결정** — codex 모델 호환 이슈를 (a) API key 발급 / (b) mediafine 패턴 / (c) 다른 모델 중 어느 길로 갈지 사용자에게 확인
3. **§3.3** mediafine repo deploy key 등록 — 사용자 측 작업
4. **§3.1** codex-bridge 구현 → kdb-worker sweeper 성공 확인
5. **§3.2** admin UI 이관/구현
6. **§3.4 / §3.5** cutover 마무리 단계

---

## 8. 한 줄 요약

> `.52`의 KDB 독립 서비스는 **API/DB/공개도메인이 전부 가동 중**이지만, worker의 **LLM 추출 단계 (codex-bridge)** 와 **관리자 UI** 두 컴포넌트가 비어 있다.
> 차단 원인은 (1) codex CLI ChatGPT 인증의 모델 제한, (2) mediafine private repo 접근 부재. 두 차단이 풀리면 약 반나절 작업으로 마무리 가능.
