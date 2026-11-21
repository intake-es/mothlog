import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import type { DayCount } from '../core/api';
import { day } from '../core/format';
import { I18n, plural, t } from '../core/i18n';

/**
 * Los eventos por día del detalle, en barras compactas: grises,
 * con una línea base, y hoy abierto y discontinuo porque el día no ha acabado.
 */
@Component({
  selector: 'app-day-chart',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="chart" role="img" [attr.aria-label]="label()">
      @for (bar of bars(); track bar.date) {
        <div class="col" [class.col--today]="bar.today">
          <div class="bar" [style.height]="bar.height" [class.bar--zero]="bar.count === 0"></div>
          <span class="tip" aria-hidden="true">{{ bar.label }} · {{ bar.text }}</span>
        </div>
      }
    </div>
    <div class="axis" aria-hidden="true">
      <span>{{ first() }}</span>
      <span>{{ t('chart.today') }}</span>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .chart {
      display: flex;
      align-items: flex-end;
      gap: clamp(2px, 1.1%, 8px);
      height: 88px;
      border-bottom: 1px solid var(--chart-base);
    }
    .col {
      position: relative;
      display: flex;
      flex: 1 1 0;
      align-items: flex-end;
      justify-content: center;
      height: 100%;
    }
    .bar {
      width: 100%;
      max-width: 36px;
      min-height: 2px;
      border-radius: 3px 3px 0 0;
      background: var(--chart-bar);
      transition: background-color var(--motion-fast) var(--ease-out);
    }
    .bar--zero {
      min-height: 0;
    }
    .col:hover .bar {
      background: var(--chart-bar-hover);
    }
    .col--today .bar {
      border: 1.5px dashed var(--chart-bar-strong);
      border-bottom: 0;
      background: var(--chart-bar-current);
    }
    .tip {
      position: absolute;
      bottom: calc(100% + 6px);
      left: 50%;
      z-index: 5;
      padding: 0.35rem 0.55rem;
      border-radius: var(--radius-sm);
      background: var(--primary);
      color: var(--on-primary);
      font-size: 0.75rem;
      font-weight: 500;
      white-space: nowrap;
      font-variant-numeric: tabular-nums;
      opacity: 0;
      pointer-events: none;
      transform: translate(-50%, 2px);
      transition:
        opacity var(--motion-fast) var(--ease-out),
        transform var(--motion-fast) var(--ease-out);
    }
    .col:hover .tip {
      opacity: 1;
      transform: translate(-50%, 0);
    }
    .col:first-child .tip {
      left: 0;
      transform: none;
    }
    .col:last-child .tip {
      right: 0;
      left: auto;
      transform: none;
    }
    .axis {
      display: flex;
      justify-content: space-between;
      margin-top: 0.4rem;
      color: var(--chart-axis);
      font-size: 0.68rem;
      font-variant-numeric: tabular-nums;
    }
  `,
})
export class DayChart {
  readonly days = input<DayCount[]>([]);
  protected readonly t = inject(I18n).t;

  protected readonly bars = computed(() => {
    const days = this.days();
    const max = Math.max(1, ...days.map((d) => d.count));
    return days.map((d, i) => ({
      date: d.date,
      count: d.count,
      // Hoy sin eventos se ve igual, abierto y bajo, para que se note que el día sigue.
      height: d.count === 0 ? (i === days.length - 1 ? '6px' : '0') : `${(d.count / max) * 100}%`,
      today: i === days.length - 1,
      label: i === days.length - 1 ? t('chart.today') : day(d.date),
      text: plural('events', d.count),
    }));
  });

  protected readonly first = computed(() => {
    const d = this.days()[0];
    return d ? day(d.date) : '';
  });

  protected readonly label = computed(() => {
    const total = this.days().reduce((sum, d) => sum + d.count, 0);
    return plural('chart.last14', total);
  });
}
