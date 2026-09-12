'use strict';
// Static contract checks only. No database/network calls or source modification.
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const crypto = require('crypto');
const docs = path.resolve(__dirname, '..');
const read = f => fs.readFileSync(path.join(docs, f), 'utf8');
const supplement = JSON.parse(read('KDB_SOURCE_SUPPLEMENT.json'));
const contract = read('KDB_WRITER_DELTA_CONTRACT.md');
const dispositions = ['include', 'conditional', 'archive_only', 'exclude_operational'];

function validate(data) {
  assert.equal(data.version, 'source-supplement-design-v1');
  assert.strictEqual(data.no_row_values_read, true);
  assert.strictEqual(data.projection_targets_are_not_final_db_columns, true);
  const tables = new Set();
  let columns = 0;
  for (const db of data.databases) {
    assert(['kdb-db', 'tdb-db'].includes(db.database));
    assert(!isNaN(Date.parse(db.observed_at)));
    for (const t of db.tables) {
      const tk = db.database + '/' + t.name;
      assert(!tables.has(tk), 'duplicate table'); tables.add(tk);
      const fields = new Map();
      const cols = new Map(t.columns.map(c => [c.name, c]));
      assert.equal(cols.size, t.columns.length, 'duplicate schema column');
      columns += cols.size;
      for (const f of t.fields) {
        assert(cols.has(f.column), 'unknown column');
        assert(!fields.has(f.column), 'duplicate mapped column'); fields.set(f.column, f);
        assert(dispositions.includes(f.disposition));
        assert(f.target && f.rule && f.update_owner);
        assert.equal(f.status, 'projection_design_only_not_applied');
        assert(f.validation_cases.length > 0);
        f.validation_cases.forEach(id => assert(contract.includes('| ' + id + ' |'), 'unknown case'));
        if (['api_key', 'key_param', 'config', 'sync_state'].includes(f.column)) {
          assert.equal(f.disposition, 'exclude_operational', 'credential/collector field copied');
        }
        if (f.column === 'external_raw_payload') assert.equal(f.disposition, 'archive_only');
      }
      assert.equal(fields.size, cols.size, 'unmapped source field');
      const pk = t.key_constraints.find(k => k.definition.startsWith('PRIMARY KEY ('));
      assert(pk, 'missing source PK metadata');
      const pkCols = pk.definition.slice(13, -1).split(',').map(s => s.trim());
      pkCols.forEach(c => assert.equal(fields.get(c).disposition, 'include', 'incomplete PK trace'));
    }
  }
  assert.equal(tables.size, 13); assert.equal(columns, 161);
  assert.equal(data.source_basis.length, 2);
  let codeFiles = 0;
  for (const source of data.source_basis) {
    assert(['kdb', 'tdb'].includes(source.system));
    assert(/^[a-f0-9]{40}$/.test(source.head));
    const seen = new Set();
    for (const f of source.files) {
      assert(!path.isAbsolute(f.path) && !f.path.split('/').includes('..'));
      assert(!seen.has(f.path)); seen.add(f.path);
      assert(/^[a-f0-9]{64}$/.test(f.sha256)); codeFiles++;
    }
  }
  assert.equal(codeFiles, 24);
  return { tables: tables.size, columns, codeFiles };
}

const result = validate(supplement);
const findField = (d, table, col) => d.databases.flatMap(db => db.tables)
  .find(t => t.name === table).fields.find(f => f.column === col);
const negativeCases = [
  d => { findField(d, 'tdb_sources', 'api_key').disposition = 'conditional'; },
  d => { d.databases[0].tables[0].fields.pop(); },
  d => { const t = d.databases[0].tables[0]; t.fields[1] = t.fields[0]; },
  d => { findField(d, 'tdb_holds', 'external_id').disposition = 'archive_only'; },
  d => { findField(d, 'tdb_sources', 'state').validation_cases = ['X99']; },
  d => { d.projection_targets_are_not_final_db_columns = false; }
];
for (const mutate of negativeCases) {
  const copy = JSON.parse(JSON.stringify(supplement)); mutate(copy);
  assert.throws(() => validate(copy), assert.AssertionError, 'unsafe fixture must fail');
}
for (let n = 1; n <= 10; n++) assert(contract.includes('| W' + String(n).padStart(2, '0') + ' |'));
for (let n = 1; n <= 12; n++) assert(contract.includes('| X' + String(n).padStart(2, '0') + ' |'));
for (const f of ['KDB_WRITER_DELTA_CONTRACT.md']) {
  const s = read(f);
  assert.equal((s.match(/^```/gm) || []).length % 2, 0);
  assert(!/[\t ]+$/m.test(s));
  for (const m of s.matchAll(/\]\(([^)]+)\)/g)) {
    if (!/^https?:/.test(m[1])) assert(fs.existsSync(path.resolve(docs, m[1].split('#')[0])));
  }
}

let sourceBytesChecked = false;
if (process.argv.length > 2) {
  assert.equal(process.argv.length, 4, 'optional arguments: KDB_ROOT TDB_ROOT');
  const roots = { kdb: path.resolve(process.argv[2]), tdb: path.resolve(process.argv[3]) };
  for (const s of supplement.source_basis) for (const f of s.files) {
    const digest = crypto.createHash('sha256').update(fs.readFileSync(path.join(roots[s.system], f.path))).digest('hex');
    assert.equal(digest, f.sha256, 'source drift: ' + s.system + '/' + f.path);
  }
  sourceBytesChecked = true;
}
console.log(JSON.stringify({ result: 'PASS', additionalSourceMetadata: result,
  negativeStaticFixtures: negativeCases.length, sourceBytesChecked,
  dbConcurrencyTestsExecuted: 0, scope: 'Static design/trace validation, NOT migration, DB safety or data-quality acceptance.' }, null, 2));
