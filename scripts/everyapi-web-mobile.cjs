// Mobile and PWA acceptance against the same disposable server as everyapi-web-smoke.cjs.
const assert = require('node:assert/strict');
const http = require('node:http');
const { once } = require('node:events');

async function fits(page, selector = 'body') {
  await page.evaluate(async () => { await document.fonts.ready; await new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))); });
  const box = await page.locator(selector).evaluate(node => ({ width: node.scrollWidth, available: node.clientWidth, page: document.documentElement.scrollWidth, viewport: innerWidth,
    overflow: [...node.querySelectorAll('*')].filter(element => element.getBoundingClientRect().right > node.getBoundingClientRect().right + 1 || element.scrollWidth > element.clientWidth + 1).slice(0, 12).map(element => ({ node: element.tagName + '#' + element.id + '.' + element.className, width: element.getBoundingClientRect().width, right: element.getBoundingClientRect().right, scroll: element.scrollWidth, client: element.clientWidth })) }));
  assert(box.width <= box.available + 1 && box.page <= box.viewport + 1, `${selector} overflows horizontally: ${JSON.stringify(box)}`);
}

module.exports = async function mobile(browser, address, admin, client, upstreamKey, desktop) {
  // Interrupt a real local connection. WebKit's emulated offline mode can abort navigation before a controlling worker receives it.
  const backend = address;
  let connected = true;
  const proxy = http.createServer((request, response) => {
    if (!connected) { request.socket.destroy(); return; }
    const upstream = http.request(backend + request.url, { method: request.method, headers: request.headers }, reply => {
      response.writeHead(reply.statusCode, reply.headers);
      reply.pipe(response);
    });
    upstream.on('error', () => response.destroy());
    request.pipe(upstream);
  });
  proxy.listen(0, '127.0.0.1');
  await once(proxy, 'listening');
  address = `http://127.0.0.1:${proxy.address().port}`;
  try {
  const manifestResponse = await fetch(address + '/pwa/manifest.webmanifest');
  assert.equal(manifestResponse.status, 200, 'launcher cannot fetch the manifest without a cookie');
  const manifest = await manifestResponse.json();
  assert.equal(manifest.start_url, '/');
  assert.equal(manifest.display, 'standalone');
  assert(!JSON.stringify(manifest).includes(admin));
  for (const icon of manifest.icons) {
    const response = await fetch(address + icon.src);
    assert.equal(response.status, 200);
    const png = Buffer.from(await response.arrayBuffer());
    const size = Number(icon.sizes.split('x')[0]);
    assert.equal(png.readUInt32BE(16), size);
    assert.equal(png.readUInt32BE(20), size);
  }
  const cases = [[320, 740], [375, 812], [390, 844], [430, 932], [768, 1024], [844, 390]];
  for (const lang of ['zh', 'en']) {
    await desktop.evaluate(async lang => {
      const settings = await fetch('/api/settings').then(r => r.json());
      const response = await fetch('/api/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...settings, lang }) });
      if (!response.ok) throw new Error('Could not set mobile test language');
    }, lang);
    for (const [width, height] of cases) {
      const context = await browser.newContext({ viewport: { width, height }, isMobile: true, hasTouch: true, reducedMotion: 'reduce', locale: lang });
      let page;
      try {
        page = await context.newPage();
        page.setDefaultTimeout(10000);
        const errors = [];
        page.on('pageerror', error => errors.push(error.message));
        await page.route('**/*', route => new URL(route.request().url()).origin === address ? route.continue() : route.abort());
        await page.goto(address);
        await fits(page);
        const key = page.locator('input[name=key]');
        assert(await key.evaluate(node => parseFloat(getComputedStyle(node).fontSize) >= 16), 'phone sign-in would zoom its input');
        await key.fill(admin);
        await page.locator('button[type=submit]').tap();
        await page.locator('#pwaInstall').waitFor();
        await page.waitForFunction(() => document.body?.classList.contains('gateway-mode'));
        await fits(page);
        await page.locator('#pwaInstall').tap();
        await page.locator('dialog.everyapi-install').waitFor();
        await fits(page, 'dialog.everyapi-install');
        await page.locator('dialog.everyapi-install button').tap();
        await page.locator('dialog.everyapi-install').waitFor({ state: 'detached' });
        await page.locator('#nav [data-view=providers]').tap();
        await page.locator('#everyapiConfig').waitFor();
        await fits(page, '#everyapiConfig');
        const providerKey = page.locator('#everyapiAPIKey');
        await providerKey.fill(upstreamKey);
        assert(await providerKey.evaluate(node => parseFloat(getComputedStyle(node).fontSize) >= 16));
        // Magpie preserves reading position against scripted auto-scroll. A trusted scroll key reaches the actions before the touch tap, including mobile WebKit without mouse-wheel support.
        await page.locator('#view-providers').press('End');
        const [refreshed] = await Promise.all([page.waitForResponse(r => new URL(r.url()).pathname === '/api/everyapi/models', { timeout: 20000 }), page.getByRole('button', { name: lang === 'zh' ? '保存并刷新模型' : 'Save and refresh models', exact: true }).tap({ timeout: 20000 })]);
        assert.equal(refreshed.status(), 200);
        await page.locator('#everyapiConfig input[name=models]').waitFor();
        assert.equal(await page.locator('#addProvider').isVisible(), false);
        assert.equal(await page.locator('#nav [data-view=plugins]').isVisible(), false);
        assert.equal(await page.locator('#nav [data-view=routing]').isVisible(), false);
        for (const view of ['gateway', 'usage', 'sessions']) {
          await page.locator(`#nav [data-view=${view}]`).tap();
          await page.locator(`#view-${view}`).waitFor();
          await fits(page, `#view-${view}`);
        }
        await page.locator('#prefs').tap();
        await page.locator('#view-settings').waitFor();
        await fits(page, '#view-settings');
        assert.equal((await page.reload()).status(), 200, 'mobile refresh lost the sign-in');
        await page.locator('#pwaInstall').waitFor();
        await page.evaluate(async () => {
          await navigator.serviceWorker.ready;
          if (!navigator.serviceWorker.controller) await new Promise(resolve => navigator.serviceWorker.addEventListener('controllerchange', resolve, { once: true }));
        });
        // A real worker and Cache Storage, rather than a mocked offline response.
        const cache = await page.evaluate(async () => {
          const entries = [];
          for (const name of await caches.keys()) for (const request of await (await caches.open(name)).keys()) {
            const response = await (await caches.open(name)).match(request);
            entries.push({ path: new URL(request.url).pathname, body: await response.text() });
          }
          return entries;
        });
        assert(cache.length > 0 && cache.every(entry => entry.path === '/pwa/offline'));
        assert(!cache.some(entry => [admin, client, upstreamKey].some(secret => entry.body.includes(secret))), 'offline cache contains credentials');
        // Sign-in expiry must stay enforced with a controlling worker.
        await context.clearCookies();
        assert.equal((await page.goto(address)).status(), 401);
        await page.locator('input[name=key]').waitFor();
        connected = false;
        try { await page.goto(address + '/?view=gateway&k=never-cache-this-key'); }
        catch (error) { throw new Error(`Offline navigation failed: ${error.message}; page=${(await page.locator('body').innerText()).slice(0, 250)}; worker=${await page.evaluate(() => !!navigator.serviceWorker?.controller)}`); }
        await page.getByRole('heading', { name: lang === 'zh' ? '暂时无法连接网关' : 'Cannot reach the gateway', exact: true }).waitFor();
        assert.equal(await page.locator('a').getAttribute('href'), '/');
        await fits(page);
        assert(!(await page.locator('body').innerText()).includes('never-cache-this-key'));
        connected = true;
        await page.locator('a').tap();
        await page.locator('input[name=key]').waitFor();
        assert.deepEqual(errors, [], 'mobile browser threw JavaScript errors');
        console.log(`PASS: mobile ${lang} ${width}x${height}, sign-in, touch navigation, provider save/discovery, install help, restart-free offline/reconnect and cache isolation`);
      } catch (error) { if (page) { await page.screenshot({ path: `dist/everyapi-only-failure-${lang}-${width}.png` }); console.error("Mobile form diagnostics", await page.locator("#everyapiConfig").evaluate(form => ({ valid: form.checkValidity(), buttons: [...form.querySelectorAll("button")].map(button => ({ text: button.textContent, disabled: button.disabled, rect: button.getBoundingClientRect().toJSON() })) }))); } throw error; } finally { await context.close(); }
    }
  }
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, userAgent: 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1' });
  try {
    await context.addCookies(await desktop.context().cookies());
    const page = await context.newPage();
    await page.goto(address);
    await page.locator('#pwaInstall').tap();
    assert((await page.locator('dialog.everyapi-install p').innerText()).includes('In Safari, tap Share'));
    await page.locator('dialog.everyapi-install button').tap();
    assert(!(await page.locator('meta[name=viewport]').getAttribute('content')).includes('maximum-scale'), 'installed console prevents pinch zoom');
    await page.evaluate(() => {
      window.installPromptCalls = 0;
      const event = new Event('beforeinstallprompt', { cancelable: true });
      event.prompt = async () => { window.installPromptCalls++; };
      event.userChoice = Promise.resolve({ outcome: 'dismissed' });
      dispatchEvent(event);
    });
    await page.locator('#pwaInstall').tap();
    assert.equal(await page.evaluate(() => window.installPromptCalls), 1, 'installation button did not invoke the available browser prompt');
    await page.evaluate(() => dispatchEvent(new Event('appinstalled')));
    await page.locator('#pwaInstall').waitFor({ state: 'hidden' });
    await context.addInitScript(() => Object.defineProperty(navigator, 'standalone', { value: true }));
    await page.reload();
    await page.waitForFunction(() => document.querySelector('#pwaInstall')?.hidden);
    console.log('PASS: Safari instructions, pinch zoom, native prompt dispatch and installed-mode button visibility');
  } finally { await context.close(); }
  } finally {
    proxy.closeAllConnections();
    await new Promise(resolve => proxy.close(resolve));
  }
};
