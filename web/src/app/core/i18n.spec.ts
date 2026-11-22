import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { Component } from '@angular/core';
import { TestBed } from '@angular/core/testing';
import { Title } from '@angular/platform-browser';
import { provideRouter, Router, TitleStrategy } from '@angular/router';
import { ICONS } from '../ui/icons';
import { ago, count, day, fullDate } from './format';
import { DICTIONARIES, I18n, initialLang, lang, t } from './i18n';
import { PageTitle } from './page-title';

const APP = join(process.cwd(), 'src', 'app');

function files(dir: string): string[] {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) {
      return files(path);
    }
    return /\.(html|ts)$/.test(name) && !name.endsWith('.spec.ts') ? [path] : [];
  });
}

const KEYS = new Set<string>(Object.keys(DICTIONARIES.es));
const ICON_NAMES = new Set(Object.values(ICONS).flatMap((byName) => Object.keys(byName)));

/**
 * Literales con letras que no son texto de la interfaz: la marca, los nombres
 * de los idiomas en su propio idioma, y palabras sueltas que son valores de la
 * API, opciones de Intl o de Angular, nombres de campos o de menús.
 */
const ALLOWED = new Set([
  'intake-errors',
  'Español',
  'English',
  ...['open', 'resolved', 'silenced', 'all', 'count', 'daily', 'limit', 'cursor', 'project'],
  ...['status', 'sort', 'query', 'github', 'dev', 'events', 'org', 'vars', 'search', 'params'],
  ...['timestamp', 'data', 'message', 'category', 'type', 'level', 'default', 'app', 'libs'],
  ...['auto', 'narrow', 'second', 'minute', 'hour', 'day', 'numeric', 'short', 'conjunction'],
  ...['ago', 'full', 'clock', 'root', 'text', 'object', 'string', 'number', 'undefined', 'merge'],
  ...['textarea', 'readonly', 'fixed', 'copy', 'system', 'light', 'dark', 'lang', 'theme'],
  ...['user', 'es', 'en', 'login', 'regular', 'duotone', 'start', 'end', 'true', 'false'],
  ...['element', 'UTC'],
  ...['releases', 'env', 'page', 'new', 'continued', 'stopped', 'issues'],
  // Tipos y orígenes del historial.
  ...['comment', 'ui', 'api', 'mcp', 'ingest', 'deploy'],
  // Claves y valores del lenguaje de búsqueda, y las teclas del autocompletado.
  ...['is', 'release', 'environment', 'regressed', 'browser', 'os', 'device', 'runtime'],
  ...['prefix', 'percent', 'input', 'ArrowDown', 'ArrowUp', 'Tab', 'Enter', 'Escape'],
  'os:"Mac OS"',
  '"division by zero"',
  ...['unit', 'disjunction', 'alerts', 'filters'],
  ...['slack', 'linear', 'email', 'discord', 'telegram', 'ntfy', 'webhook'],
  ...['fatal', 'error', 'warning', 'info', 'debug', 'settings', 'page', 'id', 'days', 'ok'],
  ...['messages', 'environments', 'releases', 'localhost', 'perIssue', 'sample', 'rateLimit'],
  ...['url', 'labels', 'to', 'token', 'secret', 'sending', '\\n'],
  'fill: currentColor; stroke: none',
]);

/** Sin espacios y con pinta de código: rutas, clases, claves con punto, snake_case, MAYÚSCULAS, 6px. */
function codeLike(v: string): boolean {
  return (
    !/\s/.test(v) && (/[-_/.:()[\]<>=@#?&]/.test(v) || /^[A-Z0-9_]+$/.test(v) || /^\d/.test(v))
  );
}

function isText(literal: string): boolean {
  const v = literal.replace(/\$\{[^}]*\}/g, ' ').trim();
  return (
    /\p{L}/u.test(v) &&
    !KEYS.has(v) &&
    !ICON_NAMES.has(v) &&
    !ALLOWED.has(v) &&
    !codeLike(literal.replace(/\$\{[^}]*\}/g, '').trim())
  );
}

/** Los literales de una expresión de plantilla ('…' o "…" dentro de {{ }} o de [attr]="…"). */
function expressionLiterals(expr: string): string[] {
  return [...expr.matchAll(/'((?:\\.|[^'\\])*)'|"((?:\\.|[^"\\])*)"/g)]
    .map((m) => m[1] ?? m[2])
    .filter(isText);
}

/** De la apertura en `from` a justo después de su cierre. */
function skipBalanced(src: string, from: number, open: string, close: string): number {
  let depth = 0;
  for (let i = from; i < src.length; i++) {
    if (src[i] === open) {
      depth++;
    } else if (src[i] === close && --depth === 0) {
      return i + 1;
    }
  }
  return src.length;
}

/**
 * Lo que se lee o se oye sin pasar por t(): el texto suelto, los atributos
 * estáticos y los literales de las interpolaciones y de los atributos enlazados.
 */
function untranslated(template: string): string[] {
  const found: string[] = [];
  let text = '';
  const flush = () => {
    const v = text.replace(/\s+/g, ' ').trim();
    if (/\p{L}/u.test(v) && !ALLOWED.has(v)) {
      found.push(v);
    }
    text = '';
  };
  const src = template.replace(/<!--[\s\S]*?-->/g, '');
  let i = 0;
  while (i < src.length) {
    if (src.startsWith('{{', i)) {
      flush();
      const close = src.indexOf('}}', i);
      for (const lit of expressionLiterals(src.slice(i + 2, close))) {
        found.push(`{{ '${lit}' }}`);
      }
      i = close + 2;
    } else if (src[i] === '<' && /[a-zA-Z/!]/.test(src[i + 1] ?? '')) {
      flush();
      let j = i + 1;
      let quote = '';
      for (; j < src.length; j++) {
        const c = src[j];
        if (quote) {
          quote = c === quote ? '' : quote;
        } else if (c === '"' || c === "'") {
          quote = c;
        } else if (c === '>') {
          break;
        }
      }
      const tag = src.slice(i, j + 1);
      for (const m of tag.matchAll(/\s(aria-label|title|placeholder|alt|label)="([^"]*)"/g)) {
        if (/\p{L}/u.test(m[2]) && !ALLOWED.has(m[2])) {
          found.push(`${m[1]}="${m[2]}"`);
        }
      }
      for (const m of tag.matchAll(/\s\[([\w.-]+)\]=(?:"([^"]*)"|'([^']*)')/g)) {
        // Las clases y los estilos no se leen.
        if (/^(class|style)\b/.test(m[1])) {
          continue;
        }
        for (const lit of expressionLiterals(m[2] ?? m[3])) {
          found.push(`[${m[1]}]: '${lit}'`);
        }
      }
      i = j + 1;
    } else if (src[i] === '@' && /[a-z]/.test(src[i + 1] ?? '')) {
      flush();
      i++;
      while (/[\w ]/.test(src[i] ?? '') && src[i] !== '(' && src[i] !== '{') {
        i++;
      }
      if (src[i] === '(') {
        i = skipBalanced(src, i, '(', ')');
      }
    } else if (src[i] === '{' || src[i] === '}') {
      flush();
      i++;
    } else {
      text += src[i];
      i++;
    }
  }
  flush();
  return found;
}

/**
 * Los literales de TypeScript con letras que no son una clave, un icono ni código.
 * Los de `new Error(...)` no se pintan y quedan fuera.
 */
function codeLiterals(code: string): string[] {
  const src = code
    .replace(/(template|styles):\s*(`[\s\S]*?`|'[^']*')/g, '')
    .replace(/\/\*[\s\S]*?\*\//g, '')
    .replace(/^\s*\/\/.*$/gm, '')
    .replace(/^\s*(import|export) [^;]* from '[^']*';$/gm, '');
  const found: string[] = [];
  for (const m of src.matchAll(/(Error\()?(['"`])((?:\\.|(?!\2)[^\\])*)\2/g)) {
    if (!m[1] && isText(m[3])) {
      found.push(m[3]);
    }
    if (m[2] === '`') {
      for (const part of m[3].matchAll(/\$\{([^}]*)\}/g)) {
        found.push(...codeLiterals(part[1]));
      }
    }
  }
  return found;
}

describe('traducciones', () => {
  beforeEach(() => lang.set('es'));

  it('tienen las mismas claves en español y en inglés, sin vacíos ni huecos distintos', () => {
    const es = DICTIONARIES.es;
    const en = DICTIONARIES.en;
    const onlyEs = Object.keys(es).filter((k) => !(k in en));
    const onlyEn = Object.keys(en).filter((k) => !(k in es));
    expect({ onlyEs, onlyEn }).toEqual({ onlyEs: [], onlyEn: [] });

    const slots = (s: string) => [...s.matchAll(/\{(\w+)\}/g)].map((m) => m[1]).sort();
    const problems = Object.keys(es).flatMap((k) => {
      const key = k as keyof typeof es;
      const out: string[] = [];
      if (!es[key].trim() || !en[key].trim()) {
        out.push(`${k}: vacía`);
      }
      if (slots(es[key]).join() !== slots(en[key]).join()) {
        out.push(`${k}: huecos distintos`);
      }
      return out;
    });
    expect(problems).toEqual([]);
  });

  it('el detector encuentra texto suelto, atributos y literales sin traducir', () => {
    // Para escribir `${` dentro de los ejemplos de código.
    const D = '$';
    expect(
      untranslated(
        `<p>{{ t('list.title') }}</p> @if (x > 1 && f(y)) { <b>Hola</b> } @else { {{ n }} }
         <button aria-label="Cerrar" [attr.title]="t('detail.more')" (click)="a > b">x</button>`,
      ),
    ).toEqual(['Hola', 'aria-label="Cerrar"', 'x']);
    expect(
      untranslated(
        `<span>{{ n === 1 ? 'evento' : 'eventos' }}</span>
         {{ loadingMore() ? 'Cargando…' : 'Cargar más' }}
         <div [attr.aria-label]="'Código de ' + f.file" [appIcon]="open ? 'caret-down' : 'check'"></div>
         <span [class]="statusBadge[s]">{{ t(statusLabel[s]) }}</span>
         <span [attr.aria-current]="on ? 'true' : null">{{ menu() === 'theme' ? t('shell.theme') : '' }}</span>
         <b>{{ "Texto" }}</b><i [attr.title]='"Título"'></i>`,
      ),
    ).toEqual([
      "{{ 'evento' }}",
      "{{ 'eventos' }}",
      "{{ 'Cargando…' }}",
      "{{ 'Cargar más' }}",
      "[attr.aria-label]: 'Código de '",
      "{{ 'Texto' }}",
      "[attr.title]: 'Título'",
    ]);
    expect(
      codeLiterals(
        [
          "const a = 'Sin etiquetas'; const b = t('detail.noTags');",
          "const OPTS = [{ value: 'open', label: 'Abiertos' }, { value: 'all', label: 'Todos' }];",
          "label: i === last ? 'Hoy' : day(d.date), icon: 'check-circle', css: 'sp-badge--red',",
          `text: \`${D}{count(n)} ${D}{n === 1 ? "evento" : "eventos"}\`, path: \`${D}{BASE}/issues/\`,`,
          `seen: \`Última vez: ${D}{fullDate(x)}\`, url: '/api/0', key: 'last_seen', env: 'GITHUB_ORG',`,
          "throw new Error('no se pudo copiar');",
        ].join('\n'),
      ),
    ).toEqual([
      'Sin etiquetas',
      'Abiertos',
      'Todos',
      'Hoy',
      'evento',
      'eventos',
      `Última vez: ${D}{fullDate(x)}`,
    ]);
  });

  it('no queda texto visible fuera de las traducciones en las plantillas ni en el código', () => {
    const problems: string[] = [];
    // index.html pone el título de la pestaña mientras arranca la app.
    for (const file of [...files(APP), join(APP, '..', 'index.html')]) {
      const rel = relative(APP, file);
      // Los diccionarios son los textos; icons.ts y los *.testing.ts no pintan nada.
      if (
        rel.startsWith(join('core', 'i18n', '')) ||
        rel === join('ui', 'icons.ts') ||
        rel.endsWith('.testing.ts')
      ) {
        continue;
      }
      const src = readFileSync(file, 'utf8');
      const templates = file.endsWith('.html')
        ? [src]
        : [...src.matchAll(/template:\s*(?:`([\s\S]*?)`|'([^']*)')/g)].map((m) => m[1] ?? m[2]);
      for (const tpl of templates) {
        for (const text of untranslated(tpl)) {
          problems.push(`${rel}: ${text}`);
        }
      }
      if (file.endsWith('.ts')) {
        for (const text of codeLiterals(src)) {
          problems.push(`${rel}: '${text}'`);
        }
      }
    }
    expect(problems).toEqual([]);
  });

  it('rellena los huecos y escoge singular o plural', () => {
    const i18n = TestBed.inject(I18n);
    expect(t('detail.position', { i: 1, n: 2 })).toBe('1 de 2');
    expect(i18n.plural('events', 1)).toBe('1 evento');
    expect(i18n.plural('events', 12345)).toBe('12.345 eventos');
    expect(i18n.around('login.lede.github', 'org')).toEqual({
      before: 'Entra con tu cuenta de GitHub de ',
      after: '.',
    });
    i18n.set('en');
    expect(t('detail.position', { i: 1, n: 2 })).toBe('1 of 2');
    expect(i18n.plural('events', 12345)).toBe('12,345 events');
  });
});

describe('idioma', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
    lang.set('es');
  });

  it('sale del navegador: español si empieza por es, inglés en el resto', () => {
    localStorage.clear();
    const language = vi.spyOn(navigator, 'language', 'get');
    for (const [nav, expected] of [
      ['es', 'es'],
      ['es-MX', 'es'],
      ['ES-es', 'es'],
      ['en-US', 'en'],
      ['fr-FR', 'en'],
      ['', 'en'],
    ]) {
      language.mockReturnValue(nav);
      expect(initialLang()).toBe(expected);
    }
  });

  it('el elegido a mano se recuerda y pone <html lang>', () => {
    vi.spyOn(navigator, 'language', 'get').mockReturnValue('es-ES');
    const i18n = TestBed.inject(I18n);
    i18n.set('en');
    expect(localStorage.getItem('ie-lang')).toBe('en');
    expect(document.documentElement.lang).toBe('en');
    expect(initialLang()).toBe('en');
    i18n.set('es');
    expect(document.documentElement.lang).toBe('es');
    expect(initialLang()).toBe('es');
  });

  it('las fechas, los números y el «hace cuánto» van en el idioma elegido', () => {
    const now = Date.parse('2026-11-19T12:00:00Z');
    lang.set('en');
    expect(ago('2026-11-19T11:59:30Z', now)).toBe('now');
    expect(ago('2026-11-19T11:55:00Z', now)).toBe('5m ago');
    expect(ago('2026-11-19T09:00:00Z', now)).toBe('3h ago');
    expect(ago('2026-11-15T12:00:00Z', now)).toBe('4d ago');
    expect(day('2026-11-19')).toBe('Nov 19');
    expect(count(12345)).toBe('12,345');
    expect(fullDate('2026-11-19T12:00:00Z')).toContain('Nov');
    lang.set('es');
    expect(ago('2026-11-19T11:55:00Z', now)).toBe('hace 5 min');
    expect(day('2026-11-19')).toBe('19 nov');
    expect(count(12345)).toBe('12.345');
    expect(fullDate('2026-11-19T12:00:00Z')).toContain('nov');
  });
});

@Component({ template: '' })
class Blank {}

describe('PageTitle', () => {
  beforeEach(() => {
    lang.set('es');
    TestBed.configureTestingModule({
      providers: [
        provideRouter([{ path: '', component: Blank, title: 'title.list' }]),
        { provide: TitleStrategy, useExisting: PageTitle },
      ],
    });
  });

  afterEach(() => lang.set('es'));

  it('traduce el título de la ruta y lo cambia con el idioma, sin navegar', async () => {
    await TestBed.inject(Router).navigateByUrl('/');
    TestBed.tick();
    const title = TestBed.inject(Title);
    expect(title.getTitle()).toBe('Errores · intake-errors');
    TestBed.inject(I18n).set('en');
    TestBed.tick();
    expect(title.getTitle()).toBe('Errors · intake-errors');
  });

  it('el título de la pantalla también sigue al idioma', () => {
    const pageTitle = TestBed.inject(PageTitle);
    pageTitle.set(() => t('title.issueNamed', { title: 'TypeError: x' }));
    TestBed.tick();
    const title = TestBed.inject(Title);
    expect(title.getTitle()).toBe('TypeError: x · Errores');
    TestBed.inject(I18n).set('en');
    TestBed.tick();
    expect(title.getTitle()).toBe('TypeError: x · Errors');
  });
});
