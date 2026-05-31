# KDB 핸드오프 #7 (2026-06-01)

#6(`KDB.handoff-20260531.md`) 이후 대규모 세션. unknown 박멸 → 자동화, 국내(Daum)
피드 추가, TMDb/KOFIC enrich 연동, admin API 설정 페이지 + 연결 테스트, 외부 검색용
KDB API 키 + API 문서까지. **모든 코드 rickyjoo73/kdb main 푸시 완료(HEAD=55e3975)**.

## 0. 30초 상태 확인
`.52`(114.203.210.52:38371, user kdb) → `/data/home2/kdb.aiinplanet.com`:
```bash
docker ps --format 'table {{.Names}}\t{{.Status}}' | grep -E 'kdb|NAMES'   # kdb-app/kdb-db healthy
# unknown = 0 유지?
docker exec -i kdb-db psql -U kdb -d kdb -tAc "SELECT count(*) FROM kwave_entities WHERE entity_type='unknown';"
# 외부 검색 API (키는 .env KDB_API_KEYS)
curl -s -X POST https://kdb.aiinplanet.com/v1/lookup -H "X-KDB-Key: <KEY>" \
  -H content-type:application/json -d '{"query":"도깨비","limit":1}'
# 외부 API 연결 점검
docker exec kdb-app kdb-app api-test
```

## 1. 이번 세션 변경 (전부 배포·푸시됨)

### 1.1 unknown 박멸 + "모르면 검색"
- **resolve-unknowns**: entity_type='unknown' 161건 → **0**. 로컬 RSS + Google News 검색
  문맥으로 gpt 재분류 → 실체면 제 타입 active(잘못 rejected된 진짜 고유명사 102건 복구),
  비실체면 term+rejected. `internal/kdb/news_search.go SearchNewsContext`.
- **상시 step:ResolveUnknowns**: autopilot cycle(Hermes 13 steps)에 편입 — 신규 unknown
  자동 해소. 보수모드(문맥 없으면 안 버리고 재시도). `kdb-app resolve-unknowns [n]`(일괄).
- **drain-persons / drain-bucket**: 고유명사에 섞인 인명→인물DB, 잔여 unknown→제 타입 버킷팅.

### 1.2 동명이인 공회전 수정 (#6 잔여)
- `disambig_reviewed_at`(migration 0065) 쿨다운 — Disambiguator가 메타 없는 클러스터를
  매 cycle 재판정하던 무한 공회전 차단.

### 1.3 국내 뉴스 피드 (첫 한국어 소스)
- 네이버/다음 native RSS 폐지 → **Google News RSS 경유**. `kwave_news_whitelist` 행 추가.
- **다음**: `v.daum.net`, locale=ko, category=kcontent, rss_url = Google News 검색
  `site:v.daum.net (아이돌 OR 케이팝 OR 걸그룹 OR 보이그룹 OR 컴백 OR 데뷔 OR 신곡 OR
  드라마 OR 영화 OR 배우 OR 개봉 OR 출연 OR 주연)`. **약한 "연예" 키워드는 일반뉴스가
  섞이니 금지**(상세 [[reference-kdb-feeds]]). 네이버 엔터는 Google News가 제목을 빈 값으로
  줘서 사용 불가.

### 1.4 TMDb / KOFIC enrich 연동 (영상물 다국어 채움)
- `internal/kdb/tmdb`, `internal/kdb/kofic` 클라이언트. orchestrator **L2b**(movie/drama/show).
  TMDb=9개 언어 제목(translations), KOFIC=영화 영어제목 보강. 빈 locale만 채움.
- **오매칭 방지**(중요): 한국어/원제 정규화 일치 또는 original_language=ko 만 채택
  ("아몬드"→프랑스영화 오매칭, KOFIC list[0] 폴백 오매칭 제거). KOFIC은 정확 제목 일치만.
- 매칭된 TMDb id는 `kwave_entity_external_refs(provider=tmdb)`에 캐시(향후 credits 활용 기반).
- KMDb는 **승인 대기**라 미구현(클라이언트 X). 키는 받는 대로 KOFIC 패턴으로 추가.
- 진단: `kdb-app enrich-test <ko>`.

### 1.5 admin API 설정 페이지 + 키 관리
- `/admin/settings` — 사용 API 전체 등록(TMDb/KOFIC/KMDb/MusicBrainz/Wikidata/GoogleNews/
  Codex), 상태·소스·마스킹 표시, 키 입력/삭제 폼, **"🔌 연결 테스트"** 버튼.
- `internal/kdb/apikeys`: Specs(레지스트리) + **Resolve(DB 우선, .env fallback)** + Set + Mask + Probe.
- migration 0066 `kwave_kdb_api_settings`(키 DB 저장). enrich는 apikeys.Resolve로 키 획득.
- CLI: `kdb-app api-test`(외부 API 실측 점검).

### 1.6 외부 검색용 KDB API 키 + 문서
- `.env KDB_API_KEYS`에 테스트 키 추가(기존 보존, 콤마 목록). 인증 헤더 `X-KDB-Key` 또는
  `Authorization: Bearer`. 검색=`POST /v1/lookup`. **이미 인증 강제 중**(빈 값=open 아님).
- **`docs/KDB-API.md`** 신설 — 외부 소비자용 가이드(인증/엔드포인트/예시/스키마). git에 공개.

## 2. 적용된 마이그레이션 (.52 DB)
- `0065_disambig_review_cooldown.sql` · `0066_api_settings.sql` — 적용 완료.

## 3. .env 키 (gitignore됨, 절대 커밋 X)
`KDB_TMDB_API_TOKEN`, `KDB_TMDB_API_KEY`, `KDB_KOFIC_API_KEY`, `KDB_API_KEYS`(2개:
기존+테스트). TMDb·KOFIC 검증 완료. KMDb 미발급.

## 4. ★ 운영/배포 주의 (다음 세션 필수)
- **빌드**(호스트에 go 없음): `docker run --rm -v "$PWD":/app -w /app -v kdb-gomod:/go/pkg/mod
  golang:1.23-bookworm bash -c 'export PATH=$PATH:/usr/local/go/bin GOFLAGS=-buildvcs=false; go build ./...'`
- **배포**: `docker compose -f docker-compose.kdb.yml build kdb-app && docker compose ... up -d kdb-app`.
  ⚠️ 레거시 `docker-compose`(v1)는 `name:` 키 미지원 → 반드시 **`docker compose`(v2)**.
- **push**(deploy key는 read-only/repo 불일치): 서버 gh CLI(`aiinplanet`, push 권한 보유)로
  `git -c credential.helper='!gh auth git-credential' push https://github.com/rickyjoo73/kdb.git HEAD:main`.
- **poller 30분 가드**: 재시작해도 마지막 poll<30분이면 startup poll skip(테스트 시 주의).

## 5. 서브커맨드 모음
`drain-candidates [n]` · `drain-persons [n]` · `drain-bucket [n]` · `resolve-unknowns [n]` ·
`enrich-test <ko>` · `classify-test <ko>` · `api-test`.

## 6. 남은 작업 (우선순위)
1. **KMDb**: 승인(1~2일) 후 키 → `.env KDB_KMDB_API_KEY` + `internal/kdb/kmdb` 클라이언트
   (KOFIC 패턴) + orchestrator L2b에 배선.
2. **TMDb 적극 활용 확장** (사장님 관심): 캐시된 tmdb id로 ① credits→**신규 인물 발견**
   (없는 명사 추가), ② **notable_works(필모)** 채움(orchestrator TODO), ③ 개봉일/장르 메타.
   ①은 인물 대량 유입 가능 → 임계/검증 필요(확인 후 진행).
3. **TMDb 매칭 정확도** 추가 개선(연도 대조 등) — 현재 정규화제목/ko-origin 까지만.
4. (선택) KDB API 키 발급/회수 admin UI 관리, 채팅 노출된 테스트 키 로테이션.
5. 국내 피드 유입량 관찰 후 키워드 조정 / 다른 국내 매체 추가.

## 7. 한 줄 요약
> unknown 0 박멸+상시 자동화, 첫 국내(Daum) 피드, TMDb/KOFIC 영상물 다국어 enrich(오매칭
> 방지·id 캐시), admin API 설정·연결테스트, 외부검색 API 키+문서까지 완료·푸시.
> 다음: KMDb 연동 + TMDb credits로 인물/필모 확장.
