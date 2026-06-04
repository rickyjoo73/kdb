// pw_admin_review.js — admin 전 페이지를 인증 상태로 열어 스크린샷 + 진단 수집.
// 세션 쿠키는 KDB_ADMIN_SESSION_SECRET(raw 문자열 바이트)로 HMAC 서명해 발급
// (cmd/kdb/main.go: secret=[]byte(env), session.go: payload="email:exp").
const crypto = require('crypto');
const { chromium } = require('playwright');
const fs = require('fs');

const SECRET = process.env.KDB_SS || '';
const EMAIL = 'rickyjoo@aiinad.com';
const BASE = 'http://127.0.0.1:9101';
const OUT = '/tmp/pw';
const ENTITY_ID = process.env.ENTITY_ID || '';

function b64url(buf) { return Buffer.from(buf).toString('base64').replace(/\+/g,'-').replace(/\//g,'_').replace(/=+$/,''); }
function mintSession() {
  const exp = Math.floor(Date.now()/1000) + 3600;
  const payload = `${EMAIL}:${exp}`;
  const sig = crypto.createHmac('sha256', Buffer.from(SECRET)).update(payload).digest();
  return b64url(Buffer.from(payload)) + '.' + b64url(sig);
}

const PAGES = [
  ['00-login',        '/admin/login',              false],
  ['01-dashboard',    '/admin',                    true],
  ['02-entities',     '/admin/entities',           true],
  ['03-sources',      '/admin/entities/sources',   true],
  ['04-review',       '/admin/entities/review',    true],
  ['05-conflicts',    '/admin/entities/conflicts', true],
  ['06-whitelist',    '/admin/entities/whitelist', true],
  ['07-entity-detail','/admin/entities/'+ENTITY_ID, true],
  ['08-candidates',   '/admin/kdb/candidates',     true],
  ['09-codex-runs',   '/admin/kdb/codex-runs',     true],
  ['10-observations', '/admin/kdb/observations',   true],
  ['11-persons',      '/admin/persons',            true],
  ['12-hermes',       '/admin/hermes',             true],
  ['13-settings',     '/admin/settings',           true],
];

(async () => {
  fs.mkdirSync(OUT, { recursive: true });
  const token = mintSession();
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  await ctx.addCookies([{ name: 'kdb_admin_session', value: token, domain: '127.0.0.1', path: '/', httpOnly: true, secure: false, sameSite: 'Lax' }]);
  const report = [];
  for (const [name, path, auth] of PAGES) {
    const page = await ctx.newPage();
    const consoleErrs = [];
    page.on('console', m => { if (m.type() === 'error') consoleErrs.push(m.text()); });
    page.on('pageerror', e => consoleErrs.push('pageerror: ' + e.message));
    let status = 0, title = '', finalURL = '', bodyLen = 0, h1 = '', tables = 0, rows = 0, err = '';
    try {
      const resp = await page.goto(BASE + path, { waitUntil: 'networkidle', timeout: 20000 });
      status = resp ? resp.status() : 0;
      finalURL = page.url();
      title = await page.title();
      const info = await page.evaluate(() => ({
        bodyLen: document.body ? document.body.innerText.length : 0,
        h1: (document.querySelector('h1,h2.page,h2') || {}).innerText || '',
        tables: document.querySelectorAll('table').length,
        rows: document.querySelectorAll('table tbody tr').length,
        emptyMarks: Array.from(document.querySelectorAll('.muted')).filter(e=>/없|empty|no /.test(e.innerText)).length,
      }));
      bodyLen = info.bodyLen; h1 = info.h1; tables = info.tables; rows = info.rows;
      await page.screenshot({ path: `${OUT}/${name}.png`, fullPage: true });
    } catch (e) { err = e.message; }
    report.push({ name, path, status, finalURL: finalURL.replace(BASE,''), title, h1: (h1||'').slice(0,40), tables, rows, bodyLen, consoleErrs, err });
    await page.close();
  }
  // 로그인 페이지는 비인증 컨텍스트로 별도 캡처(쿠키 있으면 /admin 으로 리다이렉트됨).
  const anonCtx = await browser.newContext({ viewport: { width: 1440, height: 900 } });
  const lp = await anonCtx.newPage();
  try { await lp.goto(BASE + '/admin/login', { waitUntil: 'networkidle', timeout: 15000 });
        await lp.screenshot({ path: `${OUT}/00-login-anon.png`, fullPage: true }); } catch (e) {}
  await anonCtx.close();
  await browser.close();
  fs.writeFileSync(`${OUT}/report.json`, JSON.stringify(report, null, 2));
  console.log(JSON.stringify(report, null, 2));
})().catch(e => { console.error('FATAL', e); process.exit(1); });
