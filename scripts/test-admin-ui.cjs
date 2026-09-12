// Run with Playwright 1.48 and its matching browser image against generated,
// synthetic fixtures only. No production login/session or database access.
const { chromium } = require('playwright');
const http = require('http');
const fs = require('fs');
const path = require('path');
const assert = require('assert/strict');
const dir = process.env.KDB_ADMIN_PREVIEW_DIR;
if (!dir) throw new Error('KDB_ADMIN_PREVIEW_DIR required');
const allowed = new Set(['dashboard-populated.html', 'dashboard-empty.html', 'dashboard-error.html', 'workflow.html', ...['list','empty','error','detail','viewer'].map(s => 'preparations-' + s + '.html')]);
for (const state of ['list','detail','error','viewer','empty']) allowed.add('kentity-'+state+'.html');
for (const state of ['list','detail','error','viewer','empty']) allowed.add('mappings-'+state+'.html');
const server = http.createServer((req, res) => {
  const filename = new URL(req.url, 'http://localhost').pathname.slice(1);
  if (!allowed.has(filename)) { res.writeHead(404); res.end('fixture only'); return; }
  res.setHeader('Content-Type', 'text/html; charset=utf-8');
  res.end(fs.readFileSync(path.join(dir, filename)));
});
(async () => {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
  const origin = 'http://127.0.0.1:' + server.address().port;
  const browser = await chromium.launch({headless: true, args: ['--no-sandbox']});
  try {
    for (const width of [390, 1440]) {
      const page = await browser.newPage({viewport: {width, height: 1000}});
      const errors = [];
      page.on('pageerror', e => errors.push(e.message));
      for (const fixture of allowed) {
        await page.goto(origin + '/' + fixture, {waitUntil: 'networkidle'});
        await page.waitForFunction(() => getComputedStyle(document.body).backgroundColor === 'rgb(241, 245, 249)');
        assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true, 'page overflow');
        assert.equal(await page.locator('nav [aria-current="page"]').count(), 1, 'current menu');
        if (width === 390) {
          assert.equal(await page.locator('#admin-navigation').isVisible(), false);
          await page.locator('#nav-toggle').click();
          assert.equal(await page.locator('#admin-navigation').isVisible(), true);
          await page.locator('nav a').first().focus();
          await page.keyboard.press('Escape');
          assert.equal(await page.locator('#admin-navigation').isVisible(), false);
          assert.equal(await page.locator('#nav-toggle').evaluate(el => el === document.activeElement), true);
        } else {
          assert.equal(await page.locator('#admin-navigation').isVisible(), true);
          assert.equal(await page.locator('#nav-toggle').isVisible(), false);
        }
        if (fixture === 'dashboard-error.html') {
          assert.equal(await page.getByRole('alert').count(), 2);
          assert.equal(await page.locator('#overview-title').count(), 0);
        }
        if (fixture === 'workflow.html') {
          assert.equal(await page.locator('#workflow-auto-refresh').isChecked(), false);
          await page.locator('#workflow-auto-refresh').check();
          await page.reload({waitUntil: 'networkidle'});
          assert.equal(await page.locator('#workflow-auto-refresh').isChecked(), true);
          await page.locator('#workflow-auto-refresh').uncheck();
        }
        if (fixture === 'preparations-error.html') assert.equal(await page.getByRole('alert').count(), 1);
        if (fixture === 'kentity-error.html') assert.equal(await page.getByRole('alert').count(), 1);
        if (fixture === 'mappings-error.html') assert.equal(await page.getByRole('alert').count(), 1);
        if (fixture === 'mappings-viewer.html') assert.equal(await page.getByRole('button',{name:'이 UUID로 연결 승인 기록'}).count(),0);
        if (fixture === 'mappings-detail.html') {
          await page.getByText('이 UUID와의 연결 검수 기록',{exact:true}).click();
          await page.getByLabel('확인한 근거 URL (HTTPS)').fill('https://example.test/identity');
          await page.getByLabel('이름 외에 확인한 동일인 사실 (20자 이상)').fill('독립 식별자와 소속 및 활동 이력을 비교한 합성 검수 예시입니다.');
          await page.getByLabel('검수 결정 사유',{exact:true}).fill('합성 브라우저 연결 승인 검수 테스트');
          await page.getByRole('checkbox').check();
          await page.route('**/admin/kentity/mappings/*',async route=>{
            const body=new URLSearchParams(route.request().postData());
            assert.equal(body.get('revision'),'1');
            assert.equal(body.get('fingerprint'),'synthetic-source');
            assert.equal(body.get('entity_fingerprint'),'synthetic-target');
            assert.equal(body.get('decision'),'confirmed');
            assert.equal(body.get('attested'),'yes');
            assert.equal(body.get('_csrf'),'synthetic-fixture-only');
            await route.fulfill({body:'synthetic mapping decision intercepted; no mutation'});
          });
          await page.getByRole('button',{name:'이 UUID로 연결 승인 기록'}).click();
          await page.waitForURL('**/admin/kentity/mappings/*');
          await page.goto(origin+'/'+fixture,{waitUntil:'networkidle'});
        }
        if (fixture === 'kentity-viewer.html') assert.equal(await page.getByRole('button', {name: '미검증 후보로 등록'}).count(), 0);
        if (fixture === 'kentity-list.html') {
          await page.getByText('신규 분야 후보 등록',{exact:true}).click();
          assert.equal(await page.getByRole('button',{name:'미검증 후보로 등록'}).isVisible(),true);
          assert.equal(await page.getByLabel('한국어 이름').isVisible(),true);
        }
        if (fixture === 'preparations-viewer.html') assert.equal(await page.getByRole('button', {name: '제한 재시도 요청'}).count(), 0);
        if (fixture === 'preparations-detail.html') {
          assert.equal(await page.getByRole('button', {name: '제한 재시도 요청'}).count(), 2);
          assert.equal(await page.getByText('준비 완료에 포함하지 않음', {exact: true}).count(), 2);
          await page.route('**/admin/preparations/*/retry', async route => {
            const body = new URLSearchParams(route.request().postData());
            assert.equal(body.get('revision'), '2');
            assert.equal(body.get('locale'), 'ja');
            assert.equal(body.get('reason'), '테스트 복구 사유');
            assert.equal(body.get('_csrf'), 'synthetic-fixture-only');
            await route.fulfill({body: 'synthetic retry intercepted; no mutation'});
          });
          await page.getByLabel('재시도 사유').first().fill('테스트 복구 사유');
          await page.getByRole('button', {name: '제한 재시도 요청'}).first().click();
          await page.waitForURL('**/admin/preparations/*/retry');
          await page.goto(origin + '/' + fixture, {waitUntil: 'networkidle'});
        }
        await page.screenshot({path: path.join(dir, fixture.replace('.html', '-' + width + '.png')), fullPage: true});
      }
      await page.goto(origin + '/dashboard-populated.html', {waitUntil: 'networkidle'});
      await page.route('**/admin/entities?*', route => route.fulfill({body: 'search route intercepted'}));
      await page.getByLabel('고유명사 검색', {exact: true}).fill('동명이인 테스트');
      await page.getByRole('button', {name: '검색', exact: true}).click();
      await page.waitForURL('**/admin/entities?*');
      assert.equal(new URL(page.url()).searchParams.get('q'), '동명이인 테스트');
      assert.deepEqual(errors, [], 'browser runtime errors');
      console.log('PASS ' + width + 'px: dashboard/workflow/readiness/common/mapping states, roles, navigation, search, protected forms, refresh');
      await page.close();
    }
  } finally {
    await browser.close();
    server.close();
  }
})().catch(err => { console.error(err); server.close(); process.exitCode = 1; });
