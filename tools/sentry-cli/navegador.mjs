import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { extname, join } from 'node:path';
import { chromium } from 'playwright';

// Sirve <dist> como https://app.example.com, pulsa el botón y reenvía a
// <intake> lo que el SDK manda a errors.example.com, guardando cada envío en
// <salida> como tools/fixtures/navegador/grabar.mjs.
const [dist, intake, out] = process.argv.slice(2);
if (!dist || !intake || !out) {
  console.error('uso: node navegador.mjs <dist> <url de mothlog> <salida>');
  process.exit(1);
}
mkdirSync(out, { recursive: true });

const app = 'app.example.com';
const errors = 'errors.example.com';
const types = { '.js': 'text/javascript', '.html': 'text/html', '.map': 'application/json' };
let n = readdirSync(out).filter((f) => f.endsWith('.meta.json')).length;

const browser = await chromium.launch();
const context = await browser.newContext({ serviceWorkers: 'block', locale: 'es-ES', timezoneId: 'Europe/Madrid' });
await context.route('**/*', async (route) => {
  const req = route.request();
  const url = new URL(req.url());
  if (url.hostname === app) {
    const file = join(dist, url.pathname === '/' ? 'index.html' : decodeURIComponent(url.pathname));
    return route.fulfill({ body: readFileSync(file), contentType: types[extname(file)] ?? 'application/octet-stream' });
  }
  if (url.hostname === errors && req.method() === 'POST') {
    n += 1;
    const num = n;
    const base = join(out, String(num).padStart(3, '0'));
    const body = req.postDataBuffer() ?? Buffer.alloc(0);
    const headers = await req.allHeaders();
    writeFileSync(`${base}.envelope`, body);
    writeFileSync(`${base}.meta.json`, JSON.stringify({ method: 'POST', path: url.pathname, query: url.search.slice(1), headers }, null, 2));
    const res = await fetch(`${intake}${url.pathname}${url.search}`, {
      method: 'POST',
      body,
      headers: { 'content-type': headers['content-type'] ?? 'text/plain' },
    });
    console.log('envío', num, '->', res.status, await res.text());
    return route.fulfill({ status: res.status, body: '{}', contentType: 'application/json', headers: { 'access-control-allow-origin': '*' } });
  }
  if (url.hostname === errors) {
    return route.fulfill({ status: 200, headers: { 'access-control-allow-origin': '*', 'access-control-allow-headers': '*' } });
  }
  return route.abort();
});

const page = await context.newPage();
page.on('pageerror', (e) => console.log('pageerror:', e.message));
await page.goto(`https://${app}/`, { waitUntil: 'load' });
await page.waitForFunction(() => !!window.__SENTRY__);
await page.waitForTimeout(1500);
await page.click('#enviar');
await page.waitForTimeout(3000);
await browser.close();
