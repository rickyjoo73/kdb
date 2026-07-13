# KDB handoff — 29차 (2026-07-10): 고유명사 후보 정규화 + LocalFill 검색오염 차단 + 다음 병목

28차 `KDB.handoff-20260705.md` 이후. 이번 세션의 핵심은 오너가 지적한 **"`방탄소년단 진`은 새 고유명사가 아니라 `방탄소년단` 컨텍스트가 붙은 `진`"** 이라는 기준을 코드에 반영하고, LocalFill 테스트가 candidate/오염검색을 실제 보강처럼 보이게 만드는 문제를 차단한 것이다.

중요: 이번 세션에서 **DB row 업데이트/삭제는 하지 않았다**. 전부 `--dry` 또는 SELECT 확인이다. 서비스 재시작도 하지 않았다. 테스트용 빌드만 `/tmp/kdb-test`에 만들었다.

## §0 — 다음 세션 첫 확인 명령

```sh
# 작업트리 확인: 기존 미커밋 변경이 많다. 이번 세션 변경과 무관한 변경을 되돌리지 말 것.
git status --short

# 이번 세션 핵심 테스트/빌드 재확인
docker exec kdb-app sh -c 'cd /app && go test ./internal/kdb ./internal/kdb/autopilot ./internal/kdb/agents ./internal/kdbadmin && go build -o /tmp/kdb-test ./cmd/kdb'

# "방탄소년단 진"은 localfill 대상이 아니어야 한다(applied=0 기대)
docker exec kdb-app sh -c 'cd /app && KDB_LOCALFILL_DEBUG=1 /tmp/kdb-test localfill "방탄소년단 진" --dry'

# qualified member candidate 매칭 확인(쓰기 없음)
docker exec kdb-db psql -U kdb -d kdb -c "WITH matched AS (SELECT DISTINCT ON (c.id) c.canonical_ko AS candidate_ko, g.canonical_ko AS group_ko, p.canonical_ko AS person_ko FROM kwave_entities c JOIN kwave_entities g ON g.status='active' AND g.entity_type='group' AND left(c.canonical_ko, char_length(g.canonical_ko)+1)=g.canonical_ko || ' ' CROSS JOIN LATERAL (SELECT btrim(substr(c.canonical_ko, char_length(g.canonical_ko)+2)) AS tail) t JOIN kwave_entities p ON p.status='active' AND p.entity_type='person' AND (t.tail=p.canonical_ko OR t.tail=ANY(COALESCE(p.aliases_ko,'{}'::text[]))) WHERE c.status='candidate' AND c.entity_type='person' AND c.operator_locked=false ORDER BY c.id, char_length(g.canonical_ko) DESC, p.confidence DESC LIMIT 10) SELECT * FROM matched;"

# 현재 테스트 대상 상태
docker exec kdb-db psql -U kdb -d kdb -c "SELECT canonical_ko, entity_type::text, status, COALESCE(canonical_en,''), COALESCE(canonical_zh,''), COALESCE(canonical_zh_hant,''), COALESCE(canonical_en_source,''), COALESCE(canonical_zh_source,''), COALESCE(canonical_zh_hant_source,'') FROM kwave_entities WHERE canonical_ko IN ('방탄소년단 진','방탄소년단','진','김석진','호텔 델 루나','뚜두뚜두','Gimme Dat Love') ORDER BY canonical_ko, status;"
```

## §1 — 오너 기준 정리

오너 기준:

- `candidate`가 직번역/LLM 생성이면 승급 불가.
- 공식 소스에서 가져온 표기라면 승급 가능.
- AI/LLM이 스스로 만든 값은 "할 수 없다"로 봐야 한다.
- 검색할 때는 글 전체/설명문이 아니라 **해당 고유명사 원문만** 사용해야 한다.
- 없는 값만 검색으로 해결할 수 있는지 봐야 한다.
- `방탄소년단 진`은 새 고유명사가 아니라 "방탄소년단의 멤버 진"이라는 문맥 언급이다.

이 기준에 따라 `방탄소년단 진`은 LocalFill 대상도, 새 person 승급 대상도 아니다. 정본 엔티티는 이미 DB에 있다:

- `방탄소년단` active group, `canonical_en=BTS`
- `진` active person, `canonical_en=Jin`
- `김석진` active person, `canonical_en=Kim Seok Jin`, notes에 `merged into 진`
- `방탄소년단 진` candidate person, official hold 상태였으나 잘못된 qualified mention 후보

## §2 — 이번 세션 코드 변경

### 2.1 LocalFill targeted probe

파일:

- `internal/kdb/localfill.go`
- `cmd/kdb/main.go`
- `internal/kdb/localfill_test.go` 신규

변경:

- `LocalFillRunForName(ctx, pool, ko, perEntity, dry, reground)` 추가.
- CLI `kdb-app localfill "이름" --dry` 지원.
- NULL 빈칸 버그 수정: `canonical_*=''`만 보던 것을 `COALESCE(canonical_*,'')=''`로 변경.
- 검색어는 exact proper noun only:
  - `"예스24 라이브홀"`
  - fallback `예스24 라이브홀`
  - 타입/locale/official 힌트는 붙이지 않음.
- 수동 targeted localfill은 이제 `status='active'`만 본다.
  - 이전에는 `candidate`도 들어와 `방탄소년단 진`이 테스트 대상으로 섞였다.
- 검색 결과 필터 추가:
  - 제목/스니펫/URL에 원문 고유명사가 없으면 폐기.
  - 단, 공식 호스트 URL은 fetch 대상으로 보존.
- 공식 호스트 본문 일부 fetch 및 deterministic English 후보 추출 추가:
  - KBS, YES24 LIVE HALL, Cube, Apple Music, Spotify, Melon, Genie, Bugs, VIBE, QQ, NetEase, Lollapalooza 등.
  - 공식 본문 기반 deterministic fallback은 `brand_place`, `agency`, `group`, `event_tour`, `channel_outlet`의 `en`에만 제한.
- `KDB_LOCALFILL_DEBUG=1` 디버그 로그 추가.

### 2.2 Qualified member mention reject

파일:

- `internal/kdb/autopilot/sweep.go`
- `internal/kdb/autopilot/agents.go`
- `internal/kdb/agents/agent.go`

변경:

- `stepRejectQualifiedMemberMentions` 추가.
- pattern: candidate person `"<active group> <active person or person alias>"`.
- 예: `방탄소년단 진` -> existing group `방탄소년단` + existing person `진`.
- 처리: candidate를 `rejected`, confidence 0, notes에 `qualified member mention` 기록.
- `Run()` 경로뿐 아니라 Hermes `RegisterSteps`에도 `RoleStepRejectQualifiedMember`로 등록했다. Hermes 모드에서도 빠지지 않는다.

주의:

- 이 step은 다음 autopilot cycle에서 실제 DB를 갱신한다.
- 이번 세션에서는 직접 실행하지 않았다.
- SELECT dry check에서 현재 매칭된 후보:
  - `방탄소년단 진 -> 방탄소년단 + 진`
  - `우아 나나 -> 우아 + 나나`

## §3 — 테스트 결과

통과:

```sh
docker exec kdb-app sh -c 'cd /app && gofmt -w internal/kdb/localfill.go internal/kdb/localfill_test.go internal/kdb/autopilot/sweep.go internal/kdb/autopilot/agents.go internal/kdb/agents/agent.go && go test ./internal/kdb ./internal/kdb/autopilot ./internal/kdb/agents ./internal/kdbadmin && go build -o /tmp/kdb-test ./cmd/kdb'
```

결과:

- `ok github.com/rickyjoo73/kdb/internal/kdb`
- `ok github.com/rickyjoo73/kdb/internal/kdb/agents`
- `ok github.com/rickyjoo73/kdb/internal/kdbadmin`
- `internal/kdb/autopilot` no test files

수동 dry-run:

- `localfill "방탄소년단 진" --dry`
  - `applied=0`
  - candidate가 active-only 필터로 제외됨.
- `localfill "호텔 델 루나" --dry`
  - `applied=0`
  - SearXNG 결과가 원문 고유명사를 포함하지 않아 전부 폐기.
- `localfill "뚜두뚜두" --dry`
  - `applied=0`
  - SearXNG 결과가 전부 오염이라 폐기.
- `localfill "Gimme Dat Love" --dry`
  - `zh`, `zh_hant`에서 `Gimme Dat Love`를 3/3 grounded로 찾음.
  - 단, 근거가 Bilibili/YouTube 중심이라 공식 음원 소스 기준으로는 약하다. 바로 DB 쓰기 금지.

## §4 — 현재 병목 진단

이번 테스트에서 드러난 다음 병목은 Gemma가 아니다.

1. **SearXNG/메타검색 품질 병목**
   - exact Korean proper noun으로 검색해도 Chrome/SHEIN/Microsoft/일반 웹문서 같은 무관 결과가 많다.
   - 새 필터는 오염을 막지만, 공식/유효 결과가 없으면 아무것도 채울 수 없다.

2. **공식 소스 discovery connector 부족**
   - `호텔 델 루나`: `canonical_zh=德鲁纳酒店`, `zh_hant=德魯納酒店`은 이미 있음. `en`은 비어 있음. SearXNG로는 official/title source를 못 찾았다.
   - `뚜두뚜두`: song_album active, 여러 locale 빈칸. 음원 플랫폼/공식 MV/Apple/Spotify/Melon/Genie/Bugs/VIBE/QQ/NetEase 쪽 connector가 필요.
   - `Gimme Dat Love`: search로 값은 잡히지만 공식 음원 소스가 아니면 승급 근거로 약함.

3. **candidate 정규화와 localfill이 섞이면 안 됨**
   - `방탄소년단 진`은 missing-locale 문제가 아니라 candidate canonical_ko 오염이다.
   - 이런 류는 검색하지 말고 정규화/기각해야 한다.

## §5 — 이미 있는 소스 파이프라인 상태

`internal/kdb/source_policy.go`에 source pipeline registry가 이미 있다. admin settings의 "소스 파이프라인" 표시도 이 정책 기반이다.

주요 라인:

- `person`: Wikidata, Naver People, KOFIC/KMDb, Naver Encyc
- `works`: TMDb, Netflix, Disney+, TVING/Wavve/Watcha/Coupang/Viki, broadcaster official
- `events`: Lollapalooza, YES24 LIVE HALL, broadcaster awards
- `music`: Cube official, MusicBrainz, iTunes, Discogs, Spotify, Warner Japan, Melon/Genie/Bugs/VIBE, QQ/NetEase/Tencent, KOMCA, YouTube official
- `discovery`: Naver Search, Kakao Search, Gemini/search tools, Baidu Baike, Namuwiki, SearXNG local-search
- `derived`: romanization, OpenCC, codex-fallback

현재 정책상 맞는 기준:

- Discovery/Namuwiki/Gemini/LLM search는 source 후보 찾기용. 자동 승급 앵커 아님.
- LLM/codex/gemma 합성값은 승급 근거 아님.
- 공식/구조화 provider 또는 official page anchor만 candidate->active 승급 근거.

## §6 — 다음 세션 권장 작업

### P0. 이번 변경을 실제 운영에 반영할지 결정

현재 코드는 워크스페이스에만 있고 `kdb-app` live process는 재시작하지 않았다.

운영 반영 시:

```sh
docker exec kdb-app sh -c 'cd /app && go test ./internal/kdb ./internal/kdb/autopilot ./internal/kdb/agents ./internal/kdbadmin && go build -o /tmp/kdb-app ./cmd/kdb'
docker restart kdb-app
```

주의: restart 전에 `git diff`를 확인하라. 작업트리에 이번 세션 전부터 존재하던 미커밋 변경이 많다. unrelated 변경을 되돌리지 말 것.

### P1. Naver Search를 LocalFill discovery source로 연결

현재 `internal/kdb/naver/naver.go`에는 DB-backed settings 기반 `NewFromSettings`와 `Search(kind, query, display)`가 있다. 이미 admin settings에도 Naver 키가 들어간 상태로 보인다.

권장:

- Korean proper noun은 SearXNG보다 Naver Search를 먼저 사용.
- `encyc`, `webkr`, `news`를 source discovery로 분리.
- 결과도 exact proper noun 포함 필터 적용.
- Naver Encyc는 confirm/review용이지 자동 승급 앵커가 아님.

### P2. Works 공식 page connector

`호텔 델 루나`, `화려한 날들`, `KBS 2TV`, `2025 KBS 연기대상` 류:

- KBS/MBC/SBS/JTBC/tvN/Mnet/ENA broadcaster official site search.
- TMDb alt titles/langlinks 우선.
- OTT(Netflix/Disney/TVING/Wavve/Watcha/Coupang/Viki) 공식 page anchor.
- 시상식/수상부문은 entity보다 relation/fact로 저장할지 검토.

### P3. Music official catalog connector

`뚜두뚜두`, `Gimme Dat Love`, `(G)I-DLE` 류:

- Apple Music/iTunes: 현재 confirm-only drain 있음. 새 값 discovery/fill에는 아직 약함.
- Spotify: plan 상태. OAuth 설정 후 ID 확인용으로만.
- Melon/Genie/Bugs/VIBE: 공식 국내 DSP catalog로 등록 제목 확인.
- QQ/NetEase/Tencent: 중화권 공식/준공식 catalog. 직역값 금지, 플랫폼 등록명만 채택.
- YouTube official channel/MV: evidence/review용. canonical 자동 채택은 금지.

### P4. Qualified mention 확장

현재는 `group + person`만 보수적으로 처리한다. 이후 확장 후보:

- `작품명 + 캐릭터명`은 별도 character relation일 수 있어 자동 reject 금지.
- `방송사 + 프로그램명`, `소속사 + 인물명`도 단순 reject 위험이 있으니 먼저 review-only.
- `그룹명 + 멤버명`만 현 단계 자동 reject가 안전하다.

## §7 — 변경 파일 주의

이번 세션에서 직접 건드린 핵심:

- `internal/kdb/localfill.go`
- `internal/kdb/localfill_test.go`
- `cmd/kdb/main.go`의 `localfill` targeted CLI 부분
- `internal/kdb/autopilot/sweep.go`의 qualified member step
- `internal/kdb/autopilot/agents.go`의 Hermes step 등록
- `internal/kdb/agents/agent.go`의 role 추가
- 이 handoff와 `.codex/memories/kdb-handoff-pointer.md`

주의:

- `git status`에는 위 외에도 많은 dirty 파일이 있다. 이전 세션 또는 다른 작업의 변경일 수 있다.
- 특히 `cmd/kdb/main.go`, `internal/kdb/autopilot/sweep.go`에는 이번 세션 이전 변경도 섞여 있다. diff 전체를 이번 세션 작업으로 간주하지 말 것.

## §8 — 한 줄 결론

`방탄소년단 진`은 "검색해서 채울 고유명사"가 아니라 "후보 canonical_ko 오염"이다. 이번 세션은 이 오염이 LocalFill/승급 라인에 들어가지 않게 막았다. 다음 병목은 Gemma가 아니라 **exact 고유명사에서 공식 소스를 찾아오는 source discovery connector(Naver/공식방송사/OTT/음원 플랫폼)** 이다.

연관: `KDB.handoff-20260705.md`, `KDB.handoff-20260622-2.md`, `internal/kdb/source_policy.go`, `internal/kdb/localfill.go`.
