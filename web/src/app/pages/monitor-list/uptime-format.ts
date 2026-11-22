import type { UptimeMonitor } from '../../core/api';
import { type Key, locale, plural, t } from '../../core/i18n';

export type UptimeState = 'pending' | 'up' | 'down' | 'paused';

/** El estado que se enseña: un monitor pausado sale como pausado, esté como esté. */
export function uptimeState(m: Pick<UptimeMonitor, 'paused' | 'status'>): UptimeState {
  return m.paused ? 'paused' : m.status;
}

export const UPTIME_STATE_LABEL: Record<UptimeState, Key> = {
  pending: 'uptime.status.pending',
  up: 'uptime.status.up',
  down: 'uptime.status.down',
  paused: 'uptime.status.paused',
};

export const UPTIME_STATE_BADGE: Record<UptimeState, string> = {
  pending: 'sp-badge--gray',
  up: 'sp-badge--green',
  down: 'sp-badge--red',
  paused: 'sp-badge--gray',
};

/** Los intervalos del formulario, en segundos. */
export const INTERVALS = [30, 60, 120, 300, 600, 900, 1800, 3600];

/** «Cada 30 s», «Cada minuto», «Cada 5 minutos», «Cada hora». */
export function intervalText(seconds: number): string {
  if (seconds < 60 || seconds % 60 !== 0) {
    return t('uptime.everySeconds', { n: seconds });
  }
  const min = seconds / 60;
  if (min % 60 === 0) {
    return plural('monitors.every.hour', min / 60);
  }
  return plural('monitors.every.minute', min);
}

/** «99,95 %»: dos decimales salvo en el 100 justo; «—» sin datos. */
export function percent(v: number | null | undefined): string {
  if (v === null || v === undefined) {
    return '—';
  }
  const digits = v >= 100 ? 0 : 2;
  return new Intl.NumberFormat(locale(), {
    style: 'percent',
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(Math.floor(v * 100) / 10000);
}

/** El color del porcentaje: bien, regular o mal. */
export function percentClass(v: number | null | undefined): string {
  if (v === null || v === undefined) {
    return '';
  }
  if (v >= 99.9) {
    return 'pct--good';
  }
  return v >= 99 ? 'pct--warn' : 'pct--bad';
}

/** La URL sin el esquema, para leerla de un vistazo. */
export function shortURL(url: string): string {
  return url.replace(/^https?:\/\//, '').replace(/\/$/, '');
}
