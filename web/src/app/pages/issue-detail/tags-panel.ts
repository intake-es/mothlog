import { ChangeDetectionStrategy, Component, computed, inject, input } from '@angular/core';
import { RouterLink } from '@angular/router';
import type { IssueTags } from '../../core/api';
import { count } from '../../core/format';
import { I18n, locale, plural } from '../../core/i18n';
import { DEFAULT_QUERY, term } from '../../core/search';

/** Las de más uso primero; el resto, por orden alfabético. */
const ORDER = ['browser', 'os', 'device', 'runtime', 'release', 'environment', 'level'];
const SHOWN = 5;

/** La distribución de cada etiqueta del error; pulsar un valor busca por él. */
@Component({
  selector: 'app-tags-panel',
  imports: [RouterLink],
  changeDetection: ChangeDetectionStrategy.OnPush,
  template: `
    @for (g of groups(); track g.key) {
      <div class="group">
        <p class="group__head">
          <span class="group__key">{{ g.key }}</span>
          <span class="group__distinct">{{ g.distinct }}</span>
        </p>
        @for (v of g.values; track v.value) {
          <a
            class="bar"
            routerLink="/"
            [queryParams]="{ query: v.query }"
            [attr.title]="v.title"
            [attr.aria-label]="v.label"
          >
            <span class="bar__fill" [style.width.%]="v.percent"></span>
            <span class="bar__value">{{ v.value }}</span>
            <span class="bar__pct tnum">{{ v.pct }}</span>
          </a>
        }
      </div>
    } @empty {
      <p class="muted">{{ t('detail.noIssueTags') }}</p>
    }
  `,
  styles: `
    :host {
      display: block;
    }
    .group + .group {
      margin-top: 0.9rem;
    }
    .group__head {
      display: flex;
      align-items: baseline;
      justify-content: space-between;
      gap: 0.5rem;
      margin-bottom: 0.3rem;
      font-size: 0.75rem;
    }
    .group__key {
      color: var(--text);
      font-family: var(--font-mono);
      font-weight: 500;
    }
    .group__distinct {
      color: var(--text-subtle);
    }
    .bar {
      position: relative;
      display: flex;
      align-items: center;
      gap: 0.5rem;
      min-height: 26px;
      margin-top: 2px;
      padding: 0 0.5rem;
      overflow: hidden;
      border-radius: 6px;
      color: var(--text);
      font-size: 0.78rem;
      text-decoration: none;
      transition: background-color var(--motion-fast) var(--ease-out);
    }
    .bar:hover {
      background: var(--bg-hover);
    }
    .bar__fill {
      position: absolute;
      inset: 0 auto 0 0;
      border-radius: 6px;
      background: color-mix(in srgb, var(--chart-bar) 38%, transparent);
    }
    .bar__value,
    .bar__pct {
      position: relative;
    }
    .bar__value {
      flex: 1 1 auto;
      min-width: 0;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .bar__pct {
      flex: 0 0 auto;
      color: var(--text-muted);
    }
    .muted {
      color: var(--text-muted);
      font-size: 0.8125rem;
    }
  `,
})
export class TagsPanel {
  readonly tags = input.required<IssueTags | null>();

  protected readonly t = inject(I18n).t;

  protected readonly groups = computed(() => {
    const fmt = new Intl.NumberFormat(locale(), { style: 'percent', maximumFractionDigits: 1 });
    const rank = (k: string) => {
      const i = ORDER.indexOf(k);
      return i < 0 ? ORDER.length : i;
    };
    return [...(this.tags()?.tags ?? [])]
      .sort((a, b) => rank(a.key) - rank(b.key) || a.key.localeCompare(b.key))
      .map((g) => ({
        key: g.key,
        distinct: plural('tags.values', g.distinct),
        values: g.values.slice(0, SHOWN).map((v) => {
          const filter = term(g.key, v.value);
          return {
            value: v.value,
            percent: v.percent,
            pct: fmt.format(v.percent / 100),
            query: `${DEFAULT_QUERY} ${filter}`,
            title: this.t('tags.share', { count: count(v.count), total: count(g.total) }),
            label: `${this.t('tags.searchFor', { term: filter })} · ${fmt.format(v.percent / 100)}`,
          };
        }),
      }));
  });
}
