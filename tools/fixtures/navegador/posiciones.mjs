import { readFileSync } from 'node:fs';
import { basename, join } from 'node:path';
import { SourceMapConsumer } from 'source-map';

// Imprime la posición original de cada fotograma de un envío con los mapas de
// un directorio, para comprobar lo que esperan los tests.
const [envelope, maps] = process.argv.slice(2);
if (!envelope || !maps) {
  console.error('uso: node posiciones.mjs <envío> <directorio de mapas>');
  process.exit(1);
}

const lines = readFileSync(envelope, 'utf8').split('\n');
const event = JSON.parse(lines.find((l, i) => i > 0 && lines[i - 1].includes('"type":"event"')));
for (const value of event.exception.values) {
  for (const f of value.stacktrace.frames) {
    const where = `${basename(f.filename)}:${f.lineno ?? '-'}:${f.colno ?? '-'} ${f.function}`;
    if (!f.lineno) {
      console.log(where);
      continue;
    }
    let raw;
    try {
      raw = readFileSync(join(maps, `${basename(f.filename)}.map`), 'utf8');
    } catch {
      console.log(`${where} (sin mapa)`);
      continue;
    }
    const pos = await SourceMapConsumer.with(raw, null, (c) => c.originalPositionFor({ line: f.lineno, column: f.colno - 1 }));
    console.log(`${where} -> ${pos.source}:${pos.line}:${pos.column + 1}`);
  }
}
