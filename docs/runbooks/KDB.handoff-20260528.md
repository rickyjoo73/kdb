# KDB 독립 서비스 핸드오프 #3 (2026-05-28)

`KDB.handoff-20260527-2.md` 이후 codex-bridge / kdb-admin / login 흐름 완성. 페이지 디자인까지는 mediafine 스타일로 끌어올렸으나, **페이지 콘텐츠(필드/표/통계)는 mediafine 대비 빈약** — 다음 세션 핵심 과제는 §3.

## 0. 30초 상태 확인

`.52`(`114.203.210.52`, port 38371, user `kdb`)에 ssh:

```bash
# 0-1) 새 셸이면 docker 그룹 + Node 22 PATH 자동 적용
exec bash -l
id              # groups 에 docker
node --version  # v22.22.3
codex --version # codex-cli 0.134.0 (user 영역, /usr/local 0.118.0은 미사용)

# 0-2) KDB 컨테이너 5개 healthy
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep -E 'kdb|codex-bridge|NAMES'
# 기대: kdb-db / kdb-api / kdb-worker / codex-bridge / kdb-admin 모두 Up + healthy

# 0-3) 공개 도메인 — JSON API + admin UI
curl -sS https://kdb.aiinplanet.com/v1/health
# → {"entities":...,"ok":true,"phase":"1A","service":"kdb-api"}
curl -sS -o /dev/null -w '%{http_code} %{redirect_url}\n' https://kdb.aiinplanet.com/admin
# → 302 https://kdb.aiinplanet.com/admin/login?next=%2Fadmin

# 0-4) worker sweeper 성공 비율
docker logs --tail 5 kdb-worker 2>&1 | grep 'Sweeper: done' | tail -3
# 기대: succeeded > 0
```

전부 통과면 §3 (admin 페이지 보강) 으로.

---

## 1. 인프라 / 컨테이너 (handoff #2 대비 추가/변경)

### 1.1 codex-bridge

- 이미지: `kdb-codex-bridge:local` (Node 22 + `@openai/codex@0.134.0`)
- 컨테이너: `codex-bridge`, network `kdb_internal`, port `127.0.0.1:9002`
- 인증: ChatGPT 계정 (`~/.codex` bind-mount RW, uid 1014)
- 모델: **`gpt-5.5`** (ChatGPT 인증으로 사용 가능한 유일 모델 — 0.134.0 이상 필수)
- 검증: 첫 sweep cycle `succeeded=7 failed=0` 기록됨.
- 코드: `scripts/codex_bridge/{server.mjs,Dockerfile}` + `docker-compose.kdb.yml` service
- worker env: `KDB_CODEX_BRIDGE_URL`, `CODEX_MODEL=gpt-5.5`, `CODEX_REASONING_EFFORT=medium`

### 1.2 kdb-admin (신규)

- 이미지: `golang:1.23-bookworm` + `go run -buildvcs=false ./cmd/kdb-admin`
- 컨테이너: `kdb-admin`, networks `kdb_internal` + `dockeraiinplanetcom_backend`
- 포트: `127.0.0.1:9101:9101`
- compose profile: `admin` → 띄울 때 `--profile admin` 필요
- `.env` 의존: 없음(전부 optional). 추천 한 줄: `KDB_ADMIN_SESSION_SECRET=$(openssl rand -hex 32)` — 미설정 시 ephemeral 생성 (재시작 시 모든 사용자 재로그인).
- 인증: cookie session (HMAC-SHA256, 7일), DB 저장 `kwave_kdb_admin_users` (bcrypt cost=12)
- first-run setup: `/admin/setup` — admin 0명일 때만 동작, 1명 생기는 즉시 404
- 현재 admin: `rickyjoo@aiinad.com` (DB row 1개)

### 1.3 nginx vhost 변경

`/home/docker.aiinplanet.com/nginx/conf.d/kdb.aiinplanet.com.conf` 에 `location /admin` 추가됨. nginx 컨테이너는 `kdb-admin` 호스트명을 internal DNS로 해석. 기존 `location /` (kdb-api) 그대로 유지. backup: `.bak-20260528-065447`.

수정 권한: kdb 사용자가 docker 그룹 소속이라 `docker exec nginx` 후 `nginx -t` + `nginx -s reload` 가능 (root 명시 권한 불필요).

### 1.4 DB schema 추가

`migrations/0057_kdb_admin_users.sql` — `kwave_kdb_admin_users` 테이블 (bcrypt hash + 5-fail/15분 lockout). 이미 적용됨.

### 1.5 git 상태

| commit | 내용 |
|---|---|
| `1cb2480` | feat: add KDB admin UI (kdb-admin) with mediafine-style layout |
| `d699c87` | feat: add codex-bridge service for KDB worker LLM extraction |

`main` 브랜치, **origin push 미완료** — deploy key가 read-only로 등록되어 권한 부족.

---

## 2. 인증 / 도구 갱신

### 2.1 codex CLI 모델 제약 (handoff #2 §2.3 해결됨)

`codex-cli 0.134.0`을 user 영역(`~/.local/opt/node-22/bin/codex`)에 설치 → ChatGPT 계정으로 `gpt-5.5` 모델 사용 가능. 다른 모델(`gpt-5`, `gpt-5-codex`, `o3` 등)은 여전히 차단. API key 발급 불필요.

### 2.2 mediafine `.59` ssh

사용자가 password 직접 알려줘서 mediafine repo 코드를 .52로 발췌함. **password 노출됨 — 회전 필수** ([[feedback-secret-handling]] §4 참조).

접속 정보 (회전 후 갱신):
- host: `114.203.210.59`
- port: `38371`
- user: `mediafine`
- mediafine repo: `/home/global.mediafine.co.kr/`
- admin code: `internal/admin/{handlers,middleware,templates}/`

발췌본 위치: `/tmp/mfadmin/` (재부팅 시 사라짐). 다음 세션은 ssh 재접속 또는 deploy key 등록 후 git clone 권장.

### 2.3 GitHub deploy key

`rickyjoo73/kdb` deploy key가 **read-only**로 등록되어 `git push` 실패. fingerprint: `SHA256:6FDSo5IawyaqeSd11OUheDjbGF1kpR8ULI5JHemgzSs`.

GitHub Settings → Deploy keys → 해당 키 "**Allow write access**" 체크박스만 켜주면 즉시 push 가능. 또는 PAT 발급.

---

## 3. 남은 작업 (다음 세션 핵심)

### 3.1 admin 페이지를 mediafine 수준으로 보강 [in progress, 0%]

현재 10개 페이지 모두 read-only 표시는 되지만 필드/필터/통계가 mediafine 대비 매우 빈약. **라인 수 격차**:

| 페이지 | mediafine | KDB | ratio |
|---|---|---|---|
| entity_sources | 311 | 25 | **12.4x** |
| entities | 204 | 42 | 4.8x |
| entity_conflicts | 105 | 23 | 4.5x |
| entity_detail | 330 | 82 | 4.0x |
| entity_whitelist | 155 | 39 | 3.9x |
| persons | 136 | 42 | 3.2x |
| entity_review | 67 | 26 | 2.5x |
| kdb_observations | 78 | 32 | 2.4x |
| kdb_candidates | 94 | 41 | 2.2x |
| kdb_codex_runs | 70 | 41 | 1.7x |

**진행 순서 (1 step = 1 commit 권장):**

- **A**: `entitiesList` + `entity_detail` 보강
  - 유형 필터(`?type=person` 등 13개 enum), 유형 분포 카드, 다국어 컬럼(en/ja/vi/id/es/pt-br/zh-hant/zh), aliases_ko chip, last_verified
  - 위/아래 pagination + total/index, 빈 결과 안내
  - entity_detail: 9-locale aliases (KO/EN/JA/VI/ID/ES/PT-BR/ZH-Hant/ZH legacy)
  - entity_detail: 인물 상세 섹션 (`kwave_persons` LEFT JOIN by `name_ko = canonical_ko` when `entity_type='person'`)
  - entity_detail: 관계 섹션 (`kwave_entity_relations` from/to, 데이터 0건이지만 코드 준비)
  - entity_detail: 조사 이력 (`kwave_entity_research_queue` WHERE `entity_ko = canonical_ko`)
  - 제외: 외부 ID (`kwave_entity_external_ids` 미존재), Resolver Cascade History (`kwave_entity_resolver_history` 미존재) — KDB는 아직 Wikidata cascade 미구현
- **B**: `entity_sources`
  - 9 locale 채움 비율 (`kwave_entities` `canonical_*` IS NOT NULL 비율) + progress bar
  - cascade 설명 (1차 Wikidata / 2차 TMDb·KOFIC·MusicBrainz·KMDb / 3차 K-news whitelist / 4차 LLM)
  - missing-locale queue 표 — backfill POST endpoint는 Phase 2
- **C**: `persons` + `entity_conflicts` + `entity_whitelist`
  - persons: role 필터, agency/birth_year/gender/groups/notable_works, 검색
  - conflicts: 별칭 충돌 유지, click → 해결 UI (Phase 2)
  - whitelist: trust/enabled 필터, locale별 sub-summary, 도메인별 상세
- **D**: `kdb_candidates` + `kdb_codex_runs` + `kdb_observations` + `entity_review`
  - 각 페이지에 status 필터, total/pagination, 운영자 액션 버튼 placeholder

**SQL 참고:**
- entity_type enum (13): `person`, `group`, `show`, `drama`, `movie`, `song_album`, `agency`, `channel_outlet`, `brand_place`, `event_tour`, `character`, `term`, `unknown`
- person_role enum (16): `idol`, `singer`, `rapper`, `actor`, `broadcaster`, `comedian`, `director`, `producer`, `model`, `creator`, `athlete`, `politician`, `businessperson`, `journalist`, `fictional`, `other`
- 현재 entities=1503, persons=555

**디자인 패턴 (mediafine과 일치):**
- 카드: `bg-white rounded-lg shadow-sm border border-slate-200`
- 카드 헤더: `px-3 py-2 bg-slate-50 border-b border-slate-200 text-xs font-medium text-slate-700`
- 표 헤더: `bg-slate-50 text-xs text-slate-600 uppercase`
- 행 hover: `hover:bg-slate-50`
- 빈 상태: `p-4 text-sm text-slate-400`
- entity_type badge: `bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono`
- 별칭 chip: `bg-slate-100 text-slate-700 rounded px-1.5 py-0.5`
- 상태 색: matched=green-700, rejected=orange-700, error=red-700, in_progress=blue-700, pending=amber-700, done=green-700, failed=red-700

발췌본 위치: `/tmp/mfadmin/templates/pages/*.html` (재부팅 시 사라짐 — 다음 세션은 ssh로 다시 받을 것).

### 3.2 GitHub push 권한 확보

deploy key write 권한(또는 PAT)이 풀리면:
```bash
git push origin main   # 1cb2480 + 후속 commit 들
```

### 3.3 mediafine `.59` → KDB API URL 전환 (cutover 마무리)

handoff #2 §3.4 그대로. `.59` 권한자(또는 mediafine 계정) 작업.

```env
# .59 mediafine .env
KDB_API_URL=https://kdb.aiinplanet.com
KDB_API_KEY=<.52와 동일 키>
```

### 3.4 site search backfill

mediafine `EntitySourcesSiteSearchPath` POST → KDB 쪽 endpoint 신설 필요. mediafine `handlers/content_pages.go` 의 SiteSearch handler 참조.

---

## 4. 보안 미해결 (회전 필수)

이번 세션에서 평문 노출된 secret 4개. 모두 회전 권고:

1. **`KDB_API_KEYS`** — handoff #2부터 누적 노출 (`docker compose config` 실행 시)
2. **`KDB_POSTGRES_PASSWORD`** — 동일
3. **사용자 비밀번호 `jdch2002!@`** — handoff #2 시점
4. **mediafine `.59` `jdch0513!#`** — 이번 세션 (3.x 페이지 분석 위해 ssh 접속)

`.env` 출력 시 redaction은 단어 경계 매칭 + 알파넘 32+자 sed 마스킹 ([[feedback-secret-handling]]).

또 `KDB_ADMIN_SESSION_SECRET` 안 박혀있어 매 재시작마다 모든 admin 로그아웃. `.env`에 `KDB_ADMIN_SESSION_SECRET=$(openssl rand -hex 32)` 한 줄 추가 권장.

---

## 5. 외부 참조 / 메모리

- handoff #1: `docs/runbooks/KDB.handoff-20260527.md`
- handoff #2: `docs/runbooks/KDB.handoff-20260527-2.md`
- 마스터 플랜: `docs/KDB_PLATFORM_SEPARATION_PLAN.md`
- entity 모델: `docs/ENTITY_DB_PLATFORM.md`
- mediafine admin 발췌본(휘발성): `/tmp/mfadmin/`

memory는 `.claude/projects/-data-home2-kdb-aiinplanet-com/memory/MEMORY.md` 인덱스 → 개별 파일.

---

## 6. 한 줄 요약

> `.52`의 KDB는 **API + worker + LLM bridge + admin UI** 모두 가동. cookie session 로그인 + 10개 메뉴 + mediafine 스타일 sidebar 완성. **남은 일은 페이지 콘텐츠를 mediafine 수준으로 채우는 것** (가장 큰 격차 entity_sources 12.4x), 그리고 push 권한 풀기.
