# KDB handoff — 2026-06-04 (9차)

이 세션 핵심: **"고유명사/인물 다국어가 안 채워진다"의 진짜 원인을 잡아 수정 + 운영 자동화 + admin 전수 UI 검수.**

## 0. 30초 상태 점검
```sh
cd /data/home2/kdb.aiinplanet.com
DSN='postgres://kdb:147f6037443ea3df5dd9df240ce51ff2bbbab1098911e2ee@127.0.0.1:5432/kdb'
docker ps --filter name=kdb-app --format '{{.Status}}'
docker exec kdb-app sh -c 'echo reasoning=$CODEX_REASONING_EFFORT timeout=$CODEX_BRIDGE_TIMEOUT_MS'
# enrich backlog(에이전트 실제 대상) / 미해결 인시던트 / exhausted
docker exec kdb-db psql "$DSN" -t -A -F'|' -c "select
 (select count(*) from kwave_kdb_hermes_runs where resolved=false and status<>'ok') open_incidents,
 (select count(*) from kwave_kdb_enrich_attempts where exhausted) exhausted_fields;"
# 각 role 마지막 실행(분 전) — 전체 cycle 완주 여부
docker exec kdb-db psql "$DSN" -t -A -F'|' -c "select role, round(extract(epoch from (now()-max(created_at)))/60) min_ago from kwave_kdb_hermes_runs group by role order by min_ago desc;"
```
admin: https://kdb.aiinplanet.com/admin (또는 host http://127.0.0.1:9101). 운영 지표는 `/admin/hermes` "Enrich 수렴 현황" 카드.

## 1. 근본원인 2건 — 수정·배포·커밋 완료
**"느리다"의 실체는 throughput 부족이 아니라 enrich 수렴 불능이었음.**

- **(a) 원장 컬럼 누락**: `kwave_kdb_enrich_attempts` 에 `last_source` 컬럼이 없는데(0062 누락) Enricher `recordAttempt` 가 거기 INSERT → **전건 silent-fail**(에러 `_,_=` 로 삼킴) → `exhausted` 영구 미설정. 8개 locale 다 찼지만 정당하게 빈 person 필드(예: 배우의 groups)만 남은 **"가짜 backlog" ~1600건이 매 cycle 무한 재선택**되어 codex 예산 독식 → 진짜 빈 다국어 entity 가 굶음.
  → **migration `0072_enrich_attempts_last_source.sql`** (적용 완료) 로 컬럼 추가.
- **(b) Select 수렴 가드 결함**: `enricher.Select` 가 `(16 - exhausted_count) > 0` 라 일부 필드만 exhausted 여도 재선택 → gaps 없는 Noop 무한루프. → **필드별 exhaustion 인지 쿼리**로 교체(`internal/kdb/agents/enricher/agent.go`). 이제 0 으로 단조 수렴.

부수: **reasoning effort 가 그동안 완전 미배선**(`extractor.go` "codex CLI 호출엔 미사용") → `codexcli.Runner` 에 `-c model_reasoning_effort=<effort>` 배선(`CODEX_REASONING_EFFORT` env). 

커밋 `db2430d`(원장+Select+배선+drain+Phase3 관측성), 모두 origin/main push 됨.

## 2. 운영 자동화 (Phase 3/4) — 완료
- **Phase 3 관측성**: `/admin/hermes` 상단에 `Enrich 수렴 현황` 카드(backlog / source-exhausted(7d) / 24h enriched / 예상 소진 ETA). `handlers_hermes.go enrichBacklogStats`.
- **Phase 4 유입 제어** (`5918d18`): `drain-candidates/drain-bucket/drain-persons/resolve-unknowns` 분류 직후 자동으로 `enricher.DrainConcurrent` 체이닝 → 대량 import 가 빈 entity backlog 스파이크를 안 만들게. `KDB_AUTO_ENRICH_AFTER_DRAIN=0` 으로 비활성.
- **일회성 burst**: `kdb-app drain-enrich [workers]` (Enricher cascade 로 backlog 수렴, `KDB_DRAIN_DEBUG=1` 로깅). 가짜 backlog 814건(비뮤지션 groups)은 사용자 승인하에 exhausted 일괄 마킹함(7일 쿨다운 자가복구).

## 3. ★ reasoning xhigh → high 로 낮춤 (이 세션 마지막 결정, 라이브)
- **xhigh + codex 동시성1 직렬화** 가 맞물려 **전체 Hermes cycle 이 30분 주기를 초과·정체**(2026-06-04 실측: step/Enricher role 95~187분 미실행, enrich/classify 동반 지연, 인시던트 미해소). codex 는 ChatGPT OAuth refresh-token 1회용 때문에 **동시성 1 강제**(풀면 백엔드 다운) — 못 푸는 하드 제약이라, reasoning 을 낮춰 cycle 을 빠르게 함.
- `.env CODEX_REASONING_EFFORT=high`, `CODEX_BRIDGE_TIMEOUT_MS=120000` 로 변경 후 재생성. **결과: cycle 재개 확인**(Enricher 등 다시 돌고 인시던트 261→184 자동 정리 시작).
- **다국어 채움 다 끝난 뒤** 정밀 보정이 필요하면 일시적으로 xhigh 로 올렸다 되돌릴 것.

## 4. admin 전수 UI 검수 (Playwright) — A1/A2 처리, B 잔여
도구: `scripts/pw_admin_review.js` (owner 세션 HMAC 발급 → 14페이지 스샷+진단). 실행:
```sh
export NODE_PATH=/data/home2/kdb.aiinplanet.com/.npm/_npx/e41f203b7505f1fb/node_modules
export KDB_SS=$(grep '^KDB_ADMIN_SESSION_SECRET=' .env | cut -d= -f2-)
export ENTITY_ID=<uuid>; node scripts/pw_admin_review.js   # /tmp/pw/*.png
```
주의: headless chromium 에 CJK 폰트 없으면 한글이 □ → `~/.local/share/fonts/kr.ttf`(Noto Sans KR) 설치해둠. 전 페이지 200·콘솔에러 0(기본 건전성 양호).

- **A1 (완료, `ddf88be`)**: `handleHermes` 가 대문자 `"Title"/"Active"` 를 넘겨 `<title>` 빈값·nav 미강조였음(컨벤션은 소문자 `title`/`page`) → 정합.
- **A2 (완료, `ddf88be`)**: 미해결 인시던트 266건 = 06-02~03 codex 서킷브레이커 open 기간 "circuit breaker open — skipping run" 잔여(에이전트 정상인데 안 치워짐). 근본수정 = breaker-skip 을 critical→benign(resolved=true) 적재 + **role 이 건강한 run 마치면 과거 미해결 인시던트 자동 resolve**(`resolveOpenIncidents`, supervisor 의 ok/empty/breaker 경로 모두). 배포 후 cycle 돌며 자동 소멸 중(261→184→…0). **즉시 일괄 정리는 auto-mode 가 차단**(감사테이블 보호) — 자동해소로 비워지므로 불필요.
- **B 잔여(미착수, UX 개선)**: B1 대형 테이블 sticky header 없음(entities/persons/observations/conflicts/codex-runs, 100+행 덤프) · B2 entities 행 비대(다국어 8개+alias 한 셀, DOM 78KB) · B3 dashboard 하단 raw 운영노트 블록 · B4 소스마크(W/L/O) 범례 페이지 노출.

## 5. 남은 작업 (다음 세션)
1. **수렴/인시던트 자동 소멸 확인**: §0 점검으로 open_incidents→0, enrich backlog 감소 추적. cycle 이 30분 안에 완주하는지(role min_ago 가 다 <35).
2. **B1~B4 UX 개선** (B1 sticky header 부터).
3. 이전 이월: TMDb/KOFIC 필모 backfill, 미디어 워커 하이브리드 통합(KDB=후보발굴원, 섀도모드), `KDB_ADMIN_SESSION_SECRET` 은 이제 `.env` 에 설정됨(영구).

## 빌드/배포/푸시
- 코드 변경: `docker compose -f docker-compose.kdb.yml build kdb-app && docker compose -f docker-compose.kdb.yml up -d kdb-app` (env 변경도 `up -d` 로 반영; `restart` 는 env 미반영).
- push: `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`.
- **백엔드는 프롬프트/세션과 무관하게 컨테이너에서 독립 실행** — Claude Code 꺼도 계속 돔.

HEAD = `ddf88be` (이 핸드오프 커밋 후 갱신). 연관: [[project-kdb-cutover]], [[reference-kdb-hermes-agents]].
