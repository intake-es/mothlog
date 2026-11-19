import { readFileSync, rmSync, writeFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import * as esbuild from 'esbuild';

const [dsn, release = 'fixture-release-1'] = process.argv.slice(2);
if (!dsn) {
  console.error('uso: node build.mjs <dsn> [release]');
  process.exit(1);
}

const out = 'dist';
rmSync(out, { recursive: true, force: true });
const result = await esbuild.build({
  entryPoints: ['src/main.ts'],
  bundle: true,
  splitting: true,
  format: 'esm',
  target: 'es2022',
  minify: true,
  sourcemap: true,
  sourcesContent: true,
  entryNames: '[name]-[hash]',
  chunkNames: 'chunk-[hash]',
  outdir: out,
  metafile: true,
  define: { SENTRY_DSN: JSON.stringify(dsn), SENTRY_RELEASE: JSON.stringify(release) },
});

// esbuild escribe las fuentes relativas a dist/ (../src/carrito.ts); se dejan
// relativas a la raíz del proyecto, como las de un build de Angular.
for (const file of Object.keys(result.metafile.outputs).filter((f) => f.endsWith('.map'))) {
  const map = JSON.parse(readFileSync(file, 'utf8'));
  map.sources = map.sources.map((s) => s.replace(/^(\.\.\/)+/, ''));
  writeFileSync(file, JSON.stringify(map));
}

const main = Object.entries(result.metafile.outputs).find(([, o]) => o.entryPoint === 'src/main.ts')[0];
writeFileSync(
  join(out, 'index.html'),
  `<!doctype html>
<html lang="es">
<head><meta charset="utf-8"><title>Pedidos</title></head>
<body>
<button id="enviar">Enviar pedido</button>
<output id="resumen"></output>
<script type="module" src="/${basename(main)}"></script>
</body>
</html>
`,
);
console.log(Object.keys(result.metafile.outputs).map((f) => basename(f)).join('\n'));
