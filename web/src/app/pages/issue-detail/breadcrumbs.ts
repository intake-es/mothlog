import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { clock } from '../../core/format';
import { I18n } from '../../core/i18n';

export interface Crumb {
  time: string;
  category: string;
  message: string;
  level: string;
}

const MAX = 50;

function text(v: unknown): string {
  if (v === null || v === undefined) {
    return '';
  }
  return typeof v === 'string' ? v : JSON.stringify(v);
}

/** Los breadcrumbs del SDK, como lista o como {"values": [...]}: los 50 últimos, del más viejo al más nuevo. */
export function parseCrumbs(raw: unknown): Crumb[] {
  const list = Array.isArray(raw)
    ? raw
    : raw && typeof raw === 'object' && Array.isArray((raw as { values?: unknown }).values)
      ? (raw as { values: unknown[] }).values
      : [];
  return list
    .filter((c): c is Record<string, unknown> => !!c && typeof c === 'object')
    .slice(-MAX)
    .map((c) => {
      const ts = c['timestamp'];
      const ms =
        typeof ts === 'number' ? ts * 1000 : typeof ts === 'string' ? Date.parse(ts) : Number.NaN;
      const data = c['data'];
      let message = text(c['message']);
      if (!message && data && typeof data === 'object') {
        message = Object.entries(data as Record<string, unknown>)
          .map(([k, v]) => `${k}: ${text(v)}`)
          .join(' · ');
      }
      return {
        time: Number.isNaN(ms) ? '' : clock(ms),
        category: text(c['category']) || text(c['type']) || 'default',
        message,
        level: text(c['level']),
      };
    });
}

@Component({
  selector: 'app-breadcrumbs',
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @if (crumbs().length === 0) {
      <p class="none">{{ t('crumbs.none') }}</p>
    } @else {
      <ol class="crumbs">
        @for (c of crumbs(); track $index) {
          <li class="crumb">
            <time class="crumb__time tnum">{{ c.time }}</time>
            <span class="crumb__cat">{{ c.category }}</span>
            <span class="crumb__msg" [class.crumb__msg--error]="c.level === 'error' || c.level === 'fatal'" [class.crumb__msg--warning]="c.level === 'warning'">{{ c.message }}</span>
          </li>
        }
      </ol>
    }
  `,
  styles: `
    .none {
      color: var(--text-muted);
      font-size: 0.8125rem;
    }
    .crumbs {
      margin: 0;
      padding: 0;
      overflow: hidden;
      border: 1px solid var(--border);
      border-radius: var(--radius-md);
      list-style: none;
    }
    .crumb {
      display: grid;
      grid-template-columns: 4.75rem 8.5rem minmax(0, 1fr);
      gap: 0.75rem;
      align-items: baseline;
      padding: 0.45rem 0.75rem;
      font-size: 0.78rem;
    }
    .crumb + .crumb {
      border-top: 1px solid var(--border);
    }
    .crumb__time {
      color: var(--text-subtle);
    }
    .crumb__cat {
      overflow: hidden;
      color: var(--text-muted);
      font-weight: 500;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .crumb__msg {
      font-family: var(--font-mono);
      font-size: 0.74rem;
      overflow-wrap: anywhere;
    }
    .crumb__msg--error {
      color: var(--danger);
    }
    .crumb__msg--warning {
      color: var(--warning-text);
    }
    @media (max-width: 768px) {
      .crumb {
        grid-template-columns: 4.25rem minmax(0, 1fr);
      }
      .crumb__msg {
        grid-column: 1 / -1;
      }
    }
  `,
})
export class Breadcrumbs {
  readonly raw = input<unknown>();
  protected readonly t = inject(I18n).t;
  protected readonly crumbs = computed(() => parseCrumbs(this.raw()));
}
