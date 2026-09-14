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
const override = read('docker-compose.override.yml');   // server59(옛 서버)용
const hostFile = read('docker-compose.aiin23.yml');     // aiin23(현 운영)용
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

// ── 기본값 탈출이 유지되는지. Postgres 공장 기본값은 작은 DB 기준이라
// 53만 행에서는 계획이 어긋난다. 특히 random_page_cost 는 SSD 에서 반드시 낮춰야 한다.
for (const setting of ['shared_buffers=1GB', 'random_page_cost=1.1', 'work_mem=16MB']) {
  assert(dbBlock.includes(setting), 'kdb-db must pin ' + setting + ' (factory defaults misplan at 500k+ rows)');
}

// ── 2026-09-01 사고: CI 가 -f 없이 compose 를 불러 장비별 파일이 통째로 빠졌다.
// 그 파일이 들고 있는 것이 망·계정·경로다(옛 서버에선 고정 IP 였다). 빠지면 컨테이너가
// 엉뚱하게 뜬다. **파일 이름이 아니라 "기반 + 장비별 둘을 -f 로 준다"가 불변식이다** —
// 2026-09-14 이전에서 장비별 파일 이름이 바뀌자 이름을 박아 둔 이 검사가 걸렸다.
const composePair = /-f docker-compose\.kdb\.yml -f (docker-compose\.[A-Za-z0-9_.-]+\.yml)/.exec(deploy);
assert(composePair, 'deploy must pass BOTH compose files with -f (host file carries networks/user/paths)');
assert(fs.existsSync(path.join(repo, composePair[1])),
  'deploy references ' + composePair[1] + ' but the file is not in the repo');
assert(composePair[1] !== 'docker-compose.kdb.yml', 'the second -f must be a host-specific file');

// 장비별 파일은 **장비마다 따로** 있어야 한다. 한 파일에 두 장비를 담으려다
// 추적 파일을 덮어써서 git reset --hard 한 번에 사라질 뻔한 적이 있다(2026-09-14).
assert(/ipv4_address:\s*172\.19\.0\.240/.test(override),
  'server59 override must keep the dockers_backend static IP (중계 kdb-shim 이 물려받는 자리)');
assert(/user:\s*"1000:1000"/.test(hostFile),
  'aiin23 file must pin user 1000:1000 (배포 가드가 이 값으로 병합 여부를 판정한다)');
assert(/KDB_WORKER_ENABLED/.test(hostFile),
  'aiin23 file must wire KDB_WORKER_ENABLED (대기 상태로 띄울 수 있어야 한다)');

// ── 배포 가드가 "지금 장비에서 참인 것"을 검사하는지.
// 옛 장비용 고정 IP 검사를 그대로 두었더니 **정상 배포를 매번 되돌렸다**(2026-09-14 실증).
assert(!/expected 172\.19\.0\.240/.test(deploy),
  'deploy still checks kdb-app for the old static IP — 그 IP 는 이제 옛 서버의 중계가 쥐고 있다');
assert(/127\.0\.0\.1:9100\/v1\/health/.test(deploy),
  'deploy must verify the host port publish (옛 서버 SSH 터널이 붙는 자리)');

// ── 단계 디렉터리의 SQL 이 .gitignore 에 걸려 있지 않은지.
// .gitignore 는 `*.sql` 을 통째로 막고 예외를 하나씩 연다. docs/p4 를 만들면서 예외를
// 안 열었더니 `git add -A` 도 커밋도 성공한 채 **파일만 빠졌다**. 서버에서 실행이
// 죽고 나서야 드러났다 — 조용히 실패하는 부류라 사람 눈으로는 안 잡힌다.
const ignore = read('.gitignore');
for (const dir of fs.readdirSync(path.join(repo, 'docs'), { withFileTypes: true })) {
  if (!dir.isDirectory() || !/^p\d+$/.test(dir.name)) continue;
  const hasSQL = fs.readdirSync(path.join(repo, 'docs', dir.name)).some(f => f.endsWith('.sql'));
  if (!hasSQL) continue;
  assert(ignore.includes('!docs/' + dir.name + '/*.sql'),
    '.gitignore must un-ignore docs/' + dir.name + '/*.sql (silently dropped from commits otherwise)');
}

// ── 자원 한도가 '장비 전체'로 돌아가지 않았는지.
// 종전엔 세 컨테이너 모두 mem_limit 16g · cpus 8.0 이었다. 16GB 8스레드 장비에서
// 각자 장비 전체를 허용받는 것은 한도가 아니다. 한 앱이 CPU 375% 를 혼자 쓰는 것을
// 실제로 관측했고, 막는 것이 없었다.
const HOST_MEM_GB = 16, HOST_CPUS = 8;
for (const svc of ['kdb-db', 'kdb-app', 'kdb-searxng']) {
  const i = base.indexOf('\n  ' + svc + ':');
  assert(i >= 0, 'service ' + svc + ' missing from compose');
  const block = base.slice(i, i + 400);
  const mem = /^\s*mem_limit:\s*(\d+)g\s*$/m.exec(block);
  const cpu = /^\s*cpus:\s*"([\d.]+)"\s*$/m.exec(block);
  assert(mem, svc + ' must declare mem_limit');
  assert(cpu, svc + ' must declare cpus');
  assert(Number(mem[1]) < HOST_MEM_GB,
    svc + ' mem_limit ' + mem[1] + 'g is the whole host — that is not a limit');
  assert(Number(cpu[1]) < HOST_CPUS,
    svc + ' cpus ' + cpu[1] + ' is every host thread — that is not a limit');
}

// ── 데이터는 이름 붙은 볼륨에 있어야 한다. 컨테이너 재생성이 데이터를 지우면 안 된다.
assert(/pgdata:\/var\/lib\/postgresql\/data/.test(dbBlock), 'kdb-db data must live on the named volume');

console.log(JSON.stringify({ result: 'PASS', shmSizeBytes: shmBytes,
  composeFilesPinnedInDeploy: true, staticIpPinned: true,
  scope: 'Repo-local runtime config invariants; no network or database access.' }, null, 2));
