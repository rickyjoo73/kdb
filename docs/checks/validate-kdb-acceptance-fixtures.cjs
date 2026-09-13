#!/usr/bin/env node
// Static validation of P0.08 acceptance fixtures. No DB, no network. Design-only guard:
// fixtures must stay synthetic (placeholder names), unexecuted, mapped to the ledger's M-table,
// and every judge path must exist in the registry with an independence rule.
const fs = require('fs');
const path = require('path');
const assert = require('assert');
const docs = path.resolve(__dirname, '..');
const read = f => fs.readFileSync(path.join(docs, f), 'utf8');
const fx = JSON.parse(read('checks/kdb-acceptance-fixtures.json'));
assert.equal(fx.version, 'acceptance-fixtures-design-v1');
assert.equal(fx.status, 'design_only_not_executed');
assert.strictEqual(fx.safety.database_reads_or_writes_performed, false);
assert.strictEqual(fx.safety.real_person_or_place_names_embedded, false);
assert(fx.evidence_independence_rule && fx.evidence_independence_rule.length > 40);
const judges = new Set(Object.keys(fx.judge_paths));
assert(judges.has('constructed_truth') && judges.has('evidence_provider_disjoint') && judges.has('operator_conflict_only'));
const todo = read('KDB_INTEGRATION_TODO.md');
const placeholder = /^(인물|작품|장소|명칭|대상|회사|단체|표기)-[A-Z][A-Za-z0-9-]*$/;
assert.equal(fx.m_cases.length, 14);
assert.equal(new Set(fx.m_cases.map(c => c.id)).size, 14);
for (let n = 1; n <= 14; n++) {
  const id = 'M' + String(n).padStart(2, '0');
  const c = fx.m_cases.find(c => c.id === id);
  assert(c, 'missing ' + id);
  assert(todo.includes('| ' + id + ' |'), id + ' not in ledger M-table');
  assert(c.title && c.fixture && c.ground_truth && Array.isArray(c.assertions) && c.assertions.length >= 2);
  assert(Array.isArray(c.invariants) && c.invariants.every(i => /^I(0[1-9]|1[0-2])$/.test(i)));
  assert(/^P[1-7]/.test(c.stage));
  assert(c.judge_path.includes('constructed_truth'), id + ' synthetic case must fix truth by construction');
  c.judge_path.forEach(j => assert(judges.has(j), id + ' unknown judge ' + j));
  assert(c.execution_status.startsWith('not_executed'));
  const names = JSON.stringify(c.fixture).match(/"name":"([^"]+)"/g) || [];
  names.forEach(m => assert(placeholder.test(m.slice(8, -1)), id + ' non-placeholder name ' + m));
}
assert.equal(fx.t_cases.length, 19);
assert.equal(new Set(fx.t_cases.map(c => c.id)).size, 19);
assert.equal(new Set(fx.t_cases.map(c => c.place_type)).size, 19);
const classification = read('KDB_CLASSIFICATION_RULES.md');
let totalRows = 0;
for (const c of fx.t_cases) {
  assert(/^T(0[1-9]|1[0-9])$/.test(c.id));
  assert(Number.isInteger(c.total) && c.total > 0 && c.with_qid <= c.total && c.with_disambiguator <= c.total);
  assert(Array.isArray(c.targets) && c.targets.length > 0 && Array.isArray(c.boundary) && c.boundary.length > 0);
  assert(classification.includes('| ' + c.place_type + ' |'), c.place_type + ' not in classification §8');
  totalRows += c.total;
}
assert.equal(totalRows, 536329, 'T totals must equal TDB row count observed 2026-09-12');
fx.t_cases_common.judge_path.forEach(j => assert(judges.has(j)));
assert(fx.t_cases_common.accept_rule.includes('agree') && fx.t_cases_common.ground_truth_rule.includes('never'));
const md = read('KDB_ACCEPTANCE_FIXTURES.md');
assert.equal((md.match(/^```/gm) || []).length % 2, 0);
assert(!/[\t ]+$/m.test(md), 'trailing whitespace');
for (const m of md.matchAll(/\]\(([^)#]+)(#[^)]*)?\)/g)) if (!/^https?:/.test(m[1])) assert(fs.existsSync(path.resolve(docs, m[1])), 'missing link ' + m[1]);
console.log(JSON.stringify({ result: 'PASS', mCases: 14, tCases: 19, tRowsCovered: totalRows, judgePaths: judges.size, executed: 0,
  scope: 'Static design validation only; no DB constraints, no LLM calls, no replay.' }));
