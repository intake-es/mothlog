import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import type { CheckInMark } from '../core/api';
import { ago, fullDate } from '../core/format';
import { t } from '../core/i18n';
import { CHECKIN_STATUS_LABEL, duration } from '../pages/monitor-list/monitor-format';

interface Bar {
  status: string;
  title: string;
}

/** Las últimas ejecuciones de un monitor, de la más vieja a la más nueva, una barra por ejecución. */
@Component({
  selector: 'app-checkin-strip',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { role: 'img', '[attr.aria-label]': 'label()' },
  template: `
    @for (b of bars(); track $index) {
      <span class="bar" [class]="'bar bar--' + b.status" [attr.title]="b.title"></span>
    }
  `,
  styles: `
    :host {
      display: inline-flex;
      align-items: stretch;
      gap: 2px;
      height: var(--strip-h, 18px);
    }
    .bar {
      width: var(--strip-w, 5px);
      border-radius: 2px;
      background: var(--border);
    }
    .bar--ok {
      background: var(--success);
    }
    .bar--error,
    .bar--timeout {
      background: var(--danger);
    }
    .bar--missed {
      background: var(--warning);
    }
    .bar--in_progress {
      background: var(--info);
    }
  `,
})
export class CheckInStrip {
  /** Del más nuevo al más viejo, como llegan de la API. */
  readonly marks = input.required<CheckInMark[]>();
  /** Huecos que se rellenan vacíos para que todas las tiras midan lo mismo. */
  readonly slots = input(30);

  protected readonly bars = computed<Bar[]>(() => {
    const now = Date.now();
    const marks = this.marks().slice(0, this.slots()).reverse();
    const empty = Array.from({ length: this.slots() - marks.length }, () => ({
      status: '',
      title: '',
    }));
    return [
      ...empty,
      ...marks.map((m) => ({
        status: m.status,
        title: [
          t(CHECKIN_STATUS_LABEL[m.status]),
          fullDate(m.started_at),
          ago(m.started_at, now),
          duration(m.duration_ms),
        ]
          .filter((s) => s && s !== '—')
          .join(' · '),
      })),
    ];
  });

  protected readonly label = computed(() => {
    const marks = this.marks().slice(0, this.slots());
    const bad = marks.filter((m) => m.status !== 'ok' && m.status !== 'in_progress').length;
    return t('monitors.strip', { n: marks.length, bad });
  });
}
