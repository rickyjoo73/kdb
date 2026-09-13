'use strict';
// 운영 설정 불변식 검사. 저장소 파일만 읽고 네트워크·DB 접근은 하지 않는다.
//
// 여기 있는 것은 전부 **운영에서 한 번 터졌거나 터질 뻔한 것**이다. 문서가 아니라
// compose/워크플로 파일에 사는 값이라 다른 검사기가 보지 못했다.
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const repo = path.resolve(__dirname, '..', '..');
const read = f => fs.readFileSync(path.join(repo, f), 'utf8');

const base = read('docker-compose.kdb.yml');
const override = read('docker-compose.override.yml');
const deploy = read('.github/workflows/deploy.yml');

// ── D-39: kdb-db 의 /dev/shm.
// Docker 기본 64MB 에서는 공통 원장(53만 행)의 병렬 집계가
// `could not resize shared memory segment ... No space left on device` 로 죽는다.
// 이 줄이 사라지면 같은 장애가 조용히 돌아온다.
const dbBlock = base.slice(base.indexOf('\n  kdb-db:'), base.indexOf('\n  kdb-app:'));
const shm = /^\s*shm_size:\s*(\d+)(gb?|mb?)\s*$/im.exec(dbBlock);
assert(shm, 'kdb-db must declare shm_size (D-39: Docker default 64MB kills parallel queries)');
const shmBytes = Number(shm[1]) * (shm[2].toLowerCase().startsWith('g') ? 1024 ** 3 : 1024 ** 2);
assert(shmBytes >= 1024 ** 3, 'kdb-db shm_size must be at least 1GB, got ' + shm[0].trim());

// ── 2026-09-01 사고: CI 가 -f 없이 compose 를 불러 override 가 통째로 빠졌고,
// 그 override 가 들고 있던 게 kdb-app 의 dockers_backend 고정 IP 였다. 동적 IP 로
// 재생성되면 무관한 컨테이너가 그 IP 를 물려받아 nginx 가 트래픽을 엉뚱하게 보낸다.
assert(/-f docker-compose\.kdb\.yml -f docker-compose\.override\.yml/.test(deploy),
  'deploy must pass BOTH compose files with -f (override carries the static IP)');
assert(/ipv4_address:\s*172\.19\.0\.240/.test(override),
  'kdb-app static IP on dockers_backend must stay pinned');

// ── 데이터는 이름 붙은 볼륨에 있어야 한다. 컨테이너 재생성이 데이터를 지우면 안 된다.
assert(/pgdata:\/var\/lib\/postgresql\/data/.test(dbBlock), 'kdb-db data must live on the named volume');

console.log(JSON.stringify({ result: 'PASS', shmSizeBytes: shmBytes,
  composeFilesPinnedInDeploy: true, staticIpPinned: true,
  scope: 'Repo-local runtime config invariants; no network or database access.' }, null, 2));
