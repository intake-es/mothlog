import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import type { HealthHour } from '../../core/api';
import { count, dayHour, percent } from '../../core/format';
import { I18n, plural } from '../../core/i18n';

/**
 * Las sesiones de la release por hora: cada barra es una hora, en gris las
 * que acabaron sin crash y en rojo, encima, las que tuvieron uno.
 */
@Component({
  selector: 'app-session-chart',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <div class="chart" role="img" [attr.aria-label]="label()">
      @for (bar of bars(); track bar.hour) {
        <div class="col">
          <div class="stack" [style.height]="bar.height">
            <div class="part part--crashed" [style.flex-grow]="bar.crashed"></div>
            <div class="part" [style.flex-grow]="bar.ok"></div>
          </div>
          <span class="tip" aria-hidden="true">{{ bar.tip }}</span>
        </div>
      }
    </div>
    <div class="axis" aria-hidden="true">
      <span>{{ t('chart.sessions.start') }}</span>
      <span class="legend">
        <span class="key"><i class="dot"></i>{{ t('chart.sessions.legendOk') }}</span>
        <span class="key"><i class="dot dot--crashed"></i>{{ t('chart.sessions.legendCrashed') }}</span>
      </span>
      <span>{{ t('chart.sessions.now') }}</span>
    </div>
  `,
  styles: `
    :host {
      display: block;
    }
    .chart {
      display: flex;
      align-items: flex-end;
      gap: 2px;
      height: 96px;
      border-bottom: 1px solid var(--chart-base);
    }
    .col {
      position: relative;
      display: flex;
      flex: 1 1 0;
      align-items: flex-end;
      height: 100%;
    }
    .stack {
      display: flex;
      flex-direction: column;
      width: 100%;
      overflow: hidden;
      border-radius: 2px 2px 0 0;
    }
    .part {
      flex-basis: 0;
      min-height: 0;
      background: var(--chart-bar);
      transition: background-color var(--motion-fast) var(--ease-out);
    }
    .part--crashed {
      background: var(--danger);
    }
    .col:hover .part:not(.part--crashed) {
      background: var(--chart-bar-hover);
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
    .col:nth-child(-n + 8) .tip {
      left: 0;
      transform: none;
    }
    .col:nth-last-child(-n + 8) .tip {
      right: 0;
      left: auto;
      transform: none;
    }
    .axis {
      display: flex;
      justify-content: space-between;
      align-items: center;
      margin-top: 0.4rem;
      color: var(--chart-axis);
      font-size: 0.68rem;
      font-variant-numeric: tabular-nums;
    }
    .legend {
      display: inline-flex;
      gap: 0.9rem;
    }
    .key {
      display: inline-flex;
      align-items: center;
      gap: 0.3rem;
    }
    .dot {
      width: 8px;
      height: 8px;
      border-radius: 2px;
      background: var(--chart-bar);
    }
    .dot--crashed {
      background: var(--danger);
    }
  `,
})
export class SessionChart {
  readonly hours = input<HealthHour[]>([]);
  protected readonly t = inject(I18n).t;

  protected readonly bars = computed(() => {
    const hours = this.hours();
    const top = Math.max(1, ...hours.map((h) => h.sessions));
    return hours.map((h) => {
      const parts = [dayHour(h.hour), plural('chart.sessions', h.sessions)];
      if (h.sessions > 0) {
        parts.push(this.t('chart.sessions.crashed', { n: count(h.sessions_crashed) }));
        parts.push(percent(h.crash_free_sessions));
      }
      return {
        hour: h.hour,
        height: h.sessions === 0 ? '0' : `${Math.max(3, (h.sessions / top) * 100)}%`,
        crashed: h.sessions_crashed,
        ok: Math.max(0, h.sessions - h.sessions_crashed),
        tip: parts.join(' · '),
      };
    });
  });

  protected readonly label = computed(() => {
    const hours = this.hours();
    const n = hours.reduce((sum, h) => sum + h.sessions, 0);
    const crashed = hours.reduce((sum, h) => sum + h.sessions_crashed, 0);
    return this.t('chart.sessions.label', { n: count(n), crashed: count(crashed) });
  });
}
