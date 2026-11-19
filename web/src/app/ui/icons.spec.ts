import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { ICONS } from './icons';

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      return files(path);
    }
    return /\.(html|ts)$/.test(name) && !name.endsWith('.spec.ts') ? [path] : [];
  });
}

// Lo que se pinta y no está en el registro sale vacío: falla aquí y no en pantalla.
describe('iconos', () => {
  it('todos los que se usan están en src/app/ui/icons.ts (node scripts/icons.mjs)', () => {
    const missing: string[] = [];
    for (const file of files(join(process.cwd(), 'src', 'app'))) {
      const src = readFileSync(file, 'utf8');
      for (const tag of src.matchAll(/<svg\b[^>]*>/g)) {
        const name = /\sappIcon="([a-z-]+)"/.exec(tag[0])?.[1];
        if (!name) {
          continue;
        }
        const weight = (/\sweight="(\w+)"/.exec(tag[0])?.[1] ?? 'regular') as keyof typeof ICONS;
        if (!ICONS[weight]?.[name]) {
          missing.push(`${file}: ${name} (${weight})`);
        }
      }
      for (const m of src.matchAll(/(?:icon|Icon)\s*[:=]\s*'([a-z-]+)'/g)) {
        if (!ICONS.regular[m[1]]) {
          missing.push(`${file}: ${m[1]}`);
        }
      }
      for (const m of src.matchAll(/\[appIcon\]="([^"]+)"/g)) {
        for (const lit of m[1].matchAll(/'([a-z-]+)'/g)) {
          if (!ICONS.regular[lit[1]]) {
            missing.push(`${file}: ${lit[1]}`);
          }
        }
      }
    }
    expect(missing).toEqual([]);
  });
});
