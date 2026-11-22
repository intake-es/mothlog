/**
 * Lo justo del lenguaje de búsqueda para la interfaz: partir la búsqueda en
 * términos, saber cuál se está escribiendo y poner o quitar uno. Quien la
 * valida es el servidor (internal/search).
 */

export interface Token {
  start: number;
  end: number;
  raw: string;
  negate: boolean;
  /** Sin dos puntos, null: texto libre o una clave a medio escribir. */
  key: string | null;
  value: string;
}

const KEY = /^[A-Za-z_][\w.-]*$/;

export const BUILTIN_KEYS = ['is', 'release', 'environment', 'level', 'project', 'user'] as const;
export const STATUSES = ['open', 'resolved', 'resolved_next', 'silenced', 'regressed'] as const;
export type SearchStatus = (typeof STATUSES)[number];

/** La búsqueda de la lista cuando no hay otra. */
export const DEFAULT_QUERY = 'is:open';

export function tokenize(q: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  while (i < q.length) {
    while (i < q.length && /\s/.test(q[i])) {
      i++;
    }
    if (i >= q.length) {
      break;
    }
    const start = i;
    let quoted = false;
    while (i < q.length && (quoted || !/\s/.test(q[i]))) {
      if (q[i] === '\\' && quoted) {
        i += 2;
        continue;
      }
      if (q[i] === '"') {
        quoted = !quoted;
      }
      i++;
    }
    out.push(parseToken(q.slice(start, Math.min(i, q.length)), start));
  }
  return out;
}

function parseToken(raw: string, start: number): Token {
  const negate = raw.startsWith('!');
  const body = negate ? raw.slice(1) : raw;
  const colon = body.indexOf(':');
  const key = colon > 0 ? body.slice(0, colon) : '';
  const isKey = KEY.test(key) && !body.slice(colon + 1).startsWith('//');
  return {
    start,
    end: start + raw.length,
    raw,
    negate,
    key: isKey ? key : null,
    value: unquote(isKey ? body.slice(colon + 1) : body),
  };
}

const DQ = '"';
const BS = '\\';

/** Quita las comillas y los escapes \" y \\ de un valor. */
function unquote(v: string): string {
  if (!v.startsWith(DQ)) {
    return v;
  }
  const end = v.length > 1 && v.endsWith(DQ) ? v.length - 1 : v.length;
  let out = '';
  for (let i = 1; i < end; i++) {
    if (v[i] === BS && i + 1 < end) {
      i++;
    }
    out += v[i];
  }
  return out;
}

export function quote(v: string): string {
  const plain = v !== '' && ![...v].some((c) => c === DQ || c === BS || /\s/.test(c));
  return plain ? v : DQ + v.replaceAll(BS, BS + BS).replaceAll(DQ, BS + DQ) + DQ;
}

export function term(key: string, value: string, negate = false): string {
  return `${negate ? '!' : ''}${key}:${quote(value)}`;
}

/** El término bajo el cursor, o uno vacío en el cursor si está entre espacios. */
export function tokenAt(q: string, caret: number): Token {
  const found = tokenize(q).find((t) => caret >= t.start && caret <= t.end);
  return found ?? { start: caret, end: caret, raw: '', negate: false, key: null, value: '' };
}

/** Cambia el término [start, end) por text, con un espacio detrás si cierra un valor. */
export function replaceToken(
  q: string,
  t: Pick<Token, 'start' | 'end'>,
  text: string,
): { query: string; caret: number } {
  const after = q.slice(t.end).replace(/^\s*/, '');
  const spaced = text.endsWith(':') ? text : `${text} `;
  const query = `${q.slice(0, t.start)}${spaced}${after}`;
  return { query, caret: t.start + spaced.length };
}

/** El valor del primer término key: sin negar, o null. */
export function termValue(q: string, key: string): string | null {
  const t = tokenize(q).find((t) => t.key === key && !t.negate);
  return t ? t.value : null;
}

/**
 * Quita los términos key: sin negar y, si hay valor, pone uno nuevo donde
 * estaba el primero (o al principio).
 */
export function setTerm(q: string, key: string, value: string | null): string {
  const tokens = tokenize(q);
  const at = tokens.findIndex((t) => t.key === key && !t.negate);
  const kept = tokens.filter((t) => !(t.key === key && !t.negate)).map((t) => t.raw);
  if (value !== null) {
    kept.splice(at < 0 ? 0 : at, 0, term(key, value));
  }
  return kept.join(' ');
}
