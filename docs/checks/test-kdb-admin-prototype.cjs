'use strict';
// Browser checks against a local design prototype only. Never loads admin/API URLs.
const assert = require('assert');
const path = require('path');
const { pathToFileURL } = require('url');
const { chromium } = require(process.argv[3] || 'playwright');
async function run() {
  const browser = await chromium.launch({ headless: true });
  try {
    for (const width of [390, 1440]) {
      const page = await browser.newPage({ viewport: { width, height: 1000 } });
      const errors = [], network = [];
      page.on('pageerror', error => errors.push(error.message));
      page.on('request', request => { if (/^https?:/.test(request.url())) network.push(request.url()); });
      await page.goto(pathToFileURL(path.resolve(process.argv[2])).href);
      assert.equal(await page.locator('.entity').count(), 5);
      assert.equal(await page.locator('#domain option').count(), 9);
      await page.locator('#query').fill('김민수');
      assert.equal(await page.locator('.entity').count(), 2);
      await page.locator('#compare').click();
      const ids = await page.locator('#comparison .uuid').allTextContents();
      assert.equal(ids.length, 2); assert.notEqual(ids[0], ids[1]);
      await page.locator('[data-close=comparebox]').click();
      await page.locator('.entity [data-id]').first().click();
      assert((await page.locator('#detailbody').innerText()).includes('일본어 준비로 세지 않음'));
      await page.locator('[data-close=detail]').click();
      await page.locator('#query').fill('');
      await page.locator('#role').selectOption('entertainer');
      assert.equal(await page.locator('.entity').count(), 3);
      await page.locator('#role').selectOption('actor');
      assert.equal(await page.locator('.entity').count(), 2);
      await page.locator('#role').selectOption('');
      await page.locator('#domain').selectOption('politics');
      assert.equal(await page.locator('.entity').count(), 0);
      assert((await page.locator('#rows').innerText()).includes('기능이 없는 뜻은 아닙니다'));
      await page.locator('#domain').selectOption('');
      await page.locator('#loadstate').selectOption('error');
      assert((await page.locator('[role=alert]').innerText()).includes('0건이 아닙니다'));
      await page.locator('#loadstate').selectOption('normal');
      await page.locator('#mode').selectOption('viewer');
      assert(await page.locator('#register').isDisabled());
      await page.locator('#mode').selectOption('reviewer');
      await page.locator('#register').click();
      assert.equal(await page.locator('[name=type] option').count(), 13);
      assert.equal(await page.locator('[name=roles] option').count(), 26);
      await page.locator('[name=ko]').fill('합성 신규 인물');
      await page.locator('[name=domains]').selectOption(['entertainment', 'culture']);
      await page.locator('[name=roles]').selectOption(['singer', 'actor']);
      await page.locator('[name=reason]').fill('합성 인물 원천 ID와 소속을 확인하는 입력 예시');
      await page.locator('[name=subtype]').selectOption('real');
      await page.locator('#newform [type=submit]').click();
      let preview = await page.locator('#result').innerText();
      assert(preview.includes('"database_write": false')); assert(preview.includes('"singer"') && preview.includes('"actor"'));
      await page.locator('[name=type]').selectOption('location');
      assert(await page.locator('#roleslabel').isHidden());
      assert.equal(await page.locator('[name=roles]').evaluate(e => Array.from(e.selectedOptions).length), 0);
      await page.locator('[name=subtype]').selectOption('restaurant');
      await page.locator('#newform [type=submit]').click();
      preview = await page.locator('#result').innerText(); assert(preview.includes('"roles": []'));
      await page.locator('[data-close=registration]').click();
      for (const view of ['overview', 'review', 'fill', 'settings', 'entities']) {
        await page.locator('[data-view=' + view + ']').click();
        assert.equal(await page.locator('[aria-current=page]').count(), 1);
      }
      const overflow = await page.evaluate(() => document.documentElement.scrollWidth > innerWidth);
      assert.equal(overflow, false, 'Horizontal page overflow at ' + width);
      assert.deepEqual(errors, []); assert.deepEqual(network, []);
      if (process.argv[4]) await page.screenshot({ path: path.join(process.argv[4], 'kdb-design-' + width + '.png'), fullPage: true });
      console.log(JSON.stringify({ width, result: 'PASS', entityIsolation: true, domains: 8, types: 13,
        forms: 'preview only', httpRequests: network.length, scope: 'prototype, not production API/security/UAT' }));
      await page.close();
    }
  } finally { await browser.close(); }
}
run().catch(error => { console.error(error); process.exitCode = 1; });
