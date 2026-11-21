import { mkdirSync, readdirSync, writeFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { join } from 'node:path';

// Reenvía a <destino> lo que recibe en 127.0.0.1:<puerto> y guarda cada
// petición en <salida>/NNN.body y NNN.meta.json (sin Authorization), con la
// respuesta.
const [puerto, destino, out] = process.argv.slice(2);
if (!puerto || !destino || !out) {
  console.error('uso: node proxy.mjs <puerto> <destino> <salida>');
  process.exit(1);
}

mkdirSync(out, { recursive: true });
createServer(async (req, res) => {
  const chunks = [];
  for await (const c of req) chunks.push(c);
  const body = Buffer.concat(chunks);
  const path = req.url;
  const n = readdirSync(out).filter((f) => f.endsWith('.meta.json')).length + 1;
  const base = join(out, String(n).padStart(3, '0'));

  const headers = { ...req.headers };
  for (const h of ['host', 'content-length', 'connection', 'te', 'transfer-encoding']) delete headers[h];
  const r = await fetch(destino + path, { method: req.method, headers, body: body.length ? body : undefined });
  const text = await r.text();

  delete headers.authorization;
  writeFileSync(`${base}.body`, body);
  writeFileSync(
    `${base}.meta.json`,
    `${JSON.stringify({ method: req.method, path, headers, status: r.status, response: text }, null, 2)}\n`,
  );
  res.writeHead(r.status, { 'content-type': r.headers.get('content-type') ?? 'application/json' });
  res.end(text);
}).listen(Number(puerto), '127.0.0.1');
