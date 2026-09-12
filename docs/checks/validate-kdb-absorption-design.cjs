'use strict';
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const docs = path.resolve(__dirname, '..');
const read = file => fs.readFileSync(path.join(docs, file), 'utf8');
const inventory = JSON.parse(read('KDB_TDB_SCHEMA_INVENTORY.json'));
const mapping = JSON.parse(read('KDB_TDB_FIELD_MAPPING.json'));
const writers = JSON.parse(read('KDB_FIELD_WRITER_MAP.json'));
assert.equal(mapping.version, 'field-mapping-design-v2');
assert.equal(writers.version, 'field-writer-map-v1');
assert.equal(writers.sources.length, 14);
const writerById = new Map(writers.sources.map(w => [w.id, w]));
assert.equal(writerById.size, 14);
let sourcePathReferences = 0;
for (const w of writers.sources) {
  assert(['confirmed_multiple_writers', 'migration_owned_no_runtime_writer_found', 'unconfirmed_writer'].includes(w.writer_confirmation));
  assert(w.source_paths.length > 0);
  for (const p of w.source_paths) {
    assert(!path.isAbsolute(p.path) && !p.path.split('/').includes('..') && /\.(go|sql)$/.test(p.path));
    assert(p.role && /^[a-f0-9]{64}$/.test(p.sha256), 'source path must pin observed bytes');
    sourcePathReferences++;
  }
}
const inventoryTables = new Map();
const schema = new Map();
let tableCount = 0, columnCount = 0;
for (const db of inventory.databases) for (const table of db.tables) {
  tableCount++; columnCount += table.columns.length;
  inventoryTables.set(db.database + '/' + table.name, table);
  for (const column of table.columns) {
    const key = [db.database, table.name, column.name].join('/');
    assert(!schema.has(key), 'Duplicate schema field ' + key); schema.set(key, column);
  }
}
const seen = new Set(), coveredTables = new Set();
for (const field of mapping.fields) {
  const key = [field.database, field.table, field.column].join('/');
  assert(schema.has(key), 'Unknown source field ' + key);
  assert(!seen.has(key), 'Duplicate mapping ' + key); seen.add(key);
  coveredTables.add([field.database, field.table].join('/'));
  assert(['include', 'conditional', 'archive_only', 'exclude_operational'].includes(field.disposition));
  assert(field.target && field.rule && field.status === 'design_only_not_applied');
  assert(field.trace && field.trace.required === true && typeof field.trace.pk_component === 'boolean');
  assert(field.target_adoption && field.target_adoption.target && field.target_adoption.rule);
  assert(['conditional', 'archive_only', 'exclude_operational'].includes(field.target_adoption.disposition));
  assert(writers.allowed_update_owners.includes(field.update_owner));
  const writer = writerById.get(field.writer_ref);
  assert(writer && writer.database === field.database && writer.table === field.table);
  assert(writer.callback_roles.includes(field.update_owner), 'callback owner missing in registry');
  assert(field.validation_cases.length > 0);
  field.validation_cases.forEach(c => assert(/^(X(0[1-9]|1[0-2])|M(0[1-9]|1[0-4]))$/.test(c)));
  assert(!/source_projection|guard_kind=operator_lock/.test(field.target + ' ' + field.target_adoption.target), 'undefined control destination');
  if (['archive_only', 'exclude_operational'].includes(field.disposition)) {
    assert.equal(field.trace.capture, 'source_reference_and_digest_only');
    assert.equal(field.target_adoption.disposition, field.disposition);
  }
  const table = inventoryTables.get(field.database + '/' + field.table);
  const pk = table.constraints.find(c => c.definition.startsWith('PRIMARY KEY ('));
  assert(pk, 'selected source PK missing');
  const isPk = pk.definition.slice(13, -1).split(',').map(c => c.trim()).includes(field.column);
  assert.equal(field.trace.pk_component, isPk, 'incorrect source PK classification');
  if (isPk) {
    assert.equal(field.disposition, 'include');
    assert.equal(field.target, 'kentity_migration_records.source_pk.' + field.column);
  }
  if (field.column === 'operator_locked') {
    assert(field.target_adoption.target.includes('operator_locked'));
    assert(!/source_state|source_merged_into/.test(field.target_adoption.target));
    assert(field.rule.includes('false') && field.rule.includes('true'));
  }
}
for (const key of schema.keys()) if (coveredTables.has(key.split('/').slice(0, 2).join('/'))) {
  assert(seen.has(key), 'Unmapped selected source field ' + key);
}
assert.equal(tableCount, 32); assert.equal(columnCount, 387);
assert.equal(coveredTables.size, 14); assert.equal(seen.size, 185);
const files = ['KDB_IDENTITY_CONTRACT.md', 'KDB_NAME_READINESS_CONTRACT.md', 'KDB_TARGET_SCHEMA.md',
  'KDB_ABSORPTION_CONTRACT_REVIEW.md', 'KDB_ENTITY_ADMIN_UI_DESIGN.md', 'KDB_ACCEPTANCE_LIMITS.md', 'KDB_INTEGRATION_TODO.md',
  'KDB_WRITER_DELTA_CONTRACT.md', 'KDB_API_COMPATIBILITY_CASES.md', 'KDB_MIGRATION_CONTROL_SCHEMA.md', 'KDB_WRITER_AUTHORITY.md'];
for (const file of files) {
  const text = read(file);
  assert.equal((text.match(/^```/gm) || []).length % 2, 0, 'Unclosed code fence ' + file);
  assert(!/[\t ]+$/m.test(text), 'Trailing whitespace ' + file);
  for (const match of text.matchAll(/\]\(([^)]+)\)/g)) {
    if (/^https?:/.test(match[1])) continue;
    assert(fs.existsSync(path.resolve(docs, match[1].split('#')[0])), 'Missing link ' + match[1]);
  }
}
const todo = read('KDB_INTEGRATION_TODO.md');
const tasks = Array.from(todo.matchAll(/^- \[([ x])\] \*\*(P\d\.\d+)\*\*/gm));
assert.equal(tasks.length, 66); assert.equal(new Set(tasks.map(m => m[2])).size, 66);
for (let n = 1; n <= 14; n++) assert(todo.includes('| M' + String(n).padStart(2, '0') + ' |'));
for (let n = 0; n <= 7; n++) assert(todo.includes('G' + n));
const identity = read('KDB_IDENTITY_CONTRACT.md');
for (let n = 1; n <= 12; n++) assert(identity.includes('| I' + String(n).padStart(2, '0') + ' |'));
const target = read('KDB_TARGET_SCHEMA.md');
const control = read('KDB_MIGRATION_CONTROL_SCHEMA.md');
for (let n = 1; n <= 10; n++) assert(control.includes('| C' + String(n).padStart(2, '0') + ' |'));
const authority = read('KDB_WRITER_AUTHORITY.md');
for (let n = 1; n <= 6; n++) assert(authority.includes('| A' + String(n).padStart(2, '0') + ' |'));
for (let n = 1; n <= 8; n++) assert(target.includes('— S' + String(n).padStart(2, '0')), 'Missing schema review item');
const replay = JSON.parse(read('checks/kdb-api-compatibility-cases.json'));
assert.equal(replay.status, 'design_only_not_executed');
assert.equal(replay.cases.length, 12);
assert.equal(new Set(replay.cases.map(c => c.id)).size, 12);
assert.equal(replay.source_metadata.length, 10);
replay.source_metadata.forEach(s => assert(/^[a-f0-9]{64}$/.test(s.sha256)));
for (let n = 1; n <= 12; n++) {
  const c = replay.cases.find(c => c.id === 'R' + String(n).padStart(2, '0'));
  assert(c && c.fixture && c.expected_legacy && c.proposed_target && c.code_evidence.length);
  assert(c.execution_status.startsWith('not_executed'));
  assert.equal(c.actual_consumer_status, 'unconfirmed');
}
assert.strictEqual(replay.safety.server_calls_performed, false);
assert.strictEqual(replay.safety.credentials_read_or_embedded, false);
const ui = read('ui/kdb-entity-console.html');
assert(!/\bfetch\s*\(|XMLHttpRequest|https?:\/\//.test(ui), 'Prototype must have no network calls');
assert(ui.includes('database_write:false'));
console.log(JSON.stringify({ result: 'PASS', schemaTables: tableCount, schemaColumns: columnCount,
  mappedSourceTables: coveredTables.size, mappedSourceFields: seen.size, unmappedInSelectedTables: 0,
  tasks: tasks.length, completedDesignTasks: tasks.filter(t => t[1] === 'x').map(t => t[2]),
  schemaReviewSpecifications: 8, apiReplaySpecifications: replay.cases.length, apiReplaysExecuted: 0,
  fieldsWithWriterAndAdoptionContract: mapping.fields.length, sourceWriterRegistries: writerById.size,
  sourcePkComponents: mapping.fields.filter(f => f.trace.pk_component).length,
  sourcePathReferences,
  scope: 'Static inventory/mapping/document consistency; not DB migration or data-quality approval.' }, null, 2));
