# DB 자동 백업 정상화 검증 — 2026-09-12

## 원인과 적용 상태

- 2026-08-03 12:00 이후 정기 성공 기록이 끊겨 있었다.
- 이전 `scripts/kdb-db-backup.sh`는 저장 경로를
  `/home/kdb.aiinplanet.com/backups`로 고정했다. 현재 해당 상위 경로가 없으며,
  실제 운영 checkout과 kdb 계정 홈은 `/data/home2/kdb.aiinplanet.com`이다.
- 점검 시작 시 작업 트리에는 스크립트 경로 수정과 회귀 테스트가 이미 반영되어
  있었고, 13:58 수동 백업 성공 기록이 있었다. 이 수정은 보존하고 실제 운영
  환경, cron 실행, 격리 복원까지 추가 검증했다.
- 현재 스크립트는 자신의 위치를 기준으로 `backups`를 정한다.
  `KDB_BACKUP_DIR`로 절대 경로 지정도 가능하다.
- `pipefail`로 pg_dump 실패를 감지하고, gzip 무결성·최소 크기 확인 후 확정한다.
  DB 성공 기록까지 저장한 다음에만 기존 14일 보존 정책을 적용한다.
  동시 실행 잠금, 임시 파일 정리, 파일 권한 600, DB 기록 실패 감지도 적용돼 있다.

## 자동 실행 확인

- kdb 사용자 crontab의 기존 백업 두 항목은 올바른 실행 경로를 사용한다.

```cron
0 3 * * * /bin/bash /data/home2/kdb.aiinplanet.com/scripts/kdb-db-backup.sh
0 12 * * * /bin/bash /data/home2/kdb.aiinplanet.com/scripts/kdb-db-backup.sh
```

- 호스트 시간대 `Asia/Seoul`, cron 서비스 `active` 확인.
- 최소 환경(`env -i`, PATH만 지정)에서 14:12 백업 성공.
- 임시 일회성 cron 검증에서 15:03:01에 kdb(UID 1014, docker 그룹)로 실제 실행,
  15:03:14에 성공. 종료 코드 0.
- cron 생성 파일: `backups/kdb-20260912-150301.sql.gz`, 91,611,816 bytes,
  권한 600, `gzip -t` 통과.
- `kwave_kdb_backup_log`: 15:03:14 KST, `status=ok`.
  관리자 운영 점검의 동일 조회 기준으로 0시간 전 / 87 MB이다.
- 검증용 cron 항목 제거 완료. 등록 전/후 crontab 전체 내용 일치 확인.
  기존 백업 일정과 다른 작업은 변경하지 않았다.

## 실제 복원 검증

- 복원 검증 파일: `backups/kdb-20260912-141227.sql.gz`, 91,564,388 bytes.
- SHA256: `6ffee2d35b67bfb204cef4ec5a8aad75e8c92233f7350e9a22fe2a28a67d5f55`.
- PostgreSQL 16 임시 컨테이너, 외부 네트워크 없음, 호스트 공개 포트 없음,
  전용 tmpfs에 `psql -X -v ON_ERROR_STOP=1`로 복원 성공.
- 백업의 COPY 데이터와 복원 DB의 각 테이블 건수를 비교:
  **54개 테이블, 총 907,657행 모두 일치**.
- 검증용 컨테이너 제거 완료. 운영 DB에 복원하지 않았다.
- 스크립트 문법 검사 및 `python3 -m unittest scripts/test_kdb_db_backup.py -v` 4건 통과.
  성공, 부분 dump 실패, 최소 크기 미달, DB 기록 실패를 검사한다.

## 운영 자료

- 백업 로그: `backups/backup.log`.
- 이번 증빙: `backups/backup-repair-20260912/` 아래 cron 실행 결과,
  등록 전후 crontab, 복원 로그, 테이블별 건수, checksum.
- 14:05 로그의 Docker 소켓 `operation not permitted`는 점검 중 샌드박스에서
  실행한 검증이 차단된 기록이다. 이후 호스트 최소 환경과 실제 cron 실행은 성공했다.
- 외부 관리자 URL은 비로그인 요청에서 정상적으로 302를 반환했다.
  인증된 브라우저 화면 자체는 확인하지 않았으며, 화면이 사용하는 운영 DB 조회를
  직접 확인했다.

```sh
./scripts/kdb-db-backup.sh
docker exec kdb-db psql -X -U kdb -d kdb -P pager=off -c \
  "SELECT created_at,file,size_bytes,status FROM kwave_kdb_backup_log ORDER BY created_at DESC LIMIT 3;"
```
