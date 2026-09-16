#!/usr/bin/env node
// 마이그레이션이 **스스로 트랜잭션을 여는지** 본다.
//
// ★왜 필요한가 (2026-09-15 실측).
//   배포 러너는 `psql_run -f -` 로 파일을 통째로 흘려보내는데 `--single-transaction`
//   이 없다. 그래서 파일 안에 BEGIN/COMMIT 이 없으면 각 문장이 **따로 커밋**된다.
//   0135 에서 불변식이 실패했을 때 앞선 UPDATE 471행이 이미 커밋돼 있었다 —
//   불변식은 "실패하면 되돌린다"고 믿고 쓰는 장치인데, 되돌릴 것이 없었다.
//
//   고칠 자리는 러너가 아니다. 135개 중 63개가 이미 자기 BEGIN/COMMIT 을 갖고 있어서
//   러너에 `--single-transaction` 을 붙이면 그것들이 "이미 트랜잭션 안"이라고 경고를
//   내거나 중첩으로 깨진다. 파일마다 스스로 열게 하고, 여기서 그것을 강제한다.
//
// ★예외: CREATE INDEX CONCURRENTLY 는 트랜잭션 안에서 돌 수 없다(Postgres 제약).
//   그런 파일은 트랜잭션을 못 열므로 면제하되, **면제 사유를 파일에 적게** 한다 —
//   면제가 조용하면 다음 사람이 그냥 따라 한다.
const fs = require("fs");
const path = require("path");

const dir = path.resolve(process.cwd(), "migrations");
if (!fs.existsSync(dir)) {
  console.error("FAIL: migrations/ 가 없다");
  process.exit(1);
}

// 주석과 **달러 인용 본문**을 지우고 센다.
//
// ★`END;` 를 트랜잭션 종료로 세면 안 된다 — PL/pgSQL 블록도 `END;` 로 끝난다.
//   처음 그렇게 썼다가 0123(DO 블록 여럿)이 "BEGIN 1 / END 2" 로 오탐됐다.
//   트랜잭션을 닫는 것은 `COMMIT` 하나뿐이다. 그리고 DO $$ … $$ 본문 안의
//   BEGIN/END 는 트랜잭션과 무관하므로 세기 전에 통째로 지운다.
function stripNoise(sql) {
  return sql
    .replace(/\$([A-Za-z_]*)\$[\s\S]*?\$\1\$/g, " ") // $$ … $$ · $chk$ … $chk$
    .replace(/\/\*[\s\S]*?\*\//g, " ")
    .split("\n")
    .map((l) => l.replace(/--.*$/, ""))
    .join("\n");
}

// ★`ALTER TYPE … ADD VALUE` 를 면제에서 뺀다 (2026-09-16).
//
//   PostgreSQL 11 까지는 트랜잭션 안에서 못 돌았고 그래서 여기 들어 있었다.
//   **PG 12 부터 된다.** 운영은 16.14 다.
//
//   실측으로 드러났다 — 0143·0146 은 BEGIN/COMMIT 을 갖고 운영에 정상 적용됐는데
//   이 검사만 "BEGIN 을 빼라"고 했다. 검사가 실제와 반대였다.
//
//   진짜 제약은 따로다: **추가한 값을 같은 트랜잭션 안에서 쓸 수 없다.**
//   그래서 0143 은 enum 추가와 그 값을 쓰는 UPDATE 를 다른 파일/문장으로 나눈다.
//   그건 이 검사가 볼 수 있는 성질이 아니라 마이그레이션 작성자의 몫이다.
const EXEMPT = /CREATE\s+(UNIQUE\s+)?INDEX\s+CONCURRENTLY|DROP\s+INDEX\s+CONCURRENTLY|VACUUM/i;
const EXEMPT_NOTE = /트랜잭션\s*(밖|불가|면제)|CONCURRENTLY/;

// ★이미 운영에 적용된 파일 22개는 목록으로 못박는다 (2026-09-15).
//
//   지금 고쳐도 운영은 안 바뀌고(이미 적용됨), 22개를 한꺼번에 손대면 그중 하나라도
//   트랜잭션과 안 맞을 때 회귀가 통째로 막힌다. 새 파일부터 강제하는 것이 목적이다.
//   목록에 있으면 통과시키되 **줄어들기만 해야 한다** — 목록에 없는 이름이 여기 오면
//   그건 새로 생긴 빚이므로 막는다. 목록의 파일을 고쳐 BEGIN/COMMIT 을 넣으면
//   아래에서 "목록에 있는데 이미 고쳐졌다"로 알려 주고, 그때 목록에서 지우면 된다.
const LEGACY = new Set([
  "0057_kdb_admin_users.sql",
  "0058_pg_trgm.sql",
  "0059_last_enriched_at.sql",
  "0077_kdb_api_requests.sql",
  "0080_kdb_netflix_source.sql",
  "0081_kdb_mydramalist_source.sql",
  "0082_kdb_romanization_source.sql",
  "0083_kdb_opencc_source.sql",
  "0085_research_queue_source_url.sql",
  "0086_kdb_backup_log.sql",
  "0088_kdb_source_pipeline_sources.sql",
  "0090_identity_correction_audit.sql",
  "0092_kdb_request_terms.sql",
  "0093_kdb_translate_cache.sql",
  "0094_kdb_source_priority_gtranslate.sql",
  "0095_translate_cache_entity_scope.sql",
  "0096_kdb_adjudication_log.sql",
  "0097_kdb_recheck_log.sql",
  "0098_kdb_evidence_refs.sql",
  "0099_kdb_evidence_refs_snippet.sql",
  "0100_kdb_fill_retry_input_hash.sql",
  "0133_kentity_names_evidence_index.sql",
]);

let bad = 0;
let checked = 0;
let legacyLeft = 0;
let exempt = 0;
for (const name of fs.readdirSync(dir).filter((f) => f.endsWith(".sql")).sort()) {
  const raw = fs.readFileSync(path.join(dir, name), "utf8");
  const body = stripNoise(raw);
  checked++;

  const opens = (body.match(/\bBEGIN\s*;/gi) || []).length;
  const closes = (body.match(/\bCOMMIT\s*;/gi) || []).length;

  if (EXEMPT.test(body)) {
    exempt++;
    if (opens > 0) {
      console.error(`FAIL ${name}: CONCURRENTLY/VACUUM 은 트랜잭션 안에서 못 돈다 — BEGIN 을 빼라`);
      bad++;
    } else if (!EXEMPT_NOTE.test(raw)) {
      console.error(`FAIL ${name}: 트랜잭션을 안 여는 이유가 파일에 없다 — 왜 면제인지 적어라`);
      bad++;
    }
    continue;
  }

  if (opens === 0) {
    if (LEGACY.has(name)) {
      legacyLeft++;
      continue;
    }
    console.error(
      `FAIL ${name}: BEGIN/COMMIT 이 없다 — 배포 러너에 --single-transaction 이 없어서 ` +
        `중간에 실패하면 앞 문장이 커밋된 채로 남는다(0135 에서 471행이 그랬다)`
    );
    bad++;
    continue;
  }
  if (LEGACY.has(name)) {
    console.error(`FAIL ${name}: 고쳐졌는데 아직 LEGACY 목록에 있다 — 목록에서 지워라`);
    bad++;
  }
  if (opens !== closes) {
    console.error(`FAIL ${name}: BEGIN ${opens}개 / COMMIT ${closes}개 — 짝이 안 맞는다`);
    bad++;
  }
}

console.log(`검사 ${checked}개 · 면제 ${exempt}개 · 옛 빚 ${legacyLeft}개 · 위반 ${bad}개`);
process.exit(bad === 0 ? 0 : 1);
