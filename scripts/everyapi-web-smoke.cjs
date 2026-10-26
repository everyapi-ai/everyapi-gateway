// Exercise the built distribution and its real browser UI without external services.
const assert = require('node:assert/strict');
const http = require('node:http');
const net = require('node:net');
const fs = require('node:fs/promises');
const os = require('node:os');
const path = require('node:path');
const crypto = require('node:crypto');
const { spawn, execFile } = require('node:child_process');
const { once } = require('node:events');
const { promisify } = require('node:util');
const { chromium, webkit } = require('playwright');

async function freePort() {
  const server = net.createServer();
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  const port = server.address().port;
  await new Promise(resolve => server.close(resolve));
  return port;
}

async function stopGateway(child) {
  if (!child || child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, 'exit');
  const signal = name => {
    try { process.platform !== 'win32' ? process.kill(-child.pid, name) : child.kill(name); }
    catch (err) { if (err.code !== 'ESRCH') throw err; }
  };
  let timer;
  signal('SIGTERM');
  try { await Promise.race([exited, new Promise(resolve => { timer = setTimeout(resolve, 10000); })]); }
  finally { clearTimeout(timer); }
  if (child.exitCode === null && child.signalCode === null) signal('SIGKILL');
  await exited;
}

async function main() {
  const data = await fs.mkdtemp(path.join(os.tmpdir(), 'everyapi-web-smoke-'));
  const admin = 'smoke-administrator-key-123456';
  const client = 'smoke-client-key-123456789';
  const upstreamKey = 'smoke-upstream-key-123456789';
  let child, browser;
  let output = '';
  const upstream = http.createServer((req, res) => {
    res.setHeader('Content-Type', 'application/json');
    if (req.headers.authorization !== `Bearer ${upstreamKey}` && req.headers['x-api-key'] !== upstreamKey && req.headers['x-goog-api-key'] !== upstreamKey) {
      res.writeHead(401).end('{"error":"fixture requires upstream credentials"}');
    } else if (req.url === '/v1/models') {
      res.end('{"object":"list","data":[{"id":"smoke-model","object":"model"}]}');
    } else if (req.url === '/v1beta/models') {
      res.end('{"models":[{"name":"models/smoke-model","supportedGenerationMethods":["generateContent"]}]}');
    } else if (req.url === '/v1/chat/completions') {
      res.end('{"id":"chat-smoke","object":"chat.completion","model":"smoke-model","choices":[{"index":0,"message":{"role":"assistant","content":"smoke passed"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}');
    } else {
      res.writeHead(404).end('{}');
    }
  });
  // The binary's catalog and news requests must not leave this fixture network.
  upstream.on('connect', (_req, socket) => socket.end('HTTP/1.1 502 Bad Gateway\r\n\r\n'));
  try {
    upstream.listen(0, '127.0.0.1');
    await once(upstream, 'listening');
    const base = `http://127.0.0.1:${upstream.address().port}`;
    const internalPort = await freePort();
    const webPort = await freePort();
    const start = async () => {
      output = '';
      child = spawn(path.resolve(process.argv[2] || 'dist/everyapi-web'), ['--data-dir', data, '--addr', `127.0.0.1:${webPort}`], {
        env: {
          ...process.env,
          EVERYAPI_DATA_DIR: data,
          EVERYAPI_API_BASE: base,
          EVERYAPI_API_KEY: '',
          EVERYAPI_API_KEY_FILE: '',
          EVERYAPI_WEB_KEY: admin,
          EVERYAPI_WEB_KEY_FILE: '',
          EVERYAPI_GATEWAY_KEY: client,
          EVERYAPI_GATEWAY_KEY_FILE: '',
          EVERYAPI_INTERNAL_ADDR: `127.0.0.1:${internalPort}`,
          EVERYAPI_PUBLIC_URL: '',
          EVERYAPI_REGION: 'global',
          HTTP_PROXY: base, HTTPS_PROXY: base, ALL_PROXY: base,
          http_proxy: base, https_proxy: base, all_proxy: base,
          NO_PROXY: process.env.REAL_GITHUB_PERSISTENCE ? '127.0.0.1,localhost,api.github.com' : '127.0.0.1,localhost',
          no_proxy: process.env.REAL_GITHUB_PERSISTENCE ? '127.0.0.1,localhost,api.github.com' : '127.0.0.1,localhost',
        },
        detached: process.platform !== 'win32',
        stdio: ['ignore', 'pipe', 'pipe'],
      });
      return new Promise((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error('gateway startup timed out')), 20000);
        const read = chunk => {
          output += chunk.toString();
          const match = output.match(/EveryAPI Gateway on (http:\/\/127\.0\.0\.1:\d+)/);
          if (match) { clearTimeout(timer); resolve(match[1]); }
        };
        child.stdout.on('data', read);
        child.stderr.on('data', read);
        child.once('error', err => { clearTimeout(timer); reject(err); });
        child.once('exit', code => { clearTimeout(timer); reject(new Error(`gateway exited ${code}: ${output}`)); });
      });
    };
    const address = await start();
    assert(!output.includes(admin), 'startup log leaked management password');
    assert(!output.includes(client), 'startup log leaked client key');
    assert(!output.includes(upstreamKey), 'startup log leaked upstream key');
    await promisify(execFile)(path.resolve(process.argv[2] || 'dist/everyapi-web'), ['--addr', `127.0.0.1:${webPort}`, '--healthcheck'], { timeout: 5000 });
    const noKey = await fetch(address + '/v1/models');
    assert.equal(noKey.status, 401, 'loopback client bypassed authentication');
    const engine = process.env.BROWSER === 'webkit' ? webkit : chromium;
    browser = await engine.launch({ headless: true, ...(process.env.PLAYWRIGHT_EXECUTABLE_PATH ? { executablePath: process.env.PLAYWRIGHT_EXECUTABLE_PATH } : {}) });
    const page = await browser.newPage({ viewport: { width: 1440, height: 960 }, locale: 'en-US', reducedMotion: 'reduce' });
    const pageErrors = [];
    page.on('pageerror', err => pageErrors.push(err.message));
    await page.route('**/*', route => new URL(route.request().url()).origin === address ? route.continue() : route.abort());
    await page.goto(address);
    await page.locator('input[name=key]').fill(admin);
    await page.locator('button[type=submit]').click();
    await page.waitForFunction(() => document.body?.classList.contains('gateway-mode') && document.querySelector('.everyapi-chat-form'));
    assert.equal(await page.title(), 'EveryAPI Gateway');
    assert.equal(await page.locator('.brand').innerText(), 'EveryAPI Gateway');
    assert.equal(await page.locator('[data-view=agents]').isVisible(), false, 'server showed local-agent controls');
    const state = await page.evaluate(() => fetch('/api/settings').then(r => r.json()));
    assert.equal(state.gatewayOn, true);
    assert.equal(state.noAccountDiscovery, true);
    assert.equal(state.noAutoUpdate, true);
    // Configure the single EveryAPI connection through its real UI, from a blank-key install.
    await page.locator('#nav [data-view=providers]').click();
    await page.locator('#everyapiAPIKey').fill(upstreamKey);
    const fetched = page.waitForResponse(r => new URL(r.url()).pathname === '/api/everyapi/models');
    await page.locator('#everyapiConfig .everyapi-actions button[type=button]').click();
    assert.equal((await fetched).status(), 200);
    await page.locator('#everyapiConfig input[name=models]').waitFor();
    const providers = await page.evaluate(() => fetch('/api/providers').then(r => r.json()));
    assert.deepEqual(providers.providers.map(p => p.id), ['everyapi']);
    assert.deepEqual(providers.presets.map(p => p.id), ['everyapi']);
    assert.equal(providers.gateway.url, address);
    assert.equal(providers.gateway.requireClientKey, true);
    assert.equal(await page.locator('#addProvider').isVisible(), false);
    assert.equal(await page.locator('[data-view=plugins]').isVisible(), false);
    assert.equal(await page.locator('[data-view=routing]').isVisible(), false);
    assert.equal(await page.evaluate(async () => (await fetch('/api/provider/save', { method: 'POST', body: JSON.stringify({ id: 'other', preset: 'openai', key: 'no' }) })).status), 404);
    const listing = await fetch(address + '/v1/models', { headers: { Authorization: `Bearer ${client}` } });
    assert.equal(listing.status, 200);
    assert((await listing.json()).data.some(m => m.id === 'everyapi/smoke-model'), 'discovered model missing from client API');
    await page.evaluate(async () => {
      const settings = await fetch('/api/settings').then(r => r.json());
      const saved = await fetch('/api/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...settings, redact: true }) });
      if (!saved.ok) throw new Error('masking configuration failed');
    });
    const relay = await fetch(address + '/v1/chat/completions', {
      method: 'POST',
      headers: { Authorization: `Bearer ${client}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: 'everyapi/smoke-model', messages: [{ role: 'user', content: 'hello' }] }),
    });
    assert.equal(relay.status, 200);
    assert.equal((await relay.json()).choices[0].message.content, 'smoke passed');
    await require('./everyapi-web-projects.cjs')(browser, page, address);
    await page.locator('[data-view=gateway]').click();
    await page.locator('#view-gateway').waitFor({ state: 'visible' });
    await page.waitForFunction(key => document.querySelector('#connect .snip')?.textContent.includes(key), client);
    const snippet = await page.locator('#connect .snip').textContent();
    assert.equal(await page.locator('#connectAddress').count(), 0, 'public URL was offered twice');
    assert(!snippet.includes('OPENAI_API_KEY=magpie'), 'UI offered an invalid arbitrary client key');
    assert(snippet.includes(address), 'UI example used the private internal listener');
    await page.waitForFunction(() => document.querySelector('#gwModels')?.textContent.includes('smoke-model'));
    await page.evaluate(() => document.fonts.ready);
    if (process.argv[3]) await page.screenshot({ path: path.resolve(process.argv[3]), fullPage: true });
    assert.deepEqual(pageErrors, [], 'browser threw JavaScript errors');
    for (const [lang, button, wrong, stale, install] of [
      ['zh', '登录', '管理密码不正确', '登录已失效', '安装应用'],
      ['en', 'Sign in', 'Incorrect management password', 'Your sign-in has expired', 'Install app'],
      ['zh-TW', '登入', '管理密碼不正確', '登入已失效', '安裝應用程式'],
      ['ja', 'ログイン', '管理パスワードが正しくありません', 'ログインの有効期限が切れました', 'アプリをインストール'],
      ['de', 'Anmelden', 'Falsches Verwaltungspasswort', 'Ihre Anmeldung ist abgelaufen', 'App installieren'],
    ]) {
      await page.evaluate(async lang => {
        const settings = await fetch('/api/settings').then(r => r.json());
        const saved = await fetch('/api/settings', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...settings, lang }) });
        if (!saved.ok) throw new Error('language save failed');
      }, lang);
      const context = await browser.newContext({ locale: 'en-US' });
      try {
        const login = await context.newPage();
        await login.route('**/*', route => new URL(route.request().url()).origin === address ? route.continue() : route.abort());
        assert.equal((await login.goto(address)).status(), 401);
        assert.equal(await login.locator('html').getAttribute('lang'), lang);
        assert.equal(await login.getByLabel(lang === 'zh' ? '管理密码' : lang === 'zh-TW' ? '管理密碼' : lang === 'ja' ? '管理パスワード' : lang === 'de' ? 'Verwaltungspasswort' : 'Management password', { exact: true }).count(), 1);
        assert(!(await login.locator('body').innerText()).includes('per host, not per port'));
        await login.locator('input[name=key]').fill('wrong-administrator-key');
        await login.getByRole('button', { name: button, exact: true }).click();
        await login.getByRole('status').filter({ hasText: wrong }).waitFor();
        assert.equal((await context.cookies()).length, 0);
        assert.equal((await login.goto(address + '/?k=invalid-link-key')).status(), 401);
        await login.getByRole('status').filter({ hasText: wrong }).waitFor();
        assert.equal(await login.locator('input[name=back]').inputValue(), '/', 'invalid link credential survived in return URL');
        await login.locator('input[name=key]').fill(admin);
        await login.getByRole('button', { name: button, exact: true }).click();
        await login.waitForFunction(() => document.body?.classList.contains('gateway-mode'));
        await login.getByRole('button', { name: install, exact: true }).click();
        await login.getByRole('heading', { name: install, exact: true }).waitFor();
        await login.locator('dialog.everyapi-install button').click();
        assert.equal((await login.reload()).status(), 200, 'refresh lost authenticated browser session');
        const cookies = await context.cookies();
        const session = cookies.find(c => c.value === admin);
        assert(session && session.httpOnly && session.expires > Date.now() / 1000, 'sign-in cookie must persist');
        await context.addCookies([{ ...session, value: 'old-administrator-key' }]);
        assert.equal((await login.goto(address)).status(), 401);
        await login.getByRole('status').filter({ hasText: stale }).waitFor();
      } finally {
        await context.close();
      }
    }
    console.log('PASS: localized sign-in in five languages, incorrect/stale credentials, persistent browser session after reload');
    if (process.env.MOBILE_PWA) await require('./everyapi-web-mobile.cjs')(browser, address, admin, client, upstreamKey, page);
    // Seed a private grant as an already-completed authorization. OAuth consent
    // is covered separately; this checks the production binary and disk format.
    const id = crypto.randomBytes(24).toString('hex');
    const token = process.env.REAL_GITHUB_PERSISTENCE ? (await promisify(execFile)('gh', ['auth', 'token'], { timeout: 10000 })).stdout.trim() : 'persisted-fixture-token';
    const checkout = path.join(data, 'github-workspaces', 'persistence-project');
    await fs.mkdir(checkout, { recursive: true, mode: 0o700 });
    await fs.writeFile(path.join(checkout, 'README.md'), '# Retained project after restart\n');
    await fs.writeFile(path.join(data, 'github-oauth.json'), JSON.stringify({ client_id: 'persistence-client', client_secret: 'fixture-secret', organization: 'everyapi-ai' }), { mode: 0o600 });
    const nonce = crypto.randomBytes(12);
    const key = crypto.createHash('sha256').update('everyapi-github-session-key-v1:' + id).digest();
    const cipher = crypto.createCipheriv('aes-256-gcm', key, nonce);
    cipher.setAAD(Buffer.from('everyapi-github-session-v1:persistence-client:everyapi-ai'));
    const grant = JSON.stringify({ Token: token, Username: 'persistence-user', Expires: new Date(Date.now() + 400 * 86400000).toISOString(), Workspace: { Repo: 'everyapi-ai/everyapi-gateway', Ref: 'main', Path: checkout, LastPrompt: 'Retain this project.' } });
    const encrypted = Buffer.concat([nonce, cipher.update(grant), cipher.final(), cipher.getAuthTag()]);
    const grants = path.join(data, 'github-sessions');
    await fs.mkdir(grants, { mode: 0o700 });
    await fs.writeFile(path.join(grants, crypto.createHash('sha256').update(id).digest('hex') + '.session'), encrypted, { mode: 0o600 });
    const phone = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, serviceWorkers: 'block' });
    try {
      await phone.addCookies(await page.context().cookies());
      await phone.addCookies([{ name: 'magpie_github_session', value: id, url: address, httpOnly: true, sameSite: 'Lax', expires: Date.now() / 1000 + 400 * 86400 }]);
      const readWorkspace = async () => {
        const response = await phone.request.get(address + '/api/github/workspace');
        assert.equal(response.status(), 200, 'stored grant requires authorization again');
        const workspace = await response.json();
        assert.equal(workspace.repo, 'everyapi-ai/everyapi-gateway');
        assert.equal(workspace.prompt, 'Retain this project.');
        if (process.env.REAL_GITHUB_PERSISTENCE) {
          const repos = await phone.request.get(address + '/api/github/repos');
          assert.equal(repos.status(), 200, 'persisted real GitHub grant stopped working');
          assert((await repos.json()).repos.some(repo => repo.full_name === 'everyapi-ai/everyapi-gateway'));
        }
      };
      await readWorkspace();
      await stopGateway(child);
      assert.equal(await start(), address, 'restart changed origin');
      await readWorkspace();
      const reopened = await phone.newPage();
      await reopened.route('**/*', route => new URL(route.request().url()).origin === address ? route.continue() : route.abort());
      await reopened.goto(address + '/settings?view=sessions');
      await reopened.locator('.everyapi-file-content').filter({ hasText: '# Retained project after restart' }).waitFor();
      await reopened.reload();
      await reopened.locator('.everyapi-file-content').filter({ hasText: '# Retained project after restart' }).waitFor();
      assert.equal(await reopened.locator('.everyapi-project-auth').isVisible(), false);
      assert.equal((await phone.request.post(address + '/api/github/disconnect', { headers: { Origin: address } })).status(), 200);
      assert.equal((await phone.request.get(address + '/api/github/workspace')).status(), 401);
      console.log(`PASS: production process restart, same browser cookie, retained project, iPhone viewport reload and explicit disconnect (${process.env.REAL_GITHUB_PERSISTENCE ? 'real GitHub API grant; preauthorized, no OAuth consent' : 'fixture grant'})`);
    } finally { await phone.close(); }
    console.log('PASS: blank-key startup, sign-in, Web provider configuration, EveryAPI branding, isolated gateway mode, model discovery, authenticated relay, usable connection examples, credential-safe startup logs');
  } finally {
    if (browser) await browser.close();
    await stopGateway(child);
    await new Promise(resolve => upstream.close(resolve));
    await fs.rm(data, { recursive: true, force: true, maxRetries: 5, retryDelay: 100 });
  }
}

main().catch(err => { console.error(err); process.exitCode = 1; });
