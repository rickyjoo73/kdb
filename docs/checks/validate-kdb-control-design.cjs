'use strict';
// Executable design examples, not an importer. Reads local metadata only.
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const docs = path.resolve(__dirname, '..');
const read = f => fs.readFileSync(path.join(docs, f), 'utf8');
const inventory = JSON.parse(read('KDB_TDB_SCHEMA_INVENTORY.json'));
const supplement = JSON.parse(read('KDB_SOURCE_SUPPLEMENT.json'));
const mapping = JSON.parse(read('KDB_TDB_FIELD_MAPPING.json'));
const tableScope = JSON.parse(read('KDB_TABLE_SCOPE.json'));
const selected = new Set(mapping.fields.map(f => f.database + '/' + f.table));
const schemas = new Map();
for (const db of inventory.databases.concat(supplement.databases)) for (const table of db.tables) {
  const key = db.database + '/' + table.name;
  if (!selected.has(key) && !table.fields) continue;
  const pk = (table.constraints || table.key_constraints).find(k => /^PRIMARY KEY \(/.test(k.definition));
  assert(pk, 'source without actual PK metadata: ' + key);
  const names = pk.definition.slice(13, -1).split(',').map(n => n.trim());
  const columns = names.map(name => {
    const c = table.columns.find(x => x.name === name); assert(c);
    return { name, type: c.udt || c.type };
  });
  assert(!schemas.has(key), 'duplicate selected source table'); schemas.set(key, columns);
}

function canonicalKey(input, descriptor) {
  assert(input && typeof input === 'object' && !Array.isArray(input));
  const names = descriptor.map(c => c.name).sort();
  assert.deepStrictEqual(Object.keys(input).sort(), names, 'PK missing or extra column');
  const out = {};
  for (const name of names) {
    const c = descriptor.find(x => x.name === name), field = input[name];
    assert(field && typeof field === 'object' && !Array.isArray(field));
    assert.deepStrictEqual(Object.keys(field).sort(), ['type', 'value']);
    assert.equal(field.type, c.type, 'source PK type mismatch');
    assert.equal(typeof field.value, 'string', 'integer must not pass through JS Number');
    let value = field.value;
    if (c.type === 'uuid') {
      assert(/^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$/.test(value));
      value = value.toLowerCase();
    } else if (['int2', 'int4', 'int8'].includes(c.type)) {
      assert(/^(0|-?[1-9][0-9]*)$/.test(value), 'integer encoding is not canonical');
      const bits = { int2: 16n, int4: 32n, int8: 64n }[c.type], n = BigInt(value);
      const half = 1n << (bits - 1n);
      assert(n >= -half && n < half, 'integer outside source range');
    } else assert.equal(c.type, 'text', 'PK type needs separate reviewed contract');
    out[name] = { type: c.type, value };
  }
  const text = JSON.stringify(out);
  assert(Buffer.byteLength(text, 'utf8') <= 512, 'PK exceeds pilot key size');
  return text;
}

function validateCanonicalText(text, descriptor) {
  const encoded = canonicalKey(JSON.parse(text), descriptor);
  // Reject duplicates, hidden extra bytes, and noncanonical input before hashing.
  assert.equal(text, encoded, 'canonical typed PK encoding required');
  return text;
}
function sourceIdentity(system, table, key, descriptor) {
  assert(/^[a-z][a-z0-9_]{0,63}$/.test(system));
  assert(/^[a-z][a-z0-9_]{0,63}$/.test(table));
  return JSON.stringify([system, table, validateCanonicalText(key, descriptor)]);
}

let pkExamples = 0;
for (const descriptor of schemas.values()) {
  const input = {};
  for (const c of descriptor) input[c.name] = { type: c.type,
    value: c.type === 'uuid' ? '00000000-0000-4000-8000-000000000001' : c.type.startsWith('int') ? '1' : 'fixture' };
  const text = canonicalKey(input, descriptor);
  assert.equal(validateCanonicalText(text, descriptor), text); pkExamples++;
}
assert.equal(pkExamples, 27);
const scopeNames = new Set(), scopeCounts = {};
for (const db of tableScope.databases) for (const t of db.tables) {
  const key = db.database + '/' + t.name;
  assert(!scopeNames.has(key)); scopeNames.add(key);
  assert(Object.prototype.hasOwnProperty.call(tableScope.meaning, t.category));
  assert.strictEqual(t.source_deletion_authorized, false);
  assert.equal(t.status, 'not_migrated_by_this_inventory');
  if (t.contract) assert(fs.existsSync(path.resolve(docs, t.contract)));
  if (t.category === 'selected_source') assert(schemas.has(key), 'selected table has no PK schema');
  if (schemas.has(key)) assert.equal(t.category, 'selected_source', 'selected source disappeared from ledger');
  scopeCounts[t.category] = (scopeCounts[t.category] || 0) + 1;
}
assert.equal(scopeNames.size, 87); assert.equal(scopeCounts.selected_source, 27);
const composite = schemas.get('tdb-db/tdb_src_records');
const value = { source_code: { type: 'text', value: 'fixture' }, external_id: { type: 'text', value: '000123' } };
const key = canonicalKey(value, composite);
assert.equal(JSON.parse(key).external_id.value, '000123');
assert.equal(canonicalKey({ external_id: value.external_id, source_code: value.source_code }, composite), key);
assert.notEqual(sourceIdentity('tdb', 'tdb_src_records', key, composite), sourceIdentity('tdb', 'tdb_place_links', key, composite));
assert.notEqual(sourceIdentity('tdb', 'tdb_src_records', key, composite), sourceIdentity('kdb', 'tdb_src_records', key, composite));
const bigintSchema = [{ name: 'id', type: 'int8' }];
const bigintKey = canonicalKey({ id: { type: 'int8', value: '9007199254740993' } }, bigintSchema);
assert.equal(JSON.parse(bigintKey).id.value, '9007199254740993');
const negativeExamples = [
  () => canonicalKey({ source_code: value.source_code }, composite),
  () => canonicalKey(Object.assign({ name: { type: 'text', value: 'fixture' } }, value), composite),
  () => canonicalKey({ id: { type: 'int8', value: 9007199254740993 } }, bigintSchema),
  () => canonicalKey({ id: { type: 'int8', value: '9223372036854775808' } }, bigintSchema),
  () => canonicalKey({ id: { type: 'int8', value: '01' } }, bigintSchema),
  () => canonicalKey({ id: { type: 'int8', value: null } }, bigintSchema),
  () => canonicalKey({ id: { type: 'text', value: '1' } }, bigintSchema),
  () => validateCanonicalText('{"id":{"type":"int8","value":"1"},"id":{"type":"int8","value":"2"}}', bigintSchema),
  () => canonicalKey({ source_code: value.source_code, external_id: { type: 'text', value: '가'.repeat(512) } }, composite)
];
negativeExamples.forEach(test => assert.throws(test));
const contract = read('KDB_MIGRATION_CONTROL_SCHEMA.md');
for (let n = 1; n <= 10; n++) assert(contract.includes('| C' + String(n).padStart(2, '0') + ' |'));
assert(!/[\t ]+$/m.test(contract));
assert.equal((contract.match(/^```/gm) || []).length % 2, 0);
for (const m of contract.matchAll(/\]\(([^)]+)\)/g)) {
  if (!/^https?:/.test(m[1])) assert(fs.existsSync(path.resolve(docs, m[1].split('#')[0])));
}
console.log(JSON.stringify({ result: 'PASS', selectedSourcePkExamples: pkExamples,
  observedPublicTableNames: scopeNames.size, tableScopeCategories: scopeCounts,
  negativePkExamples: negativeExamples.length, bigintPrecisionPreserved: true,
  dbTransactionsExecuted: 0, scope: 'Local typed-PK design examples only, NOT database migration/constraints/concurrency acceptance.' }, null, 2));
