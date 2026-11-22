import type { CheckInStatus, MonitorStatus, Schedule } from '../../core/api';
import { type Key, locale, plural, t } from '../../core/i18n';

export const MONITOR_STATUS_LABEL: Record<MonitorStatus, Key> = {
  pending: 'monitors.status.pending',
  ok: 'monitors.status.ok',
  error: 'monitors.status.error',
  missed: 'monitors.status.missed',
  timeout: 'monitors.status.timeout',
};

export const CHECKIN_STATUS_LABEL: Record<CheckInStatus, Key> = {
  in_progress: 'monitors.status.in_progress',
  ok: 'monitors.status.ok',
  error: 'monitors.status.error',
  missed: 'monitors.status.missed',
  timeout: 'monitors.status.timeout',
};

export const STATUS_BADGE: Record<MonitorStatus | CheckInStatus, string> = {
  pending: 'sp-badge--gray',
  in_progress: 'sp-badge--blue',
  ok: 'sp-badge--green',
  error: 'sp-badge--red',
  missed: 'sp-badge--yellow',
  timeout: 'sp-badge--red',
};

const UNITS = ['minute', 'hour', 'day', 'week', 'month', 'year'] as const;

/** «0 3 * * *» o «Cada 2 horas». */
export function scheduleText(s: Schedule): string {
  if (s.type === 'crontab') {
    return s.value;
  }
  const unit = UNITS.find((u) => u === s.unit) ?? 'minute';
  return plural(`monitors.every.${unit}`, s.value);
}

/** «en 4 min», «en 3 h»; «ya» si la hora ha pasado. */
export function until(iso: string | null, now = Date.now()): string {
  if (!iso) {
    return '—';
  }
  const d = Date.parse(iso) - now;
  if (Number.isNaN(d)) {
    return '—';
  }
  if (d <= 0) {
    return t('monitors.due');
  }
  const rel = new Intl.RelativeTimeFormat(locale(), { numeric: 'auto', style: 'narrow' });
  const min = 60_000;
  if (d < 60 * min) {
    return rel.format(Math.max(1, Math.ceil(d / min)), 'minute');
  }
  if (d < 24 * 60 * min) {
    return rel.format(Math.round(d / (60 * min)), 'hour');
  }
  return rel.format(Math.round(d / (24 * 60 * min)), 'day');
}

/** «201 ms», «1,2 s», «3 min 4 s», «2 h 5 min». */
export function duration(ms: number | null): string {
  if (ms === null) {
    return '—';
  }
  const nf = (n: number, unit: 'millisecond' | 'second' | 'minute' | 'hour', digits = 0) =>
    new Intl.NumberFormat(locale(), {
      style: 'unit',
      unit,
      unitDisplay: 'short',
      maximumFractionDigits: digits,
    }).format(n);
  if (ms < 1000) {
    return nf(ms, 'millisecond');
  }
  if (ms < 60_000) {
    return nf(ms / 1000, 'second', 1);
  }
  const s = Math.floor(ms / 1000);
  if (s < 3600) {
    return `${nf(Math.floor(s / 60), 'minute')} ${nf(s % 60, 'second')}`;
  }
  return `${nf(Math.floor(s / 3600), 'hour')} ${nf(Math.floor(s / 60) % 60, 'minute')}`;
}
