import type { IssueStatus } from './api';
import { type Key, locale } from './i18n';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

const cache = new Map<string, Intl.DateTimeFormat | Intl.NumberFormat | Intl.RelativeTimeFormat>();

function cached<T extends Intl.DateTimeFormat | Intl.NumberFormat | Intl.RelativeTimeFormat>(
  name: string,
  make: (loc: string) => T,
): T {
  const loc = locale();
  const key = `${name}|${loc}`;
  let f = cache.get(key);
  if (!f) {
    f = make(loc);
    cache.set(key, f);
  }
  return f as T;
}

/** «ahora», «hace 5 min», «hace 3 h», «hace 4 d» en el idioma elegido, y la fecha a partir de un mes. */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) {
    return '—';
  }
  const t = Date.parse(iso);
  if (Number.isNaN(t)) {
    return '—';
  }
  const d = Math.max(0, now - t);
  if (d >= 30 * DAY) {
    return shortDate(iso);
  }
  const rel = cached(
    'ago',
    (loc) => new Intl.RelativeTimeFormat(loc, { numeric: 'auto', style: 'narrow' }),
  );
  if (d < MINUTE) {
    return rel.format(0, 'second');
  }
  if (d < HOUR) {
    return rel.format(-Math.floor(d / MINUTE), 'minute');
  }
  if (d < DAY) {
    return rel.format(-Math.floor(d / HOUR), 'hour');
  }
  return rel.format(-Math.floor(d / DAY), 'day');
}

export function fullDate(iso: string | null | undefined): string {
  if (!iso) {
    return '';
  }
  const f = cached(
    'full',
    (loc) =>
      new Intl.DateTimeFormat(loc, {
        day: 'numeric',
        month: 'short',
        year: 'numeric',
        hour: '2-digit',
        minute: '2-digit',
        second: '2-digit',
      }),
  );
  return f.format(new Date(iso));
}

export function shortDate(iso: string): string {
  const f = cached(
    'short',
    (loc) => new Intl.DateTimeFormat(loc, { day: 'numeric', month: 'short', year: 'numeric' }),
  );
  return f.format(new Date(iso));
}

/** Un día de la serie diaria (YYYY-MM-DD, en UTC): «19 nov», «Nov 19». */
export function day(date: string): string {
  const f = cached(
    'day',
    (loc) => new Intl.DateTimeFormat(loc, { day: 'numeric', month: 'short', timeZone: 'UTC' }),
  );
  return f.format(new Date(`${date}T00:00:00Z`)).replace('.', '');
}

export function clock(iso: string | number): string {
  const f = cached(
    'clock',
    (loc) =>
      new Intl.DateTimeFormat(loc, { hour: '2-digit', minute: '2-digit', second: '2-digit' }),
  );
  return f.format(new Date(iso));
}

export function count(n: number): string {
  return cached('count', (loc) => new Intl.NumberFormat(loc)).format(n);
}

/** Separa «TypeError: mensaje» en el tipo y el mensaje para pintarlos distinto. */
export function splitTitle(title: string, type: string): { type: string; message: string } {
  if (type && title.startsWith(`${type}: `)) {
    return { type, message: title.slice(type.length + 2) };
  }
  if (type && title === type) {
    return { type, message: '' };
  }
  return { type: '', message: title };
}

/** La clave del nombre de cada estado; el valor de la API no cambia. */
export const STATUS_LABEL: Record<IssueStatus, Key> = {
  open: 'status.open',
  resolved: 'status.resolved',
  resolved_next: 'status.resolved_next',
  silenced: 'status.silenced',
};

/** El chip de cada estado: rojo lo que falla, gris lo que no pide nada. */
export const STATUS_BADGE: Record<IssueStatus, string> = {
  open: 'sp-badge--red',
  resolved: 'sp-badge--green',
  resolved_next: 'sp-badge--blue',
  silenced: 'sp-badge--gray',
};
