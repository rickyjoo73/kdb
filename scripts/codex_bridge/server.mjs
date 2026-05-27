// codex-bridge HTTP server.
//
// kdb-worker → POST /kdb_extract → spawn `codex exec` (ChatGPT auth, model=gpt-5.5)
//   with --output-schema kdb_extract.schema.json → reply with {spellings:[...]}.
//
// All persistent codex state (auth.json, sqlite logs, sessions) lives in CODEX_HOME
// which is bind-mounted from the host. The container runs as the same uid that owns
// the host directory so codex can read+write without permission conflicts.

import http from 'node:http';
import { spawn } from 'node:child_process';
import { mkdtempSync, readFileSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const PORT = parseInt(process.env.PORT || '9002', 10);
const CODEX_BIN = process.env.CODEX_BIN || 'codex';
const MODEL_DEFAULT = process.env.CODEX_BRIDGE_MODEL || 'gpt-5.5';
const SCHEMA_PATH = process.env.CODEX_BRIDGE_SCHEMA || '/app/schemas/kdb_extract.schema.json';
const TIMEOUT_MS = parseInt(process.env.CODEX_BRIDGE_TIMEOUT_MS || '90000', 10);
const FORCE_MODEL = process.env.CODEX_BRIDGE_FORCE_MODEL === '1';

if (!existsSync(SCHEMA_PATH)) {
  console.error(`[fatal] schema not found at ${SCHEMA_PATH}`);
  process.exit(1);
}

function log(level, msg, extra) {
  const line = { ts: new Date().toISOString(), level, msg, ...(extra || {}) };
  console.log(JSON.stringify(line));
}

function buildPrompt({ locale, title, description, hints }) {
  const hintLines = (hints || []).map(h =>
    `  - canonical_ko="${h.canonical_ko}" matched_text="${h.matched}" entity_id=${h.entity_id}`
  ).join('\n');
  return [
    'You extract foreign-language spellings of Korean K-content entities (people, groups, works) from a single news headline + description.',
    'Output JSON only — an output schema is enforced by the runtime; emit nothing outside the JSON object.',
    '',
    `Target locale (output language code): ${locale}`,
    `Title: ${title || ''}`,
    `Description: ${description || ''}`,
    'Pre-matched entity hints (cheap-gate found these Korean entities in the source text):',
    hintLines || '  (none — discover from scratch)',
    '',
    'Rules:',
    `- For each Korean entity that appears in the source, emit one "spellings" entry per ${locale}-language form actually used in the source text.`,
    '- ko_hint = canonical Korean form. Use the hint\'s canonical_ko when applicable; otherwise your best canonical guess in Korean (Hangul).',
    `- locale = "${locale}" (echo).`,
    '- spelling = the foreign-language form exactly as it appears in the source (preserve casing/diacritics).',
    '- confidence: 0.95+ when the source text explicitly contains the foreign spelling and the Korean mapping is unambiguous; 0.7~0.9 when likely but inferred; <0.7 = omit.',
    '- Do not put Korean text into "spelling". Do not invent entities that are not in the source.',
    '- If nothing qualifies, return {"spellings": []}.',
  ].join('\n');
}

function runCodex(prompt, model) {
  const workDir = mkdtempSync(join(tmpdir(), 'codex-bridge-'));
  const lastMsgFile = join(workDir, 'last.txt');
  return new Promise((resolve, reject) => {
    const args = [
      'exec',
      '--model', model,
      '--sandbox', 'read-only',
      '--skip-git-repo-check',
      '--ephemeral',
      '--ignore-user-config',
      '--ignore-rules',
      '--output-schema', SCHEMA_PATH,
      '--output-last-message', lastMsgFile,
      '--color', 'never',
      '-C', workDir,
      '-',
    ];
    const child = spawn(CODEX_BIN, args, {
      env: process.env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stderr = '';
    child.stdout.on('data', () => {}); // drain; we read the last-message file instead
    child.stderr.on('data', d => { stderr += d.toString(); });
    const timer = setTimeout(() => {
      try { child.kill('SIGKILL'); } catch {}
      reject(new Error(`codex timeout after ${TIMEOUT_MS}ms`));
    }, TIMEOUT_MS);
    child.on('error', err => {
      clearTimeout(timer);
      reject(err);
    });
    child.on('close', code => {
      clearTimeout(timer);
      try {
        if (code !== 0) {
          const tail = stderr.split('\n').filter(Boolean).slice(-5).join(' | ');
          throw new Error(`codex exit ${code}: ${tail || '(no stderr)'}`);
        }
        if (!existsSync(lastMsgFile)) {
          throw new Error('codex produced no last-message file');
        }
        const txt = readFileSync(lastMsgFile, 'utf8').trim();
        if (!txt) throw new Error('codex last-message file empty');
        const parsed = JSON.parse(txt);
        if (!parsed || !Array.isArray(parsed.spellings)) {
          throw new Error('response missing "spellings" array');
        }
        resolve(parsed);
      } catch (e) {
        reject(e);
      } finally {
        try { rmSync(workDir, { recursive: true, force: true }); } catch {}
      }
    });
    child.stdin.write(prompt);
    child.stdin.end();
  });
}

function readBody(req, limitBytes = 256 * 1024) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let total = 0;
    req.on('data', c => {
      total += c.length;
      if (total > limitBytes) {
        req.destroy();
        reject(new Error('request body too large'));
        return;
      }
      chunks.push(c);
    });
    req.on('end', () => resolve(Buffer.concat(chunks).toString('utf8')));
    req.on('error', reject);
  });
}

function sendJSON(res, status, obj) {
  const body = JSON.stringify(obj);
  res.writeHead(status, {
    'Content-Type': 'application/json; charset=utf-8',
    'Content-Length': Buffer.byteLength(body),
  });
  res.end(body);
}

const server = http.createServer(async (req, res) => {
  try {
    if (req.method === 'GET' && req.url === '/health') {
      return sendJSON(res, 200, {
        ok: true,
        provider: 'codex-cli',
        model_default: MODEL_DEFAULT,
        force_model: FORCE_MODEL,
      });
    }
    if (req.method === 'POST' && req.url === '/kdb_extract') {
      const raw = await readBody(req);
      let data;
      try {
        data = JSON.parse(raw);
      } catch {
        return sendJSON(res, 400, { spellings: [], error: 'invalid JSON body' });
      }
      if (!data || typeof data !== 'object') {
        return sendJSON(res, 400, { spellings: [], error: 'body must be a JSON object' });
      }
      const reqModel = (typeof data.model === 'string' && data.model.trim()) || MODEL_DEFAULT;
      const model = FORCE_MODEL ? MODEL_DEFAULT : reqModel;
      const prompt = buildPrompt({
        locale: data.locale || 'en',
        title: data.title || '',
        description: data.description || '',
        hints: Array.isArray(data.hints) ? data.hints : [],
      });
      const started = Date.now();
      try {
        const result = await runCodex(prompt, model);
        log('info', 'extract ok', {
          model,
          locale: data.locale,
          hint_count: (data.hints || []).length,
          spelling_count: result.spellings.length,
          duration_ms: Date.now() - started,
        });
        return sendJSON(res, 200, result);
      } catch (e) {
        log('warn', 'extract failed', {
          model,
          locale: data.locale,
          hint_count: (data.hints || []).length,
          duration_ms: Date.now() - started,
          error: String(e.message || e),
        });
        return sendJSON(res, 200, { spellings: [], error: String(e.message || e) });
      }
    }
    sendJSON(res, 404, { error: 'not found' });
  } catch (e) {
    sendJSON(res, 500, { error: String(e.message || e) });
  }
});

server.listen(PORT, '0.0.0.0', () => {
  log('info', 'codex-bridge listening', {
    port: PORT,
    model_default: MODEL_DEFAULT,
    force_model: FORCE_MODEL,
    schema: SCHEMA_PATH,
    timeout_ms: TIMEOUT_MS,
  });
});

for (const sig of ['SIGINT', 'SIGTERM']) {
  process.on(sig, () => {
    log('info', 'shutting down', { signal: sig });
    server.close(() => process.exit(0));
    setTimeout(() => process.exit(0), 5000).unref();
  });
}
