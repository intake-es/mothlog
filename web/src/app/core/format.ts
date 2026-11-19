import type { IssueStatus } from './api';

const MINUTE = 60_000;
const HOUR = 60 * MINUTE;
const DAY = 24 * HOUR;

/** «ahora», «hace 5 min», «hace 3 h», «hace 4 d», y la fecha a partir de un mes. */
export function ago(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) {
    return '—';
  }
  const t = Date.parse(iso);
  if (Number.isNaN(t)) {
    return '—';
  }
  const d = Math.max(0, now - t);
  if (d < MINUTE) {
    return 'ahora';
  }
  if (d < HOUR) {
    return `hace ${Math.floor(d / MINUTE)} min`;
  }
  if (d < DAY) {
    return `hace ${Math.floor(d / HOUR)} h`;
  }
  if (d < 30 * DAY) {
    return `hace ${Math.floor(d / DAY)} d`;
  }
  return shortDate(iso);
}

const dateTime = new Intl.DateTimeFormat('es-ES', {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

const dateOnly = new Intl.DateTimeFormat('es-ES', {
  day: 'numeric',
  month: 'short',
  year: 'numeric',
});
const dayMonth = new Intl.DateTimeFormat('es-ES', {
  day: 'numeric',
  month: 'short',
  timeZone: 'UTC',
});
const timeOnly = new Intl.DateTimeFormat('es-ES', {
  hour: '2-digit',
  minute: '2-digit',
  second: '2-digit',
});

export function fullDate(iso: string | null | undefined): string {
  return iso ? dateTime.format(new Date(iso)) : '';
}

export function shortDate(iso: string): string {
  return dateOnly.format(new Date(iso));
}

/** Un día de la serie diaria (YYYY-MM-DD, en UTC): «19 nov». */
export function day(date: string): string {
  return dayMonth.format(new Date(`${date}T00:00:00Z`)).replace('.', '');
}

export function clock(iso: string | number): string {
  return timeOnly.format(new Date(iso));
}

const numbers = new Intl.NumberFormat('es-ES');

export function count(n: number): string {
  return numbers.format(n);
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

export const STATUS_LABEL: Record<IssueStatus, string> = {
  open: 'Abierto',
  resolved: 'Resuelto',
  resolved_next: 'Resuelto en la siguiente versión',
  silenced: 'Silenciado',
};

/** El chip de cada estado: rojo lo que falla, gris lo que no pide nada. */
export const STATUS_BADGE: Record<IssueStatus, string> = {
  open: 'sp-badge--red',
  resolved: 'sp-badge--green',
  resolved_next: 'sp-badge--blue',
  silenced: 'sp-badge--gray',
};
