# KDB 핸드오프 #6 (2026-05-31)

`KDB.handoff-20260530.md`(#5) 이후 — 운영자 지시 "할 수 있는 잔여 작업을 순서대로 진행"에서 출발해 **autopilot_log Hermes 우회**·**Disambiguator 공회전**을 발견·수정하고, 미커밋 변경분을 전부 커밋, schema latent trap을 제거.

## 0. 30초 상태 확인

`.52`(`114.203.210.52`, port 38371, user `kdb`) ssh 후 `/data/home2/kdb.aiinplanet.com`:

```bash
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep -E 'kdb|NAMES'   # 둘 다 healthy

# autopilot cycle 기록 (이제 Hermes 모드에서도 cycle 당 1 row — handoff #6 §1)
docker exec -i kdb-db psql -U kdb -d kdb -c \
 "SELECT ran_at, classified, classify_deferred, promoted, enriched, alias_resolved FROM kwave_kdb_autopilot_log ORDER BY ran_at DESC LIMIT 5;"

# Disambiguator 수렴 확인 (stamped 증가 → 공회전 멈춤)
docker exec -i kdb-db psql -U kdb -d kdb -c \
 "SELECT count(*) FILTER (WHERE disambig_reviewed_at IS NOT NULL) stamped, count(*) FILTER (WHERE needs_disambig) flagged FROM kwave_entities;"

# classify 회귀 (group 0.99)
docker exec kdb-app kdb-app classify-test 방탄소년단 2>&1 | tail -1
```

## 1. ★ autopilot_log 가 Hermes 모드에서 비어 있던 것 수정

**증상**: `kwave_kdb_autopilot_log`(0064) 0 rows → admin 대시보드 "최근 autopilot cycle" 표가 죽어 있음.

**원인**: `KDB_HERMES_ENABLED=1`(현재 운영값)이면 `cmd/kdb/main.go`의 `buildAutopilotRunner`가 `hermes.SuperviseCycle`을 돌리고, persistLog가 든 `auto.Run()`은 우회됨. **autopilot 자체는 정상**(Hermes가 cycle 당 12 step을 hermes_runs에 audit). 단지 새 0064 audit 테이블/대시보드 표만 빈 것.

**수정**(`internal/kdb/hermes/`): `SuperviseCycle` 끝에 `persistAutopilotLog(cycleID)` 추가 — 방금 기록한 `kwave_kdb_hermes_runs` rows를 cycle_id로 집계(role→category, `items_out`=Acted; classify_deferred = classify role의 `items_in-items_out`)해 autopilot_log에 1 row INSERT. 테이블 없으면 silent skip.

**검증**: 재배포 후 첫 cycle(556s) 완료 시 row 기록 확인 — enriched=9, classify_deferred=20.

## 2. ★ Disambiguator 공회전(Phase 2) 수정

**증상**: Disambiguator가 매 cycle 돌지만 `items_out=0`, `needs_disambig` 91건 정체, `disambig` 라벨은 1건뿐.

**원인**: 이름만 같고 role/agency/works가 없는 클러스터를 gpt가 전부 uncertain → `quarantine`. quarantine이 `updated_at=now()`로 갱신하는데 near-name seed Select가 `ORDER BY updated_at DESC`라, quarantine된 항목이 **큐 맨 앞으로 점프해 매 cycle 재선택·재판정**(gpt-5.5 호출만 소모, 수렴 X). `needs_disambig`는 Phase 1 `markHomonymsIfConflict`가 flag한 진짜 동명이인도 포함 → 단순 제외 불가.

**수정**:
- `migrations/0065`: `kwave_entities.disambig_reviewed_at TIMESTAMPTZ` + 부분 인덱스. (`.52` DB 적용 완료)
- `decide.go quarantine`: `disambig_reviewed_at=now()` 기록, **`updated_at` 갱신 제거**(점프 원인 차단).
- `cluster.go` Select(exact + near-name): 쿨다운(`reviewCooldown="14 days"`) 내 재판정 항목 제외. 리졸브(merge/distinct)는 `needs_disambig` 해제로 자연 제외, 쿨다운 후엔 enrich로 메타데이터가 채워졌을 수 있어 1회 재시도.

**검증**: 재배포 후 Disambiguator run이 4건 quarantine → `disambig_reviewed_at` 4건 stamped 확인. 기존 91건(reviewed_at=NULL)은 cycle마다 1회씩 stamp되며 ~11h 내 seed 풀 소진 → 공회전 종료.

## 3. schema latent trap 제거

`//go:embed schemas/...`는 `internal/kdb/codexcli/schemas/`(handoff #5에서 고친 것)를 임베드 → fix는 라이브. 그러나 "원본 copies"인 `scripts/codex_schemas/`는 구버전이라 strict-mode 버그 4건(classify/disambiguate/fill_person/gatekeeper)이 남아 **divergent** — 재생성 시 classify-killing 버그 재발 위험. 고친 임베드본으로 동기화(7/7 OK).

## 4. 커밋 (미푸시 — 권한 blocker)

이번 세션 4 commit + handoff #5의 미커밋분 포함, origin 대비 **HEAD 기준 미푸시 누적**:
```
6492481 fix(disambiguator): 공회전 방지 쿨다운 + scripts schema 동기화
3fc440b fix(hermes): cycle 종료 시 autopilot_log summary 기록
96b23bf fix(autopilot): codex strict-mode schema 복구 + 9개 언어 enrich + candidate drain
caceaa8 feat(admin): 언어별 채움 바  (handoff #5, 이미 commit 돼 있었음 / 미푸시)
```
`kdb`(19MB 바이너리)는 `.gitignore`(`/kdb`)로 제외.

### ★ push blocker (검증함)
- 원격 `git@github.com:rickyjoo73/kdb.git`. push 시 `Permission to rickyjoo73/kdb.git denied to deploy key`.
- `ssh -T git@github.com` → `Hi aiinplanet/kdb.aiinplanet.com!` 로 인증됨 = 로드된 deploy key(`~/.ssh/id_github_kdb_aiinplanet`)의 정체가 **push 대상 repo(rickyjoo73/kdb)와 불일치**. 이 키가 다른 repo의 deploy key일 공산.
- **해결**: 아래 공개키를 `rickyjoo73/kdb` → Settings → Deploy keys 에 **Allow write access 체크**로 (재)등록. 또는 rickyjoo73/kdb write 권한 계정의 PAT로 HTTPS 전환.
  ```
  ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAICGwq+jXNPauTSY8QpW0fUJl7Nxy3gfM9zsO83Uc18M4 kdb@114.203.210.52
  ```
- 권한 풀린 뒤: `git push origin main` (현재 4 commit 대기).

## 5. 남은 작업 (다음 세션)
1. **GitHub push** — §4 deploy key write 권한 부여 후 `git push origin main`.
2. TMDb/KOFIC/KMDb key — drama 114·movie 19 다국어 채움률. 사용자 발급 결정.
3. Disambiguator 쿨다운 후속 — 14일 후 재시도가 enrich로 채워진 메타데이터로 실제 resolve 되는지 관찰. 안 되면 enrich를 disambig 전 우선순위로.
4. playwright admin UI 검증(admin cookie) — homonym 페이지에서 needs_disambig 91건 운영자 수동 disambig 지정 흐름 확인.
5. (선택) drain admin 버튼/cron 승격.

## 6. 운영 도구
```bash
# codex schema strict-mode 감사 (양 경로 모두)
for d in internal/kdb/codexcli/schemas scripts/codex_schemas; do for f in $d/*.json; do python3 - "$f" <<'PY'
import json,sys; d=json.load(open(sys.argv[1])); bad=[]
def c(n,p="root"):
 if isinstance(n,dict) and n.get("type")=="object" and "properties" in n:
  m=set(n["properties"])-set(n.get("required",[]))
  if m or n.get("additionalProperties",True)!=False: bad.append((p,sorted(m)))
  for k,v in n["properties"].items(): c(v,p+"."+k)
 if isinstance(n,dict) and n.get("type")=="array" and "items" in n: c(n["items"],p+"[]")
c(d); print(sys.argv[1],"BAD",bad) if bad else print(sys.argv[1],"OK")
PY
done; done

# 재배포 (코드 변경 후)
docker compose -f docker-compose.kdb.yml build kdb-app && docker compose -f docker-compose.kdb.yml up -d kdb-app
# 주의: 레거시 docker-compose(v1)는 name: 키 미지원 → 반드시 `docker compose`(v2) 사용.

# 컨테이너 빌드/테스트 (호스트에 go 없음)
docker run --rm -v "$PWD":/app -w /app -v kdb-gomod:/go/pkg/mod golang:1.23-bookworm \
  bash -c 'export PATH=$PATH:/usr/local/go/bin GOFLAGS=-buildvcs=false; go build ./... && go test ./internal/kdb/...'
```

## 7. 한 줄 요약
> Hermes 모드에서 비어 있던 autopilot_log 대시보드 표를 cycle 집계로 복구하고, Disambiguator가 메타데이터 없는 클러스터를 매 cycle 재판정하던 공회전을 쿨다운(0065)으로 차단. schema latent trap 동기화. 4 commit **미푸시** — deploy key가 push 대상 repo와 불일치(§4), write 권한 deploy key 재등록 필요.
