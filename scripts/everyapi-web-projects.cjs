// Browser regression fixtures cover the Sessions UI; real git and model tool calls are exercised by Go tests separately.
const assert = require('node:assert/strict');

module.exports = async function projects(browser, desktop, address) {
  const context = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, reducedMotion: 'reduce', serviceWorkers: 'block' });
  let selected = false, running = false, done = false, clones = 0, prs = 0;
  const prompt = 'Update the project documentation.';
  const repo = 'everyapi-ai/example';
  const result = { message: 'Updated documentation.', files: ['README.md'], diff: 'diff --git a/README.md b/README.md\n+Updated documentation.\n' };
  const errors = [];
  const requests = [];
  let page;
  try {
    await context.addCookies(await desktop.context().cookies());
    await context.route('**/*', async route => {
      const request = route.request(), url = new URL(request.url());
      if (url.pathname.startsWith('/api/github/')) requests.push(url.pathname);
      if (url.origin !== address) return route.abort();
      const json = (body, status = 200) => route.fulfill({ status, contentType: 'application/json', body: JSON.stringify(body) });
      switch (url.pathname) {
        case '/api/github/status': return json({ configured: true, managed: true, organization: 'everyapi-ai', callback_url: address + '/_github/callback' });
        case '/api/github/repos': return json({ repos: [{ full_name: repo, default_branch: 'main' }] });
        case '/api/github/workspace':
          if (request.method() === 'POST') { selected = true; clones++; }
          return json({ selected, repo, ref: 'main', files: ['README.md', 'src/index.js'], running, prompt: running || done ? prompt : '', result: done ? result : null });
        case '/api/github/file': return json({ path: url.searchParams.get('path'), content: '# Project documentation\n', truncated: false });
        case '/api/github/agent':
          if (request.method() === 'POST') { assert.equal(request.postDataJSON().prompt, prompt); running = true; return json({ running: true }, 202); }
          return json({ running: running && !done, result: done ? result : null, activity: ['正在分析 · 第 1 步', 'read_file README.md'] });
        case '/api/github/pull-request':
          prs++;
          return prs === 1 ? json({ error: 'Please retry the request.' }, 502) : json({ html_url: 'https://github.com/everyapi-ai/example/pull/1' });
        default: return route.continue();
      }
    });
    page = await context.newPage();
    page.setDefaultTimeout(10000);
    page.on('pageerror', err => errors.push(err.message));
    await page.goto(address + '/settings?view=sessions', { waitUntil: 'domcontentloaded' });
    await page.locator('.everyapi-choose-project').waitFor();
    await page.waitForFunction(() => document.querySelector('.everyapi-project-select')?.options.length === 2);
    assert.equal(await page.locator('.everyapi-project-select').inputValue(), '', 'first repository looked selected without cloning');
    assert.equal(await page.locator('.everyapi-github-config-edit').isVisible(), false, 'Vault credentials can be overwritten from a misleading form');
    assert.equal(await page.locator('.everyapi-chat-form .everyapi-chat-model').count(), 1, 'model picker is outside the composer');
    assert.equal(await page.locator('#nav [data-view=providers]').isVisible(), false, 'phone exposes the EveryAPI settings tab');
    await page.locator('.everyapi-workbench-tabs button[data-panel="project"]').tap();
    await page.locator('.everyapi-project-select').selectOption(repo);
    await page.waitForFunction(() => document.querySelector('.everyapi-project-status')?.textContent.includes('已准备好'));
    assert.equal(clones, 1, 'selecting the only repository did not clone it once');
    await page.locator('.everyapi-workbench-tabs button[data-panel="project"]').tap();
    await page.locator('.everyapi-file-search').fill('readme');
    assert.equal(await page.locator('.everyapi-workspace-file').count(), 1);
    await page.locator('.everyapi-workspace-file').tap();
    await page.locator('.everyapi-file-content').filter({ hasText: '# Project documentation' }).waitFor();
    await page.getByRole('button', { name: '关闭文件', exact: true }).tap();
    await page.locator('.everyapi-workbench-tabs button[data-panel="center"]').tap();
    await page.locator('.everyapi-chat-input').fill(prompt);
    await page.locator('.everyapi-chat-send').tap();
    await page.waitForFunction(() => document.querySelector('.everyapi-chat-input')?.disabled);
    await page.locator('.everyapi-task-activity summary').filter({ hasText: 'read_file README.md' }).waitFor();
    assert(await page.getByRole('button', { name: '停止任务', exact: true }).isVisible(), 'running task has no stop control');
    await page.reload();
    await page.locator('.everyapi-workbench-tabs button[data-panel="center"]').tap();
    await page.waitForFunction(() => document.querySelector('.everyapi-chat-input')?.disabled && document.querySelector('.everyapi-project-select')?.value === 'everyapi-ai/example');
    done = true;
    await page.locator('.everyapi-workbench-tabs button[data-panel="review"]').tap();
    await page.locator('.everyapi-project-diff').waitFor();
    await page.waitForFunction(() => !document.querySelector('.everyapi-chat-input').disabled);
    assert.equal(clones, 1, 'refresh cloned the repository and lost the workspace');
    assert.match(await page.locator('.everyapi-chat-messages').textContent(), /Updated documentation/);
    await page.locator('#nav [data-view=usage]').tap();
    await page.locator('#nav [data-view=sessions]').tap();
    await page.locator('.everyapi-workbench-tabs button[data-panel="review"]').tap();
    await page.locator('.everyapi-project-diff').waitFor();
    await page.locator('.everyapi-project-pr').tap();
    await page.waitForFunction(() => document.querySelector('.everyapi-project-status')?.textContent.includes('Please retry'));
    await page.locator('.everyapi-project-pr').tap();
    await page.locator('.everyapi-project-pr-link a[href="https://github.com/everyapi-ai/example/pull/1"]').waitFor();
    assert.equal(prs, 2, 'PR failure did not offer a working retry');
    const size = await page.evaluate(() => ({ width: document.documentElement.scrollWidth, viewport: innerWidth }));
    assert(size.width <= size.viewport + 1, 'project review overflows the phone horizontally');
    assert.equal(await page.locator('.everyapi-project-pr').isVisible(), false);
    if (process.env.WORKBENCH_SCREENSHOTS) await page.screenshot({ path: 'dist/workbench-phone-review.png', fullPage: true });
    await page.setViewportSize({ width: 1440, height: 960 });
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    for (const panel of ['project', 'center', 'review']) assert(await page.locator('.everyapi-workbench-' + panel).isVisible(), `desktop ${panel} panel is hidden`);
    const panels = await page.locator('.everyapi-workbench-grid > *').evaluateAll(nodes => nodes.map(node => { const r = node.getBoundingClientRect(); return { x: r.x, right: r.right, y: r.y }; }));
    assert(panels[0].right <= panels[1].x && panels[1].right <= panels[2].x && panels.every(panel => panel.y === panels[0].y), 'desktop workspace columns overlap or wrap');
    const composer = await page.locator('.everyapi-chat-form').evaluate(node => ({ bottom: node.getBoundingClientRect().bottom, viewport: innerHeight }));
    assert(composer.bottom <= composer.viewport, `desktop composer falls below viewport: ${JSON.stringify(composer)}`);
    if (process.env.WORKBENCH_SCREENSHOTS) await page.screenshot({ path: 'dist/workbench-desktop.png', fullPage: true });
    assert.deepEqual(errors, []);
    console.log('PASS: desktop workspace columns, phone initial task panel, file search/preview, Sessions selection, composer model, task polling/recovery, review and PR retry (API fixtures)');
  } catch (error) {
    console.error('Project fixture diagnostics:', { requests, errors, url: page?.url(), text: page ? (await page.locator('body').innerText()).slice(0, 1600) : '' });
    throw error;
  } finally { await context.close(); }
};
