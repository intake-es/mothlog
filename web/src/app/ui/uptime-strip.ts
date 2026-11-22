import { ChangeDetectionStrategy, Component, computed, input } from '@angular/core';
import type { UptimeCheck } from '../core/api';
import { ago, fullDate } from '../core/format';
import { t } from '../core/i18n';
import { duration } from '../pages/monitor-list/monitor-format';

interface Bar {
  state: string;
  title: string;
}

/** Las últimas comprobaciones de un monitor de disponibilidad, de la más vieja a la más nueva. */
@Component({
  selector: 'app-uptime-strip',
  changeDetection: ChangeDetectionStrategy.OnPush,
  host: { role: 'img', '[attr.aria-label]': 'label()' },
  template: `
    @for (b of bars(); track $index) {
      <span [class]="'bar bar--' + b.state" [attr.title]="b.title"></span>
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
    .bar--fail {
      background: var(--danger);
    }
  `,
})
export class UptimeStrip {
  /** De la más nueva a la más vieja, como llegan de la API. */
  readonly checks = input.required<UptimeCheck[]>();
  readonly slots = input(30);

  protected readonly bars = computed<Bar[]>(() => {
    const now = Date.now();
    const checks = this.checks().slice(0, this.slots()).reverse();
    const empty = Array.from({ length: this.slots() - checks.length }, () => ({
      state: 'empty',
      title: '',
    }));
    return [
      ...empty,
      ...checks.map((c) => ({
        state: c.ok ? 'ok' : 'fail',
        title: [
          c.ok ? t('uptime.check.ok') : t('uptime.check.fail'),
          c.status_code === null ? '' : String(c.status_code),
          duration(c.duration_ms),
          c.ok ? '' : (c.error ?? ''),
          fullDate(c.checked_at),
          ago(c.checked_at, now),
        ]
          .filter((s) => s && s !== '—')
          .join(' · '),
      })),
    ];
  });

  protected readonly label = computed(() => {
    const checks = this.checks().slice(0, this.slots());
    return t('uptime.strip', { n: checks.length, bad: checks.filter((c) => !c.ok).length });
  });
}
