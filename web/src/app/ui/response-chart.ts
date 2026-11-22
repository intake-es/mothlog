import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import type { ResponseTimes, UptimeCheck } from '../core/api';
import { fullDate } from '../core/format';
import { I18n, locale, t } from '../core/i18n';
import { duration } from '../pages/monitor-list/monitor-format';

const MIN = 60_000;
const SLOT = 15 * MIN;
const DAY = 24 * 60 * MIN;
const [MOVE, LINE, CLOSE] = ['M', 'L', 'Z'];
const SLOTS = 96;
/** Con un monitor nuevo, la gráfica enseña desde que existe, pero al menos dos horas. */
const MIN_SLOTS = 8;
/** Con comprobaciones sueltas, al menos el último minuto. */
const MIN_SPAN = MIN;
const W = 960;
const H = 120;

interface Hit {
  x: number;
  w: number;
  /** Dónde va la marca roja de un fallo. */
  cx: number;
  fail: boolean;
  title: string;
}

interface Model {
  /** Los puntos de la línea, de izquierda a derecha; null corta la línea. */
  points: ([number, number] | null)[];
  hits: Hit[];
  /** Ancho de la marca roja de un fallo. */
  failW: number;
  /** Puntos que se ven sueltos: todos si son pocos. */
  dots: boolean;
  start: string;
  label: string;
  top: number;
}

/**
 * El tiempo de respuesta de las últimas 24 h en tramos de 15 min: una línea con
 * su área y, en rojo, abajo, los tramos con alguna comprobación fallida. Si
 * las comprobaciones de las 24 h caben en `checks`, las pinta una a una desde
 * la primera; si el monitor es de hace menos de un día, empieza al crearlo.
 */
@Component({
  selector: 'app-response-chart',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="frame">
      <span class="ymax tnum" aria-hidden="true">{{ topText() }}</span>
      <svg
        class="chart"
        [attr.viewBox]="'0 0 ' + w + ' ' + h"
        preserveAspectRatio="none"
        role="img"
        [attr.aria-label]="model().label"
      >
        <line class="grid" x1="0" [attr.x2]="w" y1="0.5" y2="0.5" />
        <line class="grid" x1="0" [attr.x2]="w" [attr.y1]="h / 2" [attr.y2]="h / 2" />
        @for (p of areas(); track $index) {
          <path class="area" [attr.d]="p" />
        }
        @for (p of lines(); track $index) {
          <path class="line" [attr.d]="p" vector-effect="non-scaling-stroke" />
        }
        @for (p of dots(); track $index) {
          <path class="dot" [attr.d]="p" vector-effect="non-scaling-stroke" />
        }
        @for (s of model().hits; track $index) {
          @if (s.fail) {
            <rect class="fail" [attr.x]="s.cx - model().failW / 2" [attr.y]="h - 6" [attr.width]="model().failW" height="6" />
          }
          <rect class="hit" [attr.x]="s.x" y="0" [attr.width]="s.w" [attr.height]="h">
            <title>{{ s.title }}</title>
          </rect>
        }
      </svg>
    </div>
    <div class="axis" aria-hidden="true">
      <span>{{ model().start }}</span>
      <span>{{ t('uptime.axis.now') }}</span>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .frame {
      position: relative;
      border-bottom: 1px solid var(--chart-base);
    }
    .chart {
      display: block;
      width: 100%;
      height: 120px;
      overflow: visible;
    }
    .ymax {
      position: absolute;
      top: -0.1rem;
      right: 0;
      color: var(--chart-axis);
      font-size: 0.68rem;
      transform: translateY(-100%);
    }
    .grid {
      stroke: var(--border);
      stroke-dasharray: 3 4;
      vector-effect: non-scaling-stroke;
    }
    .area {
      fill: color-mix(in srgb, var(--chart-bar-strong) 14%, transparent);
    }
    .line {
      fill: none;
      stroke: var(--chart-bar-strong);
      stroke-width: 1.5;
      stroke-linejoin: round;
    }
    .dot {
      fill: none;
      stroke: var(--chart-bar-strong);
      stroke-width: 6;
      stroke-linecap: round;
    }
    .fail {
      fill: var(--danger);
    }
    .hit {
      fill: transparent;
    }
    .hit:hover {
      fill: color-mix(in srgb, var(--text) 6%, transparent);
    }
    .axis {
      display: flex;
      justify-content: space-between;
      margin-top: 0.4rem;
      color: var(--chart-axis);
      font-size: 0.68rem;
    }
  `,
})
export class ResponseChart {
  readonly buckets = input<ResponseTimes[]>([]);
  /** Las últimas comprobaciones, de la más nueva a la más vieja. */
  readonly checks = input<UptimeCheck[]>([]);
  /** Cuándo se creó el monitor. */
  readonly since = input<string | null>(null);
  /** La hora del final de la gráfica; se cambia en los tests. */
  readonly now = input<number>(Date.now());

  protected readonly t = inject(I18n).t;
  protected readonly w = W;
  protected readonly h = H;

  /** Las comprobaciones de las últimas 24 h, de la más vieja a la más nueva, si están todas. */
  private readonly raw = computed<UptimeCheck[] | null>(() => {
    const now = this.now();
    const list = this.checks()
      .filter((c) => {
        const at = Date.parse(c.checked_at);
        return at > now - DAY && at <= now;
      })
      .sort((a, b) => Date.parse(a.checked_at) - Date.parse(b.checked_at));
    const total = this.buckets().reduce((n, b) => n + b.checks, 0);
    return list.length > 0 && total <= list.length ? list : null;
  });

  protected readonly model = computed<Model>(() => {
    const raw = this.raw();
    return raw ? this.fromChecks(raw) : this.fromBuckets();
  });

  protected readonly topText = computed(() => duration(this.model().top));

  private fromBuckets(): Model {
    const end = Math.floor(this.now() / SLOT) * SLOT + SLOT;
    const start24 = end - SLOTS * SLOT;
    const byIndex = new Map<number, ResponseTimes>();
    for (const b of this.buckets()) {
      const i = Math.floor((Date.parse(b.at) - start24) / SLOT);
      if (i >= 0 && i < SLOTS) {
        byIndex.set(i, b);
      }
    }
    const since = this.since();
    let first = since ? Math.floor((Date.parse(since) - start24) / SLOT) : 0;
    first = Math.max(0, Math.min(first, SLOTS - MIN_SLOTS, ...byIndex.keys()));
    const n = SLOTS - first;
    const slotW = W / n;
    const max = Math.max(1, ...[...byIndex.values()].map((b) => b.avg_ms ?? 0));
    const top = niceTop(max);
    const hits: Hit[] = [];
    const points: ([number, number] | null)[] = [];
    for (let i = first; i < SLOTS; i++) {
      const b = byIndex.get(i);
      const x = (i - first) * slotW;
      const parts = [fullDate(new Date(start24 + i * SLOT).toISOString())];
      if (b) {
        parts.push(t('uptime.chartAvg', { ms: duration(b.avg_ms) }));
        if (b.failures) {
          parts.push(t('uptime.chartFailures', { n: b.failures, total: b.checks }));
        }
      }
      hits.push({ x, w: slotW, cx: x + slotW / 2, fail: !!b?.failures, title: parts.join(' · ') });
      points.push(b?.avg_ms != null ? [x + slotW / 2, y(b.avg_ms, top)] : null);
    }
    return {
      points,
      hits,
      failW: slotW,
      dots: n <= 24,
      start:
        first === 0 ? t('uptime.axis.start') : t('uptime.axis.since', { time: span(n * SLOT) }),
      label: t('uptime.chart', {
        max: duration(max),
        bad: hits.filter((s) => s.fail).length,
      }),
      top,
    };
  }

  private fromChecks(list: UptimeCheck[]): Model {
    const now = this.now();
    const oldest = Date.parse(list[0].checked_at);
    const total = Math.min(DAY, Math.max(MIN_SPAN, (now - oldest) * 1.1));
    const start = now - total;
    const xs = list.map((c) => ((Date.parse(c.checked_at) - start) / total) * W);
    const ok = list.filter((c) => c.ok);
    const max = Math.max(1, ...ok.map((c) => c.duration_ms));
    const top = niceTop(max);
    const hits = list.map((c, i): Hit => {
      const left =
        i === 0 ? Math.max(0, xs[0] - ((xs[1] ?? W) - xs[0]) / 2) : (xs[i - 1] + xs[i]) / 2;
      const right = i === xs.length - 1 ? W : (xs[i] + xs[i + 1]) / 2;
      const parts = [fullDate(c.checked_at), duration(c.duration_ms)];
      if (c.ok && c.status_code !== null) {
        parts.push(String(c.status_code));
      } else if (!c.ok) {
        parts.push(c.error ? `${t('uptime.chartFailed')}: ${c.error}` : t('uptime.chartFailed'));
      }
      return { x: left, w: right - left, cx: xs[i], fail: !c.ok, title: parts.join(' · ') };
    });
    return {
      points: list.flatMap((c, i): [number, number][] =>
        c.ok ? [[xs[i], y(c.duration_ms, top)]] : [],
      ),
      hits,
      failW: 6,
      dots: true,
      start: t('uptime.axis.since', { time: span(total) }),
      label: t('uptime.chartChecks', {
        n: list.length,
        time: span(total),
        max: duration(max),
        bad: list.length - ok.length,
      }),
      top,
    };
  }

  /** Los trozos seguidos con datos, como listas de puntos. */
  private readonly runs = computed(() => {
    const out: [number, number][][] = [];
    let run: [number, number][] = [];
    for (const p of this.model().points) {
      if (p === null) {
        if (run.length) {
          out.push(run);
        }
        run = [];
        continue;
      }
      run.push(p);
    }
    if (run.length) {
      out.push(run);
    }
    return out;
  });

  protected readonly lines = computed(() =>
    this.runs()
      .filter((run) => run.length > 1)
      .map((run) => `M${path(run)}`),
  );

  protected readonly areas = computed(() =>
    this.runs()
      .filter((run) => run.length > 1)
      .map((run) => {
        const first = run[0][0].toFixed(1);
        const last = run[run.length - 1][0].toFixed(1);
        return `${MOVE}${first},${H}${LINE}${path(run)}${LINE}${last},${H}${CLOSE}`;
      }),
  );

  /** Un punto suelto, o todos si son pocos: un trazo de largo cero con la punta redonda. */
  protected readonly dots = computed(() => {
    const all = this.model().dots;
    return this.runs()
      .filter((run) => all || run.length === 1)
      .flat()
      .map(([x, y]) => {
        const at = `${x.toFixed(1)},${y.toFixed(1)}`;
        return `${MOVE}${at}${LINE}${at}`;
      });
  });
}

function y(ms: number, top: number): number {
  return H - 8 - (ms / top) * (H - 16);
}

/** El techo de la escala: un número redondo por encima del máximo, para que no toque el borde. */
export function niceTop(max: number): number {
  const target = max * 1.15;
  const pow = 10 ** Math.floor(Math.log10(target));
  for (const step of [1, 2, 2.5, 5, 10]) {
    if (step * pow >= target) {
      return step * pow;
    }
  }
  return 10 * pow;
}

/** «5 min», «2 h», «6 h 15 min». */
function span(ms: number): string {
  const nf = (n: number, unit: 'minute' | 'hour') =>
    new Intl.NumberFormat(locale(), { style: 'unit', unit, unitDisplay: 'short' }).format(n);
  const m = Math.max(1, Math.round(ms / MIN));
  if (m < 60) {
    return nf(m, 'minute');
  }
  const [h, rest] = [Math.floor(m / 60), m % 60];
  return rest ? `${nf(h, 'hour')} ${nf(rest, 'minute')}` : nf(h, 'hour');
}

function path(pts: [number, number][]): string {
  return pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join('L');
}
