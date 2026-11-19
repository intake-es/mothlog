import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { extname, join } from 'node:path';
import { chromium } from 'playwright';

const [out] = process.argv.slice(2);
if (!out) {
  console.error('uso: node grabar.mjs <salida>');
  process.exit(1);
}
mkdirSync(out, { recursive: true });

const app = 'app.example.com';
const errors = 'errors.example.com';
const types = { '.js': 'text/javascript', '.html': 'text/html', '.map': 'application/json' };
let n = readdirSync(out).filter((f) => f.endsWith('.meta.json')).length;
const blocked = new Set();

const browser = await chromium.launch();
const context = await browser.newContext({ serviceWorkers: 'block', locale: 'es-ES', timezoneId: 'Europe/Madrid' });
await context.route('**/*', async (route) => {
  const req = route.request();
  const url = new URL(req.url());
  if (url.hostname === app) {
    const file = join('dist', url.pathname === '/' ? 'index.html' : decodeURIComponent(url.pathname));
    return route.fulfill({ body: readFileSync(file), contentType: types[extname(file)] ?? 'application/octet-stream' });
  }
  if (url.hostname === errors) {
    if (req.method() === 'POST') {
      n += 1;
      const base = join(out, String(n).padStart(3, '0'));
      writeFileSync(`${base}.envelope`, req.postDataBuffer() ?? Buffer.alloc(0));
      const meta = { method: 'POST', path: url.pathname, query: url.search.slice(1), headers: await req.allHeaders() };
      writeFileSync(`${base}.meta.json`, JSON.stringify(meta, null, 2));
    }
    return route.fulfill({ status: 200, body: '{}', contentType: 'application/json', headers: { 'access-control-allow-origin': '*' } });
  }
  blocked.add(url.hostname);
  return route.abort();
});

const page = await context.newPage();
page.on('pageerror', (e) => console.log('pageerror:', e.message));
await page.goto(`https://${app}/`, { waitUntil: 'load' });
await page.waitForFunction(() => !!window.__SENTRY__);
await page.waitForTimeout(1500);
await page.click('#enviar');
await page.waitForTimeout(3000);
console.log('envíos:', n, '| bloqueados:', [...blocked].join(', ') || 'ninguno');
await browser.close();
