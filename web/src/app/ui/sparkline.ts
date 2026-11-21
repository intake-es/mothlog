import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import type { DayCount } from '../core/api';
import { plural } from '../core/i18n';

const BAR = 4;
const GAP = 2;
const HEIGHT = 22;

/** La minigráfica de 14 días de una fila: barras grises, un trazo en los días sin eventos. */
@Component({
  selector: 'app-sparkline',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    <svg
      [attr.width]="width()"
      [attr.height]="height"
      [attr.viewBox]="'0 0 ' + width() + ' ' + height"
      role="img"
      [attr.aria-label]="label()"
    >
      <title>{{ label() }}</title>
      @for (bar of bars(); track bar.x) {
        <rect
          [attr.x]="bar.x"
          [attr.y]="height - bar.h"
          [attr.width]="barWidth"
          [attr.height]="bar.h"
          [attr.rx]="bar.h > 2 ? 1 : 0"
          [class.empty]="bar.empty"
        />
      }
    </svg>
  `,
  styles: `
    :host {
      display: inline-flex;
      line-height: 0;
    }
    rect {
      fill: var(--chart-bar);
    }
    rect.empty {
      fill: var(--chart-base);
    }
  `,
})
export class Sparkline {
  readonly days = input<DayCount[] | undefined>([]);

  protected readonly height = HEIGHT;
  protected readonly barWidth = BAR;

  protected readonly width = computed(() => {
    const n = this.days()?.length ?? 0;
    return Math.max(0, n * (BAR + GAP) - GAP);
  });

  protected readonly bars = computed(() => {
    const days = this.days() ?? [];
    const max = Math.max(1, ...days.map((d) => d.count));
    return days.map((d, i) => ({
      x: i * (BAR + GAP),
      h: d.count === 0 ? 1 : Math.max(3, Math.round((d.count / max) * HEIGHT)),
      empty: d.count === 0,
    }));
  });

  protected readonly label = computed(() => {
    const total = (this.days() ?? []).reduce((sum, d) => sum + d.count, 0);
    return plural('chart.last14', total);
  });
}
