// Design-only lint: reads documents and Go source; no network or database writes.
// Run from any directory: node docs/checks/validate-kdb-classification.cjs
'use strict';
const fs = require('fs');
const path = require('path');
const docs = path.resolve(__dirname, '..');
const repo = path.resolve(docs, '..');
const spec = fs.readFileSync(path.join(docs, 'KDB_CLASSIFICATION_RULES.md'), 'utf8');
function assert(ok, message) { if (!ok) throw new Error(message); }
function rows(section) {
  const part = spec.split('## ' + section + '. ')[1];
  assert(part, 'Missing section ' + section);
  return part.split('\n## ')[0].split('\n')
    .filter(line => /^\| [a-z][a-z_]* \|/.test(line))
    .map(line => line.split('|').slice(1, -1).map(value => value.trim()))
    .filter(row => !['code', 'entity_type', 'source_code'].includes(row[0]));
}
function unique(values, label) {
  assert(new Set(values).size === values.length, label + ': duplicate code');
}
function same(actual, expected, label) {
  assert(JSON.stringify(actual.slice().sort()) === JSON.stringify(expected.slice().sort()), label + ': mismatch');
}
const types = rows(2).map(row => row[0]);
const subtypes = rows(3);
const roles = rows(4);
const domains = rows(5).map(row => row[0]);
unique(types, 'types');
unique(subtypes.map(row => row[0] + '.' + row[1]), 'subtypes');
unique(roles.map(row => row[0]), 'roles');
unique(domains, 'domains');
subtypes.forEach(row => assert(types.includes(row[0]), 'Unknown subtype type: ' + row[0]));
const parents = new Map(roles.map(row => [row[0], row[1]]));
for (const [code, parent] of parents) {
  assert(parent === '—' || parents.has(parent), 'Unknown role parent: ' + code);
  const visited = new Set();
  let current = code;
  while (current !== '—') {
    assert(!visited.has(current), 'Role cycle: ' + code);
    visited.add(current);
    assert(visited.size <= 3, 'Role depth exceeded: ' + code);
    current = parents.get(current);
  }
}
same(rows(6).map(row => row[0]), 'person group show drama movie song_album agency channel_outlet brand_place event_tour character term unknown'.split(' '), 'KDB 2026-09-12 enum snapshot');
same(rows(7).map(row => row[0]), 'idol singer rapper actor broadcaster comedian director producer model creator athlete politician businessperson journalist fictional other'.split(' '), 'KDB 2026-09-12 role snapshot');
same(rows(8).map(row => row[0]), 'accommodation admin_region cultural_facility district education festival_event food heritage legal_dong leisure_sports nature organization other person restaurant road shopping tourist_spot transit transport travel_course work'.split(' '), 'TDB 2026-09-12 type snapshot');
const targets = new Set(subtypes.map(row => row[0] + '.' + row[1]));
for (const section of [6, 8]) for (const row of rows(section)) for (const target of row[1].split(' / ')) {
  assert(types.includes(target) || targets.has(target), 'Unknown mapping target: ' + target);
}
for (const row of rows(7).filter(row => !['fictional', 'other'].includes(row[0]))) {
  assert(parents.has(row[1].split(';')[0]), 'Unknown role mapping: ' + row[0]);
}
const go = fs.readFileSync(path.join(repo, 'internal/kentity/store.go'), 'utf8');
function goCodes(name) {
  const match = go.match(new RegExp('var ' + name + ' = map\\[string\\]bool\\{([^}]+)'));
  assert(match, 'Go baseline changed: ' + name + '; review compatibility test');
  return Array.from(match[1].matchAll(/"([a-z_]+)": true/g)).map(item => item[1]);
}
same(types.filter(code => !['brand', 'character'].includes(code)), goCodes('supportedTypes'), 'Current common 11-code compatibility');
same(domains, goCodes('supportedDomains'), 'Current domain compatibility');
const adapter = fs.readFileSync(path.join(repo, 'internal/kentity/tdb_types.go'), 'utf8');
const adapterPart = adapter.split('var tdbTypes = map[string]TDBType{')[1];
assert(adapterPart, 'TDB adapter changed; review compatibility test');
same(rows(8).map(row => row[0]), Array.from(adapterPart.split('\n}')[0].matchAll(/"([a-z_]+)":\s*\{/g)).map(item => item[1]), 'TDB adapter coverage');
for (const file of ['KDB_CLASSIFICATION_RULES.md', 'KDB_TARGET_SCHEMA.md']) {
  const content = fs.readFileSync(path.join(docs, file), 'utf8');
  assert((content.match(/^```/gm) || []).length % 2 === 0, 'Unclosed code fence: ' + file);
  for (const link of content.matchAll(/\]\(([^)]+)\)/g)) {
    if (/^https?:/.test(link[1])) continue;
    assert(fs.existsSync(path.resolve(docs, link[1].split('#')[0])), 'Missing link: ' + link[1]);
  }
}
console.log(JSON.stringify({ result: 'PASS', types: types.length, subtypes: subtypes.length,
  roles: roles.length, domains: domains.length, kdbTypeMappings: rows(6).length,
  kdbRoleMappings: rows(7).length, tdbTypeMappings: rows(8).length,
  scope: 'Static design validation only; not semantic classification, DB constraints, or runtime tests.' }, null, 2));
