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
const CLASSIFY_SCHEMA_PATH = process.env.CODEX_BRIDGE_CLASSIFY_SCHEMA || '/app/schemas/kdb_classify.schema.json';
const FILL_LOCALE_SCHEMA_PATH = process.env.CODEX_BRIDGE_FILL_LOCALE_SCHEMA || '/app/schemas/kdb_fill_locale.schema.json';
const TIMEOUT_MS = parseInt(process.env.CODEX_BRIDGE_TIMEOUT_MS || '90000', 10);
const FORCE_MODEL = process.env.CODEX_BRIDGE_FORCE_MODEL === '1';

if (!existsSync(SCHEMA_PATH)) {
  console.error(`[fatal] schema not found at ${SCHEMA_PATH}`);
  process.exit(1);
}
if (!existsSync(CLASSIFY_SCHEMA_PATH)) {
  console.warn(`[warn] classify schema not found at ${CLASSIFY_SCHEMA_PATH} — /kdb_classify will 500`);
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

function runCodex(prompt, model, schemaPath) {
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
      '--output-schema', schemaPath || SCHEMA_PATH,
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

function buildFillLocalePrompt({ ko, entity_type, known, missing, wikidata, sitelinks, primary_role, aliases_ko }) {
  const kn = known && typeof known === 'object' ? known : {};
  const knLines = Object.entries(kn)
    .filter(([_, v]) => v && String(v).trim())
    .map(([k, v]) => `  - ${k}: "${v}"`).join('\n');
  const miss = Array.isArray(missing) ? missing : [];
  const sl = sitelinks && typeof sitelinks === 'object' ? sitelinks : {};
  const slLines = Object.entries(sl)
    .filter(([_, v]) => v && String(v).trim())
    .map(([k, v]) => `  - ${k}: ${v}`).join('\n');
  const aliKo = Array.isArray(aliases_ko) ? aliases_ko : [];
  return [
    'You synthesize missing foreign-language spellings for a known Korean K-content entity.',
    'Output JSON only — an output schema is enforced.',
    '',
    `Korean canonical name: "${ko}"`,
    `entity_type: ${entity_type || '?'}${primary_role ? ` (primary_role=${primary_role})` : ''}`,
    aliKo.length ? `Korean aliases: ${aliKo.join(', ')}` : 'Korean aliases: (none)',
    'Known spellings (do NOT regenerate these — use as romanization basis):',
    knLines || '  (none)',
    `Missing locales (synthesize these): ${miss.join(', ') || '(none)'}`,
    wikidata ? `Wikidata: Q=${wikidata.qid || '?'} description="${wikidata.description || ''}"` : 'Wikidata: (none)',
    slLines ? `Wikipedia URLs by locale:\n${slLines}` : 'Wikipedia URLs: (none)',
    '',
    'Rules:',
    '- Each output spelling must be how local media in that locale would actually print the name (not literal translation).',
    '- For person/group: use locale-appropriate romanization (revised Romanization for en; katakana for ja with "・" between surname-given; Latin script for vi/es/id/pt-br/zh-Hant=Traditional Chinese).',
    '- For drama/movie/show: use the official localized title if widely known (e.g., "오징어 게임" pt-br = "Round 6", es = "El juego del calamar"). Otherwise transliteration.',
    '- For brand/agency/term: prefer the form used by domestic press in that language.',
    '- If you don\'t know for a locale and cannot derive confidently, put it in "skipped" — do NOT guess.',
    '- confidence: 0.7-0.9 when supported by Wikidata sitelinks (clear evidence); 0.5 default for romanization from known en/ja; <0.5 → skip instead.',
    '- Do NOT include locales that already have a known spelling.',
  ].join('\n');
}

function buildClassifyPrompt({ ko, spellings, source_domains, notes, wikidata, search_hits }) {
  const sp = spellings && typeof spellings === 'object' ? spellings : {};
  const spLines = Object.entries(sp)
    .filter(([_, v]) => v && String(v).trim())
    .map(([k, v]) => `  - ${k}: "${v}"`).join('\n');
  const sd = Array.isArray(source_domains) ? source_domains : [];
  const wd = wikidata && typeof wikidata === 'object' ? wikidata : null;
  const hits = Array.isArray(search_hits) ? search_hits.slice(0, 8) : [];
  return [
    'You classify a Korean K-content entity into one of 13 entity_type enum values.',
    'Output JSON only — an output schema is enforced. Emit nothing outside the JSON object.',
    '',
    `Korean canonical name: "${ko}"`,
    'Multilingual spellings observed in Korean / global media:',
    spLines || '  (none)',
    `Source media domains (${sd.length}): ${sd.join(', ') || '(none)'}`,
    `Existing notes: ${notes || '(none)'}`,
    wd ? `Wikidata candidate: Q=${wd.qid || '?'} label="${wd.label || ''}" description="${wd.description || ''}"` : 'Wikidata: (not queried)',
    hits.length ? `External search hits (titles from local news): \n${hits.map(h => '  - ' + h).join('\n')}` : 'External search hits: (none)',
    '',
    'INPUT INTEGRITY CHECK (do this FIRST):',
    '- If ko contains lone Hangul jamo (e.g., "ㄱ" "ㅁ" "ㅇ" "ㅏ") embedded between syllables — this is a typo from broken RSS/OCR.',
    '- In that case set entity_type="unknown", confidence ≤ 0.30, needs_search=true, and reason="자소 분리 오타: <ko 정정안>".',
    '',
    'NON-ENTITY FILTER (most important — do this SECOND):',
    '- Reject Korean general words / verbs / adverbs / phrases that are NOT proper nouns.',
    '- Examples that MUST be classified as entity_type="term" with confidence ≤ 0.30:',
    '    "건강하게" (adverb "in good health"), "세계일주" (common noun "world tour"),',
    '    "춤" ("dance"), "파일럿" ("pilot" as common loanword), "훌리건" ("hooligan"),',
    '    "오십견" (medical term "frozen shoulder"), "마녀" ("witch" when not a known work title),',
    '    "신사" ("gentleman" when context is general), "상도" ("business ethics" when ambiguous).',
    '- If ko looks like a complete sentence, slogan, or descriptive phrase (조사 included, e.g. "~하게", "~이다", "~었다") → entity_type="term", confidence ≤ 0.30.',
    '- A proper noun typically has NO Korean particle ("이/가/을/를/에/와/하게/하다") attached.',
    '- If unsure but ko is a single common Korean word with NO supporting media evidence → entity_type="term", confidence ≤ 0.40, needs_search=true.',
    '',
    '- Otherwise proceed with classification below.',
    '',
    'Classification rules:',
    '- person: 한국 인물 (배우/가수/idol/감독/MC/스포츠 등). 2-4 자 한글 + 외래어 음역(예: ja "アン・ウンジン", en "Surname-Given") = 강한 단서.',
    '- group: K-pop 그룹 / 보이밴드 / 걸그룹 / 듀오 (예: BTS, BLACKPINK, NewJeans). 멤버 인물 X.',
    '- drama: 한국 드라마 시리즈 (TV/Netflix/OTT 드라마 시리즈).',
    '- movie: 한국 영화 (극장 공개작).',
    '- show: 한국 예능 / 버라이어티 / 토크쇼 (드라마 X).',
    '- song_album: 곡명 / 앨범명 (그룹/가수가 아닌 작품).',
    '- agency: 연예 기획사 (HYBE/JYP/SM/YG 등).',
    '- channel_outlet: 방송사 / 뉴스 매체 / 채널 (KBS/SBS/Mnet/티빙).',
    '- brand_place: 브랜드 / 장소 / 회사 (K-content 관련).',
    '- event_tour: 콘서트 / 투어 / 시상식 / 페스티벌.',
    '- character: 작품 속 캐릭터 (실존 인물 X).',
    '- term: 일반 명사 / 개념 / 한국 문화 용어 (예: 한복, 김치).',
    '- unknown: 정보 부족 시에만 (가능하면 다른 type 선택).',
    '',
    'Confidence guide:',
    '- 0.95+ : 외래어 음역 / persons-sync 명시 / Wikidata K-content label 일치 등 1 의미 단서.',
    '- 0.8~0.95 : 강한 패턴 (인명형/제목구조) + 매체 수 ≥ 2.',
    '- 0.6~0.8 : 일반 추정 (단일 단서, K-content RSS 매체).',
    '- <0.6 : 동음이의어 가능 / 정보 부족. needs_search=true 권장.',
    '',
    'If entity_type=person, also fill primary_role (배우 → actor / 가수 → singer / idol → idol / 코미디언 → comedian / 감독 → director / etc).',
    'If you genuinely cannot decide above 0.6 confidence, set needs_search=true and pick best-guess type with low conf.',
  ].join('\n');
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
    if (req.method === 'POST' && req.url === '/kdb_fill_locale') {
      const raw = await readBody(req);
      let data;
      try { data = JSON.parse(raw); }
      catch { return sendJSON(res, 400, { error: 'invalid JSON body' }); }
      if (!data || typeof data !== 'object' || !data.ko) {
        return sendJSON(res, 400, { error: 'ko required' });
      }
      const reqModel = (typeof data.model === 'string' && data.model.trim()) || MODEL_DEFAULT;
      const model = FORCE_MODEL ? MODEL_DEFAULT : reqModel;
      const prompt = buildFillLocalePrompt(data);
      const started = Date.now();
      try {
        const result = await runCodex(prompt, model, FILL_LOCALE_SCHEMA_PATH);
        log('info', 'fill_locale ok', {
          model, ko: data.ko,
          filled: Array.isArray(result.spellings) ? result.spellings.length : 0,
          skipped: Array.isArray(result.skipped) ? result.skipped.length : 0,
          duration_ms: Date.now() - started,
        });
        return sendJSON(res, 200, result);
      } catch (e) {
        log('warn', 'fill_locale failed', {
          model, ko: data.ko, duration_ms: Date.now() - started,
          error: String(e.message || e),
        });
        return sendJSON(res, 200, { spellings: [], skipped: [], error: String(e.message || e) });
      }
    }
    if (req.method === 'POST' && req.url === '/kdb_classify') {
      const raw = await readBody(req);
      let data;
      try { data = JSON.parse(raw); }
      catch { return sendJSON(res, 400, { error: 'invalid JSON body' }); }
      if (!data || typeof data !== 'object' || !data.ko) {
        return sendJSON(res, 400, { error: 'ko required' });
      }
      const reqModel = (typeof data.model === 'string' && data.model.trim()) || MODEL_DEFAULT;
      const model = FORCE_MODEL ? MODEL_DEFAULT : reqModel;
      const prompt = buildClassifyPrompt(data);
      const started = Date.now();
      try {
        const result = await runCodex(prompt, model, CLASSIFY_SCHEMA_PATH);
        log('info', 'classify ok', {
          model, ko: data.ko, entity_type: result.entity_type,
          confidence: result.confidence, duration_ms: Date.now() - started,
        });
        return sendJSON(res, 200, result);
      } catch (e) {
        log('warn', 'classify failed', {
          model, ko: data.ko, duration_ms: Date.now() - started,
          error: String(e.message || e),
        });
        return sendJSON(res, 200, { entity_type: 'unknown', confidence: 0, reason: String(e.message || e) });
      }
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
        const result = await runCodex(prompt, model, SCHEMA_PATH);
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
