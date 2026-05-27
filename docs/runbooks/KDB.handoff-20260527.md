# KDB 독립 서비스 전환 메모 (2026-05-27)

## 취지와 최종 목적

KDB는 `global.mediafine.co.kr` 안에 딸린 기능이 아니라, `kdb.aiinplanet.com`에서 독립 DB와 독립 API/worker를 운영하는 별도 서비스가 되어야 한다.

최종 구조는 다음과 같다.

- 운영 원본 repo: `https://github.com/rickyjoo73/kdb`
- 운영 도메인: `https://kdb.aiinplanet.com`
- 운영 서버: `114.203.210.52` (`ssh kdb@114.203.210.52 -p 38371`)
- KDB는 자체 Postgres DB를 가진다.
- `global.mediafine.co.kr`은 KDB 테이블/worker를 직접 운영하지 않는다.
- `global.mediafine.co.kr`은 KDB API만 호출해서 고유명사 lookup/resolution을 처리한다.
- `global.mediafine.co.kr` 내부의 KDB DB/worker 제거는 `kdb.aiinplanet.com`이 완전히 검증된 뒤에만 진행한다.

다른 에이전트가 혼동하면 안 되는 핵심:

- `114.203.210.52`가 `kdb.aiinplanet.com` DNS 대상 서버다.
- `114.203.210.59`는 현재 `global.mediafine.co.kr` 서버이며, 여기서 돌린 KDB는 임시 검증/브릿지 상태다.
- 최종 배포와 공개 도메인 설정은 반드시 `.52` 서버에서 끝나야 한다.

## GitHub repo 상태

`https://github.com/rickyjoo73/kdb` main에 독립 DB compose 구성이 push되어 있다.

- 커밋: `27a6b3c40cf7bfd6dbd5b021f9cbb6bc8ad015fc`
- 주요 변경:
  - `kdb-db` Postgres 서비스 추가
  - `kdb-api`, `kdb-worker`가 `kdb-db`를 사용하도록 구성
  - `.env.example`에 독립 DB 설정 추가
  - KDB API/worker 코드와 migrations 동기화
  - 독립 DB 전환 runbook 추가

## 현재 `.59` 임시 검증 상태

`/data/home2/kdb.aiinplanet.com`에 KDB repo가 배포되어 있고, 아래 컨테이너가 실행 중이다.

- `kdb-platform-kdb-db-1`
- `kdb-platform-kdb-api-1`
- `kdb-platform-kdb-worker-1`

이 환경은 실제 공개 도메인 서버가 아니라 `.59`의 임시 검증 환경이다.

검증 결과:

- `kdb-api` health 정상
- `kdb-worker` 정상 처리 확인
- sweeper 로그:
  - `done processed=30 succeeded=30 failed=0`
  - 이후 `processed=1 succeeded=1 failed=0`
- 최근 데이터 증가 확인:
  - `kwave_entities`: 1493
  - 최근 90분 observation: 54
- `global.mediafine.co.kr` app은 현재 `KDB_API_URL=http://kdb-api:9100`으로 API 호출 중
- `KDB_API_KEY`는 설정되어 있음
- app 컨테이너 내부에서 `/v1/lookup`에 `BTS` query 호출 성공

주의:

- `.59`의 KDB는 최종 목적지가 아니다.
- mediafine이 현재 내부 Docker DNS로 `.59` KDB API를 호출하고 있으므로, `.52` 전환이 완료되기 전까지는 함부로 중단하지 않는다.

## 데이터 이관 상태

`global.mediafine.co.kr`의 기존 `mediafine-db-1`에서 KDB 관련 테이블을 dump했다.

dump 파일:

- `.59`: `/tmp/kdb-mediafine-dump.sql`
- `.52`: `/data/home2/kdb.aiinplanet.com/kdb-mediafine-dump.sql`

checksum:

- `8bf31b6f2179a2cb351e4781b2d6da7d7732289f8a153340686401d19b0fcf89`

dump에는 mediafine 본문 DB와 직접 엮이는 FK를 제거했다.

- 제거한 FK:
  - `kwave_entity_research_queue_source_id_fkey`
  - `kwave_person_research_queue_source_id_fkey`
- KDB 내부 FK는 유지했다.

중요한 답:

- `.59` 임시 독립 KDB DB에는 데이터 restore가 완료되었고, worker가 새 DB에 쓰는 것까지 확인했다.
- `.52` 실제 `kdb.aiinplanet.com` 서버에는 repo, `.env`, dump 파일까지만 준비되었다.
- `.52` 실제 서버의 DB 컨테이너 설치/기동과 dump restore는 아직 완료되지 않았다. Docker 권한이 없어 실행하지 못했다.

## `.52` 실제 서버 상태와 blocker

`.52` 접속 정보:

- host: `114.203.210.52`
- ssh port: `38371`
- user: `kdb`
- 작업 경로: `/data/home2/kdb.aiinplanet.com`

확인된 상태:

- repo는 최신 커밋 `27a6b3c40cf7bfd6dbd5b021f9cbb6bc8ad015fc`
- `.env` 생성 완료, 권한 `600`
- dump 업로드 완료, checksum 일치
- `kdb.aiinplanet.com` DNS는 `.52`를 가리킴

blocker:

- `kdb` 계정이 `docker` 그룹에 없음
- `kdb` 계정은 `sudo` 권한 없음
- `/var/run/docker.sock` 접근 불가
- 그래서 `.52`에서 Docker compose up, DB restore, nginx/certbot 설정, 로그 확인을 진행할 수 없음

필요한 권한 조치:

```bash
usermod -aG docker kdb
```

또는 Docker/nginx 권한이 있는 계정으로 접속해야 한다.

권한 변경 후에는 `kdb` 계정 재로그인이 필요하다.

## 다음 에이전트 작업 순서

1. `.52`에서 Docker 권한 확보 확인

```bash
ssh kdb@114.203.210.52 -p 38371
id
docker ps
```

2. `.52` repo 확인

```bash
cd /data/home2/kdb.aiinplanet.com
git status --short
git rev-parse HEAD
```

HEAD가 `27a6b3c40cf7bfd6dbd5b021f9cbb6bc8ad015fc`인지 확인한다.

3. `.52`에서 KDB DB/API/worker 기동

```bash
cd /data/home2/kdb.aiinplanet.com
docker compose --env-file .env -f docker-compose.kdb.yml up -d kdb-db
docker compose --env-file .env -f docker-compose.kdb.yml up -d kdb-api kdb-worker
```

4. `.52` DB에 enum type 생성 후 dump restore

`.59`에서 restore할 때 dump가 enum type을 포함하지 않아 먼저 생성해야 했다. 필요한 enum:

`kwave_entity_type`

- person
- group
- show
- drama
- movie
- song_album
- agency
- channel_outlet
- brand_place
- event_tour
- character
- term
- unknown

`person_role`

- idol
- singer
- rapper
- actor
- broadcaster
- comedian
- director
- producer
- model
- creator
- athlete
- politician
- businessperson
- journalist
- fictional
- other

그 다음 `/data/home2/kdb.aiinplanet.com/kdb-mediafine-dump.sql`을 restore한다.

5. migrations 적용

최소 확인된 필요 migration:

- `migrations/0050_kdb_source_priority_function.sql`
- `migrations/0051_kdb_can_replace_function.sql`

6. `.52` 내부 API 검증

```bash
curl -sS http://127.0.0.1:9100/v1/health
```

expected:

- `ok: true`
- `entities` count가 1489 이상

7. `.52` nginx/certbot 구성

현재 `https://kdb.aiinplanet.com/v1/health`는 KDB가 아니라 기존 PHP/nginx 404로 응답한다.

해야 할 일:

- `.52`의 실제 nginx 구성을 찾아 `kdb.aiinplanet.com` vhost 추가
- `/.well-known/acme-challenge` webroot 설정
- certbot으로 `kdb.aiinplanet.com` 인증서 발급
- HTTPS server에서 `/v1/`을 `http://kdb-api:9100` 또는 host loopback `http://127.0.0.1:9100`으로 proxy
- nginx reload

8. 공개 도메인 검증

```bash
curl -sS https://kdb.aiinplanet.com/v1/health
```

9. `global.mediafine.co.kr` API URL 전환

`.52` 공개 KDB API가 완전히 검증된 뒤에만 mediafine `.env`를 바꾼다.

```env
KDB_API_URL=https://kdb.aiinplanet.com
KDB_API_KEY=...
```

API key 값은 노출하지 않는다. `.52`의 `.env`와 mediafine `.env`가 같은 key를 쓰도록 맞춘다.

10. mediafine 재기동 및 lookup 검증

```bash
docker compose --env-file .env -f docker-compose.prod.yml up -d app
```

app 컨테이너 내부에서 `/v1/lookup` query를 검증한다.

11. 최종 제거 작업

`kdb.aiinplanet.com` 독립 서비스가 안정적으로 동작하고 mediafine이 공개 API로 정상 호출하는 것이 확인된 뒤에만 진행한다.

- `global.mediafine.co.kr` 내 KDB worker 중단/제거
- mediafine DB 안의 KDB 테이블 제거 여부 검토
- mediafine repo 안의 임시 `docker-compose.kdb.yml` 운영 의존 제거
- 백업 확보 후 단계적으로 삭제

## 하면 안 되는 일

- `.59` 임시 KDB를 최종 운영으로 착각하지 말 것
- `.52` restore 전 mediafine 내부 KDB 테이블을 삭제하지 말 것
- API key, DB password 등 secret 값을 로그나 답변에 노출하지 말 것
- `global.mediafine.co.kr`의 KDB 제거를 공개 KDB 검증 전에 진행하지 말 것
