# 격리 회귀 환경 — aiin23

운영 DB 를 건드리지 않고 **실제 스키마·실제 데이터**에 대고 시험하는 자리다.
실행은 [`scripts/run-isolated-regression.sh`](../scripts/run-isolated-regression.sh).

---

## 1. 왜 이렇게 생겼나

**① template 은 운영 전량 사본이다.**
2026-09-14 에 종전 회귀 template 을 재어 보니 표기가 **1,249건**이었다(운영은 2,686,762).
표도 85개 중 84개뿐이었다. 그런 환경에서의 "격리 회귀 5/5 통과"는 현재 스키마·데이터에
대한 통과가 아니다 — 무엇을 시험했는지 말할 수 없는 PASS 였다.
지금은 운영 `kdb-db` 에서 `pg_basebackup` 으로 통째로 뜬다. 같은 장비라 **37초**다.

**② 체크아웃은 워크트리가 아니라 별도 클론이다.**
워크트리는 같은 저장소의 브랜치를 공유한다. 배포 체크아웃이 `main` 을 쥐고 있으면
워크트리는 `main` 을 체크아웃할 수 없고 **실패한다**. 옛 서버에서 그 실패가 `|| true` 에
삼켜져 회귀가 **5커밋 전 코드를 조용히 시험**하고 있었다.
지금은 클론을 따로 두고, 받은 SHA 가 원격 `main` 과 같은지 **대조해서 다르면 멈춘다**.

**③ shm 이 1GB 다.**
옛 회귀 컨테이너는 256MB 였다. 표기가 268만 행이 된 지금 병렬 집계가 D-39 와 똑같이
`could not resize shared memory segment` 로 죽는다. 회귀가 운영과 다른 조건에서 돌면
회귀가 아니다.

**④ 포트를 호스트에 게시하지 않는다.**
시험 컨테이너가 `--network container:kdb-p1-restore-db` 로 붙으므로 밖에 열 이유가 없다.

---

## 2. 구성

```
컨테이너  kdb-p1-restore-db   postgres:16-alpine  mem 2g  shm 1g  (포트 미게시)
볼륨      kdb-p1-restore-vol
DB        kdb                         ← template. 운영 사본
          kdb_platform_migration_test ← 매 실행마다 template 에서 새로 만든다
접속      postgres://kdb:p1restore_disposable@127.0.0.1:5432/kdb_platform_migration_test
체크아웃  /home/aiin/kdb/regress   (배포 체크아웃 /home/aiin/kdb/repo 와 별개)
캐시      /home/aiin/kdb/gocache · /home/aiin/kdb/gomod
```

`p1restore_disposable` 은 **버리는 암호**다. 이 인스턴스는 밖에 열려 있지 않고 안에 든
것은 운영 사본이며, 운영 암호는 basebackup 으로 딸려 온 뒤 곧바로 이 값으로 바꾼다.

---

## 3. 처음 만들 때 / template 을 새로 고칠 때

```bash
# 23번에서
docker rm -f kdb-p1-restore-db; docker volume rm kdb-p1-restore-vol
docker volume create kdb-p1-restore-vol

# 운영 kdb 를 물리 복사 (37초). 운영은 읽기만 한다.
docker exec kdb-db pg_basebackup -U kdb -D - -Ft -X fetch --checkpoint=fast \
  | docker run --rm -i -v kdb-p1-restore-vol:/d alpine:3 sh -c 'cd /d && tar -xf -'
docker run --rm -v kdb-p1-restore-vol:/d alpine:3 sh -c 'chown -R 70:70 /d && chmod 700 /d'

docker run -d --name kdb-p1-restore-db --restart unless-stopped \
  --memory 2g --shm-size 1g \
  -v kdb-p1-restore-vol:/var/lib/postgresql/data \
  -e POSTGRES_USER=kdb -e POSTGRES_DB=kdb -e POSTGRES_PASSWORD=p1restore_disposable \
  -e TZ=Asia/Seoul postgres:16-alpine \
  postgres -c shared_buffers=512MB -c work_mem=16MB -c maintenance_work_mem=256MB \
           -c random_page_cost=1.1 -c effective_io_concurrency=200

# basebackup 으로 운영 암호가 딸려 왔다. 버리는 암호로 바꾼다.
docker exec kdb-p1-restore-db psql -U kdb -d kdb -c \
  "ALTER USER kdb PASSWORD 'p1restore_disposable'"
```

**언제 새로 고치나.** 마이그레이션을 넣었을 때, 대량 흡수·정리를 했을 때.
template 이 운영과 멀어지면 회귀가 무엇을 시험하는지 다시 말할 수 없게 된다.
확인은 한 줄이면 된다:

```bash
docker exec kdb-p1-restore-db psql -U kdb -d kdb -At -c \
  "SELECT count(*) FROM kentity_names"      # 운영 kdb-db 와 자릿수가 같아야 한다
```

---

## 4. 실행

```bash
bash scripts/run-isolated-regression.sh        # build/vet · 격리 · 전체 · race · 정적검사
bash scripts/run-isolated-regression.sh 빠름   # 격리 회귀와 정적검사만
```

23번에 `node` 가 없으므로 정적 검사는 `node:20-alpine` 컨테이너로 돈다.
`go` 도 없으므로 시험은 `golang:1.23-bookworm` 으로 돈다. **sudo 없이 전부 된다.**
