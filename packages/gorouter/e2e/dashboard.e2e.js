// End-to-end dashboard test: real gorouter binary, real browser, fake upstream + fake IdP.
// Usage: BROWSER=/path/to/chrome-or-brave node dashboard.e2e.js
import http from 'node:http';
import { spawn, execFileSync } from 'node:child_process';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import puppeteer from 'puppeteer-core';

const BROWSER = process.env.BROWSER || '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser';
const KEY = 'e2e-key';
const root = path.resolve(import.meta.dirname, '..');
const tmp = mkdtempSync(path.join(tmpdir(), 'gr-e2e-'));

const listen = (handler) => new Promise((res) => { const s = http.createServer(handler); s.listen(0, '127.0.0.1', () => res(s)); });
const port = (s) => s.address().port;

// ---- fakes ----
const seenAuth = [];
const upstream = await listen((req, res) => {
  seenAuth.push(req.headers.authorization);
  req.resume().on('end', () => { res.setHeader('content-type', 'application/json');
    res.end(JSON.stringify({ id: '1', model: 'm', choices: [{ message: { role: 'assistant', content: 'pong from upstream' }, finish_reason: 'stop' }], usage: { prompt_tokens: 3, completion_tokens: 2 } })); });
});
let polls = 0;
const idp = await listen((req, res) => {
  let body = ''; req.on('data', (c) => body += c).on('end', () => {
    res.setHeader('content-type', 'application/json');
    if (req.url === '/device') return res.end(JSON.stringify({ device_code: 'dc', user_code: 'WXYZ-9999', verification_uri: 'https://idp.example/activate', expires_in: 120, interval: 1 }));
    if (req.url === '/token' && body.includes('device_code')) return res.end(JSON.stringify(++polls < 2 ? { error: 'authorization_pending' } : { access_token: 'at-device', refresh_token: 'rt', expires_in: 3600 }));
    res.statusCode = 400; res.end('{}');
  });
});

// ---- gorouter ----
const cfg = {
  listen: ':0', api_keys: [KEY],
  providers: [
    { id: 'p1', type: 'openai', base_url: `http://127.0.0.1:${port(upstream)}`, api_key: 'static-key', models: ['m'] },
    { id: 'sub', type: 'openai', base_url: `http://127.0.0.1:${port(upstream)}`, models: ['m'],
      oauth: { auth_url: `http://127.0.0.1:${port(idp)}/authorize`, token_url: `http://127.0.0.1:${port(idp)}/token`, device_url: `http://127.0.0.1:${port(idp)}/device`, client_id: 'cid', scopes: ['a'] } },
  ],
};
// ':0' is not usable for the dashboard URL; pick a free port first.
const free = await listen(() => {}); const GR_PORT = port(free); await new Promise((r) => free.close(r));
cfg.listen = `127.0.0.1:${GR_PORT}`;
writeFileSync(path.join(tmp, 'c.json'), JSON.stringify(cfg));
execFileSync('go', ['build', '-o', path.join(tmp, 'gr'), './cmd/gorouter'], { cwd: root, stdio: 'inherit' });
const gr = spawn(path.join(tmp, 'gr'), ['-config', path.join(tmp, 'c.json'), '-db', path.join(tmp, 't.db')], { stdio: 'ignore' });
for (let i = 0; i < 50; i++) { try { if ((await fetch(`http://127.0.0.1:${GR_PORT}/health`)).ok) break; } catch {} await new Promise((r) => setTimeout(r, 100)); }

const browser = await puppeteer.launch({ executablePath: BROWSER, headless: 'new', args: ['--no-sandbox'] });
const results = [];
const step = async (name, fn) => { try { await fn(); results.push(['ok', name]); } catch (e) { results.push(['FAIL', name + ': ' + e.message]); } };
const consoleErrors = [];
try {
  const page = await browser.newPage();
  page.on('pageerror', (e) => consoleErrors.push(e.message));
  page.on('console', (m) => m.type() === 'error' && consoleErrors.push(m.text()));
  page.on('dialog', async (d) => { consoleErrors.push('dialog: ' + d.message()); await d.dismiss(); });
  const url = `http://127.0.0.1:${GR_PORT}/dashboard/`;
  const text = (sel) => page.$eval(sel, (e) => e.textContent);

  await step('enter API key and load overview', async () => {
    await page.goto(url);
    await page.type('#key', KEY);
    await page.$eval('#key', (e) => e.dispatchEvent(new Event('change')));
    await page.waitForFunction(() => document.body.innerText.includes('Usage by target'), { timeout: 5000 });
  });

  await step('wrong key shows an error card', async () => {
    const ctx = await browser.createBrowserContext(); const p2 = await ctx.newPage(); await p2.goto(url);
    await p2.waitForFunction(() => document.querySelector('.card.bad'), { timeout: 5000 });
    await ctx.close();
  });

  await step('Connect opens an authorization URL with PKCE', async () => {
    await page.goto(url + '#providers');
    await page.waitForSelector('button[data-c="sub"]');
    await page.evaluate(() => { window.open = (u) => { window.__opened = u; }; });
    await page.click('button[data-c="sub"]');
    await page.waitForFunction(() => window.__opened, { timeout: 5000 });
    const u = new URL(await page.evaluate(() => window.__opened));
    assert.equal(u.searchParams.get('code_challenge_method'), 'S256');
    assert.equal(u.searchParams.get('client_id'), 'cid');
    assert.ok(u.searchParams.get('state'));
  });

  await step('Device code login connects the account', async () => {
    await page.click('button[data-dev="sub"]');
    await page.waitForFunction(() => document.querySelector('#device')?.innerText.includes('WXYZ-9999'), { timeout: 5000 });
    await page.waitForFunction(() => document.querySelector('.pill.ok')?.textContent.startsWith('connected'), { timeout: 15000 });
  });

  await step('Playground routes through the OAuth account', async () => {
    await page.goto(url + '#playground');
    await page.waitForSelector('#model');
    await page.$eval('#model', (e) => (e.value = ''));
    await page.type('#model', 'sub/m');
    await page.click('#go');
    await page.waitForFunction(() => document.querySelector('#out')?.textContent.includes('pong from upstream'), { timeout: 5000 })
      .catch(async (e) => { throw new Error(e.message + ' | #out=' + (await text('#out'))); });
    assert.ok(seenAuth.includes('Bearer at-device'), 'upstream saw ' + JSON.stringify(seenAuth));
    assert.match(await text('#out'), /target: sub\/m/);
  });

  await step('Config save hot-reloads and new alias works', async () => {
    await page.goto(url + '#config');
    await page.waitForSelector('#cfg');
    await page.waitForFunction(() => document.querySelector('#cfg').value.includes('providers'));
    await page.$eval('#cfg', (e) => { const c = JSON.parse(e.value); c.aliases = { fast: 'p1/m' }; e.value = JSON.stringify(c); });
    await page.click('#save');
    await page.waitForFunction(() => document.querySelector('#msg')?.textContent.startsWith('Applied'), { timeout: 5000 });
    await page.goto(url + '#playground');
    await page.waitForSelector('#model');
    await page.$eval('#model', (e) => (e.value = ''));
    await page.type('#model', 'fast');
    await page.click('#go');
    await page.waitForFunction(() => document.querySelector('#out')?.textContent.includes('pong from upstream'), { timeout: 5000 });
    assert.ok(seenAuth.includes('Bearer static-key'));
  });

  await step('Config save rejects invalid JSON config', async () => {
    await page.goto(url + '#config');
    await page.waitForFunction(() => document.querySelector('#cfg')?.value.includes('providers'));
    await page.$eval('#cfg', (e) => { e.value = '{"providers":[{"id":"x","type":"nope"}]}'; });
    await page.click('#save');
    await page.waitForFunction(() => document.querySelector('#msg')?.classList.contains('bad'), { timeout: 5000 });
  });

  await step('Disconnect removes the connection', async () => {
    await page.goto(url + '#providers');
    await page.waitForSelector('button[data-d="sub"]');
    await page.click('button[data-d="sub"]');
    await page.waitForFunction(() => !document.querySelector('button[data-d="sub"]'), { timeout: 5000 });
  });

  await step('no unexpected console errors', async () => {
    const real = consoleErrors.filter((e) => !/401|Unauthorized|Failed to load resource/.test(e));
    assert.deepEqual(real, []);
  });
} finally {
  await Promise.race([browser.close(), new Promise((r) => setTimeout(r, 3000))]);
  gr.kill(); upstream.close(); idp.close();
}
for (const [s, n] of results) console.log(s === 'ok' ? '✓' : '✗', n);
process.exit(results.some(([s]) => s !== 'ok') ? 1 : 0);
