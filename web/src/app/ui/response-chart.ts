import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import type { ResponseTimes } from '../core/api';
import { fullDate } from '../core/format';
import { I18n, t } from '../core/i18n';
import { duration } from '../pages/monitor-list/monitor-format';

const SLOT = 15 * 60_000;
const [MOVE, LINE, CLOSE] = ['M', 'L', 'Z'];
const SLOTS = 96;
const W = 960;
const H = 120;

interface Slot {
  x: number;
  avg: number | null;
  failures: number;
  title: string;
}

/**
 * El tiempo de respuesta medio de las últimas 24 h en tramos de 15 min: una
 * línea con su área, y en rojo, abajo, los tramos con alguna comprobación fallida.
 */
@Component({
  selector: 'app-response-chart',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="frame">
      <span class="ymax tnum" aria-hidden="true">{{ maxText() }}</span>
      <svg
        class="chart"
        [attr.viewBox]="'0 0 ' + w + ' ' + h"
        preserveAspectRatio="none"
        role="img"
        [attr.aria-label]="label()"
      >
        <line class="grid" x1="0" [attr.x2]="w" y1="0.5" y2="0.5" />
        <line class="grid" x1="0" [attr.x2]="w" [attr.y1]="h / 2" [attr.y2]="h / 2" />
        @for (p of areas(); track $index) {
          <path class="area" [attr.d]="p" />
        }
        @for (p of lines(); track $index) {
          <path class="line" [attr.d]="p" vector-effect="non-scaling-stroke" />
        }
        @for (s of slots(); track s.x) {
          @if (s.failures) {
            <rect class="fail" [attr.x]="s.x" [attr.y]="h - 6" [attr.width]="slotW" height="6" />
          }
          <rect class="hit" [attr.x]="s.x" y="0" [attr.width]="slotW" [attr.height]="h">
            <title>{{ s.title }}</title>
          </rect>
        }
      </svg>
    </div>
    <div class="axis" aria-hidden="true">
      <span>{{ t('uptime.axis.start') }}</span>
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
  /** La hora del final de la gráfica; se cambia en los tests. */
  readonly now = input<number>(Date.now());

  protected readonly t = inject(I18n).t;
  protected readonly w = W;
  protected readonly h = H;
  protected readonly slotW = W / SLOTS;

  private readonly max = computed(() => Math.max(1, ...this.buckets().map((b) => b.avg_ms ?? 0)));

  protected readonly maxText = computed(() => duration(this.max()));

  protected readonly slots = computed<Slot[]>(() => {
    const end = Math.floor(this.now() / SLOT) * SLOT + SLOT;
    const start = end - SLOTS * SLOT;
    const byIndex = new Map<number, ResponseTimes>();
    for (const b of this.buckets()) {
      const i = Math.floor((Date.parse(b.at) - start) / SLOT);
      if (i >= 0 && i < SLOTS) {
        byIndex.set(i, b);
      }
    }
    return Array.from({ length: SLOTS }, (_, i) => {
      const b = byIndex.get(i);
      const at = new Date(start + i * SLOT).toISOString();
      const parts = [fullDate(at)];
      if (b) {
        parts.push(t('uptime.chartAvg', { ms: duration(b.avg_ms) }));
        if (b.failures) {
          parts.push(t('uptime.chartFailures', { n: b.failures, total: b.checks }));
        }
      }
      return {
        x: i * this.slotW,
        avg: b?.avg_ms ?? null,
        failures: b?.failures ?? 0,
        title: parts.join(' · '),
      };
    });
  });

  /** Los trozos seguidos con datos, como listas de puntos. */
  private readonly runs = computed(() => {
    const max = this.max();
    const out: [number, number][][] = [];
    let run: [number, number][] = [];
    for (const s of this.slots()) {
      if (s.avg === null) {
        if (run.length) {
          out.push(run);
        }
        run = [];
        continue;
      }
      run.push([s.x + this.slotW / 2, H - 8 - (s.avg / max) * (H - 16)]);
    }
    if (run.length) {
      out.push(run);
    }
    return out;
  });

  /** Un tramo suelto se pinta como un segmento del ancho del tramo. */
  private widen(run: [number, number][]): [number, number][] {
    if (run.length > 1) {
      return run;
    }
    const [x, y] = run[0];
    return [
      [x - this.slotW / 2, y],
      [x + this.slotW / 2, y],
    ];
  }

  protected readonly lines = computed(() => this.runs().map((run) => `M${path(this.widen(run))}`));

  protected readonly areas = computed(() =>
    this.runs().map((run) => {
      const pts = this.widen(run);
      const first = pts[0][0].toFixed(1);
      const last = pts[pts.length - 1][0].toFixed(1);
      return `${MOVE}${first},${H}${LINE}${path(pts)}${LINE}${last},${H}${CLOSE}`;
    }),
  );

  protected readonly label = computed(() =>
    t('uptime.chart', {
      max: this.maxText(),
      bad: this.slots().filter((s) => s.failures > 0).length,
    }),
  );
}

function path(pts: [number, number][]): string {
  return pts.map(([x, y]) => `${x.toFixed(1)},${y.toFixed(1)}`).join('L');
}
