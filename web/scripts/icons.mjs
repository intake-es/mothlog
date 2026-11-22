#!/usr/bin/env node
// Regenera src/app/ui/icons.ts con los iconos de Phosphor de ICONS.
// Al usar uno nuevo, se añade aquí y se vuelve a correr: icons.spec.ts falla si falta.
import { readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const ROOT = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const ASSETS = join(ROOT, 'node_modules', '@phosphor-icons', 'core', 'assets');
const OUT = join(ROOT, 'src', 'app', 'ui', 'icons.ts');

const ICONS = {
  regular: [
    'arrow-counter-clockwise',
    'arrow-left',
    'bell',
    'bell-slash',
    'calendar-blank',
    'caret-down',
    'caret-left',
    'caret-right',
    'check',
    'check-circle',
    'circle-dashed',
    'clock',
    'copy',
    'desktop',
    'discord-logo',
    'dots-three',
    'envelope-simple',
    'funnel',
    'gear',
    'github-logo',
    'globe-simple',
    'kanban',
    'magnifying-glass',
    'moon',
    'package',
    'paper-plane-tilt',
    'plus',
    'rocket-launch',
    'sign-out',
    'slack-logo',
    'sort-descending',
    'squares-four',
    'stack',
    'sun',
    'tag',
    'telegram-logo',
    'translate',
    'trash',
    'user',
    'warning-circle',
    'webhooks-logo',
    'bell-ringing',
    'x',
  ],
  fill: ['bug'],
  duotone: ['check-circle', 'lock-key', 'magnifying-glass', 'warning-circle'],
};

const SUFFIX = { regular: '', fill: '-fill', duotone: '-duotone' };

const paths = (weight, name) => {
  const svg = readFileSync(join(ASSETS, weight, `${name}${SUFFIX[weight]}.svg`), 'utf8');
  return [...svg.matchAll(/<path d="([^"]+)"/g)].map((m) => m[1]);
};

let out = `// Generado por \`node scripts/icons.mjs\` desde @phosphor-icons/core: no editar a mano.

export type IconWeight = 'regular' | 'fill' | 'duotone';

export const ICONS: Readonly<Record<IconWeight, Readonly<Record<string, readonly string[]>>>> = {
`;
for (const [weight, names] of Object.entries(ICONS)) {
  out += `  ${weight}: {\n`;
  for (const name of [...names].sort()) {
    out += `    '${name}': [\n${paths(weight, name)
      .map((d) => `      '${d}',\n`)
      .join('')}    ],\n`;
  }
  out += '  },\n';
}
out += '};\n';
writeFileSync(OUT, out);
console.log(`${OUT}: ${Object.values(ICONS).flat().length} iconos`);
